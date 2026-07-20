package cli

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pbiondich/openmrs-cli/internal/client"
	"github.com/pbiondich/openmrs-cli/internal/config"
	"github.com/pbiondich/openmrs-cli/internal/secrets"
)

// sessionServer serves /ws/rest/v1/session with the given auth result.
func sessionServer(t *testing.T, authenticated bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/session") {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authenticated": authenticated,
			"user":          map[string]any{"display": "Test User"},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// isolatedConfig points OMRS_CONFIG at a temp file so tests never touch
// the real ~/.config/omrs/config.json.
func isolatedConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("OMRS_CONFIG", path)
	return path
}

func TestCompleteLoginStoresSecretInCredentialStore(t *testing.T) {
	secrets.MockInit()
	isolatedConfig(t)
	srv := sessionServer(t, true)

	cfg := &config.Config{Profiles: map[string]config.Profile{}}
	if err := completeLogin(cfg, "test-prof", srv.URL, "alice", "pw123", false); err != nil {
		t.Fatal(err)
	}

	// Password must be in the (mock) credential store, not the config file.
	pw, err := secrets.Get("test-prof")
	if err != nil || pw != "pw123" {
		t.Fatalf("secret not stored: pw=%q err=%v", pw, err)
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := saved.Profiles["test-prof"]
	if p.Password != "" {
		t.Fatal("plaintext password must not be written when the store works")
	}
	if p.PasswordStore != "keychain" {
		t.Fatalf("passwordStore=%q", p.PasswordStore)
	}
	if p.User != "alice" {
		t.Fatalf("user=%q", p.User)
	}
	// First profile becomes the default even without setDefault.
	if saved.DefaultProfile != "test-prof" {
		t.Fatalf("defaultProfile=%q", saved.DefaultProfile)
	}
}

func TestCompleteLoginRejectsBadCredentialsAndPersistsNothing(t *testing.T) {
	secrets.MockInit()
	cfgPath := isolatedConfig(t)
	srv := sessionServer(t, false) // server says authenticated: false

	cfg := &config.Config{Profiles: map[string]config.Profile{}}
	err := completeLogin(cfg, "test-prof", srv.URL, "alice", "wrong", false)
	api, ok := err.(*client.APIError)
	if !ok || api.Code != client.CodeAuth {
		t.Fatalf("want AUTH error, got %v", err)
	}
	if _, err := secrets.Get("test-prof"); err == nil {
		t.Fatal("secret must not be stored on failed validation")
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Fatal("config file must not be written on failed validation")
	}
}

func TestCompleteLoginFallsBackToConfigFileWhenStoreUnavailable(t *testing.T) {
	secrets.MockInitWithError(os.ErrPermission)
	t.Cleanup(secrets.MockInit)
	isolatedConfig(t)
	srv := sessionServer(t, true)

	cfg := &config.Config{Profiles: map[string]config.Profile{}}
	if err := completeLogin(cfg, "headless", srv.URL, "bob", "pw", false); err != nil {
		t.Fatal(err)
	}
	saved, _ := config.Load()
	p := saved.Profiles["headless"]
	if p.Password != "pw" || p.PasswordStore != "" {
		t.Fatalf("expected config-file fallback, got %+v", p)
	}
}

func TestCompleteLoginRejectsBadURL(t *testing.T) {
	secrets.MockInit()
	isolatedConfig(t)
	cfg := &config.Config{Profiles: map[string]config.Profile{}}
	if err := completeLogin(cfg, "p", "ftp://example.org/openmrs", "u", "pw", false); err == nil {
		t.Fatal("non-http scheme must be rejected")
	}
	if err := completeLogin(cfg, "p", "https://user:pass@example.org/openmrs", "u", "pw", false); err == nil {
		t.Fatal("embedded credentials must be rejected")
	}
}

func TestCompleteLoginSetDefaultOverridesExisting(t *testing.T) {
	secrets.MockInit()
	isolatedConfig(t)
	srv := sessionServer(t, true)

	cfg := &config.Config{
		DefaultProfile: "other",
		Profiles:       map[string]config.Profile{"other": {URL: "http://localhost/openmrs"}},
	}
	if err := completeLogin(cfg, "demo", srv.URL, "admin", "pw", true); err != nil {
		t.Fatal(err)
	}
	saved, _ := config.Load()
	if saved.DefaultProfile != "demo" {
		t.Fatalf("setDefault must override, got %q", saved.DefaultProfile)
	}
}

func TestPromptDefault(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("typed-value\n\n"))
	if got := promptDefault(r, "x: ", "fallback"); got != "typed-value" {
		t.Fatalf("got %q", got)
	}
	if got := promptDefault(r, "x: ", "fallback"); got != "fallback" {
		t.Fatalf("empty line must return default, got %q", got)
	}
}

func TestCompleteLoginPropagatesHTTPAuthError(t *testing.T) {
	secrets.MockInit()
	isolatedConfig(t)
	// 401 from /session (distinct from authenticated:false body).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/session") {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid username/password"}}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{Profiles: map[string]config.Profile{}}
	err := completeLogin(cfg, "p", srv.URL, "alice", "wrong", false)
	api, ok := err.(*client.APIError)
	if !ok || api.Code != client.CodeAuth {
		t.Fatalf("want AUTH from HTTP 401, got %v", err)
	}
	if api.HTTPStatus != http.StatusUnauthorized {
		t.Fatalf("httpStatus=%d", api.HTTPStatus)
	}
}
