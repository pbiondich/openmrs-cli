package cli

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
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

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const allSections = "visit,problems,meds,allergies,vitals,encounters,programs"

// section holds one summary section's outcome. Status is one of:
// "ok" (items present), "none" (fetched fine, nothing recorded),
// "unavailable" (fetch failed; see error). Agents must distinguish
// none-recorded from unavailable.
type section struct {
	Status string `json:"status"`
	Source string `json:"source,omitempty"`
	Items  []any  `json:"items"`
	Error  string `json:"error,omitempty"`
}

var patientSummaryCmd = &cobra.Command{
	Use:   "summary <mrn-or-uuid>",
	Short: "Clinical summary for one patient (IPS-aligned sections)",
	Long: `Assembles a patient summary from parallel REST and FHIR queries:
active visit, problems, medications, allergies, vitals, recent
encounters with their observations, and program enrollments.

Sections follow the International Patient Summary (IPS) core where it
applies. Medications and vitals prefer the FHIR2 module and fall back to
REST (noted in the output). A section that fails to load is marked
"unavailable" and the rest of the summary still renders; "none" means
the server answered and nothing is recorded.`,
	Example: `  omrs patient summary 5574MO-2
  omrs patient summary dd8e5b3d-1691-11df-97a5-7038c432aabf
  omrs patient summary 5574MO-2 --sections problems,meds,allergies --json`,
	Args: cobra.ExactArgs(1),
	RunE: runPatientSummary,
}

func runPatientSummary(cmd *cobra.Command, args []string) error {
	c, err := newClient()
	if err != nil {
		return err
	}

	patient, err := resolvePatient(c, args[0])
	if err != nil {
		return err
	}
	uuid, _ := patient["uuid"].(string)

	wanted := map[string]bool{}
	for _, s := range strings.Split(summarySections, ",") {
		wanted[strings.TrimSpace(s)] = true
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
	run("problems", func() *section {
		return restSection(c, "condition", url.Values{"patientUuid": {uuid}, "v": {"default"}}, "rest")
	})
	run("allergies", func() *section {
		return restSection(c, "patient/"+uuid+"/allergy", url.Values{"v": {"default"}}, "rest")
	})
	run("programs", func() *section {
		return restSection(c, "programenrollment", url.Values{"patient": {uuid}, "v": {"default"}}, "rest")
	})
	run("meds", func() *section { return medsSection(c, uuid) })
	run("vitals", func() *section { return vitalsSection(c, uuid) })
	run("encounters", func() *section { return encountersSection(c, uuid) })

	wg.Wait()

	result := map[string]any{
		"patient":  patient,
		"sections": sections,
	}

	for name, s := range sections {
		if s.Status == "unavailable" {
			fmt.Fprintf(os.Stderr, `{"warning":"section %q unavailable: %s"}`+"\n", name, s.Error)
		}
	}

	if outputMode() == output.ModeJSON {
		return output.Print(result, output.ModeJSON, "")
	}
	renderSummary(patient, sections)
	return nil
}

// resolvePatient turns an MRN or UUID into a full patient record. An
// ambiguous MRN is an error: a clinical summary must never guess.
func resolvePatient(c *client.Client, ref string) (map[string]any, error) {
	if uuidRe.MatchString(ref) {
		return c.Get("patient/"+ref, url.Values{"v": {"full"}})
	}
	data, err := c.Get("patient", url.Values{"q": {ref}, "v": {"full"}, "limit": {"10"}})
	if err != nil {
		return nil, err
	}
	results, _ := data["results"].([]any)
	// Prefer exact identifier matches over fuzzy name hits.
	var matches []map[string]any
	for _, r := range results {
		rec, _ := r.(map[string]any)
		for _, idAny := range asSlice(rec["identifiers"]) {
			id, _ := idAny.(map[string]any)
			display, _ := id["display"].(string)
			if _, val, ok := strings.Cut(display, "= "); ok && strings.EqualFold(strings.TrimSpace(val), ref) {
				matches = append(matches, rec)
				break
			}
		}
	}
	if len(matches) == 0 && len(results) == 1 {
		rec, _ := results[0].(map[string]any)
		matches = append(matches, rec)
	}
	switch len(matches) {
	case 0:
		return nil, &client.APIError{Message: fmt.Sprintf("no patient found with identifier %q", ref), Code: client.CodeNotFound}
	case 1:
		return matches[0], nil
	default:
		var cands []string
		for _, m := range matches {
			cands = append(cands, fmt.Sprintf("%s (%s)", output.Extract(m, "display"), output.Extract(m, "uuid")))
		}
		return nil, &client.APIError{
			Message: fmt.Sprintf("identifier %q matches %d patients; use a UUID", ref, len(matches)),
			Code:    client.CodeBadRequest,
			Detail:  strings.Join(cands, "; "),
		}
	}
}

func restSection(c *client.Client, path string, params url.Values, source string) *section {
	data, err := c.Get(path, params)
	if err != nil {
		return &section{Status: "unavailable", Source: source, Items: []any{}, Error: err.Error()}
	}
	items, _ := data["results"].([]any)
	status := "ok"
	if len(items) == 0 {
		status = "none"
		items = []any{}
	}
	return &section{Status: status, Source: source, Items: items}
}

// medsSection prefers FHIR MedicationRequest (richer, cleaner) and falls
// back to REST orders. The FHIR status search param is broken on some
// fhir2 versions, so active-filtering happens client-side.
func medsSection(c *client.Client, uuid string) *section {
	bundle, err := c.GetFHIR("MedicationRequest", url.Values{"patient": {uuid}, "_count": {"100"}})
	if err == nil {
		// One drug can carry many still-active refill orders; the med
		// list wants unique drugs, most recent order winning.
		latest := map[string]map[string]any{}
		for _, e := range asSlice(bundle["entry"]) {
			entry, _ := e.(map[string]any)
			res, _ := entry["resource"].(map[string]any)
			if res["status"] != "active" {
				continue
			}
			med := output.Extract(res, "medicationReference.display|medicationCodeableConcept.text")
			prev, seen := latest[med]
			if !seen || output.Extract(res, "authoredOn") > output.Extract(prev, "authoredOn") {
				latest[med] = res
			}
		}
		var items []any
		for _, res := range latest {
			items = append(items, res)
		}
		sort.Slice(items, func(i, j int) bool {
			a, _ := items[i].(map[string]any)
			b, _ := items[j].(map[string]any)
			return output.Extract(a, "medicationReference.display|medicationCodeableConcept.text") <
				output.Extract(b, "medicationReference.display|medicationCodeableConcept.text")
		})
		status := "ok"
		if len(items) == 0 {
			status = "none"
			items = []any{}
		}
		return &section{Status: status, Source: "fhir", Items: items}
	}

	fmt.Fprintln(os.Stderr, `{"warning":"FHIR unavailable for medications; falling back to REST orders"}`)
	data, err := c.Get("order", url.Values{"patient": {uuid}, "v": {"default"}, "limit": {"100"}})
	if err != nil {
		return &section{Status: "unavailable", Source: "rest-orders", Items: []any{}, Error: err.Error()}
	}
	var items []any
	for _, r := range asSlice(data["results"]) {
		rec, _ := r.(map[string]any)
		if rec["type"] == "drugorder" && rec["action"] != "DISCONTINUE" && rec["dateStopped"] == nil {
			items = append(items, rec)
		}
	}
	status := "ok"
	if len(items) == 0 {
		status = "none"
		items = []any{}
	}
	return &section{Status: status, Source: "rest-orders", Items: items}
}

// vitalsSection returns the latest observation per vital-sign code via
// FHIR. Servers whose concepts lack vital-signs category mappings
// legitimately return none.
func vitalsSection(c *client.Client, uuid string) *section {
	bundle, err := c.GetFHIR("Observation", url.Values{
		"patient": {uuid}, "category": {"vital-signs"}, "_sort": {"-date"}, "_count": {"40"},
	})
	if err != nil {
		return &section{Status: "unavailable", Source: "fhir", Items: []any{}, Error: err.Error()}
	}
	seen := map[string]bool{}
	var items []any
	for _, e := range asSlice(bundle["entry"]) {
		entry, _ := e.(map[string]any)
		res, _ := entry["resource"].(map[string]any)
		code := output.Extract(res, "code.text")
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		items = append(items, res)
	}
	status := "ok"
	if len(items) == 0 {
		status = "none"
		items = []any{}
	}
	return &section{Status: status, Source: "fhir", Items: items}
}

// encountersSection fetches all encounters, keeps the most recent N, and
// attaches each one's observations.
func encountersSection(c *client.Client, uuid string) *section {
	data, err := c.GetAll("encounter", url.Values{"patient": {uuid}, "v": {"default"}}, 2000)
	if err != nil {
		return &section{Status: "unavailable", Source: "rest", Items: []any{}, Error: err.Error()}
	}
	encs := asSlice(data["results"])
	sort.SliceStable(encs, func(i, j int) bool {
		a, _ := encs[i].(map[string]any)
		b, _ := encs[j].(map[string]any)
		da, _ := a["encounterDatetime"].(string)
		db, _ := b["encounterDatetime"].(string)
		return da > db
	})
	if len(encs) > summaryEncounters {
		encs = encs[:summaryEncounters]
	}

	var wg sync.WaitGroup
	for _, e := range encs {
		enc, _ := e.(map[string]any)
		wg.Add(1)
		go func(enc map[string]any) {
			defer wg.Done()
			encUUID, _ := enc["uuid"].(string)
			obsData, err := c.Get("obs", url.Values{"encounter": {encUUID}, "v": {"default"}, "limit": {"100"}})
			if err != nil {
				enc["obs"] = []any{}
				return
			}
			enc["obs"] = asSlice(obsData["results"])
		}(enc)
	}
	wg.Wait()

	status := "ok"
	if len(encs) == 0 {
		status = "none"
		encs = []any{}
	}
	return &section{Status: status, Source: "rest", Items: encs}
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func dateOnly(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

// renderSummary prints the human panel.
func renderSummary(patient map[string]any, sections map[string]*section) {
	name := output.Extract(patient, "person.display|display")
	fmt.Printf("%s · %s, %sy · DOB %s\n", name,
		output.Extract(patient, "person.gender"),
		output.Extract(patient, "person.age"),
		dateOnly(output.Extract(patient, "person.birthdate")))
	if output.Extract(patient, "person.dead") == "true" {
		fmt.Printf("DECEASED %s\n", dateOnly(output.Extract(patient, "person.deathDate")))
	}
	for _, idAny := range asSlice(patient["identifiers"]) {
		id, _ := idAny.(map[string]any)
		fmt.Printf("ID  %s\n", output.Extract(id, "display"))
	}
	fmt.Printf("uuid %s\n", output.Extract(patient, "uuid"))

	printSection := func(title, name string, line func(map[string]any) string) {
		s, ok := sections[name]
		if !ok {
			return
		}
		fmt.Printf("\n%s\n", title)
		switch s.Status {
		case "unavailable":
			fmt.Printf("  (unavailable: %s)\n", s.Error)
		case "none":
			if name == "allergies" {
				fmt.Println("  No known allergies (none recorded)")
			} else {
				fmt.Println("  (none)")
			}
		default:
			for _, item := range s.Items {
				rec, _ := item.(map[string]any)
				fmt.Printf("  • %s\n", line(rec))
				if name == "encounters" {
					for _, o := range asSlice(rec["obs"]) {
						ob, _ := o.(map[string]any)
						fmt.Printf("      - %s\n", output.Extract(ob, "display"))
					}
				}
			}
		}
	}

	printSection("ACTIVE VISIT", "visit", func(r map[string]any) string {
		return fmt.Sprintf("%s · %s · since %s",
			output.Extract(r, "visitType.display"),
			output.Extract(r, "location.display"),
			dateOnly(output.Extract(r, "startDatetime")))
	})
	printSection("PROBLEMS", "problems", func(r map[string]any) string {
		return fmt.Sprintf("%s — %s",
			output.Extract(r, "condition.coded.display|condition.nonCoded|display"),
			strings.ToLower(output.Extract(r, "clinicalStatus")))
	})
	medsTitle := "MEDICATIONS (active)"
	if s := sections["meds"]; s != nil && s.Source == "rest-orders" {
		medsTitle = "MEDICATIONS (active, via REST orders)"
	}
	printSection(medsTitle, "meds", func(r map[string]any) string {
		if med := output.Extract(r, "medicationReference.display|medicationCodeableConcept.text"); med != "" {
			line := med
			if dose := output.Extract(r, "dosageInstruction.text"); dose != "" {
				line += " · " + dose
			}
			if d := output.Extract(r, "authoredOn"); d != "" {
				line += " · since " + dateOnly(d)
			}
			return line
		}
		return fmt.Sprintf("%s · since %s", output.Extract(r, "display"), dateOnly(output.Extract(r, "dateActivated")))
	})
	printSection("ALLERGIES", "allergies", func(r map[string]any) string {
		return output.Extract(r, "display")
	})
	printSection("VITALS (latest)", "vitals", func(r map[string]any) string {
		return fmt.Sprintf("%s: %s %s (%s)",
			output.Extract(r, "code.text"),
			output.Extract(r, "valueQuantity.value"),
			output.Extract(r, "valueQuantity.unit"),
			dateOnly(output.Extract(r, "effectiveDateTime")))
	})
	printSection(fmt.Sprintf("RECENT ENCOUNTERS (last %d)", summaryEncounters), "encounters", func(r map[string]any) string {
		return fmt.Sprintf("%s  %s · %s",
			dateOnly(output.Extract(r, "encounterDatetime")),
			output.Extract(r, "encounterType.display"),
			output.Extract(r, "location.display"))
	})
	printSection("PROGRAMS", "programs", func(r map[string]any) string {
		line := output.Extract(r, "display")
		if d := output.Extract(r, "dateEnrolled"); d != "" {
			line += " · enrolled " + dateOnly(d)
		}
		if d := output.Extract(r, "dateCompleted"); d != "" {
			line += " · completed " + dateOnly(d)
		}
		return line
	})
}

func init() {
	patientSummaryCmd.Flags().StringVar(&summarySections, "sections", allSections, "comma-separated sections to include")
	patientSummaryCmd.Flags().IntVar(&summaryEncounters, "encounters", 5, "number of recent encounters (with their obs)")
	patientCmd.AddCommand(patientSummaryCmd)
}
