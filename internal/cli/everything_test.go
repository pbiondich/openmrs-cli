package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/pbiondich/openmrs-cli/internal/client"
)

func TestCompactPatientAndObs(t *testing.T) {
	p := compactPatient(map[string]any{
		"uuid":    "p1",
		"display": "Jane Doe",
		"person": map[string]any{
			"display":   "Jane Doe",
			"gender":    "F",
			"birthdate": "1980-01-15T00:00:00.000+0000",
			"dead":      false,
		},
		"identifiers": []any{
			map[string]any{"identifier": "1000A", "display": "OpenMRS ID = 1000A"},
		},
	})
	if p["t"] != "Patient" || p["id"] != "p1" || p["name"] != "Jane Doe" || p["sex"] != "F" {
		t.Fatalf("%v", p)
	}
	if p["birthDate"] != "1980-01-15" {
		t.Fatalf("birthDate=%v", p["birthDate"])
	}

	o := compactObs(map[string]any{
		"uuid":        "o1",
		"obsDatetime": "2024-03-01T10:00:00.000-0500",
		"concept":     map[string]any{"uuid": "c-bp", "display": "Systolic BP"},
		"value":       120.0,
		"encounter":   map[string]any{"uuid": "e1"},
	})
	if o["t"] != "Observation" || o["enc"] != "e1" {
		t.Fatalf("%v", o)
	}
	val, _ := o["value"].(map[string]any)
	if val["n"] != 120.0 {
		t.Fatalf("value=%v", o["value"])
	}
}

func TestCompactOrderSkipsNonDrug(t *testing.T) {
	if compactOrder(map[string]any{"uuid": "x", "type": "testorder", "display": "CBC"}) != nil {
		t.Fatal("non-drug order should be skipped")
	}
	m := compactOrder(map[string]any{
		"uuid": "m1", "type": "drugorder", "display": "AL", "dateActivated": "2024-01-01",
	})
	if m == nil || m["t"] != "MedicationRequest" {
		t.Fatalf("%v", m)
	}
}

func TestFetchEverythingPackageIntegration(t *testing.T) {
	uuid := "dd8e5b3d-1691-11df-97a5-7038c432aabf"
	withResolvedServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		empty := map[string]any{"results": []any{}}

		switch {
		case strings.Contains(path, "/patient/"+uuid) && !strings.Contains(path, "allergy"):
			write(map[string]any{
				"uuid": uuid, "display": "Test",
				"person": map[string]any{"display": "Test", "gender": "M", "age": 30},
			})
		case strings.Contains(path, "/encounter"):
			write(map[string]any{"results": []any{
				map[string]any{
					"uuid": "enc1", "encounterDatetime": "2024-01-01T12:00:00.000Z",
					"encounterType": map[string]any{"display": "Visit"},
				},
			}})
		case strings.Contains(path, "/obs"):
			write(map[string]any{"results": []any{
				map[string]any{
					"uuid": "obs1", "obsDatetime": "2024-01-01T12:05:00.000Z",
					"concept": map[string]any{"display": "Weight", "uuid": "cw"},
					"value":   70.5,
				},
			}})
		case strings.Contains(path, "/condition"):
			write(map[string]any{"results": []any{
				map[string]any{
					"uuid": "cond1", "clinicalStatus": "ACTIVE",
					"display": "Malaria",
					"condition": map[string]any{"coded": map[string]any{"display": "Malaria"}},
				},
				map[string]any{
					"uuid": "cond2", "clinicalStatus": "HISTORY_OF",
					"display": "Old fracture",
				},
			}})
		case strings.Contains(path, "/allergy"):
			write(empty)
		case strings.Contains(path, "/order"):
			write(map[string]any{"results": []any{
				map[string]any{"uuid": "ord1", "type": "drugorder", "display": "Drug A"},
				map[string]any{"uuid": "ord2", "type": "testorder", "display": "Lab"},
			}})
		case strings.Contains(path, "/programenrollment"):
			write(empty)
		default:
			// patient resolve by uuid only hits patient path above
			http.NotFound(w, r)
		}
	})

	oldJSON := flags.jsonOut
	t.Cleanup(func() { flags.jsonOut = oldJSON })
	flags.jsonOut = true

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	devNull, _ := os.Open(os.DevNull)
	os.Stdout = w
	os.Stderr = devNull
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	runErr := runPatientEverything(cmd, []string{uuid})
	_ = w.Close()
	_ = devNull.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	if runErr != nil {
		t.Fatal(runErr)
	}
	body, _ := io.ReadAll(r)
	var pkg map[string]any
	if err := json.Unmarshal(body, &pkg); err != nil {
		t.Fatalf("%v body=%s", err, body)
	}
	if pkg["kind"] != "everything" || pkg["patient"] != uuid {
		t.Fatalf("%v", pkg)
	}
	n, _ := pkg["n"].(map[string]any)
	// HISTORY_OF condition included (recall, not summary filter).
	if int(n["Condition"].(float64)) != 2 {
		t.Fatalf("conditions n=%v (want 2 including history)", n["Condition"])
	}
	// Only drugorder projected.
	if int(n["MedicationRequest"].(float64)) != 1 {
		t.Fatalf("meds n=%v", n["MedicationRequest"])
	}
	entries, _ := pkg["e"].([]any)
	var types []string
	for _, e := range entries {
		m, _ := e.(map[string]any)
		types = append(types, m["t"].(string))
	}
	joined := strings.Join(types, ",")
	if !strings.Contains(joined, "Patient") || !strings.Contains(joined, "Encounter") {
		t.Fatalf("types=%v", types)
	}
}

func TestFetchEverythingListFailure(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "no"}})
	})
	res := fetchEverythingList(c, "Observation", "obs", url.Values{"patient": {"x"}}, 10, compactObs)
	if res.failed == nil {
		t.Fatal("expected failed")
	}
	if res.failed["code"] != client.CodeForbidden {
		t.Fatalf("%v", res.failed)
	}
}
