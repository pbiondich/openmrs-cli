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

func TestNormalizeServerURL(t *testing.T) {
	ok, err := NormalizeServerURL("https://demo.example/openmrs/")
	if err != nil || ok != "https://demo.example/openmrs" {
		t.Fatalf("got %q err=%v", ok, err)
	}
	if _, err := NormalizeServerURL("https://user:pass@demo.example/openmrs"); err == nil {
		t.Fatal("userinfo should fail")
	}
	if _, err := NormalizeServerURL("ftp://demo.example/openmrs"); err == nil {
		t.Fatal("ftp should fail")
	}
	// localhost http is fine
	if _, err := NormalizeServerURL("http://localhost/openmrs"); err != nil {
		t.Fatal(err)
	}
	// remote http refused by default
	if _, err := NormalizeServerURL("http://example.com/openmrs"); err == nil {
		t.Fatal("remote cleartext HTTP must fail without allow")
	}
	t.Cleanup(func() { SetAllowInsecureHTTP(false) })
	SetAllowInsecureHTTP(true)
	if _, err := NormalizeServerURL("http://example.com/openmrs"); err != nil {
		t.Fatal(err)
	}
	SetAllowInsecureHTTP(false)
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
	// On headless Linux (CI) the store may be unavailable rather than
	// "not found"; both paths must hard-fail without a password override.
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

	// Env password must work even when the keyring is missing or down
	// (GitHub Actions has no org.freedesktop.secrets).
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

func TestOriginKey(t *testing.T) {
	a, err := OriginKey("https://Demo.Example/openmrs/")
	if err != nil {
		t.Fatal(err)
	}
	b, err := OriginKey("https://demo.example/openmrs")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("path/case should not matter: %q vs %q", a, b)
	}
	c, err := OriginKey("http://demo.example/openmrs")
	if err != nil {
		t.Fatal(err)
	}
	if a == c {
		t.Fatal("http vs https must differ")
	}
	d, err := OriginKey("https://evil.example/openmrs")
	if err != nil {
		t.Fatal(err)
	}
	if a == d {
		t.Fatal("hosts must differ")
	}
}

func TestResolveRefusesProfilePasswordForDifferentOrigin(t *testing.T) {
	withTempConfig(t, &Config{
		DefaultProfile: "prod",
		Profiles: map[string]Profile{
			"prod": {URL: "https://hospital.example/openmrs", User: "nurse", Password: "secret-pw"},
		},
	})

	// Same origin (path differ) still uses the stored password.
	res, err := Resolve(Overrides{Server: "https://hospital.example/openmrs/"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Password != "secret-pw" || res.URL != "https://hospital.example/openmrs" {
		t.Fatalf("%+v", res)
	}

	// Different host: must not attach the profile password.
	_, err = Resolve(Overrides{Server: "https://attacker.example/openmrs"})
	if err == nil {
		t.Fatal("expected origin binding error")
	}
	if !errors.Is(err, ErrCredentialOrigin) {
		t.Fatalf("err=%v want ErrCredentialOrigin", err)
	}

	// Env server override is the same attack path.
	t.Setenv("OMRS_SERVER", "https://attacker.example/openmrs")
	_, err = Resolve(Overrides{})
	if !errors.Is(err, ErrCredentialOrigin) {
		t.Fatalf("env override: err=%v", err)
	}
	t.Setenv("OMRS_SERVER", "")

	// Invocation-scoped password may target any host.
	res, err = Resolve(Overrides{Server: "https://attacker.example/openmrs", Password: "oneshot"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Password != "oneshot" {
		t.Fatalf("%+v", res)
	}

	t.Setenv("OMRS_PASSWORD", "fromenv")
	res, err = Resolve(Overrides{Server: "https://other.example/openmrs"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Password != "fromenv" {
		t.Fatalf("%+v", res)
	}
}

func TestResolveKeychainPasswordOriginBound(t *testing.T) {
	// secrets package: use MockInit from a small integration via real keyring
	// only when available — instead store password in file field to cover
	// the same binding path as keychain success (origin check is shared).
	// Keychain path with successful Get is exercised when mock is used from
	// cli tests; here we assert file-password origin bind which uses the
	// same branch after profilePassword is set.
	withTempConfig(t, &Config{
		DefaultProfile: "local",
		Profiles: map[string]Profile{
			"local": {URL: "http://localhost/openmrs", User: "admin", Password: "local-secret"},
		},
	})
	_, err := Resolve(Overrides{Server: "http://127.0.0.1/openmrs"})
	// localhost vs 127.0.0.1 are different hosts → refuse
	if !errors.Is(err, ErrCredentialOrigin) {
		t.Fatalf("loopback name vs IP should not share credentials: err=%v", err)
	}
	res, err := Resolve(Overrides{Server: "http://localhost:80/openmrs"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Password != "local-secret" {
		t.Fatalf("%+v", res)
	}
}

func TestClearProfileSecrets(t *testing.T) {
	p := Profile{URL: "https://a.example/openmrs", Password: "x", PasswordStore: "keychain"}
	ClearProfileSecrets("any", &p)
	if p.Password != "" || p.PasswordStore != "" {
		t.Fatalf("%+v", p)
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
