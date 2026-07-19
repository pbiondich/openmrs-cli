package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pbiondich/openmrs-cli/internal/client"
	"github.com/pbiondich/openmrs-cli/internal/config"
)

func testClient(t *testing.T, h http.HandlerFunc) *client.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return client.New(config.Resolved{URL: srv.URL})
}

func TestResolvePatientByUUID(t *testing.T) {
	uuid := "dd8e5b3d-1691-11df-97a5-7038c432aabf"
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, uuid) {
			t.Errorf("path=%s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"uuid":    uuid,
			"display": "Test Patient",
		})
	})
	p, err := resolvePatient(c, uuid)
	if err != nil {
		t.Fatal(err)
	}
	if p["uuid"] != uuid {
		t.Fatalf("%v", p)
	}
}

func TestResolvePatientUsesIdentifierParamFirst(t *testing.T) {
	var sawIdentifier, sawQ bool
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("identifier") == "1001HPV" {
			sawIdentifier = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []any{
					map[string]any{
						"uuid":    "from-id",
						"display": "Alice",
						"identifiers": []any{
							map[string]any{"identifier": "1001HPV"},
						},
					},
				},
			})
			return
		}
		if q.Get("q") != "" {
			sawQ = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []any{
					map[string]any{"uuid": "from-q", "display": "Wrong", "identifiers": []any{}},
				},
			})
			return
		}
		http.NotFound(w, r)
	})
	p, err := resolvePatient(c, "1001HPV")
	if err != nil {
		t.Fatal(err)
	}
	if !sawIdentifier {
		t.Fatal("expected identifier= query")
	}
	if sawQ {
		t.Fatal("must not fall through to q= when identifier= hits")
	}
	if p["uuid"] != "from-id" {
		t.Fatalf("%v", p)
	}
}

func TestResolvePatientIdentifierNotInFuzzyTopPage(t *testing.T) {
	// Classic failure mode of the old code: exact MRN only appears via
	// identifier=, not in the first page of q=.
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("identifier") == "RARE-MRN-99" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []any{
					map[string]any{
						"uuid":    "rare",
						"display": "Rare Patient",
						"identifiers": []any{
							map[string]any{"identifier": "RARE-MRN-99"},
						},
					},
				},
			})
			return
		}
		if q.Get("q") == "RARE-MRN-99" {
			// Fuzzy page full of unrelated names, no exact ID match.
			var results []any
			for i := 0; i < 10; i++ {
				results = append(results, map[string]any{
					"uuid":        "other-" + string(rune('a'+i)),
					"display":     "Other",
					"identifiers": []any{},
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
			return
		}
		http.NotFound(w, r)
	})
	p, err := resolvePatient(c, "RARE-MRN-99")
	if err != nil {
		t.Fatal(err)
	}
	if p["uuid"] != "rare" {
		t.Fatalf("got %v, want rare patient from identifier=", p)
	}
}

func TestResolvePatientAmbiguousIdentifier(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("identifier") == "SHARED" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []any{
					map[string]any{"uuid": "u1", "display": "A"},
					map[string]any{"uuid": "u2", "display": "B"},
				},
			})
			return
		}
		http.NotFound(w, r)
	})
	_, err := resolvePatient(c, "SHARED")
	var api *client.APIError
	if err == nil || !asAPIErr(err, &api) || api.Code != client.CodeBadRequest {
		t.Fatalf("err=%v", err)
	}
}

func TestResolvePatientAmbiguousName(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("identifier") != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{
				map[string]any{"uuid": "u1", "display": "John A", "identifiers": []any{}},
				map[string]any{"uuid": "u2", "display": "John B", "identifiers": []any{}},
			},
		})
	})
	_, err := resolvePatient(c, "john")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	var api *client.APIError
	if !asAPIErr(err, &api) || api.Code != client.CodeBadRequest {
		t.Fatalf("err=%v", err)
	}
}

func TestResolvePatientNotFound(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	})
	_, err := resolvePatient(c, "NO-SUCH-MRN")
	var api *client.APIError
	if err == nil || !asAPIErr(err, &api) || api.Code != client.CodeNotFound {
		t.Fatalf("err=%v", err)
	}
}

func TestResolvePatientUniqueName(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("identifier") != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{
				map[string]any{"uuid": "only", "display": "Unique Name", "identifiers": []any{}},
			},
		})
	})
	p, err := resolvePatient(c, "Unique Name")
	if err != nil {
		t.Fatal(err)
	}
	if p["uuid"] != "only" {
		t.Fatalf("%v", p)
	}
}

func TestResolvePatientEmptyRef(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not call server")
	})
	_, err := resolvePatient(c, "  ")
	var api *client.APIError
	if err == nil || !asAPIErr(err, &api) || api.Code != client.CodeBadRequest {
		t.Fatalf("err=%v", err)
	}
}

func TestStatusForError(t *testing.T) {
	if statusForError(&client.APIError{Code: client.CodeForbidden, HTTPStatus: 403}) != statusWithheld {
		t.Fatal("403 should be withheld")
	}
	if statusForError(&client.APIError{Code: client.CodeAuth, HTTPStatus: 401}) != statusUnavailable {
		t.Fatal("401 should be unavailable at section level")
	}
	if statusForError(&client.APIError{Code: client.CodeConnection}) != statusUnavailable {
		t.Fatal("connection -> unavailable")
	}
}

func asAPIErr(err error, target **client.APIError) bool {
	if e, ok := err.(*client.APIError); ok {
		*target = e
		return true
	}
	return false
}
