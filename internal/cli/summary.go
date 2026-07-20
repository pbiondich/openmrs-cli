package cli

import (
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/pbiondich/openmrs-cli/internal/client"
	"github.com/pbiondich/openmrs-cli/internal/output"
)

var (
	summarySections   string
	summaryEncounters int
)

const allSections = "visit,problems,meds,allergies,vitals,encounters,programs"

var patientSummaryCmd = &cobra.Command{
	Use:   "summary <identifier-or-uuid>",
	Short: "Clinical summary for one patient (IPS-aligned sections)",
	Long: `Assembles a patient summary from parallel REST and FHIR queries:
active visit, problems, medications, allergies, vitals, recent
encounters with their observations, and program enrollments.

The patient resolves from a UUID, an exact identifier match (OpenMRS
identifier= lookup: MRN, old ID, national ID, ...), or a unique name via
fuzzy search when no identifier hits; an ambiguous reference errors with
the candidates listed.

Sections follow the International Patient Summary (IPS) core where it
applies. Medications and vitals prefer the FHIR2 module and fall back to
REST (noted in the output). Section statuses follow a six-state absence
model: ok, confirmed-none (the record asserts absence), none (nothing
recorded, no assertion), unavailable (fetch failed), withheld (access
denied), plus partial (and truncated when a fetch cap may have dropped
rows). Treat none/ok as complete only when partial and truncated are
absent. A failed section never stops the rest of the summary from
rendering.`,
	Example: `  omrs patient summary 5574MO-2
  omrs patient summary dd8e5b3d-1691-11df-97a5-7038c432aabf
  omrs patient summary 5574MO-2 --sections problems,meds,allergies --json`,
	Args: cobra.ExactArgs(1),
	RunE: runPatientSummary,
}

func runPatientSummary(cmd *cobra.Command, args []string) error {
	c, err := newClient(cmd.Context())
	if err != nil {
		return err
	}

	patient, err := resolvePatient(c, args[0])
	if err != nil {
		return err
	}
	uuid, _ := patient["uuid"].(string)

	// Unknown section names are a USAGE error, not a silent empty summary
	// — a typo'd --sections must never masquerade as an empty chart.
	validSections := map[string]bool{}
	for _, v := range strings.Split(allSections, ",") {
		validSections[v] = true
	}
	wanted := map[string]bool{}
	for _, s := range strings.Split(summarySections, ",") {
		name := strings.TrimSpace(s)
		if name == "" {
			continue
		}
		if !validSections[name] {
			return &client.APIError{
				Message: fmt.Sprintf("unknown section %q (valid sections: %s)", name, allSections),
				Code:    client.CodeUsage,
			}
		}
		wanted[name] = true
	}
	if len(wanted) == 0 {
		return &client.APIError{
			Message: fmt.Sprintf("no sections requested (valid sections: %s)", allSections),
			Code:    client.CodeUsage,
		}
	}

	sections := map[string]*section{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	run := func(name string, fetch func() *section) {
		if !wanted[name] {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := fetch()
			mu.Lock()
			sections[name] = s
			mu.Unlock()
		}()
	}

	run("visit", func() *section {
		return restSection(c, "visit", url.Values{"patient": {uuid}, "includeInactive": {"false"}, "v": {"default"}}, "rest")
	})
	run("problems", func() *section { return problemsSection(c, uuid) })
	run("allergies", func() *section { return allergiesSection(c, uuid) })
	run("programs", func() *section {
		return restSection(c, "programenrollment", url.Values{"patient": {uuid}, "v": {"default"}}, "rest")
	})
	run("meds", func() *section { return medsSection(c, uuid) })
	run("vitals", func() *section { return vitalsSection(c, uuid) })
	run("encounters", func() *section { return encountersSection(c, uuid) })

	wg.Wait()

	// counts gives a reader the shape of successful sections. Failed
	// sections are null (not 0) so agents never confuse "fetch failed"
	// with "nothing recorded".
	counts := map[string]any{}
	for name, s := range sections {
		counts[name] = sectionCount(s)
	}
	result := map[string]any{
		"counts":   counts,
		"patient":  patient,
		"sections": sections,
	}

	for name, s := range sections {
		if s.Status == statusUnavailable || s.Status == statusWithheld {
			output.Warn("section %q unavailable: %s", name, s.Error)
		}
		if s.Truncated {
			output.Warn("section %q is truncated: a fetch cap may have omitted rows", name)
		} else if s.Partial {
			output.Warn("section %q is partial: nested fetch failed or results incomplete", name)
		}
	}

	if outputMode() == output.ModeJSON {
		return output.Print(result, output.ModeJSON, "")
	}
	renderSummary(patient, sections)
	return nil
}

func init() {
	patientSummaryCmd.Flags().StringVar(&summarySections, "sections", allSections, "comma-separated sections to include")
	patientSummaryCmd.Flags().IntVar(&summaryEncounters, "encounters", 5, "number of recent encounters (with their obs)")
	patientCmd.AddCommand(patientSummaryCmd)
}
