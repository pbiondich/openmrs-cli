// Package client is the HTTP client for the OpenMRS REST Web Services API.
package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pbiondich/openmrs-cli/internal/config"
)

// Error codes with stable CLI exit codes; agents can key on both.
const (
	CodeUnknown    = "UNKNOWN"     // exit 1
	CodeUsage      = "USAGE"       // exit 1 (bad command/flag/args)
	CodeAuth       = "AUTH"        // exit 2
	CodeConnection = "CONNECTION"  // exit 3
	CodeNotFound   = "NOT_FOUND"   // exit 4
	CodeBadRequest = "BAD_REQUEST" // exit 5
)

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

type Client struct {
	baseURL string // e.g. https://dev3.openmrs.org/openmrs (no trailing slash)
	user    string
	pass    string
	http    *http.Client
}

func New(r config.Resolved) *Client {
	return &Client{
		baseURL: strings.TrimRight(r.URL, "/"),
		user:    r.User,
		pass:    r.Password,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) BaseURL() string { return c.baseURL }

// Get performs a GET against /ws/rest/v1/<path> and decodes the JSON response.
func (c *Client) Get(path string, params url.Values) (map[string]any, error) {
	u := c.baseURL + "/ws/rest/v1/" + strings.TrimLeft(path, "/")
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	return c.getURL(u)
}

// GetFHIR performs a GET against the FHIR2 module's R4 endpoint. An
// OperationOutcome response is surfaced as an error so callers can fall
// back to the REST API.
func (c *Client) GetFHIR(path string, params url.Values) (map[string]any, error) {
	u := c.baseURL + "/ws/fhir2/R4/" + strings.TrimLeft(path, "/")
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	out, err := c.getURL(u)
	if err != nil {
		return nil, err
	}
	if out["resourceType"] == "OperationOutcome" {
		return nil, &APIError{Message: "FHIR server returned an OperationOutcome error", Code: CodeUnknown}
	}
	return out, nil
}

func (c *Client) getURL(u string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, &APIError{Message: fmt.Sprintf("invalid request URL %s: %v", u, err), Code: CodeBadRequest}
	}
	if c.user != "" || c.pass != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		msg := fmt.Sprintf("connection to %s failed: %v", c.baseURL, err)
		var uerr *url.Error
		if errors.As(err, &uerr) && uerr.Timeout() {
			msg = fmt.Sprintf("request to %s timed out after 30s", c.baseURL)
		}
		return nil, &APIError{Message: msg, Code: CodeConnection}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, &APIError{Message: fmt.Sprintf("reading response: %v", err), Code: CodeConnection}
	}

	if resp.StatusCode >= 400 {
		return nil, apiErrorFromResponse(resp.StatusCode, body)
	}

	// 204 No Content (and empty bodies generally) mean "success, nothing
	// to return" — e.g. a patient with no recorded allergies. Render as
	// an empty result set rather than a parse failure.
	if resp.StatusCode == http.StatusNoContent || len(strings.TrimSpace(string(body))) == 0 {
		return map[string]any{"results": []any{}}, nil
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, &APIError{
			Message:    fmt.Sprintf("server returned non-JSON response (HTTP %d) — is %s an OpenMRS server?", resp.StatusCode, c.baseURL),
			Code:       CodeUnknown,
			HTTPStatus: resp.StatusCode,
		}
	}
	return out, nil
}

// apiErrorFromResponse maps HTTP status to error codes and surfaces the
// OpenMRS error body shape {"error":{"message","code","detail"}} when present.
func apiErrorFromResponse(status int, body []byte) *APIError {
	code := CodeUnknown
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		code = CodeAuth
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
// Returns {"results": [...]} in the same shape as a single page.
func (c *Client) GetAll(path string, params url.Values, capItems int) (map[string]any, error) {
	u := c.baseURL + "/ws/rest/v1/" + strings.TrimLeft(path, "/")
	if len(params) > 0 {
		u += "?" + params.Encode()
	}

	var all []any
	for u != "" {
		page, err := c.getURL(u)
		if err != nil {
			return nil, err
		}
		results, _ := page["results"].([]any)
		all = append(all, results...)
		if len(all) >= capItems {
			all = all[:capItems]
			fmt.Fprintf(os.Stderr, `{"warning":"pagination cap (%d) reached; results truncated"}`+"\n", capItems)
			break
		}
		u = nextLink(page)
	}
	return map[string]any{"results": all}, nil
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
