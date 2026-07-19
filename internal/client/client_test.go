package client

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pbiondich/openmrs-cli/internal/config"
)

func TestExitCode(t *testing.T) {
	cases := map[string]int{
		CodeAuth:       2,
		CodeConnection: 3,
		CodeNotFound:   4,
		CodeBadRequest: 5,
		CodeForbidden:  6,
		CodeUsage:      1,
		CodeUnknown:    1,
		"OTHER":        1,
	}
	for code, want := range cases {
		if got := ExitCode(code); got != want {
			t.Errorf("ExitCode(%q)=%d want %d", code, got, want)
		}
	}
}

func TestAPIErrorFromResponse(t *testing.T) {
	body := []byte(`{"error":{"message":"Not allowed","detail":"line1\nstack"}}`)
	err := apiErrorFromResponse(http.StatusForbidden, body)
	if err.Code != CodeForbidden {
		t.Fatalf("code=%q want FORBIDDEN", err.Code)
	}
	if err.HTTPStatus != 403 {
		t.Fatalf("status=%d", err.HTTPStatus)
	}
	if err.Message != "Not allowed" {
		t.Fatalf("message=%q", err.Message)
	}
	if err.Detail != "line1" {
		t.Fatalf("detail=%q want first line only", err.Detail)
	}

	auth := apiErrorFromResponse(http.StatusUnauthorized, nil)
	if auth.Code != CodeAuth {
		t.Fatalf("401 code=%q want AUTH", auth.Code)
	}
	nf := apiErrorFromResponse(http.StatusNotFound, nil)
	if nf.Code != CodeNotFound {
		t.Fatalf("404 code=%q", nf.Code)
	}
}

func TestGetMapsStatusCodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openmrs/ws/rest/v1/ok":
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
		case "/openmrs/ws/rest/v1/denied":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"nope"}}`))
		case "/openmrs/ws/rest/v1/unauth":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	c := New(config.Resolved{URL: srv.URL + "/openmrs", User: "u", Password: "p"})

	if _, err := c.Get("ok", nil); err != nil {
		t.Fatalf("ok: %v", err)
	}

	_, err := c.Get("denied", nil)
	var api *APIError
	if err == nil || !asAPI(err, &api) || api.Code != CodeForbidden {
		t.Fatalf("denied: err=%v", err)
	}

	_, err = c.Get("unauth", nil)
	if err == nil || !asAPI(err, &api) || api.Code != CodeAuth {
		t.Fatalf("unauth: err=%v", err)
	}
}

func asAPI(err error, target **APIError) bool {
	if e, ok := err.(*APIError); ok {
		*target = e
		return true
	}
	return false
}

func TestGetAllPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		switch page {
		case "":
			next := srvURL(r) + "/ws/rest/v1/items?page=2"
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []any{map[string]any{"id": "1"}, map[string]any{"id": "2"}},
				"links":   []any{map[string]any{"rel": "next", "uri": next}},
			})
		case "2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []any{map[string]any{"id": "3"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c := New(config.Resolved{URL: srv.URL})
	out, err := c.GetAll("items", nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	results := out["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("got %d results", len(results))
	}
	if out["truncated"] == true {
		t.Fatal("should not be truncated")
	}
}

func srvURL(r *http.Request) string {
	return "http://" + r.Host
}

func TestGetAllRejectsOffOriginNext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{map[string]any{"id": "1"}},
			"links": []any{map[string]any{
				"rel": "next",
				"uri": "https://evil.example/steal",
			}},
		})
	}))
	t.Cleanup(srv.Close)

	c := New(config.Resolved{URL: srv.URL, User: "u", Password: "secret"})
	_, err := c.GetAll("items", nil, 100)
	if err == nil {
		t.Fatal("expected off-origin next to fail")
	}
	var api *APIError
	if !asAPI(err, &api) || api.Code != CodeBadRequest {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), "off-origin") {
		t.Fatalf("message=%q", err.Error())
	}
}

func TestGetAllItemCapSetsTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{
				map[string]any{"id": "1"},
				map[string]any{"id": "2"},
				map[string]any{"id": "3"},
			},
		})
	}))
	t.Cleanup(srv.Close)

	c := New(config.Resolved{URL: srv.URL})
	out, err := c.GetAll("items", nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(out["results"].([]any)) != 2 {
		t.Fatalf("len=%d", len(out["results"].([]any)))
	}
	if out["truncated"] != true {
		t.Fatal("expected truncated")
	}
}

func TestGetAllEmptyPageStops(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		next := srvURL(r) + "/ws/rest/v1/items?loop=1"
		if n == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []any{map[string]any{"id": "1"}},
				"links":   []any{map[string]any{"rel": "next", "uri": next}},
			})
			return
		}
		// Empty results but sticky next — must not loop forever.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{},
			"links":   []any{map[string]any{"rel": "next", "uri": next}},
		})
	}))
	t.Cleanup(srv.Close)

	c := New(config.Resolved{URL: srv.URL})
	out, err := c.GetAll("items", nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits=%d want 2", hits.Load())
	}
	if len(out["results"].([]any)) != 1 {
		t.Fatalf("results=%v", out["results"])
	}
}

func TestSanitizeNextURLRelative(t *testing.T) {
	c := New(config.Resolved{URL: "https://demo.example/openmrs"})
	got, err := c.sanitizeNextURL("/openmrs/ws/rest/v1/patient?startIndex=25")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "https://demo.example/") {
		t.Fatalf("got %q", got)
	}
}

func TestSanitizeNextURLRejectsUserinfo(t *testing.T) {
	c := New(config.Resolved{URL: "https://demo.example/openmrs"})
	_, err := c.sanitizeNextURL("https://user:pass@demo.example/openmrs/ws/rest/v1/x")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestGetQueryParams(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	}))
	t.Cleanup(srv.Close)

	c := New(config.Resolved{URL: srv.URL})
	params := url.Values{}
	params.Set("q", "john")
	params.Set("limit", "5")
	if _, err := c.Get("patient", params); err != nil {
		t.Fatal(err)
	}
	if got.Get("q") != "john" || got.Get("limit") != "5" {
		t.Fatalf("query=%v", got)
	}
}
