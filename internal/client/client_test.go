package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestSanitizeNextURLRejectsNonAPIPath(t *testing.T) {
	c := New(config.Resolved{URL: "https://demo.example/openmrs"})
	_, err := c.sanitizeNextURL("https://demo.example/admin")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSanitizeNextURLRejectsDotDotEscape(t *testing.T) {
	c := New(config.Resolved{URL: "https://demo.example/openmrs"})
	// Contains /ws/rest/ as a substring before Clean; after Clean becomes /openmrs/admin.
	_, err := c.sanitizeNextURL("https://demo.example/openmrs/ws/rest/v1/../../admin")
	if err == nil {
		t.Fatal("expected error for .. escape from API path")
	}
	// Legitimate relative segment under rest still OK.
	got, err := c.sanitizeNextURL("https://demo.example/openmrs/ws/rest/v1/patient/../encounter")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "/ws/rest/") {
		t.Fatalf("got %q", got)
	}
}

func TestIsAllowedAPIPath(t *testing.T) {
	if !isAllowedAPIPath("/openmrs", "/openmrs/ws/rest/v1/patient") {
		t.Fatal("rest under base")
	}
	if !isAllowedAPIPath("/openmrs", "/openmrs/ws/fhir2/R4/Patient") {
		t.Fatal("fhir under base")
	}
	if isAllowedAPIPath("/openmrs", "/openmrs/admin") {
		t.Fatal("admin must fail")
	}
	if isAllowedAPIPath("/openmrs", "/openmrs/ws/rest/v1/../../admin") {
		// Cleaned form is checked by callers after Clean; this raw path still
		// has .. until Clean — isAllowedAPIPath cleans internally.
		t.Fatal("dot-dot escape must fail after clean")
	}
}

func TestOffOriginRedirectRefused(t *testing.T) {
	// First hop 302 to evil host; client must not follow.
	var hops atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(evil.Close)

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/steal", http.StatusFound)
	}))
	t.Cleanup(origin.Close)

	c := New(config.Resolved{URL: origin.URL, User: "u", Password: "p"})
	_, err := c.Get("session", nil)
	if err == nil {
		t.Fatal("expected redirect error")
	}
	if hops.Load() != 0 {
		t.Fatalf("evil host was contacted %d times", hops.Load())
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

func TestDoCanceledContext(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(2 * time.Second)
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	}))
	t.Cleanup(srv.Close)

	// No client timeout so cancel is what stops the wait.
	c := NewWithHTTP(config.Resolved{URL: srv.URL}, &http.Client{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	_, err := c.GetContext(ctx, "slow", nil)
	if err == nil {
		t.Fatal("expected cancel error")
	}
	var api *APIError
	if !asAPI(err, &api) || api.Code != CodeUnknown {
		t.Fatalf("err=%v", err)
	}
	if !strings.Contains(err.Error(), "cancel") {
		t.Fatalf("message=%q", err.Error())
	}
}

func TestWithContextPropagates(t *testing.T) {
	var sawCancel atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		sawCancel.Store(true)
	}))
	t.Cleanup(srv.Close)

	c := NewWithHTTP(config.Resolved{URL: srv.URL}, &http.Client{})
	ctx, cancel := context.WithCancel(context.Background())
	c = c.WithContext(ctx)
	cancel()
	_, err := c.Get("x", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDoPOSTMethod(t *testing.T) {
	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	t.Cleanup(srv.Close)

	c := New(config.Resolved{URL: srv.URL})
	out, err := c.Do(context.Background(), http.MethodPost, srv.URL+"/ws/rest/v1/x", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost {
		t.Fatalf("method=%s", method)
	}
	if out["ok"] != true {
		t.Fatalf("%v", out)
	}
}

func TestGetAllExactCapIsNotTruncated(t *testing.T) {
	// Exactly cap items with no next link: complete, never "truncated".
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{
				map[string]any{"uuid": "a"}, map[string]any{"uuid": "b"},
			},
		})
	}))
	defer srv.Close()
	c := New(config.Resolved{URL: srv.URL})
	out, err := c.GetAll("thing", url.Values{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if out["truncated"] == true {
		t.Fatal("exact-cap complete fetch must not claim truncated")
	}
	if len(out["results"].([]any)) != 2 {
		t.Fatalf("%v", out["results"])
	}
}

func TestGetAllCapWithNextLinkIsTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{map[string]any{"uuid": "a"}, map[string]any{"uuid": "b"}},
			"links": []any{map[string]any{
				"rel": "next", "uri": srvURL(r) + "/ws/rest/v1/thing?startIndex=2",
			}},
		})
	}))
	defer srv.Close()
	c := New(config.Resolved{URL: srv.URL})
	out, err := c.GetAll("thing", url.Values{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if out["truncated"] != true {
		t.Fatal("cap hit with a next page advertised must be truncated")
	}
}
