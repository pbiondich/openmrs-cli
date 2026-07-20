package cli

import (
	"strings"
	"testing"
)

func TestIsInstancePath(t *testing.T) {
	uuid := "dd8e5b3d-1691-11df-97a5-7038c432aabf"
	cases := []struct {
		path string
		want bool
	}{
		{"patient/" + uuid, true},
		{"/patient/" + uuid, true},
		{"patient/" + uuid + "/", true},
		{"PATIENT/" + strings.ToUpper(uuid), true},
		{"patient", false},
		{"patient/" + uuid + "/encounter", false},
		{"obs", false},
		{"patient/not-a-uuid", false},
		{"", false},
		{"/", false},
		{"patient/" + uuid + "/something/" + uuid, false},
	}
	for _, tc := range cases {
		if got := isInstancePath(tc.path); got != tc.want {
			t.Errorf("isInstancePath(%q)=%v want %v", tc.path, got, tc.want)
		}
	}
}

func TestBuildGetQuery(t *testing.T) {
	path, params, err := buildGetQuery("patient?q=john&limit=3", nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != "patient" {
		t.Fatalf("path=%q", path)
	}
	if params.Get("q") != "john" || params.Get("limit") != "3" {
		t.Fatalf("params=%v", params)
	}

	path, params, err = buildGetQuery("obs", []string{"patient=abc", "concept=def"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "obs" || params.Get("patient") != "abc" || params.Get("concept") != "def" {
		t.Fatalf("path=%q params=%v", path, params)
	}

	// Inline + flags merge; flags can add a second value for the same key.
	path, params, err = buildGetQuery("concept?q=malaria", []string{"v=full", "q=extra"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "concept" {
		t.Fatalf("path=%q", path)
	}
	if got := params["q"]; len(got) != 2 || got[0] != "malaria" || got[1] != "extra" {
		t.Fatalf("q values=%v", got)
	}
	if params.Get("v") != "full" {
		t.Fatalf("v=%q", params.Get("v"))
	}
}

func TestBuildGetQueryRejectsBadParam(t *testing.T) {
	for _, bad := range []string{"noseconds", "=value", ""} {
		_, _, err := buildGetQuery("patient", []string{bad})
		if err == nil {
			t.Fatalf("expected error for %q", bad)
		}
		if !strings.Contains(err.Error(), "--param must be key=value") {
			t.Fatalf("message=%v", err)
		}
	}
}
