package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func withTempConfig(t *testing.T, cfg *Config) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	t.Setenv("OMRS_CONFIG", path)
	// Clear credential-related env so tests don't inherit the shell.
	t.Setenv("OMRS_PASSWORD", "")
	t.Setenv("OMRS_USER", "")
	t.Setenv("OMRS_SERVER", "")
	t.Setenv("OMRS_PROFILE", "")
	if cfg != nil {
		if err := Save(cfg); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDefaultHasNoPasswords(t *testing.T) {
	d := Default()
	for name, p := range d.Profiles {
		if p.Password != "" {
			t.Errorf("profile %q has password %q", name, p.Password)
		}
		if p.PasswordStore != "" {
			t.Errorf("profile %q has passwordStore %q", name, p.PasswordStore)
		}
	}
}

func TestResolveFilePasswordAndOverrides(t *testing.T) {
	withTempConfig(t, &Config{
		DefaultProfile: "local",
		Profiles: map[string]Profile{
			"local": {URL: "http://localhost/openmrs", User: "admin", Password: "fromfile"},
		},
	})

	res, err := Resolve(Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Password != "fromfile" || res.User != "admin" {
		t.Fatalf("got %+v", res)
	}

	t.Setenv("OMRS_PASSWORD", "fromenv")
	res, err = Resolve(Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Password != "fromenv" {
		t.Fatalf("env override failed: %+v", res)
	}

	res, err = Resolve(Overrides{Password: "fromflag", User: "u2"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Password != "fromflag" || res.User != "u2" {
		t.Fatalf("flag override failed: %+v", res)
	}
}

func TestResolveMissingExplicitProfile(t *testing.T) {
	withTempConfig(t, &Config{Profiles: map[string]Profile{}})
	_, err := Resolve(Overrides{Profile: "nope"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestResolveKeychainMissingHardFails(t *testing.T) {
	// Use a profile name that almost certainly has no keychain entry.
	name := "omrs-test-no-such-keychain-entry-xyz"
	withTempConfig(t, &Config{
		DefaultProfile: name,
		Profiles: map[string]Profile{
			name: {URL: "http://localhost/openmrs", User: "admin", PasswordStore: "keychain"},
		},
	})

	_, err := Resolve(Overrides{})
	if err == nil {
		t.Fatal("expected credential store error")
	}
	if !errors.Is(err, ErrCredentialStore) {
		t.Fatalf("err=%v want ErrCredentialStore", err)
	}

	// Env password satisfies the requirement without keychain.
	t.Setenv("OMRS_PASSWORD", "env-secret")
	res, err := Resolve(Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Password != "env-secret" {
		t.Fatalf("%+v", res)
	}
}

func TestResolveNoSilentDefaultPassword(t *testing.T) {
	withTempConfig(t, &Config{Profiles: map[string]Profile{}})
	res, err := Resolve(Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Password != "" {
		t.Fatalf("unexpected default password %q", res.Password)
	}
	if res.URL != DefaultURL {
		t.Fatalf("url=%q", res.URL)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	withTempConfig(t, nil)
	cfg := Default()
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	// Permissions should be 0600 (mask may clear group/other only).
	info, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("mode=%o want no group/other bits", info.Mode().Perm())
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.DefaultProfile != "local" || len(got.Profiles) != 2 {
		t.Fatalf("%+v", got)
	}
	if got.Profiles["demo"].Password != "" {
		t.Fatal("demo must not ship with a password")
	}
}
