package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/pbiondich/openmrs-cli/internal/output"
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
		case strings.Contains(path, "/ws/fhir2/R4/Encounter"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resourceType": "Bundle",
				"entry": []any{
					map[string]any{
						"resource": map[string]any{
							"resourceType": "Encounter",
							"id":           "enc-1",
							"period":       map[string]any{"start": "2026-06-01T10:00:00-05:00"},
							"type":         []any{map[string]any{"text": "Visit"}},
							"location": []any{
								map[string]any{"location": map[string]any{"display": "Ward"}},
							},
						},
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
	old := summaryEncounters
	summaryEncounters = 5
	t.Cleanup(func() { summaryEncounters = old })

	s := encountersSection(c, "patient-uuid")
	if s.Source != "fhir" {
		t.Fatalf("source=%s", s.Source)
	}
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

func TestFetchRecentEncountersFHIRNotGetAll(t *testing.T) {
	var restEncounterHits int
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.Contains(path, "/ws/fhir2/R4/Encounter") {
			if r.URL.Query().Get("_sort") != "-date" {
				t.Errorf("_sort=%q", r.URL.Query().Get("_sort"))
			}
			if r.URL.Query().Get("_count") != "3" {
				t.Errorf("_count=%q", r.URL.Query().Get("_count"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resourceType": "Bundle",
				"entry": []any{
					map[string]any{"resource": map[string]any{
						"id": "e-new", "period": map[string]any{"start": "2026-06-02"},
						"type": []any{map[string]any{"text": "New"}},
					}},
					map[string]any{"resource": map[string]any{
						"id": "e-old", "period": map[string]any{"start": "2026-01-01"},
						"type": []any{map[string]any{"text": "Old"}},
					}},
				},
			})
			return
		}
		if path == "/ws/rest/v1/encounter" {
			restEncounterHits++
		}
		http.NotFound(w, r)
	})
	encs, source, truncated, err := fetchRecentEncounters(c, "p", 3)
	if err != nil {
		t.Fatal(err)
	}
	if source != "fhir" || restEncounterHits != 0 {
		t.Fatalf("source=%s restHits=%d", source, restEncounterHits)
	}
	if truncated {
		t.Fatal("short FHIR page should not truncate")
	}
	if len(encs) != 2 {
		t.Fatalf("encs=%d", len(encs))
	}
	if encs[0].(map[string]any)["uuid"] != "e-new" {
		t.Fatalf("order: %v", encs[0])
	}
}

func TestFetchRecentEncountersRESTFallback(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/ws/fhir2/") {
			http.Error(w, "no fhir", 404)
			return
		}
		if r.URL.Path == "/ws/rest/v1/encounter" {
			if r.URL.Query().Get("limit") != "2" {
				t.Errorf("limit=%q want 2 (not a bulk GetAll)", r.URL.Query().Get("limit"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []any{
					map[string]any{"uuid": "a", "encounterDatetime": "2026-01-01"},
					map[string]any{"uuid": "b", "encounterDatetime": "2026-06-01"},
				},
			})
			return
		}
		http.NotFound(w, r)
	})
	encs, source, _, err := fetchRecentEncounters(c, "p", 2)
	if err != nil {
		t.Fatal(err)
	}
	if source != "rest" {
		t.Fatalf("source=%s", source)
	}
	// Newest first after client sort.
	if encs[0].(map[string]any)["uuid"] != "b" {
		t.Fatalf("%v", encs[0])
	}
}

func TestFHIREncounterToSummary(t *testing.T) {
	enc := fhirEncounterToSummary(map[string]any{
		"id":     "u1",
		"period": map[string]any{"start": "2026-03-01T12:00:00Z"},
		"type":   []any{map[string]any{"text": "Adult Visit"}},
		"location": []any{
			map[string]any{"location": map[string]any{"display": "Clinic"}},
		},
	})
	if enc["uuid"] != "u1" || enc["encounterDatetime"] != "2026-03-01T12:00:00Z" {
		t.Fatalf("%v", enc)
	}
	if output.Extract(enc, "encounterType.display") != "Adult Visit" {
		t.Fatalf("%v", enc)
	}
	if output.Extract(enc, "location.display") != "Clinic" {
		t.Fatalf("%v", enc)
	}
}
