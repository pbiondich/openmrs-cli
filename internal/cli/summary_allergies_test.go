package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// allergiesSection is the only producer of confirmed-none, the one true
// confident-negative in the summary. These tests pin the clinical
// distinction: an empty list is "not assessed" (none) unless the record
// explicitly asserts "No known allergies" (confirmed-none).

func allergyHandler(t *testing.T, listStatus int, listResults []any, allergyStatus string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/allergy"):
			if listStatus >= 400 {
				w.WriteHeader(listStatus)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": map[string]any{"message": "denied"},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": listResults})
		case strings.Contains(r.URL.RawQuery, "allergyStatus"):
			_ = json.NewEncoder(w).Encode(map[string]any{"allergyStatus": allergyStatus})
		default:
			http.NotFound(w, r)
		}
	}
}

func TestAllergiesRecordedItemsAreOK(t *testing.T) {
	c := testClient(t, allergyHandler(t, 200, []any{
		map[string]any{"uuid": "a1", "display": "Penicillin — severe"},
	}, "See list"))
	s := allergiesSection(c, "patient-uuid")
	if s.Status != statusOK || len(s.Items) != 1 {
		t.Fatalf("status=%s items=%d", s.Status, len(s.Items))
	}
}

func TestAllergiesEmptyWithUnknownStatusIsNoneNotConfirmed(t *testing.T) {
	c := testClient(t, allergyHandler(t, 200, []any{}, "Unknown"))
	s := allergiesSection(c, "patient-uuid")
	if s.Status != statusNone {
		t.Fatalf("never-assessed patient must be status none, got %s", s.Status)
	}
}

func TestAllergiesEmptyWithNKAAssertionIsConfirmedNone(t *testing.T) {
	c := testClient(t, allergyHandler(t, 200, []any{}, "No known allergies"))
	s := allergiesSection(c, "patient-uuid")
	if s.Status != statusConfirmedNone {
		t.Fatalf("recorded NKA must be confirmed-none, got %s", s.Status)
	}
}

func TestAllergiesForbiddenIsWithheld(t *testing.T) {
	c := testClient(t, allergyHandler(t, http.StatusForbidden, nil, ""))
	s := allergiesSection(c, "patient-uuid")
	if s.Status != statusWithheld {
		t.Fatalf("403 must be withheld, got %s", s.Status)
	}
}

func TestAllergiesServerErrorIsUnavailable(t *testing.T) {
	c := testClient(t, allergyHandler(t, http.StatusInternalServerError, nil, ""))
	s := allergiesSection(c, "patient-uuid")
	if s.Status != statusUnavailable {
		t.Fatalf("500 must be unavailable, got %s", s.Status)
	}
	if s.Error == "" {
		t.Fatal("unavailable section must carry the error")
	}
}

func TestAllergiesStatusFetchFailureStaysNone(t *testing.T) {
	// The assertion lookup failing must not upgrade none to
	// confirmed-none — absence of proof is not proof of absence.
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/allergy") {
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	s := allergiesSection(c, "patient-uuid")
	if s.Status != statusNone {
		t.Fatalf("got %s", s.Status)
	}
}
