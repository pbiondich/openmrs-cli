package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/pbiondich/openmrs-cli/internal/client"
	"github.com/pbiondich/openmrs-cli/internal/output"
)

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

// problemsSection lists active conditions only. Inactive / history-of
// rows are not "current problems" and must not pad the problem list.
func problemsSection(c *client.Client, uuid string) *section {
	data, err := c.Get("condition", url.Values{"patientUuid": {uuid}, "v": {"default"}})
	if err != nil {
		return &section{Status: statusForError(err), Source: "rest", Items: []any{}, Error: err.Error()}
	}
	var active []any
	for _, r := range asSlice(data["results"]) {
		rec, _ := r.(map[string]any)
		if conditionIsActive(rec) {
			active = append(active, rec)
		}
	}
	return sectionFromItems("rest", active, false)
}

// conditionIsActive reports whether a Condition looks current. OpenMRS
// clinicalStatus is typically ACTIVE / INACTIVE / HISTORY_OF.
func conditionIsActive(rec map[string]any) bool {
	st := strings.ToUpper(strings.TrimSpace(output.Extract(rec, "clinicalStatus")))
	// Missing status: keep the row (safer than dropping unknowns silently).
	if st == "" {
		return true
	}
	return st == "ACTIVE"
}

// sectionCount is the item length for ok/none/confirmed-none sections,
// or nil when the section failed so counts never look like empty data.
func sectionCount(s *section) any {
	if s == nil {
		return nil
	}
	switch s.Status {
	case statusUnavailable, statusWithheld:
		return nil
	default:
		return len(s.Items)
	}
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
// back to REST orders on FHIR error *or* when FHIR returns no active meds.
// An empty FHIR success must not claim pure "none" without checking REST —
// half-configured fhir2 often yields an empty bundle while orders exist.
// Identity for "same drug" is concept/coding/reference — not display text.
func medsSection(c *client.Client, uuid string) *section {
	bundle, err := c.GetFHIR("MedicationRequest", url.Values{
		"patient": {uuid}, "_count": {fmt.Sprint(medsFHIRCount)},
	})
	if err == nil {
		items, capped := activeMedsFromFHIRBundle(bundle)
		if len(items) > 0 {
			return sectionFromItems("fhir", items, capped)
		}
		// Empty active list: prefer REST over a confident FHIR none.
		// If the FHIR page was capped, keep that incompleteness signal
		// when REST is also empty.
		if capped {
			output.Warn("FHIR medications page was full but no active meds matched; trying REST orders")
		} else {
			output.Warn("FHIR returned no active medications; trying REST orders")
		}
		rest := medsFromREST(c, uuid)
		if rest.Status == statusUnavailable || rest.Status == statusWithheld {
			// REST failed: surface FHIR empty with any cap flags rather than
			// losing the successful FHIR read entirely.
			if capped {
				return sectionFromItems("fhir", items, true)
			}
			return rest
		}
		if len(rest.Items) > 0 {
			return rest
		}
		// Both empty.
		if capped || rest.Truncated {
			return sectionFromItems("rest-orders", []any{}, true)
		}
		return sectionFromItems("fhir", []any{}, false)
	}

	output.Warn("FHIR unavailable for medications; falling back to REST orders")
	return medsFromREST(c, uuid)
}

func activeMedsFromFHIRBundle(bundle map[string]any) (items []any, capped bool) {
	entries := asSlice(bundle["entry"])
	latest := map[string]map[string]any{}
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		res, _ := entry["resource"].(map[string]any)
		if res["status"] != "active" {
			continue
		}
		key := fhirMedIdentityKey(res)
		prev, seen := latest[key]
		if !seen || output.Extract(res, "authoredOn") > output.Extract(prev, "authoredOn") {
			latest[key] = res
		}
	}
	for _, res := range latest {
		items = append(items, res)
	}
	sort.Slice(items, func(i, j int) bool {
		a, _ := items[i].(map[string]any)
		b, _ := items[j].(map[string]any)
		return medsDisplay(a) < medsDisplay(b)
	})
	return items, fhirBundleCapped(bundle, entries, medsFHIRCount)
}

func medsFromREST(c *client.Client, uuid string) *section {
	data, err := c.Get("order", url.Values{
		"patient": {uuid}, "v": {"default"}, "limit": {fmt.Sprint(medsRESTLimit)},
	})
	if err != nil {
		return &section{Status: statusForError(err), Source: "rest-orders", Items: []any{}, Error: err.Error()}
	}
	raw := asSlice(data["results"])
	latest := map[string]map[string]any{}
	for _, r := range raw {
		rec, _ := r.(map[string]any)
		if rec["type"] != "drugorder" || rec["action"] == "DISCONTINUE" || rec["dateStopped"] != nil {
			continue
		}
		key := restOrderIdentityKey(rec)
		prev, seen := latest[key]
		if !seen || output.Extract(rec, "dateActivated") > output.Extract(prev, "dateActivated") {
			latest[key] = rec
		}
	}
	var items []any
	for _, rec := range latest {
		items = append(items, rec)
	}
	sort.Slice(items, func(i, j int) bool {
		a, _ := items[i].(map[string]any)
		b, _ := items[j].(map[string]any)
		return output.Extract(a, "display") < output.Extract(b, "display")
	})
	return sectionFromItems("rest-orders", items, restPageCapped(data, raw, medsRESTLimit))
}

// vitalsSection returns the latest observation per vital-sign concept via
// FHIR. Identity is coding system+code, not code.text.
//
// Empty FHIR success is ambiguous (none recorded vs missing vital-signs
// category mappings). When the bundle is empty we try a second FHIR query
// without the category filter and keep only resources that still look like
// vitals (category coding or LOINC/common openmrs vital pattern is too
// site-specific) — instead we take newest-per-concept from uncategorized
// Observations and mark the section partial so agents do not treat it as a
// clean vital-signs panel. Prefer the categorized result whenever it has data.
func vitalsSection(c *client.Client, uuid string) *section {
	bundle, err := c.GetFHIR("Observation", url.Values{
		"patient": {uuid}, "category": {"vital-signs"}, "_sort": {"-date"},
		"_count": {fmt.Sprint(vitalsFHIRCount)},
	})
	if err != nil {
		return &section{Status: statusForError(err), Source: "fhir", Items: []any{}, Error: err.Error()}
	}
	items, capped := latestObsFromFHIRBundle(bundle, vitalsFHIRCount)
	if len(items) > 0 {
		return sectionFromItems("fhir", items, capped)
	}
	if capped {
		// Full page of non-dedupable junk is still incomplete.
		return sectionFromItems("fhir", items, true)
	}

	// Empty vital-signs category: try uncategorized observations so a
	// mis-mapped dictionary does not look like "never measured".
	output.Warn("FHIR vital-signs category returned no observations; trying uncategorized FHIR Observations")
	fb, ferr := c.GetFHIR("Observation", url.Values{
		"patient": {uuid}, "_sort": {"-date"},
		"_count": {fmt.Sprint(vitalsFHIRCount)},
	})
	if ferr != nil {
		return sectionFromItems("fhir", []any{}, false)
	}
	fbItems, fbCapped := latestObsFromFHIRBundle(fb, vitalsFHIRCount)
	if len(fbItems) == 0 {
		return sectionFromItems("fhir", []any{}, fbCapped)
	}
	// Data found only without the vital-signs filter — useful but not a
	// clean vitals panel; always partial so status none/ok is not over-read.
	s := sectionFromItems("fhir-uncategorized", fbItems, fbCapped)
	s.Partial = true
	return s
}

func latestObsFromFHIRBundle(bundle map[string]any, count int) (items []any, capped bool) {
	entries := asSlice(bundle["entry"])
	seen := map[string]bool{}
	for _, e := range entries {
		entry, _ := e.(map[string]any)
		res, _ := entry["resource"].(map[string]any)
		key := fhirObsIdentityKey(res)
		if seen[key] {
			continue
		}
		seen[key] = true
		items = append(items, res)
	}
	return items, fhirBundleCapped(bundle, entries, count)
}

// fhirMedIdentityKey prefers Medication/Concept reference or coded concept
// over display labels so the dictionary — not the string — defines sameness.
func fhirMedIdentityKey(res map[string]any) string {
	if ref := strings.TrimSpace(output.Extract(res, "medicationReference.reference")); ref != "" {
		return "ref|" + ref
	}
	if cc, ok := res["medicationCodeableConcept"].(map[string]any); ok {
		if k := codeableConceptKey(cc); k != "" {
			return k
		}
		if t := strings.TrimSpace(output.Extract(cc, "text")); t != "" {
			return "text|" + t
		}
	}
	if d := strings.TrimSpace(output.Extract(res, "medicationReference.display")); d != "" {
		return "text|" + d
	}
	if id := strings.TrimSpace(output.Extract(res, "id")); id != "" {
		return "id|" + id
	}
	return "unknown|med"
}

// fhirObsIdentityKey keys a vital by its coded concept, not code.text.
func fhirObsIdentityKey(res map[string]any) string {
	if code, ok := res["code"].(map[string]any); ok {
		if k := codeableConceptKey(code); k != "" {
			return k
		}
		if t := strings.TrimSpace(output.Extract(code, "text")); t != "" {
			return "text|" + t
		}
	}
	if id := strings.TrimSpace(output.Extract(res, "id")); id != "" {
		return "id|" + id
	}
	return "unknown|obs"
}

// restOrderIdentityKey uses OpenMRS concept/drug UUIDs when the
// representation carries them.
func restOrderIdentityKey(rec map[string]any) string {
	if u := strings.TrimSpace(output.Extract(rec, "concept.uuid")); u != "" {
		return "concept|" + u
	}
	if u := strings.TrimSpace(output.Extract(rec, "drug.uuid")); u != "" {
		return "drug|" + u
	}
	if u := strings.TrimSpace(output.Extract(rec, "uuid")); u != "" {
		return "order|" + u
	}
	return "unknown|order"
}

// codeableConceptKey returns "system|code" from the first coding with a
// code (OpenMRS FHIR usually puts the concept uuid or mapping here).
func codeableConceptKey(cc map[string]any) string {
	for _, c := range asSlice(cc["coding"]) {
		cm, _ := c.(map[string]any)
		code, _ := cm["code"].(string)
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		sys, _ := cm["system"].(string)
		return strings.TrimSpace(sys) + "|" + code
	}
	return ""
}

func medsDisplay(res map[string]any) string {
	return output.Extract(res, "medicationReference.display|medicationCodeableConcept.text|display")
}

// encountersSection loads only the most recent N encounters (default 5),
// not the patient's full history. Prefers FHIR Encounter with _sort=-date;
// falls back to a single REST page of size N (not GetAll up to thousands).
func encountersSection(c *client.Client, uuid string) *section {
	n := summaryEncounters
	if n < 1 {
		n = 5
	}
	encs, source, listTruncated, err := fetchRecentEncounters(c, uuid, n)
	if err != nil {
		return &section{Status: statusForError(err), Source: source, Items: []any{}, Error: err.Error()}
	}

	partial, truncated := attachEncounterObs(c, encs)
	if listTruncated {
		truncated = true
		partial = true
	}

	status := statusOK
	if len(encs) == 0 {
		status = statusNone
		encs = []any{}
	}
	return &section{
		Status:    status,
		Source:    source,
		Items:     encs,
		Partial:   partial,
		Truncated: truncated,
	}
}

// fetchRecentEncounters returns up to n encounters newest-first.
func fetchRecentEncounters(c *client.Client, patientUUID string, n int) (encs []any, source string, truncated bool, err error) {
	bundle, ferr := c.GetFHIR("Encounter", url.Values{
		"patient": {patientUUID},
		"_sort":   {"-date"},
		"_count":  {fmt.Sprint(n)},
	})
	if ferr == nil {
		entries := asSlice(bundle["entry"])
		for _, e := range entries {
			entry, _ := e.(map[string]any)
			res, _ := entry["resource"].(map[string]any)
			if enc := fhirEncounterToSummary(res); enc != nil {
				encs = append(encs, enc)
			}
		}
		if len(encs) > n {
			encs = encs[:n]
		}
		return encs, "fhir", fhirBundleCapped(bundle, entries, n), nil
	}

	// REST: one page of size n, sort client-side. Without server sort we
	// only know these are n encounters, not necessarily the global newest
	// — if the page is full, mark truncated.
	output.Warn("FHIR unavailable for encounters; falling back to REST (single page)")
	data, rerr := c.Get("encounter", url.Values{
		"patient": {patientUUID},
		"v":       {"default"},
		"limit":   {fmt.Sprint(n)},
	})
	if rerr != nil {
		return nil, "rest", false, rerr
	}
	raw := asSlice(data["results"])
	sort.SliceStable(raw, func(i, j int) bool {
		a, _ := raw[i].(map[string]any)
		b, _ := raw[j].(map[string]any)
		da, _ := a["encounterDatetime"].(string)
		db, _ := b["encounterDatetime"].(string)
		return da > db
	})
	if len(raw) > n {
		raw = raw[:n]
	}
	return raw, "rest", restPageCapped(data, asSlice(data["results"]), n), nil
}

// fhirEncounterToSummary maps a FHIR Encounter into the REST-like shape
// the human renderer and obs attach path already expect.
func fhirEncounterToSummary(res map[string]any) map[string]any {
	if res == nil {
		return nil
	}
	id := strings.TrimSpace(output.Extract(res, "id"))
	if id == "" {
		return nil
	}
	when := output.Extract(res, "period.start|period.end")
	typeDisp := ""
	for _, t := range asSlice(res["type"]) {
		tm, _ := t.(map[string]any)
		if d := strings.TrimSpace(output.Extract(tm, "text")); d != "" {
			typeDisp = d
			break
		}
		if d := codeableConceptDisplay(tm); d != "" {
			typeDisp = d
			break
		}
	}
	locDisp := ""
	for _, l := range asSlice(res["location"]) {
		lm, _ := l.(map[string]any)
		if d := strings.TrimSpace(output.Extract(lm, "location.display")); d != "" {
			locDisp = d
			break
		}
	}
	return map[string]any{
		"uuid":              id,
		"encounterDatetime": when,
		"encounterType":     map[string]any{"display": typeDisp},
		"location":          map[string]any{"display": locDisp},
	}
}

func codeableConceptDisplay(cc map[string]any) string {
	if d := strings.TrimSpace(output.Extract(cc, "text")); d != "" {
		return d
	}
	for _, c := range asSlice(cc["coding"]) {
		cm, _ := c.(map[string]any)
		if d := strings.TrimSpace(output.Extract(cm, "display")); d != "" {
			return d
		}
	}
	return ""
}

// attachEncounterObs loads observations for each encounter in parallel.
func attachEncounterObs(c *client.Client, encs []any) (partial, truncated bool) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, e := range encs {
		enc, _ := e.(map[string]any)
		if enc == nil {
			continue
		}
		wg.Add(1)
		go func(enc map[string]any) {
			defer wg.Done()
			encUUID, _ := enc["uuid"].(string)
			obsData, err := c.Get("obs", url.Values{
				"encounter": {encUUID}, "v": {"default"},
				"limit": {fmt.Sprint(obsPerEncounterLimit)},
			})
			if err != nil {
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
	return partial, truncated
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
