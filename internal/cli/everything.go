package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/pbiondich/openmrs-cli/internal/client"
	"github.com/pbiondich/openmrs-cli/internal/output"
)

// Default per-type caps for patient everything (REST composite package).
const (
	everythingVisitCap = 50
	everythingEncCap   = 50
	everythingObsCap   = 500
	everythingOrderCap = 100
	everythingCondCap  = 100
	everythingAllgCap  = 100
	everythingProgCap  = 50
	everythingMaxInFlight = 6
)

var (
	everythingCapVisit int
	everythingCapEnc   int
	everythingCapObs   int
	everythingCapOrder int
	everythingCapCond  int
	everythingCapAllg  int
	everythingCapProg  int
)

var patientEverythingCmd = &cobra.Command{
	Use:   "everything <identifier-or-uuid>",
	Short: "Compact REST package of a patient's data (FHIR-shaped types, high recall)",
	Long: `Assembles a capped, patient-scoped JSON package from OpenMRS REST and
projects it into a compact, typed entry list (see docs/json-output.md).

Unlike patient summary, this command prefers recall over clinical filtering:
inactive conditions and stopped orders are included when the server returns
them. It is shaped like a FHIR $everything bag of resources for familiarity,
but it is a REST composite for this CLI — not a FHIR server operation or an
OpenMRS community exchange format.

Entry types (t): Patient, Visit, Encounter, Observation, Condition,
AllergyIntolerance, MedicationRequest, EpisodeOfCare (program enrollments).
Visit is a deliberate divergence from FHIR naming: OpenMRS visits and
encounters are distinct resources on the server, and this package
reports site truth rather than remodeling it. Encounter entries carry a
visit reference when the server links them.

Global list flags (--limit, --all, --fields, --full, --ref, --start) are
not supported here; use the --cap-* flags instead.

Caps default to safe limits; hitting any cap sets truncated: true.`,
	Example: `  omrs patient everything 5574MO-2 --json
  omrs patient everything <uuid> --cap-obs 200 --json`,
	Args: cobra.ExactArgs(1),
	RunE: runPatientEverything,
}

func runPatientEverything(cmd *cobra.Command, args []string) error {
	if err := rejectUnsupportedGlobals(); err != nil {
		return err
	}
	c, err := newClient(cmd.Context())
	if err != nil {
		return err
	}

	patient, err := resolvePatient(c, args[0])
	if err != nil {
		return err
	}
	uuid, _ := patient["uuid"].(string)
	if uuid == "" {
		return &client.APIError{Message: "resolved patient has no uuid", Code: client.CodeUnknown}
	}

	caps := everythingCaps{
		Visit:     everythingCapVisit,
		Encounter: everythingCapEnc,
		Obs:       everythingCapObs,
		Order:     everythingCapOrder,
		Condition: everythingCapCond,
		Allergy:   everythingCapAllg,
		Program:   everythingCapProg,
	}
	normalizeEverythingCaps(&caps)

	pkg := fetchEverythingPackage(c, patient, uuid, caps)
	if outputMode() == output.ModeJSON {
		return output.Print(everythingPackageMap(pkg), output.ModeJSON, "")
	}
	renderEverything(pkg)
	return nil
}

// rejectUnsupportedGlobals fails loudly when list-shaping flags are
// passed: silently ignoring them would let --limit 3 look obeyed.
func rejectUnsupportedGlobals() error {
	for _, name := range []string{"limit", "all", "fields", "full", "ref", "start"} {
		f := rootCmd.PersistentFlags().Lookup(name)
		if f != nil && f.Changed {
			return &client.APIError{
				Message: fmt.Sprintf("--%s is not supported by patient everything; use the --cap-* flags", name),
				Code:    client.CodeUsage,
			}
		}
	}
	return nil
}

// everythingPackageMap converts the typed package to map[string]any for output.Print.
func everythingPackageMap(pkg everythingPackage) map[string]any {
	b, err := json.Marshal(pkg)
	if err != nil {
		return map[string]any{"kind": "everything", "error": err.Error()}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{"kind": "everything", "error": err.Error()}
	}
	return m
}

type everythingCaps struct {
	Visit, Encounter, Obs, Order, Condition, Allergy, Program int
}

func normalizeEverythingCaps(c *everythingCaps) {
	if c.Visit < 1 {
		c.Visit = everythingVisitCap
	}
	if c.Encounter < 1 {
		c.Encounter = everythingEncCap
	}
	if c.Obs < 1 {
		c.Obs = everythingObsCap
	}
	if c.Order < 1 {
		c.Order = everythingOrderCap
	}
	if c.Condition < 1 {
		c.Condition = everythingCondCap
	}
	if c.Allergy < 1 {
		c.Allergy = everythingAllgCap
	}
	if c.Program < 1 {
		c.Program = everythingProgCap
	}
}

// everythingPackage is the compact CLI JSON shape for patient everything.
type everythingPackage struct {
	Kind      string           `json:"kind"`
	Patient   string           `json:"patient"`
	Truncated bool             `json:"truncated,omitempty"`
	N         map[string]int   `json:"n"`
	Failed    []map[string]any `json:"failed,omitempty"`
	E         []map[string]any `json:"e"`
}

type everythingFetchResult struct {
	entries   []map[string]any
	t         string
	truncated bool
	failed    map[string]any // nil if ok
}

func fetchEverythingPackage(c *client.Client, patient map[string]any, uuid string, caps everythingCaps) everythingPackage {
	pkg := everythingPackage{
		Kind:    "everything",
		Patient: uuid,
		N:       map[string]int{},
		E:       []map[string]any{compactPatient(patient)},
	}
	pkg.N["Patient"] = 1

	jobs := []func() everythingFetchResult{
		func() everythingFetchResult {
			return fetchEverythingList(c, "Visit", "visit", url.Values{
				"patient": {uuid}, "v": {"default"},
			}, caps.Visit, compactVisit)
		},
		func() everythingFetchResult {
			return fetchEverythingList(c, "Encounter", "encounter", url.Values{
				"patient": {uuid}, "v": {"default"},
			}, caps.Encounter, compactEncounter)
		},
		func() everythingFetchResult {
			// Custom representation so numeric observations carry the
			// concept's units — a quantity without units is a clinical
			// hazard, not a compaction win.
			return fetchEverythingList(c, "Observation", "obs", url.Values{
				"patient": {uuid},
				"v":       {"custom:(uuid,obsDatetime,concept:(uuid,display,units),value:(uuid,display),encounter:(uuid))"},
			}, caps.Obs, compactObs)
		},
		func() everythingFetchResult {
			return fetchEverythingList(c, "Condition", "condition", url.Values{
				"patientUuid": {uuid}, "v": {"default"},
			}, caps.Condition, compactCondition)
		},
		func() everythingFetchResult {
			return fetchEverythingList(c, "AllergyIntolerance", "patient/"+uuid+"/allergy", url.Values{
				"v": {"default"},
			}, caps.Allergy, compactAllergy)
		},
		func() everythingFetchResult {
			return fetchEverythingList(c, "MedicationRequest", "order", url.Values{
				"patient": {uuid}, "v": {"default"},
			}, caps.Order, compactOrder)
		},
		func() everythingFetchResult {
			return fetchEverythingList(c, "EpisodeOfCare", "programenrollment", url.Values{
				"patient": {uuid}, "v": {"default"},
			}, caps.Program, compactProgram)
		},
	}

	sem := make(chan struct{}, everythingMaxInFlight)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, job := range jobs {
		job := job
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res := job()
			mu.Lock()
			defer mu.Unlock()
			if res.failed != nil {
				pkg.Failed = append(pkg.Failed, res.failed)
				return
			}
			if res.truncated {
				pkg.Truncated = true
			}
			pkg.E = append(pkg.E, res.entries...)
			if res.t != "" {
				pkg.N[res.t] = len(res.entries)
			}
		}()
	}
	wg.Wait()

	// Stable entry order: Patient first, then by type name, then id.
	sort.SliceStable(pkg.E, func(i, j int) bool {
		ti, _ := pkg.E[i]["t"].(string)
		tj, _ := pkg.E[j]["t"].(string)
		if ti == "Patient" && tj != "Patient" {
			return true
		}
		if tj == "Patient" && ti != "Patient" {
			return false
		}
		if ti != tj {
			return ti < tj
		}
		wi, _ := pkg.E[i]["when"].(string)
		wj, _ := pkg.E[j]["when"].(string)
		if wi != wj {
			return wi < wj
		}
		idi, _ := pkg.E[i]["id"].(string)
		idj, _ := pkg.E[j]["id"].(string)
		return idi < idj
	})
	if len(pkg.Failed) > 1 {
		sort.Slice(pkg.Failed, func(i, j int) bool {
			a, _ := pkg.Failed[i]["t"].(string)
			b, _ := pkg.Failed[j]["t"].(string)
			return a < b
		})
	}
	return pkg
}

func fetchEverythingList(
	c *client.Client,
	typeName, path string,
	params url.Values,
	cap int,
	compact func(map[string]any) map[string]any,
) everythingFetchResult {
	// Prefer GetAll up to cap so we fill the budget when the server paginates.
	data, err := c.GetAll(path, params, cap)
	if err != nil {
		fail := map[string]any{"t": typeName, "error": err.Error()}
		var api *client.APIError
		if errors.As(err, &api) {
			fail["code"] = api.Code
			if api.HTTPStatus != 0 {
				fail["httpStatus"] = api.HTTPStatus
			}
		}
		return everythingFetchResult{t: typeName, failed: fail}
	}
	raw := asSlice(data["results"])
	// GetAll reports truncation honestly (overshoot or a live next link);
	// len==cap alone is a complete result, not a truncated one.
	truncated := data["truncated"] == true
	if len(raw) > cap {
		raw = raw[:cap]
		truncated = true
	}
	entries := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		rec, _ := r.(map[string]any)
		if rec == nil {
			continue
		}
		if e := compact(rec); e != nil {
			entries = append(entries, e)
		}
	}
	return everythingFetchResult{t: typeName, entries: entries, truncated: truncated}
}

func compactPatient(rec map[string]any) map[string]any {
	e := map[string]any{"t": "Patient"}
	if id := strField(rec, "uuid"); id != "" {
		e["id"] = id
	}
	if name := output.Extract(rec, "person.display|display"); name != "" {
		e["name"] = name
	}
	if sex := output.Extract(rec, "person.gender"); sex != "" {
		e["sex"] = sex
	}
	if bd := dateOnly(output.Extract(rec, "person.birthdate")); bd != "" {
		e["birthDate"] = bd
	}
	if output.Extract(rec, "person.dead") == "true" {
		e["deceased"] = true
		if dd := dateOnly(output.Extract(rec, "person.deathDate")); dd != "" {
			e["deceasedDate"] = dd
		}
	}
	var ids []map[string]any
	for _, idAny := range asSlice(rec["identifiers"]) {
		id, _ := idAny.(map[string]any)
		row := map[string]any{}
		if v := output.Extract(id, "identifier"); v != "" {
			row["value"] = v
		}
		if v := output.Extract(id, "display"); v != "" {
			row["d"] = v
		}
		if len(row) > 0 {
			ids = append(ids, row)
		}
	}
	if len(ids) > 0 {
		e["identifiers"] = ids
	}
	return e
}

func compactVisit(rec map[string]any) map[string]any {
	e := map[string]any{"t": "Visit"}
	if id := strField(rec, "uuid"); id != "" {
		e["id"] = id
	}
	if typ := output.Extract(rec, "visitType.display"); typ != "" {
		e["type"] = typ
	}
	if loc := output.Extract(rec, "location.display"); loc != "" {
		e["location"] = loc
	}
	if s := output.Extract(rec, "startDatetime"); s != "" {
		e["start"] = s
	}
	if s := output.Extract(rec, "stopDatetime"); s != "" {
		e["end"] = s
	}
	return e
}

func compactEncounter(rec map[string]any) map[string]any {
	e := map[string]any{"t": "Encounter"}
	if id := strField(rec, "uuid"); id != "" {
		e["id"] = id
	}
	if when := output.Extract(rec, "encounterDatetime"); when != "" {
		e["when"] = when
	}
	if typ := output.Extract(rec, "encounterType.display"); typ != "" {
		e["type"] = typ
	}
	if loc := output.Extract(rec, "location.display"); loc != "" {
		e["location"] = loc
	}
	if v := output.Extract(rec, "visit.uuid"); v != "" {
		e["visit"] = v
	}
	return e
}

func compactObs(rec map[string]any) map[string]any {
	e := map[string]any{"t": "Observation"}
	if id := strField(rec, "uuid"); id != "" {
		e["id"] = id
	}
	if when := output.Extract(rec, "obsDatetime"); when != "" {
		e["when"] = when
	}
	if code := compactConcept(rec["concept"]); code != nil {
		e["code"] = code
	} else if d := output.Extract(rec, "concept.display|display"); d != "" {
		e["code"] = map[string]any{"d": d}
	}
	units := strings.TrimSpace(output.Extract(rec, "concept.units"))
	if v := compactObsValue(rec["value"], units); v != nil {
		e["value"] = v
	}
	if enc := output.Extract(rec, "encounter.uuid"); enc != "" {
		e["enc"] = enc
	}
	return e
}

func compactCondition(rec map[string]any) map[string]any {
	e := map[string]any{"t": "Condition"}
	if id := strField(rec, "uuid"); id != "" {
		e["id"] = id
	}
	if st := output.Extract(rec, "clinicalStatus"); st != "" {
		e["status"] = strings.ToLower(st)
	}
	if code := compactConcept(deepGet(rec, "condition", "coded")); code != nil {
		e["code"] = code
	} else if d := output.Extract(rec, "condition.coded.display|condition.nonCoded|display"); d != "" {
		e["code"] = map[string]any{"d": d}
	}
	// Clinical onset and record-creation time are different facts; never
	// pass audit metadata off as onset.
	if when := output.Extract(rec, "onsetDate"); when != "" {
		e["when"] = when
	} else if rec2 := output.Extract(rec, "dateCreated"); rec2 != "" {
		e["recorded"] = rec2
	}
	return e
}

func compactAllergy(rec map[string]any) map[string]any {
	e := map[string]any{"t": "AllergyIntolerance"}
	if id := strField(rec, "uuid"); id != "" {
		e["id"] = id
	}
	if d := output.Extract(rec, "display|allergen.codedAllergen.display|allergen.nonCodedAllergen"); d != "" {
		e["code"] = map[string]any{"d": d}
	}
	if sev := output.Extract(rec, "severity"); sev != "" {
		e["criticality"] = strings.ToLower(sev)
	}
	return e
}

func compactOrder(rec map[string]any) map[string]any {
	// Skip non-drug orders when type is present and not drugorder.
	if ts, ok := rec["type"].(string); ok && ts != "" && ts != "drugorder" {
		return nil
	}
	e := map[string]any{"t": "MedicationRequest"}
	if id := strField(rec, "uuid"); id != "" {
		e["id"] = id
	}
	if d := output.Extract(rec, "display|drug.display|concept.display"); d != "" {
		e["code"] = map[string]any{"d": d}
	}
	if when := output.Extract(rec, "dateActivated"); when != "" {
		e["when"] = when
	} else if rec2 := output.Extract(rec, "dateCreated"); rec2 != "" {
		e["recorded"] = rec2
	}
	// Surface stop/expire when present so callers can interpret "active".
	if action, _ := rec["action"].(string); action != "" {
		e["status"] = strings.ToLower(action)
	}
	if rec["dateStopped"] != nil {
		e["stopped"] = true
		if s := output.Extract(rec, "dateStopped"); s != "" {
			e["stoppedWhen"] = s
		}
	}
	if s := output.Extract(rec, "autoExpireDate"); s != "" {
		e["autoExpire"] = s
	}
	return e
}

func compactProgram(rec map[string]any) map[string]any {
	e := map[string]any{"t": "EpisodeOfCare"}
	if id := strField(rec, "uuid"); id != "" {
		e["id"] = id
	}
	if d := output.Extract(rec, "display|program.display"); d != "" {
		e["program"] = d
	}
	if s := output.Extract(rec, "dateEnrolled"); s != "" {
		e["start"] = s
	}
	if s := output.Extract(rec, "dateCompleted"); s != "" {
		e["end"] = s
	}
	return e
}

func compactConcept(v any) map[string]any {
	rec, _ := v.(map[string]any)
	if rec == nil {
		return nil
	}
	out := map[string]any{}
	if d := output.Extract(rec, "display|name.name|name"); d != "" {
		out["d"] = d
	}
	if u := strField(rec, "uuid"); u != "" {
		// OpenMRS concept uuid as coding when no external system is handy.
		out["c"] = u
		out["s"] = "https://openmrs.org/concept"
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func compactObsValue(v any, units string) any {
	if v == nil {
		return nil
	}
	quantity := func(n float64) map[string]any {
		q := map[string]any{"n": n}
		if units != "" {
			q["u"] = units
		}
		return q
	}
	switch x := v.(type) {
	case map[string]any:
		// Coded value: keep concept identity, not just the label.
		code := map[string]any{}
		if d := output.Extract(x, "display"); d != "" {
			code["d"] = d
		}
		if u := strField(x, "uuid"); u != "" {
			code["c"] = u
			code["s"] = "https://openmrs.org/concept"
		}
		if len(code) == 0 {
			return nil
		}
		return map[string]any{"code": code}
	case float64:
		return quantity(x)
	case float32:
		return quantity(float64(x))
	case int:
		return quantity(float64(x))
	case int64:
		return quantity(float64(x))
	case bool:
		return map[string]any{"b": x}
	case string:
		// Text obs stay text: a value_text of "10" is not a number,
		// and coercing it corrupts the record's type.
		if x == "" {
			return nil
		}
		return map[string]any{"s": x}
	default:
		s := fmt.Sprint(x)
		if s == "" || s == "<nil>" {
			return nil
		}
		return map[string]any{"s": s}
	}
}

func strField(rec map[string]any, key string) string {
	v, _ := rec[key].(string)
	return strings.TrimSpace(v)
}

func deepGet(rec map[string]any, keys ...string) any {
	var cur any = rec
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}

func renderEverything(pkg everythingPackage) {
	fmt.Printf("everything · patient %s", pkg.Patient)
	if pkg.Truncated {
		fmt.Print(" · truncated")
	}
	fmt.Println()
	// Type counts in stable order.
	types := make([]string, 0, len(pkg.N))
	for t := range pkg.N {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		fmt.Printf("  %s: %d\n", t, pkg.N[t])
	}
	for _, f := range pkg.Failed {
		fmt.Printf("  FAILED %s: %v\n", f["t"], f["error"])
	}
	fmt.Println()
	for _, e := range pkg.E {
		t, _ := e["t"].(string)
		line := compactEntryLine(e)
		fmt.Printf("  • [%s] %s\n", t, line)
	}
}

func compactEntryLine(e map[string]any) string {
	if id, _ := e["id"].(string); id != "" {
		if name, _ := e["name"].(string); name != "" {
			return name + " (" + id + ")"
		}
		if d := displayFromCode(e["code"]); d != "" {
			if when, _ := e["when"].(string); when != "" {
				return d + " · " + dateOnly(when) + " · " + id
			}
			return d + " · " + id
		}
		if p, _ := e["program"].(string); p != "" {
			return p + " · " + id
		}
		if typ, _ := e["type"].(string); typ != "" {
			if when, _ := e["when"].(string); when != "" {
				return typ + " · " + dateOnly(when) + " · " + id
			}
			return typ + " · " + id
		}
		return id
	}
	return fmt.Sprint(e)
}

func displayFromCode(v any) string {
	m, _ := v.(map[string]any)
	if m == nil {
		return ""
	}
	if d, _ := m["d"].(string); d != "" {
		return d
	}
	if c, _ := m["c"].(string); c != "" {
		return c
	}
	return ""
}

func init() {
	patientEverythingCmd.Flags().IntVar(&everythingCapVisit, "cap-visit", everythingVisitCap, "max visits")
	patientEverythingCmd.Flags().IntVar(&everythingCapEnc, "cap-encounter", everythingEncCap, "max encounters")
	patientEverythingCmd.Flags().IntVar(&everythingCapObs, "cap-obs", everythingObsCap, "max observations")
	patientEverythingCmd.Flags().IntVar(&everythingCapOrder, "cap-order", everythingOrderCap, "max drug orders")
	patientEverythingCmd.Flags().IntVar(&everythingCapCond, "cap-condition", everythingCondCap, "max conditions")
	patientEverythingCmd.Flags().IntVar(&everythingCapAllg, "cap-allergy", everythingAllgCap, "max allergies")
	patientEverythingCmd.Flags().IntVar(&everythingCapProg, "cap-program", everythingProgCap, "max program enrollments")
	patientCmd.AddCommand(patientEverythingCmd)
}
