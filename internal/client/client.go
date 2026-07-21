// Package client is the HTTP client for the OpenMRS REST Web Services API.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/pbiondich/openmrs-cli/internal/config"
)

// Error codes with stable CLI exit codes; agents can key on both.
const (
	CodeUnknown    = "UNKNOWN"     // exit 1
	CodeUsage      = "USAGE"       // exit 1 (bad command/flag/args)
	CodeAuth       = "AUTH"        // exit 2 — not authenticated / bad credentials (HTTP 401)
	CodeConnection = "CONNECTION"  // exit 3
	CodeNotFound   = "NOT_FOUND"   // exit 4
	CodeBadRequest = "BAD_REQUEST" // exit 5
	CodeForbidden  = "FORBIDDEN"   // exit 6 — authenticated but denied (HTTP 403)
)

// MaxPages bounds pagination loops even when the item cap is not reached
// (e.g. empty pages with a sticky next link).
const MaxPages = 500

const defaultTimeout = 30 * time.Second

func ExitCode(code string) int {
	switch code {
	case CodeAuth:
		return 2
	case CodeConnection:
		return 3
	case CodeNotFound:
		return 4
	case CodeBadRequest:
		return 5
	case CodeForbidden:
		return 6
	default:
		return 1
	}
}

// APIError is a structured error suitable for machine-readable output.
type APIError struct {
	Message    string `json:"error"`
	Code       string `json:"code"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

func (e *APIError) Error() string { return e.Message }

// Client talks to an OpenMRS REST (and optional FHIR2) endpoint.
// Safe for concurrent use with a shared request context (see WithContext).
type Client struct {
	baseURL string // e.g. https://dev3.openmrs.org/openmrs (no trailing slash)
	user    string
	pass    string
	http    *http.Client
	// ctx is used by Get/GetFHIR/GetAll when non-nil; otherwise Background.
	ctx context.Context
}

// New builds a client with a default 30s per-request timeout and
// same-origin + API-path redirect policy (blocks redirect-based SSRF).
func New(r config.Resolved) *Client {
	return NewWithHTTP(r, nil)
}

// NewWithHTTP builds a client with a custom HTTP client (tests, custom transport).
// If hc is nil, a default client is used. If hc is non-nil but has no
// CheckRedirect, a policy bound to this client's base URL is installed
// (same origin + REST/FHIR API path only).
func NewWithHTTP(r config.Resolved, hc *http.Client) *Client {
	c := &Client{
		baseURL: strings.TrimRight(r.URL, "/"),
		user:    r.User,
		pass:    r.Password,
	}
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	} else {
		// Copy so we don't mutate a shared client unexpectedly.
		cp := *hc
		hc = &cp
	}
	if hc.CheckRedirect == nil {
		hc.CheckRedirect = c.redirectPolicy()
	}
	c.http = hc
	return c
}

// redirectPolicy refuses cross-host/scheme redirects and same-host pivots
// outside OpenMRS REST/FHIR API paths so Basic Auth cannot ride a 302 to
// admin/actuator endpoints.
func (c *Client) redirectPolicy() func(req *http.Request, via []*http.Request) error {
	base, _ := url.Parse(c.baseURL)
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if len(via) == 0 {
			return nil
		}
		orig := via[0].URL
		if !strings.EqualFold(req.URL.Scheme, orig.Scheme) || !strings.EqualFold(req.URL.Host, orig.Host) {
			return fmt.Errorf("refusing off-origin redirect to %s", req.URL.Host)
		}
		basePath := ""
		if base != nil {
			basePath = base.Path
		}
		if !isAllowedAPIPath(basePath, req.URL.Path) {
			return fmt.Errorf("refusing redirect outside OpenMRS REST/FHIR API path (%s)", req.URL.Path)
		}
		return nil
	}
}

// isAllowedAPIPath reports whether reqPath stays under basePath/ws/rest or
// basePath/ws/fhir2 after path.Clean (so .. cannot escape into /admin).
func isAllowedAPIPath(basePath, reqPath string) bool {
	basePath = path.Clean("/" + strings.Trim(basePath, "/"))
	if basePath == "/" {
		basePath = ""
	}
	reqPath = path.Clean("/" + strings.TrimPrefix(reqPath, "/"))
	if strings.Contains(reqPath, "..") {
		return false
	}
	for _, seg := range []string{"/ws/rest", "/ws/fhir2"} {
		prefix := path.Clean(basePath + seg)
		if reqPath == prefix || strings.HasPrefix(reqPath, prefix+"/") {
			return true
		}
	}
	return false
}

// WithContext returns a shallow copy that uses ctx for subsequent requests.
// Cancel ctx to abort in-flight and follow-on GETs (e.g. summary fan-out).
func (c *Client) WithContext(ctx context.Context) *Client {
	if c == nil {
		return nil
	}
	cp := *c
	cp.ctx = ctx
	return &cp
}

func (c *Client) BaseURL() string { return c.baseURL }

func (c *Client) requestContext() context.Context {
	if c != nil && c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

// Do performs an HTTP request against a full URL and decodes JSON.
// method is e.g. http.MethodGet. body may be nil.
// This is the single transport path all GET helpers use; future write
// helpers should call Do rather than inventing a second stack.
func (c *Client) Do(ctx context.Context, method, rawURL string, body io.Reader) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, &APIError{Message: fmt.Sprintf("invalid request URL %s: %v", rawURL, err), Code: CodeBadRequest}
	}
	if c.user != "" || c.pass != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "omrs")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, &APIError{Message: "request canceled", Code: CodeUnknown}
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, &APIError{Message: fmt.Sprintf("request to %s timed out", c.baseURL), Code: CodeConnection}
		}
		msg := fmt.Sprintf("connection to %s failed: %v", c.baseURL, err)
		var uerr *url.Error
		if errors.As(err, &uerr) && uerr.Timeout() {
			msg = fmt.Sprintf("request to %s timed out after 30s", c.baseURL)
		}
		return nil, &APIError{Message: msg, Code: CodeConnection}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, &APIError{Message: fmt.Sprintf("reading response: %v", err), Code: CodeConnection}
	}

	if resp.StatusCode >= 400 {
		return nil, apiErrorFromResponse(resp.StatusCode, respBody)
	}

	// 204 No Content (and empty bodies generally) mean "success, nothing
	// to return" — e.g. a patient with no recorded allergies. Render as
	// an empty result set rather than a parse failure.
	if resp.StatusCode == http.StatusNoContent || len(strings.TrimSpace(string(respBody))) == 0 {
		return map[string]any{"results": []any{}}, nil
	}

	var out map[string]any
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, &APIError{
			Message:    fmt.Sprintf("server returned non-JSON response (HTTP %d) — is %s an OpenMRS server?", resp.StatusCode, c.baseURL),
			Code:       CodeUnknown,
			HTTPStatus: resp.StatusCode,
		}
	}
	return out, nil
}

// Get performs a GET against /ws/rest/v1/<path> and decodes the JSON response.
func (c *Client) Get(path string, params url.Values) (map[string]any, error) {
	return c.GetContext(c.requestContext(), path, params)
}

// GetContext is Get with an explicit context.
func (c *Client) GetContext(ctx context.Context, path string, params url.Values) (map[string]any, error) {
	u := c.baseURL + "/ws/rest/v1/" + strings.TrimLeft(path, "/")
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	return c.Do(ctx, http.MethodGet, u, nil)
}

// GetFHIR performs a GET against the FHIR2 module's R4 endpoint. An
// OperationOutcome response is surfaced as an error so callers can fall
// back to the REST API.
func (c *Client) GetFHIR(path string, params url.Values) (map[string]any, error) {
	return c.GetFHIRContext(c.requestContext(), path, params)
}

// GetFHIRContext is GetFHIR with an explicit context.
func (c *Client) GetFHIRContext(ctx context.Context, path string, params url.Values) (map[string]any, error) {
	u := c.baseURL + "/ws/fhir2/R4/" + strings.TrimLeft(path, "/")
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	out, err := c.Do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if out["resourceType"] == "OperationOutcome" {
		return nil, &APIError{Message: "FHIR server returned an OperationOutcome error", Code: CodeUnknown}
	}
	return out, nil
}

// apiErrorFromResponse maps HTTP status to error codes and surfaces the
// OpenMRS error body shape {"error":{"message","code","detail"}} when present.
func apiErrorFromResponse(status int, body []byte) *APIError {
	code := CodeUnknown
	switch {
	case status == http.StatusUnauthorized:
		code = CodeAuth
	case status == http.StatusForbidden:
		code = CodeForbidden
	case status == http.StatusNotFound:
		code = CodeNotFound
	case status == http.StatusBadRequest:
		code = CodeBadRequest
	}

	msg := fmt.Sprintf("HTTP %d", status)
	detail := ""
	var wrapper struct {
		Error struct {
			Message string `json:"message"`
			Detail  string `json:"detail"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &wrapper) == nil && wrapper.Error.Message != "" {
		msg = wrapper.Error.Message
		detail = firstLine(wrapper.Error.Detail)
	}
	return &APIError{Message: msg, Code: code, HTTPStatus: status, Detail: detail}
}

// firstLine trims OpenMRS's multi-line stack-trace details to their first line.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// GetAll follows links[rel=next] pagination, accumulating results up to cap.
func (c *Client) GetAll(path string, params url.Values, capItems int) (map[string]any, error) {
	return c.GetAllContext(c.requestContext(), path, params, capItems)
}

// GetAllContext is GetAll with an explicit context (checked between pages).
func (c *Client) GetAllContext(ctx context.Context, path string, params url.Values, capItems int) (map[string]any, error) {
	u := c.baseURL + "/ws/rest/v1/" + strings.TrimLeft(path, "/")
	if len(params) > 0 {
		u += "?" + params.Encode()
	}

	var all []any
	truncated := false
	var totalCount any
	for pageNum := 0; u != ""; pageNum++ {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil, &APIError{Message: "request canceled", Code: CodeUnknown}
			}
			return nil, &APIError{Message: fmt.Sprintf("request to %s timed out", c.baseURL), Code: CodeConnection}
		}
		if pageNum >= MaxPages {
			truncated = true
			warn, _ := json.Marshal(map[string]string{
				"warning": fmt.Sprintf("pagination page cap (%d) reached; results truncated", MaxPages),
			})
			fmt.Fprintln(os.Stderr, string(warn))
			break
		}

		page, err := c.Do(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		results, _ := page["results"].([]any)
		if len(results) == 0 {
			break
		}
		all = append(all, results...)
		if tc, ok := page["totalCount"]; ok {
			totalCount = tc
		}
		if len(all) >= capItems {
			// Reaching the cap exactly, with no further page advertised,
			// is a complete result — saying "truncated" there is a false
			// statement agents will act on. Only overshoot or a live next
			// link means rows were actually left behind.
			overshoot := len(all) > capItems
			all = all[:capItems]
			if overshoot || nextLink(page) != "" {
				truncated = true
				warn, _ := json.Marshal(map[string]string{
					"warning": fmt.Sprintf("pagination cap (%d) reached; results truncated", capItems),
				})
				fmt.Fprintln(os.Stderr, string(warn))
			}
			break
		}
		next, err := c.sanitizeNextURL(nextLink(page))
		if err != nil {
			return nil, err
		}
		u = next
	}
	out := map[string]any{"results": all}
	if truncated {
		out["truncated"] = true
	}
	if totalCount != nil {
		out["totalCount"] = totalCount
	}
	return out, nil
}

// sanitizeNextURL validates a pagination next link. Empty input is fine
// (end of list). Off-origin absolute URLs are rejected so credentials are
// never sent to a host other than the configured server.
func (c *Client) sanitizeNextURL(next string) (string, error) {
	if next == "" {
		return "", nil
	}
	base, err := url.Parse(c.baseURL)
	if err != nil {
		return "", &APIError{Message: fmt.Sprintf("invalid client base URL: %v", err), Code: CodeBadRequest}
	}
	u, err := url.Parse(next)
	if err != nil {
		return "", &APIError{Message: fmt.Sprintf("invalid pagination next link: %v", err), Code: CodeBadRequest}
	}
	if !u.IsAbs() {
		u = base.ResolveReference(u)
	}
	if u.User != nil {
		return "", &APIError{
			Message: "refusing pagination next link that embeds credentials",
			Code:    CodeBadRequest,
		}
	}
	if !strings.EqualFold(u.Scheme, base.Scheme) || !strings.EqualFold(u.Host, base.Host) {
		return "", &APIError{
			Message: fmt.Sprintf("refusing off-origin pagination link %s (expected host %s)", u.Host, base.Host),
			Code:    CodeBadRequest,
		}
	}
	// path.Clean so .. cannot escape; require prefix under base/ws/rest or base/ws/fhir2.
	if !isAllowedAPIPath(base.Path, u.Path) {
		return "", &APIError{
			Message: "refusing pagination next link outside OpenMRS REST/FHIR API path",
			Code:    CodeBadRequest,
		}
	}
	// Return the cleaned path form so callers follow a canonical URL.
	u.Path = path.Clean(u.Path)
	return u.String(), nil
}

func nextLink(page map[string]any) string {
	links, _ := page["links"].([]any)
	for _, l := range links {
		lm, _ := l.(map[string]any)
		if lm["rel"] == "next" {
			uri, _ := lm["uri"].(string)
			return uri
		}
	}
	return ""
}
