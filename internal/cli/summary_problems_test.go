package cli

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestConditionIsActive(t *testing.T) {
	cases := []struct {
		status string
		want   bool
	}{
		{"ACTIVE", true},
		{"active", true},
		{"INACTIVE", false},
		{"HISTORY_OF", false},
		{"", true}, // unknown: keep
	}
	for _, tc := range cases {
		rec := map[string]any{"clinicalStatus": tc.status}
		if got := conditionIsActive(rec); got != tc.want {
			t.Errorf("status %q: got %v want %v", tc.status, got, tc.want)
		}
	}
}

func TestProblemsSectionFiltersInactive(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/rest/v1/condition" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{
				map[string]any{
					"uuid":           "a",
					"clinicalStatus": "ACTIVE",
					"display":        "Asthma",
				},
				map[string]any{
					"uuid":           "b",
					"clinicalStatus": "INACTIVE",
					"display":        "Old fracture",
				},
				map[string]any{
					"uuid":           "c",
					"clinicalStatus": "HISTORY_OF",
					"display":        "Childhood asthma",
				},
			},
		})
	})
	s := problemsSection(c, "patient-uuid")
	if s.Status != statusOK || len(s.Items) != 1 {
		t.Fatalf("%+v items=%d", s, len(s.Items))
	}
	if s.Items[0].(map[string]any)["uuid"] != "a" {
		t.Fatalf("%v", s.Items[0])
	}
}

func TestProblemsSectionAllInactiveIsNone(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{
				map[string]any{"clinicalStatus": "INACTIVE", "display": "x"},
			},
		})
	})
	s := problemsSection(c, "p")
	if s.Status != statusNone || len(s.Items) != 0 {
		t.Fatalf("%+v", s)
	}
}

func TestSectionCount(t *testing.T) {
	if sectionCount(&section{Status: statusOK, Items: []any{1, 2}}) != 2 {
		t.Fatal("ok")
	}
	if sectionCount(&section{Status: statusNone, Items: []any{}}) != 0 {
		t.Fatal("none is 0")
	}
	if sectionCount(&section{Status: statusUnavailable, Items: []any{}}) != nil {
		t.Fatal("unavailable must be null")
	}
	if sectionCount(&section{Status: statusWithheld, Items: []any{}}) != nil {
		t.Fatal("withheld must be null")
	}
	if sectionCount(&section{Status: statusConfirmedNone, Items: []any{}}) != 0 {
		t.Fatal("confirmed-none is 0")
	}
}
