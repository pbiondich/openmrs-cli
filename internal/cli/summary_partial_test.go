package cli

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestSectionFromItemsCappedEmptyIsNonePartialTruncated(t *testing.T) {
	s := sectionFromItems("fhir", nil, true)
	if s.Status != statusNone || !s.Partial || !s.Truncated {
		t.Fatalf("%+v", s)
	}
	s = sectionFromItems("fhir", []any{map[string]any{"x": 1}}, true)
	if s.Status != statusOK || !s.Partial || !s.Truncated {
		t.Fatalf("%+v", s)
	}
	s = sectionFromItems("fhir", []any{}, false)
	if s.Status != statusNone || s.Partial || s.Truncated {
		t.Fatalf("%+v", s)
	}
}

func TestFHIRBundleCapped(t *testing.T) {
	entries := make([]any, medsFHIRCount)
	for i := range entries {
		entries[i] = map[string]any{}
	}
	if !fhirBundleCapped(map[string]any{}, entries, medsFHIRCount) {
		t.Fatal("full page should cap")
	}
	if fhirBundleCapped(map[string]any{}, entries[:1], medsFHIRCount) {
		t.Fatal("short page should not cap")
	}
	withNext := map[string]any{
		"link": []any{map[string]any{"relation": "next", "url": "http://x"}},
	}
	if !fhirBundleCapped(withNext, []any{map[string]any{}}, medsFHIRCount) {
		t.Fatal("next link should cap")
	}
}

func TestRESTPageCapped(t *testing.T) {
	results := make([]any, medsRESTLimit)
	if !restPageCapped(map[string]any{}, results, medsRESTLimit) {
		t.Fatal("full limit should cap")
	}
	if !restPageCapped(map[string]any{"truncated": true}, []any{}, medsRESTLimit) {
		t.Fatal("truncated flag should cap")
	}
	withNext := map[string]any{
		"links": []any{map[string]any{"rel": "next", "uri": "http://x"}},
	}
	if !restPageCapped(withNext, []any{map[string]any{}}, medsRESTLimit) {
		t.Fatal("next link should cap")
	}
	if restPageCapped(map[string]any{}, []any{map[string]any{}}, medsRESTLimit) {
		t.Fatal("short page should not cap")
	}
}

func TestMedsSectionTruncatedOnFullFHIRPage(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/fhir2/R4/MedicationRequest" {
			http.NotFound(w, r)
			return
		}
		var entry []any
		for i := 0; i < medsFHIRCount; i++ {
			entry = append(entry, map[string]any{
				"resource": map[string]any{
					"status":                    "completed", // filtered out → empty items
					"authoredOn":                "2026-01-01",
					"medicationCodeableConcept": map[string]any{"text": "drug"},
				},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resourceType": "Bundle",
			"entry":        entry,
		})
	})
	s := medsSection(c, "patient-uuid")
	if s.Status != statusNone {
		t.Fatalf("status=%s want none", s.Status)
	}
	if !s.Partial || !s.Truncated {
		t.Fatalf("want partial+truncated, got partial=%v truncated=%v", s.Partial, s.Truncated)
	}
	if len(s.Items) != 0 {
		t.Fatalf("items=%d", len(s.Items))
	}
}

func TestVitalsSectionNotTruncatedOnShortPage(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resourceType": "Bundle",
			"entry": []any{
				map[string]any{
					"resource": map[string]any{
						"code":              map[string]any{"text": "Weight"},
						"valueQuantity":     map[string]any{"value": 70.0, "unit": "kg"},
						"effectiveDateTime": "2026-01-01",
					},
				},
			},
		})
	})
	s := vitalsSection(c, "patient-uuid")
	if s.Status != statusOK || s.Partial || s.Truncated {
		t.Fatalf("%+v", s)
	}
	if len(s.Items) != 1 {
		t.Fatalf("items=%d", len(s.Items))
	}
}

func TestEncountersObsPartialOnFullPage(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == "/ws/rest/v1/encounter":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []any{
					map[string]any{
						"uuid":              "enc-1",
						"encounterDatetime": "2026-06-01T10:00:00.000-0500",
						"encounterType":     map[string]any{"display": "Visit"},
						"location":          map[string]any{"display": "Ward"},
					},
				},
			})
		case path == "/ws/rest/v1/obs":
			var results []any
			for i := 0; i < obsPerEncounterLimit; i++ {
				results = append(results, map[string]any{"uuid": "o", "display": "obs"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
		default:
			http.NotFound(w, r)
		}
	})
	// Force a single recent encounter.
	old := summaryEncounters
	summaryEncounters = 5
	t.Cleanup(func() { summaryEncounters = old })

	s := encountersSection(c, "patient-uuid")
	if !s.Partial || !s.Truncated {
		t.Fatalf("want partial+truncated: %+v", s)
	}
	if len(s.Items) != 1 {
		t.Fatalf("encs=%d", len(s.Items))
	}
	enc := s.Items[0].(map[string]any)
	if enc["obsStatus"] != obsStatusPartial {
		t.Fatalf("obsStatus=%v", enc["obsStatus"])
	}
}
