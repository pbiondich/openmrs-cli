package cli

import (
	"testing"

	"github.com/pbiondich/openmrs-cli/internal/client"
	"github.com/pbiondich/openmrs-cli/internal/config"
)

func TestRunLoginDemoRejectsConflictingFlags(t *testing.T) {
	// Save and restore package flag state used by runLoginDemo.
	oldServer, oldUser := flags.server, flags.user
	oldStdin, oldDemo := loginPasswordStdin, loginDemo
	t.Cleanup(func() {
		flags.server, flags.user = oldServer, oldUser
		loginPasswordStdin, loginDemo = oldStdin, oldDemo
	})

	cfg := &config.Config{Profiles: map[string]config.Profile{}}

	cases := []struct {
		name string
		mut  func()
	}{
		{"server", func() { flags.server = "https://other.example/openmrs" }},
		{"user", func() { flags.user = "someone" }},
		{"password-stdin", func() { loginPasswordStdin = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flags.server, flags.user = "", ""
			loginPasswordStdin = false
			tc.mut()
			err := runLoginDemo(cfg)
			if err == nil {
				t.Fatal("expected usage error")
			}
			api, ok := err.(*client.APIError)
			if !ok || api.Code != client.CodeUsage {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestDemoConstants(t *testing.T) {
	if config.DemoURL == "" || config.DemoPassword == "" {
		t.Fatal("demo constants must be set")
	}
	if config.DemoProfile != "demo" {
		t.Fatalf("profile=%q", config.DemoProfile)
	}
	// Default() demo profile points at the same public sandbox.
	d := config.Default()
	if d.Profiles["demo"].URL != config.DemoURL {
		t.Fatalf("init demo URL %q != DemoURL", d.Profiles["demo"].URL)
	}
	if d.Profiles["demo"].Password != "" {
		t.Fatal("config init must not embed the demo password")
	}
}
