package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/pbiondich/openmrs-cli/internal/client"
	"github.com/pbiondich/openmrs-cli/internal/config"
	"github.com/pbiondich/openmrs-cli/internal/output"
	"github.com/pbiondich/openmrs-cli/internal/secrets"
)

func TestRepresentation(t *testing.T) {
	old := flags
	t.Cleanup(func() { flags = old })

	flags = struct {
		server, user, profile string
		jsonOut, tableOut     bool
		full, ref             bool
		fields                string
		limit, start          int
		all                   bool
	}{}

	if got := representation(); got != "default" {
		t.Fatalf("default=%q", got)
	}
	flags.full = true
	if got := representation(); got != "full" {
		t.Fatalf("full=%q", got)
	}
	flags.full, flags.ref = false, true
	if got := representation(); got != "ref" {
		t.Fatalf("ref=%q", got)
	}
	flags.ref, flags.fields = false, "uuid, display ,person.age"
	if got := representation(); got != "custom:(uuid,display,person.age)" {
		t.Fatalf("fields=%q", got)
	}
}

func TestSetRepresentationPreservesInlineV(t *testing.T) {
	old := flags
	t.Cleanup(func() { flags = old })
	flags.full, flags.ref, flags.fields = false, false, ""

	params := url.Values{"v": {"full"}}
	setRepresentation(params)
	if params.Get("v") != "full" {
		t.Fatalf("inline v= should be preserved, got %q", params.Get("v"))
	}

	params = url.Values{}
	setRepresentation(params)
	if params.Get("v") != "default" {
		t.Fatalf("missing v= should default, got %q", params.Get("v"))
	}

	flags.ref = true
	params = url.Values{"v": {"full"}}
	setRepresentation(params)
	if params.Get("v") != "ref" {
		t.Fatalf("explicit flag should win over inline, got %q", params.Get("v"))
	}
}

func TestWrapResolveError(t *testing.T) {
	if wrapResolveError(nil) != nil {
		t.Fatal("nil stays nil")
	}
	plain := errors.New("other")
	if wrapResolveError(plain) != plain {
		t.Fatal("non-credential errors pass through")
	}
	err := wrapResolveError(config.ErrCredentialStore)
	api, ok := err.(*client.APIError)
	if !ok || api.Code != client.CodeAuth {
		t.Fatalf("want AUTH APIError, got %v", err)
	}
}

func TestGroupRunE(t *testing.T) {
	cmd := &cobra.Command{Use: "patient"}
	// Bare parent: Help() needs an output writer.
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	if err := groupRunE(cmd, nil); err != nil {
		t.Fatalf("bare parent should Help with nil error, got %v", err)
	}

	err := groupRunE(cmd, []string{"nope"})
	api, ok := err.(*client.APIError)
	if !ok || api.Code != client.CodeUsage {
		t.Fatalf("unknown subcommand should be USAGE, got %v", err)
	}
	if !strings.Contains(api.Message, "nope") {
		t.Fatalf("message=%q", api.Message)
	}
}

func TestWarnIfNoFilter(t *testing.T) {
	oldFlags, oldAll := flags, flags.all
	t.Cleanup(func() {
		flags = oldFlags
		flags.all = oldAll
	})

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = oldStderr })

	flags.all = false
	warnIfNoFilter(url.Values{}, "--patient <uuid>")
	// With a filter present, no warning.
	warnIfNoFilter(url.Values{"patient": {"x"}}, "--patient <uuid>")
	// --all suppresses the empty-filter warning.
	flags.all = true
	warnIfNoFilter(url.Values{}, "--patient <uuid>")

	_ = w.Close()
	body, _ := io.ReadAll(r)
	s := string(body)
	if !strings.Contains(s, "no filter given") {
		t.Fatalf("expected empty-filter warning, got %q", s)
	}
	// Exactly one warning line (the filtered/all cases stay quiet).
	if n := strings.Count(s, `"warning"`); n != 1 {
		t.Fatalf("want 1 warning, got %d in %q", n, s)
	}
}

func TestOutputMode(t *testing.T) {
	old := flags
	t.Cleanup(func() { flags = old })
	flags.jsonOut, flags.tableOut = true, false
	if outputMode() != output.ModeJSON {
		t.Fatal("want JSON")
	}
	flags.jsonOut, flags.tableOut = false, true
	if outputMode() != output.ModeTable {
		t.Fatal("want table")
	}
}

func TestRepresentationFlagsMutuallyExclusive(t *testing.T) {
	// MarkFlagsMutuallyExclusive is checked when the command runs.
	// Use a no-op leaf so we do not hit the network.
	leaf := &cobra.Command{
		Use:  "noop-mutex-test",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return nil },
	}
	rootCmd.AddCommand(leaf)
	t.Cleanup(func() {
		rootCmd.RemoveCommand(leaf)
		rootCmd.SetArgs(nil)
		flags.full, flags.ref, flags.fields = false, false, ""
	})

	resetRepFlags := func() {
		// Package-level vars + pflag Changed bits stick across Execute calls.
		// Mutual exclusion keys off Changed, not the value.
		flags.full, flags.ref, flags.fields = false, false, ""
		for _, name := range []string{"full", "ref", "fields"} {
			f := rootCmd.PersistentFlags().Lookup(name)
			if f == nil {
				t.Fatalf("missing flag %q", name)
			}
			_ = f.Value.Set(f.DefValue)
			f.Changed = false
		}
	}

	cases := [][]string{
		{"noop-mutex-test", "--full", "--ref"},
		{"noop-mutex-test", "--full", "--fields", "uuid"},
		{"noop-mutex-test", "--ref", "--fields", "uuid"},
	}
	for _, args := range cases {
		resetRepFlags()
		rootCmd.SetArgs(args)
		err := rootCmd.Execute()
		if err == nil {
			t.Fatalf("expected mutual exclusion error for %v", args)
		}
		if !strings.Contains(err.Error(), "if any flags in the group") &&
			!strings.Contains(err.Error(), "mutually exclusive") {
			// cobra wording varies slightly by version; accept either.
			t.Fatalf("unexpected error for %v: %v", args, err)
		}
	}

	// Single representation flag is fine.
	resetRepFlags()
	rootCmd.SetArgs([]string{"noop-mutex-test", "--full"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("single --full should work: %v", err)
	}
}

// withResolvedServer points flags + env at an httptest server so newClient
// / fetch helpers do not touch the real config or network.
func withResolvedServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	secrets.MockInit()
	isolatedConfig(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	old := flags
	t.Cleanup(func() { flags = old })
	flags.server = srv.URL
	flags.user = "admin"
	t.Setenv("OMRS_PASSWORD", "test-pw")
	t.Setenv("OMRS_SERVER", "")
	t.Setenv("OMRS_USER", "")
	t.Setenv("OMRS_PROFILE", "")
	return srv
}

func TestNewClientAndFetchOne(t *testing.T) {
	uuid := "dd8e5b3d-1691-11df-97a5-7038c432aabf"
	var sawPath string
	withResolvedServer(t, func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		if r.URL.Query().Get("v") != "ref" {
			t.Errorf("v=%q", r.URL.Query().Get("v"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"uuid":    uuid,
			"display": "One",
		})
	})
	flags.ref = true
	flags.full = false
	flags.fields = ""
	flags.jsonOut = true

	// Capture stdout from output.Print.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w
	err = fetchOne(context.Background(), "patient/"+uuid, "patient", nil)
	_ = w.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r)
	if !strings.Contains(string(body), uuid) {
		t.Fatalf("stdout=%s", body)
	}
	if !strings.Contains(sawPath, "/ws/rest/v1/patient/"+uuid) {
		t.Fatalf("path=%s", sawPath)
	}
}

func TestFetchListDataLimitAndAll(t *testing.T) {
	pages := 0
	withResolvedServer(t, func(w http.ResponseWriter, r *http.Request) {
		pages++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{
				map[string]any{"uuid": "a", "display": "A"},
			},
		})
	})
	flags.all = false
	flags.limit = 10
	flags.start = 0
	flags.full, flags.ref, flags.fields = false, false, ""

	data, err := fetchListData(context.Background(), "location", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	if len(data["results"].([]any)) != 1 {
		t.Fatalf("%v", data)
	}
	if pages != 1 {
		t.Fatalf("pages=%d", pages)
	}
}

func TestNewClientCredentialStoreError(t *testing.T) {
	secrets.MockInitWithError(os.ErrPermission)
	t.Cleanup(secrets.MockInit)
	path := isolatedConfig(t)
	// Profile declares keychain but store is broken and no OMRS_PASSWORD.
	cfg := config.Default()
	cfg.DefaultProfile = "local"
	cfg.Profiles["local"] = config.Profile{
		URL: "http://localhost/openmrs", User: "admin", PasswordStore: "keychain",
	}
	if err := os.WriteFile(path, mustJSON(t, cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	old := flags
	t.Cleanup(func() { flags = old })
	flags.server, flags.user, flags.profile = "", "", ""
	t.Setenv("OMRS_PASSWORD", "")
	t.Setenv("OMRS_SERVER", "")
	t.Setenv("OMRS_USER", "")
	t.Setenv("OMRS_PROFILE", "")

	_, err := newClient(context.Background())
	api, ok := err.(*client.APIError)
	if !ok || api.Code != client.CodeAuth {
		t.Fatalf("want AUTH from missing keychain secret, got %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
