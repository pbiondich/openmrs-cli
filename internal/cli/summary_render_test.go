package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	body, _ := io.ReadAll(r)
	return string(body)
}

// TestRenderSummaryHumanPanel pins the human rendering of every status
// the six-state model can produce, in one panel.
func TestRenderSummaryHumanPanel(t *testing.T) {
	patient := map[string]any{
		"uuid": "p-uuid",
		"person": map[string]any{
			"display": "Ada Lovelace", "gender": "F",
			"age": float64(36), "birthdate": "1990-01-01T00:00:00.000+0000",
		},
		"identifiers": []any{map[string]any{"display": "OpenMRS ID = 42-X"}},
	}
	sections := map[string]*section{
		"problems": {Status: statusOK, Source: "rest", Items: []any{
			map[string]any{"display": "Malaria", "clinicalStatus": "ACTIVE"},
		}},
		"meds":      {Status: statusUnavailable, Source: "fhir", Items: []any{}, Error: "boom"},
		"allergies": {Status: statusConfirmedNone, Source: "rest", Items: []any{}},
		"vitals":    {Status: statusWithheld, Source: "fhir", Items: []any{}, Error: "denied"},
		"programs":  {Status: statusNone, Source: "rest", Items: []any{}},
		"encounters": {Status: statusOK, Source: "rest", Partial: true, Items: []any{
			map[string]any{
				"uuid":              "e1",
				"encounterDatetime": "2026-07-01T10:00:00.000+0000",
				"encounterType":     map[string]any{"display": "Checkup"},
				"location":          map[string]any{"display": "Ward A"},
				"obsStatus":         obsStatusUnavailable,
				"obs":               []any{},
			},
			map[string]any{
				"uuid":              "e2",
				"encounterDatetime": "2026-06-01T10:00:00.000+0000",
				"encounterType":     map[string]any{"display": "Checkup"},
				"location":          map[string]any{"display": "Ward A"},
				"obsStatus":         obsStatusOK,
				"obs": []any{
					map[string]any{"display": "Pulse: 72"},
				},
			},
		}},
	}

	out := captureStdout(t, func() { renderSummary(patient, sections) })

	for _, want := range []string{
		"Ada Lovelace",
		"OpenMRS ID = 42-X",
		"Malaria — active",
		"(unavailable: boom)",                          // meds
		"No known allergies (confirmed in the record)", // allergies
		"(withheld: the server denied access)",         // vitals
		"(none recorded)",                              // programs
		"(partial — one or more nested fetches failed)",
		"(observations unavailable — fetch failed)", // encounter e1
		"Pulse: 72", // encounter e2 obs
		"2026-07-01",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("panel missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderSummaryDeceasedBanner(t *testing.T) {
	patient := map[string]any{
		"uuid": "p",
		"person": map[string]any{
			"display": "X", "gender": "M", "age": float64(80),
			"birthdate": "1946-01-01", "dead": true,
			"deathDate": "2026-01-15T00:00:00.000+0000",
		},
	}
	out := captureStdout(t, func() { renderSummary(patient, map[string]*section{}) })
	if !strings.Contains(out, "DECEASED 2026-01-15") {
		t.Fatalf("deceased banner missing:\n%s", out)
	}
}
