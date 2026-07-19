package cli

import "testing"

func TestCodeableConceptKey(t *testing.T) {
	cc := map[string]any{
		"text": "Weight (kg)",
		"coding": []any{
			map[string]any{
				"system": "https://openmrs.org/concepts",
				"code":   "5089AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			},
		},
	}
	got := codeableConceptKey(cc)
	want := "https://openmrs.org/concepts|5089AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if codeableConceptKey(map[string]any{"text": "only"}) != "" {
		t.Fatal("no coding should be empty")
	}
}

func TestFHIRObsIdentityPrefersCodingOverText(t *testing.T) {
	a := map[string]any{
		"code": map[string]any{
			"text": "Weight",
			"coding": []any{
				map[string]any{"system": "https://ciel", "code": "5089"},
			},
		},
	}
	b := map[string]any{
		"code": map[string]any{
			"text": "Body weight", // different label, same concept
			"coding": []any{
				map[string]any{"system": "https://ciel", "code": "5089"},
			},
		},
	}
	if fhirObsIdentityKey(a) != fhirObsIdentityKey(b) {
		t.Fatal("same coding must be same key")
	}
	c := map[string]any{
		"code": map[string]any{
			"text": "Weight",
			"coding": []any{
				map[string]any{"system": "https://ciel", "code": "5090"},
			},
		},
	}
	if fhirObsIdentityKey(a) == fhirObsIdentityKey(c) {
		t.Fatal("different codes must not collapse")
	}
}

func TestFHIRObsIdentityKeepsCodingWithoutText(t *testing.T) {
	res := map[string]any{
		"code": map[string]any{
			"coding": []any{
				map[string]any{"system": "https://ciel", "code": "5089"},
			},
		},
	}
	if fhirObsIdentityKey(res) != "https://ciel|5089" {
		t.Fatalf("%q", fhirObsIdentityKey(res))
	}
}

func TestFHIRMedIdentityPrefersReferenceAndCoding(t *testing.T) {
	byRef := map[string]any{
		"medicationReference": map[string]any{
			"reference": "Medication/abc",
			"display":   "Paracetamol",
		},
	}
	if fhirMedIdentityKey(byRef) != "ref|Medication/abc" {
		t.Fatalf("%q", fhirMedIdentityKey(byRef))
	}

	sameCode := map[string]any{
		"medicationCodeableConcept": map[string]any{
			"text": "Paracetamol 500mg",
			"coding": []any{
				map[string]any{"system": "https://ciel", "code": "70116"},
			},
		},
	}
	sameCodeOtherText := map[string]any{
		"medicationCodeableConcept": map[string]any{
			"text": "Acetaminophen",
			"coding": []any{
				map[string]any{"system": "https://ciel", "code": "70116"},
			},
		},
	}
	if fhirMedIdentityKey(sameCode) != fhirMedIdentityKey(sameCodeOtherText) {
		t.Fatal("same concept coding must match")
	}
}

func TestRESTOrderIdentityUsesConceptUUID(t *testing.T) {
	rec := map[string]any{
		"uuid":    "order-1",
		"display": "Paracetamol",
		"concept": map[string]any{"uuid": "concept-xyz"},
	}
	if restOrderIdentityKey(rec) != "concept|concept-xyz" {
		t.Fatalf("%q", restOrderIdentityKey(rec))
	}
	// Same concept, different order → same key (dedupe).
	rec2 := map[string]any{
		"uuid":    "order-2",
		"concept": map[string]any{"uuid": "concept-xyz"},
	}
	if restOrderIdentityKey(rec) != restOrderIdentityKey(rec2) {
		t.Fatal("orders for same concept should share key")
	}
}

func TestMedsDedupByConceptNotDisplay(t *testing.T) {
	// Simulate the collapse map used in medsSection.
	orders := []map[string]any{
		{
			"status":     "active",
			"authoredOn": "2026-01-01",
			"medicationCodeableConcept": map[string]any{
				"text": "Paracetamol",
				"coding": []any{
					map[string]any{"system": "https://ciel", "code": "70116"},
				},
			},
		},
		{
			"status":     "active",
			"authoredOn": "2026-06-01", // newer wins
			"medicationCodeableConcept": map[string]any{
				"text": "Acetaminophen 500mg",
				"coding": []any{
					map[string]any{"system": "https://ciel", "code": "70116"},
				},
			},
		},
		{
			"status":     "active",
			"authoredOn": "2026-03-01",
			"medicationCodeableConcept": map[string]any{
				"text": "Paracetamol", // same display, different concept
				"coding": []any{
					map[string]any{"system": "https://ciel", "code": "99999"},
				},
			},
		},
	}
	latest := map[string]map[string]any{}
	for _, res := range orders {
		key := fhirMedIdentityKey(res)
		prev, seen := latest[key]
		if !seen || res["authoredOn"].(string) > prev["authoredOn"].(string) {
			latest[key] = res
		}
	}
	if len(latest) != 2 {
		t.Fatalf("want 2 distinct concepts, got %d keys %v", len(latest), latest)
	}
	// Newer paracetamol/acetaminophen row wins for 70116.
	k := fhirMedIdentityKey(orders[0])
	if latest[k]["authoredOn"] != "2026-06-01" {
		t.Fatalf("newer order should win: %v", latest[k])
	}
}
