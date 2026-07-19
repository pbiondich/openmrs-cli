package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
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

// Section status literals follow the six-state absence model
// (github.com/paynejd/openmrs-cli-agent-review, cross-checked against
// FHIR Composition.section.emptyReason). A status is only ever emitted
// when the code can determine it truthfully:
//
//	ok             data present                          (state 1)
//	confirmed-none the record asserts none exists        (state 2, FHIR nilknown)
//	none           nothing recorded; no assertion made   (state 3, FHIR notasked)
//	unavailable    the fetch failed                      (state 4, FHIR unavailable)
//	withheld       the server denied access, HTTP 403    (state 6, FHIR withheld)
//
// State 5 (partial) is the section's Partial flag plus per-item markers,
// and Truncated when a hard fetch cap may have dropped rows.
const (
	statusOK            = "ok"
	statusConfirmedNone = "confirmed-none"
	statusNone          = "none"
	statusUnavailable   = "unavailable"
	statusWithheld      = "withheld"

	// Per-encounter obsStatus when the obs page filled the request limit.
	obsStatusOK          = "ok"
	obsStatusUnavailable = "unavailable"
	obsStatusPartial     = "partial"
)

// Fetch caps for summary sections. Hitting a cap sets Partial+Truncated
// so agents never treat a full page as a complete chart.
const (
	medsFHIRCount        = 100
	medsRESTLimit        = 100
	vitalsFHIRCount      = 40
	obsPerEncounterLimit = 100
	encountersGetAllCap  = 2000
)

// statusForError maps a failed fetch to unavailable, or withheld when
// the server explicitly denied access (HTTP 403 / FORBIDDEN).
func statusForError(err error) string {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) &&
		(apiErr.HTTPStatus == http.StatusForbidden || apiErr.Code == client.CodeForbidden) {
		return statusWithheld
	}
	return statusUnavailable
}

// section holds one summary section's outcome. Agents must never
// conflate the absence states; see the constants above.
type section struct {
	Status string `json:"status"`
	Source string `json:"source,omitempty"`
	Items  []any  `json:"items"`
	Error  string `json:"error,omitempty"`
	// Partial is true when the section is incomplete for any reason:
	// nested fetch failure, and/or a hard fetch cap (see Truncated).
	Partial bool `json:"partial,omitempty"`
	// Truncated is true when a page size / item cap may have dropped rows
	// (same idea as truncated on --all list fetches). Cap hits set both
	// Truncated and Partial.
	Truncated bool `json:"truncated,omitempty"`
}

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
	run("allergies", func() *section { return allergiesSection(c, uuid) })
	run("programs", func() *section {
		return restSection(c, "programenrollment", url.Values{"patient": {uuid}, "v": {"default"}}, "rest")
	})
	run("meds", func() *section { return medsSection(c, uuid) })
	run("vitals", func() *section { return vitalsSection(c, uuid) })
	run("encounters", func() *section { return encountersSection(c, uuid) })

	wg.Wait()

	// counts gives a reader (human or agent) the shape of the record
	// before the bulk of the payload — it marshals first alphabetically.
	counts := map[string]int{}
	for name, s := range sections {
		counts[name] = len(s.Items)
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

// resolvePatient turns an MRN or UUID into a full patient record. An
// ambiguous reference is an error: a clinical summary must never guess.
//
// Order: UUID direct get → REST identifier= (exact ID lookup) → fuzzy
// q= (name / free text). Identifier-first matches patient search
// --identifier and avoids false misses when the right patient is outside
// the top page of a fuzzy search.
func resolvePatient(c *client.Client, ref string) (map[string]any, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, &client.APIError{Message: "empty patient reference", Code: client.CodeBadRequest}
	}
	if uuidRe.MatchString(ref) {
		return c.Get("patient/"+ref, url.Values{"v": {"full"}})
	}

	idData, err := c.Get("patient", url.Values{
		"identifier": {ref}, "v": {"full"}, "limit": {"25"},
	})
	if err != nil {
		return nil, err
	}
	if idResults := asSlice(idData["results"]); len(idResults) > 0 {
		return choosePatient(ref, idResults, true)
	}

	qData, err := c.Get("patient", url.Values{
		"q": {ref}, "v": {"full"}, "limit": {"10"},
	})
	if err != nil {
		return nil, err
	}
	return choosePatient(ref, asSlice(qData["results"]), false)
}

// choosePatient picks a single patient from a result page.
// identifierSearch: the server already filtered by identifier=; any
// multi-hit set is ambiguity. Fuzzy q=: prefer structured exact
// identifier matches, then a unique single hit as a name convenience.
func choosePatient(ref string, results []any, identifierSearch bool) (map[string]any, error) {
	if len(results) == 0 {
		return nil, &client.APIError{
			Message: fmt.Sprintf("no patient found matching %q", ref),
			Code:    client.CodeNotFound,
		}
	}

	if identifierSearch {
		if len(results) == 1 {
			rec, _ := results[0].(map[string]any)
			return rec, nil
		}
		return nil, ambiguityError(ref, results)
	}

	// Prefer exact identifier matches over fuzzy name hits, using the
	// structured identifier value (not the display label).
	var matches []map[string]any
	for _, r := range results {
		rec, _ := r.(map[string]any)
		for _, idAny := range asSlice(rec["identifiers"]) {
			id, _ := idAny.(map[string]any)
			val, _ := id["identifier"].(string)
			if strings.EqualFold(strings.TrimSpace(val), ref) {
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
		// Multiple fuzzy hits with no exact identifier match is
		// ambiguity, not absence — report the candidates, never
		// "not found" (which an agent reads as "patient doesn't exist").
		if len(results) > 1 {
			return nil, ambiguityError(ref, results)
		}
		return nil, &client.APIError{
			Message: fmt.Sprintf("no patient found matching %q", ref),
			Code:    client.CodeNotFound,
		}
	case 1:
		return matches[0], nil
	default:
		recs := make([]any, len(matches))
		for i, m := range matches {
			recs[i] = m
		}
		return nil, ambiguityError(ref, recs)
	}
}

func ambiguityError(ref string, candidates []any) *client.APIError {
	var cands []string
	for _, r := range candidates {
		rec, _ := r.(map[string]any)
		cands = append(cands, fmt.Sprintf("%s (%s)", output.Extract(rec, "display"), output.Extract(rec, "uuid")))
	}
	return &client.APIError{
		Message: fmt.Sprintf("%q matches %d patients; use a UUID or an exact identifier", ref, len(candidates)),
		Code:    client.CodeBadRequest,
		Detail:  strings.Join(cands, "; "),
	}
}

func restSection(c *client.Client, path string, params url.Values, source string) *section {
	data, err := c.Get(path, params)
	if err != nil {
		return &section{Status: statusForError(err), Source: source, Items: []any{}, Error: err.Error()}
	}
	items, _ := data["results"].([]any)
	status := statusOK
	if len(items) == 0 {
		status = statusNone
		items = []any{}
	}
	return &section{Status: status, Source: source, Items: items}
}

// allergiesSection is the one place a true confident-negative exists:
// OpenMRS records an explicit allergyStatus assertion on the patient.
// An empty list only earns confirmed-none when the record says
// "No known allergies"; otherwise it is state 3, not assessed. The
// assertion needs its own fetch: the REST full representation omits
// allergyStatus (custom representations carry it).
func allergiesSection(c *client.Client, uuid string) *section {
	s := restSection(c, "patient/"+uuid+"/allergy", url.Values{"v": {"default"}}, "rest")
	if s.Status == statusNone {
		p, err := c.Get("patient/"+uuid, url.Values{"v": {"custom:(allergyStatus)"}})
		if err == nil && strings.EqualFold(output.Extract(p, "allergyStatus"), "No known allergies") {
			s.Status = statusConfirmedNone
		}
	}
	return s
}

// medsSection prefers FHIR MedicationRequest (richer, cleaner) and falls
// back to REST orders. The FHIR status search param is broken on some
// fhir2 versions, so active-filtering happens client-side.
func medsSection(c *client.Client, uuid string) *section {
	bundle, err := c.GetFHIR("MedicationRequest", url.Values{
		"patient": {uuid}, "_count": {fmt.Sprint(medsFHIRCount)},
	})
	if err == nil {
		entries := asSlice(bundle["entry"])
		// One drug can carry many still-active refill orders; the med
		// list wants unique drugs, most recent order winning.
		latest := map[string]map[string]any{}
		for _, e := range entries {
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
		return sectionFromItems("fhir", items, fhirBundleCapped(bundle, entries, medsFHIRCount))
	}

	output.Warn("FHIR unavailable for medications; falling back to REST orders")
	data, err := c.Get("order", url.Values{
		"patient": {uuid}, "v": {"default"}, "limit": {fmt.Sprint(medsRESTLimit)},
	})
	if err != nil {
		return &section{Status: statusForError(err), Source: "rest-orders", Items: []any{}, Error: err.Error()}
	}
	raw := asSlice(data["results"])
	var items []any
	for _, r := range raw {
		rec, _ := r.(map[string]any)
		if rec["type"] == "drugorder" && rec["action"] != "DISCONTINUE" && rec["dateStopped"] == nil {
			items = append(items, rec)
		}
	}
	return sectionFromItems("rest-orders", items, restPageCapped(data, raw, medsRESTLimit))
}

// vitalsSection returns the latest observation per vital-sign code via
// FHIR. Servers whose concepts lack vital-signs category mappings
// legitimately return none.
func vitalsSection(c *client.Client, uuid string) *section {
	bundle, err := c.GetFHIR("Observation", url.Values{
		"patient": {uuid}, "category": {"vital-signs"}, "_sort": {"-date"},
		"_count": {fmt.Sprint(vitalsFHIRCount)},
	})
	if err != nil {
		return &section{Status: statusForError(err), Source: "fhir", Items: []any{}, Error: err.Error()}
	}
	entries := asSlice(bundle["entry"])
	seen := map[string]bool{}
	var items []any
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		res, _ := entry["resource"].(map[string]any)
		code := output.Extract(res, "code.text")
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		items = append(items, res)
	}
	return sectionFromItems("fhir", items, fhirBundleCapped(bundle, entries, vitalsFHIRCount))
}

// encountersSection fetches all encounters, keeps the most recent N, and
// attaches each one's observations.
func encountersSection(c *client.Client, uuid string) *section {
	data, err := c.GetAll("encounter", url.Values{"patient": {uuid}, "v": {"default"}}, encountersGetAllCap)
	if err != nil {
		return &section{Status: statusForError(err), Source: "rest", Items: []any{}, Error: err.Error()}
	}
	encs := asSlice(data["results"])
	listTruncated := data["truncated"] == true
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
	var mu sync.Mutex
	partial := false
	truncated := listTruncated
	for _, e := range encs {
		enc, _ := e.(map[string]any)
		wg.Add(1)
		go func(enc map[string]any) {
			defer wg.Done()
			encUUID, _ := enc["uuid"].(string)
			obsData, err := c.Get("obs", url.Values{
				"encounter": {encUUID}, "v": {"default"},
				"limit": {fmt.Sprint(obsPerEncounterLimit)},
			})
			if err != nil {
				// A failed obs fetch must not masquerade as "no obs
				// recorded" — mark it unavailable.
				enc["obs"] = []any{}
				enc["obsStatus"] = obsStatusUnavailable
				enc["obsError"] = err.Error()
				mu.Lock()
				partial = true
				mu.Unlock()
				return
			}
			obs := asSlice(obsData["results"])
			enc["obs"] = obs
			if restPageCapped(obsData, obs, obsPerEncounterLimit) {
				enc["obsStatus"] = obsStatusPartial
				mu.Lock()
				partial = true
				truncated = true
				mu.Unlock()
				return
			}
			enc["obsStatus"] = obsStatusOK
		}(enc)
	}
	wg.Wait()

	status := statusOK
	if len(encs) == 0 {
		status = statusNone
		encs = []any{}
	}
	s := &section{Status: status, Source: "rest", Items: encs, Partial: partial || truncated, Truncated: truncated}
	return s
}

// sectionFromItems builds a successful section. Empty after filters is
// status none; a hit fetch cap always sets Partial+Truncated even when
// the filtered list is empty (none does not mean "complete empty chart").
func sectionFromItems(source string, items []any, capped bool) *section {
	if items == nil {
		items = []any{}
	}
	status := statusOK
	if len(items) == 0 {
		status = statusNone
	}
	return &section{
		Status:    status,
		Source:    source,
		Items:     items,
		Partial:   capped,
		Truncated: capped,
	}
}

// fhirBundleCapped is true when the response filled _count or advertises
// a next page — either means we may not have seen the full resource set.
func fhirBundleCapped(bundle map[string]any, entries []any, count int) bool {
	if len(entries) >= count {
		return true
	}
	for _, l := range asSlice(bundle["link"]) {
		lm, _ := l.(map[string]any)
		rel, _ := lm["relation"].(string)
		if rel == "" {
			rel, _ = lm["rel"].(string)
		}
		if strings.EqualFold(rel, "next") {
			return true
		}
	}
	return false
}

// restPageCapped is true when the page filled limit, the client marked
// truncated, or a next link is present.
func restPageCapped(page map[string]any, results []any, limit int) bool {
	if page["truncated"] == true {
		return true
	}
	if len(results) >= limit {
		return true
	}
	for _, l := range asSlice(page["links"]) {
		lm, _ := l.(map[string]any)
		if lm["rel"] == "next" {
			return true
		}
	}
	return false
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
		if s.Truncated {
			fmt.Println("  (partial — results may be incomplete; fetch cap reached)")
		} else if s.Partial {
			fmt.Println("  (partial — one or more nested fetches failed)")
		}
		switch s.Status {
		case statusUnavailable:
			fmt.Printf("  (unavailable: %s)\n", s.Error)
		case statusWithheld:
			fmt.Println("  (withheld: the server denied access)")
		case statusConfirmedNone:
			fmt.Println("  No known allergies (confirmed in the record)")
		case statusNone:
			if name == "allergies" {
				fmt.Println("  Not assessed (no allergy information recorded)")
			} else {
				fmt.Println("  (none recorded)")
			}
		default:
			for _, item := range s.Items {
				rec, _ := item.(map[string]any)
				fmt.Printf("  • %s\n", line(rec))
				if name == "encounters" {
					switch rec["obsStatus"] {
					case obsStatusUnavailable:
						fmt.Println("      (observations unavailable — fetch failed)")
						continue
					case obsStatusPartial:
						fmt.Println("      (observations partial — fetch cap reached)")
					}
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
