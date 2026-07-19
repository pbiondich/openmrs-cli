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

func TestResolvePatientExactIdentifier(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{
				map[string]any{
					"uuid":    "u1",
					"display": "Alice",
					"identifiers": []any{
						map[string]any{"identifier": "1001HPV"},
					},
				},
				map[string]any{
					"uuid":    "u2",
					"display": "Bob",
					"identifiers": []any{
						map[string]any{"identifier": "OTHER"},
					},
				},
			},
		})
	})
	p, err := resolvePatient(c, "1001HPV")
	if err != nil {
		t.Fatal(err)
	}
	if p["uuid"] != "u1" {
		t.Fatalf("%v", p)
	}
}

func TestResolvePatientAmbiguous(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
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
