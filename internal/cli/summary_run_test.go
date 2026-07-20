package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/pbiondich/openmrs-cli/internal/client"
)

// summaryMux is a minimal OpenMRS-shaped server for runPatientSummary.
// It covers REST resolve + section paths and FHIR2 fallbacks with empty
// or tiny payloads so the orchestrator can be exercised end-to-end.
func summaryMux(t *testing.T, patientUUID string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		write := func(v any) {
			_ = json.NewEncoder(w).Encode(v)
		}
		emptyREST := map[string]any{"results": []any{}}
		emptyFHIR := map[string]any{"resourceType": "Bundle", "type": "searchset", "entry": []any{}}

		switch {
		// Patient by UUID (resolve + allergyStatus custom).
		case strings.Contains(path, "/ws/rest/v1/patient/"+patientUUID):
			if strings.HasSuffix(path, "/allergy") {
				write(emptyREST)
				return
			}
			// custom:(allergyStatus) or full patient
			p := map[string]any{
				"uuid":    patientUUID,
				"display": "Summary Test Patient",
				"person": map[string]any{
					"display": "Summary Test Patient",
					"gender":  "M",
					"age":     40,
				},
				"identifiers": []any{
					map[string]any{"display": "OpenMRS ID = 1000A", "identifier": "1000A"},
				},
				"allergyStatus": "Unknown",
			}
			write(p)

		case strings.Contains(path, "/ws/rest/v1/visit"):
			write(map[string]any{
				"results": []any{
					map[string]any{
						"uuid":           "visit-1",
						"display":        "Facility Visit",
						"startDatetime":  "2026-07-01T10:00:00.000-0500",
						"visitType":      map[string]any{"display": "Facility Visit"},
						"location":       map[string]any{"display": "Ward"},
						"stopDatetime":   nil,
					},
				},
			})

		case strings.Contains(path, "/ws/rest/v1/condition"):
			write(map[string]any{
				"results": []any{
					map[string]any{
						"uuid":           "cond-active",
						"clinicalStatus": "ACTIVE",
						"display":        "Malaria",
						"condition":      map[string]any{"coded": map[string]any{"display": "Malaria"}},
					},
					map[string]any{
						"uuid":           "cond-hist",
						"clinicalStatus": "HISTORY_OF",
						"display":        "Old broken arm",
					},
				},
			})

		case strings.Contains(path, "/ws/rest/v1/programenrollment"):
			write(emptyREST)

		case strings.Contains(path, "/ws/rest/v1/order"):
			write(emptyREST)

		case strings.Contains(path, "/ws/rest/v1/encounter"):
			write(emptyREST)

		case strings.Contains(path, "/ws/rest/v1/obs"):
			write(emptyREST)

		case strings.Contains(path, "/ws/fhir2/R4/MedicationRequest"):
			write(emptyFHIR)

		case strings.Contains(path, "/ws/fhir2/R4/Observation"):
			write(emptyFHIR)

		case strings.Contains(path, "/ws/fhir2/R4/Encounter"):
			// Force REST encounter fallback for one path exercised in partial tests.
			w.WriteHeader(http.StatusNotFound)
			write(map[string]any{"resourceType": "OperationOutcome"})

		default:
			t.Logf("summaryMux unhandled %s %s", r.Method, path)
			http.NotFound(w, r)
		}
	}
}

func TestRunPatientSummaryJSONIntegration(t *testing.T) {
	uuid := "dd8e5b3d-1691-11df-97a5-7038c432aabf"
	withResolvedServer(t, summaryMux(t, uuid))

	oldSections, oldEnc, oldJSON := summarySections, summaryEncounters, flags.jsonOut
	t.Cleanup(func() {
		summarySections, summaryEncounters, flags.jsonOut = oldSections, oldEnc, oldJSON
	})
	// Limit sections so the test stays focused on orchestration + a few
	// real section behaviors (active-problem filter, empty allergies).
	summarySections = "visit,problems,allergies,programs"
	summaryEncounters = 5
	flags.jsonOut = true

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout, oldStderr := os.Stdout, os.Stderr
	// Discard stderr warnings (FHIR fallbacks, etc.).
	devNull, _ := os.Open(os.DevNull)
	os.Stdout = w
	os.Stderr = devNull
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	runErr := runPatientSummary(cmd, []string{uuid})
	_ = w.Close()
	_ = devNull.Close()
	os.Stdout, os.Stderr = oldStdout, oldStderr
	if runErr != nil {
		t.Fatal(runErr)
	}
	body, _ := io.ReadAll(r)

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("json: %v body=%s", err, body)
	}
	patient, _ := out["patient"].(map[string]any)
	if patient["uuid"] != uuid {
		t.Fatalf("patient=%v", patient)
	}
	counts, _ := out["counts"].(map[string]any)
	// Active visit: 1. Problems: only ACTIVE. Allergies empty→none: 0. Programs: 0.
	if counts["visit"] != float64(1) {
		t.Fatalf("counts.visit=%v", counts["visit"])
	}
	if counts["problems"] != float64(1) {
		t.Fatalf("counts.problems=%v (want 1 active only)", counts["problems"])
	}
	if counts["allergies"] != float64(0) {
		t.Fatalf("counts.allergies=%v", counts["allergies"])
	}
	if counts["programs"] != float64(0) {
		t.Fatalf("counts.programs=%v", counts["programs"])
	}
	// Unrequested sections must not appear.
	if _, ok := counts["meds"]; ok {
		t.Fatal("meds should not be fetched when omitted from --sections")
	}

	sections, _ := out["sections"].(map[string]any)
	problems, _ := sections["problems"].(map[string]any)
	if problems["status"] != statusOK {
		t.Fatalf("problems status=%v", problems["status"])
	}
	allergies, _ := sections["allergies"].(map[string]any)
	if allergies["status"] != statusNone {
		t.Fatalf("allergies status=%v (unknown allergyStatus → none, not confirmed-none)", allergies["status"])
	}
}

func TestRunPatientSummaryUnknownSection(t *testing.T) {
	uuid := "dd8e5b3d-1691-11df-97a5-7038c432aabf"
	// Client is built before section validation only after resolve — so we
	// still need a reachable server for the patient GET.
	withResolvedServer(t, summaryMux(t, uuid))

	oldSections := summarySections
	t.Cleanup(func() { summarySections = oldSections })
	summarySections = "problems,not-a-section"

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := runPatientSummary(cmd, []string{uuid})
	api, ok := err.(*client.APIError)
	if !ok || api.Code != client.CodeUsage {
		t.Fatalf("want USAGE, got %v", err)
	}
	if !strings.Contains(api.Message, "not-a-section") {
		t.Fatalf("message=%q", api.Message)
	}
}

func TestRunPatientSummaryPartialSectionFailure(t *testing.T) {
	uuid := "dd8e5b3d-1691-11df-97a5-7038c432aabf"
	withResolvedServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		if strings.Contains(path, "/ws/rest/v1/patient/"+uuid) && !strings.HasSuffix(path, "/allergy") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"uuid": uuid, "display": "P",
				"person": map[string]any{"display": "P"},
			})
			return
		}
		if strings.Contains(path, "/condition") {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "denied"},
			})
			return
		}
		if strings.Contains(path, "/allergy") {
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
			return
		}
		// Anything else: empty success so fan-out does not hang on errors.
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	})

	oldSections, oldJSON := summarySections, flags.jsonOut
	t.Cleanup(func() {
		summarySections, flags.jsonOut = oldSections, oldJSON
	})
	summarySections = "problems,allergies"
	flags.jsonOut = true

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout, oldStderr := os.Stdout, os.Stderr
	devNull, _ := os.Open(os.DevNull)
	os.Stdout = w
	os.Stderr = devNull
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	runErr := runPatientSummary(cmd, []string{uuid})
	_ = w.Close()
	_ = devNull.Close()
	os.Stdout, os.Stderr = oldStdout, oldStderr
	if runErr != nil {
		t.Fatal(runErr)
	}
	body, _ := io.ReadAll(r)
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	counts, _ := out["counts"].(map[string]any)
	// Failed section: null, not 0.
	if counts["problems"] != nil {
		t.Fatalf("problems count want null, got %v", counts["problems"])
	}
	if counts["allergies"] != float64(0) {
		t.Fatalf("allergies count=%v", counts["allergies"])
	}
	sections, _ := out["sections"].(map[string]any)
	problems, _ := sections["problems"].(map[string]any)
	if problems["status"] != statusWithheld {
		t.Fatalf("problems status=%v want withheld", problems["status"])
	}
}
