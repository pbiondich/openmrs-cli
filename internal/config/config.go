// Package config manages named server profiles stored in ~/.config/omrs/config.json
// and resolves the effective server/credentials from flags, environment, and profiles.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pbiondich/openmrs-cli/internal/secrets"
)

const (
	DefaultURL  = "http://localhost/openmrs"
	DefaultUser = "admin"

	// Public OpenMRS reference application (resets periodically).
	// Used by `omrs login --demo` and the `demo` profile from config init.
	DemoURL      = "https://dev3.openmrs.org/openmrs"
	DemoUser     = "admin"
	DemoPassword = "Admin123"
	DemoProfile  = "demo"
)

// ErrCredentialStore is returned when a profile declares passwordStore=keychain
// but the secret cannot be read and no flag/env password was supplied.
var ErrCredentialStore = errors.New("credential store")

// ErrCredentialOrigin is returned when a profile-stored password would be
// sent to a server origin other than the profile's. Stored secrets are
// origin-bound; use OMRS_PASSWORD (or re-login) for a different host.
var ErrCredentialOrigin = errors.New("credential origin")

type Profile struct {
	URL      string `json:"url"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	// PasswordStore is "keychain" when the password lives in the OS
	// credential store (written by `omrs login`) rather than this file.
	PasswordStore string `json:"passwordStore,omitempty"`
}

type Config struct {
	DefaultProfile string             `json:"defaultProfile"`
	Profiles       map[string]Profile `json:"profiles"`
}

// Resolved is the effective connection settings after applying precedence.
type Resolved struct {
	URL      string
	User     string
	Password string
	Profile  string // which profile supplied the base values, "" if none
}

// Overrides carries the connection-related CLI flag values.
type Overrides struct {
	Server   string
	User     string
	Password string
	Profile  string
}

// warnJSON emits an advisory one-line JSON warning to stderr (this
// package can't import internal/output without a cycle).
func warnJSON(msg string) {
	b, _ := json.Marshal(map[string]string{"warning": msg})
	fmt.Fprintln(os.Stderr, string(b))
}

func Path() string {
	if p := os.Getenv("OMRS_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".omrs", "config.json")
	}
	return filepath.Join(home, ".config", "omrs", "config.json")
}

// Default returns the config written by `omrs config init`.
// Profiles carry URLs and usernames only — never passwords. Credentials
// come from `omrs login`, OMRS_PASSWORD, or set-profile after init.
func Default() *Config {
	return &Config{
		DefaultProfile: "local",
		Profiles: map[string]Profile{
			"local": {URL: DefaultURL, User: DefaultUser},
			"demo":  {URL: DemoURL, User: DemoUser},
		},
	}
}

// Load reads the config file. A missing file is not an error: an empty
// config is returned and built-in defaults apply.
func Load() (*Config, error) {
	data, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{Profiles: map[string]Profile{}}, nil
		}
		return nil, fmt.Errorf("reading config %s: %w", Path(), err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", Path(), err)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]Profile{}
	}
	return &cfg, nil
}

func Save(cfg *Config) error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing config %s: %w", path, err)
	}
	return os.Chmod(path, 0o600)
}

// insecureHTTPAllowed is set by the CLI --allow-insecure-http flag.
// Env OMRS_ALLOW_INSECURE_HTTP=1|true|yes also permits cleartext remote HTTP.
var insecureHTTPAllowed bool

// SetAllowInsecureHTTP toggles cleartext HTTP to non-loopback hosts (tests + CLI flag).
func SetAllowInsecureHTTP(v bool) { insecureHTTPAllowed = v }

// AllowInsecureHTTP reports whether non-loopback http:// URLs are permitted.
func AllowInsecureHTTP() bool {
	if insecureHTTPAllowed {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OMRS_ALLOW_INSECURE_HTTP"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// AllowConfigPassword reports whether a password may be written into
// config.json when the OS credential store is unavailable.
// Default is false; opt in with OMRS_ALLOW_CONFIG_PASSWORD=1|true|yes
// or the CLI --store-password-in-config flag (via SetAllowConfigPassword).
var configPasswordAllowed bool

// SetAllowConfigPassword toggles config-file password storage (tests + CLI flag).
func SetAllowConfigPassword(v bool) { configPasswordAllowed = v }

// ConfigPasswordAllowed reports whether plaintext config password storage is allowed.
func ConfigPasswordAllowed() bool {
	if configPasswordAllowed {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OMRS_ALLOW_CONFIG_PASSWORD"))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}

// NormalizeServerURL validates and normalizes an OpenMRS base URL.
// Rejects non-http(s) schemes and embedded userinfo (credentials in the URL).
// Cleartext HTTP to a non-loopback host is refused unless AllowInsecureHTTP
// (flag or OMRS_ALLOW_INSECURE_HTTP); when allowed, a stderr warning is still emitted.
func NormalizeServerURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty server URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid server URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("server URL must use http or https, got %q", u.Scheme)
	}
	if u.User != nil {
		return "", fmt.Errorf("server URL must not embed credentials; use omrs login or OMRS_PASSWORD")
	}
	if u.Host == "" {
		return "", fmt.Errorf("server URL missing host")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		if !AllowInsecureHTTP() {
			return "", fmt.Errorf("refusing cleartext HTTP to non-local host %q; use https, or set OMRS_ALLOW_INSECURE_HTTP=1 / --allow-insecure-http for lab networks only", u.Hostname())
		}
		warnJSON("using cleartext HTTP to a non-local host; credentials will be sent unencrypted")
	}
	return strings.TrimRight(raw, "/"), nil
}

func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// OriginKey returns a stable scheme|host|port identity for credential
// binding. Paths are ignored so https://host/openmrs and
// https://host/openmrs/ match. Host comparison is case-insensitive;
// default ports 80/443 are normalized.
func OriginKey(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty server URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid server URL: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("server URL must use http or https, got %q", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("server URL missing host")
	}
	port := u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return scheme + "|" + host + "|" + port, nil
}

// SameOrigin reports whether two OpenMRS base URLs share scheme/host/port.
func SameOrigin(a, b string) (bool, error) {
	ka, err := OriginKey(a)
	if err != nil {
		return false, err
	}
	kb, err := OriginKey(b)
	if err != nil {
		return false, err
	}
	return ka == kb, nil
}

// ClearProfileSecrets removes any stored password for a profile (keychain
// and/or config-file field). Used when the profile URL origin changes so
// a secret cannot ride along to a new host.
func ClearProfileSecrets(name string, p *Profile) {
	if p == nil {
		return
	}
	_ = secrets.Delete(name)
	p.Password = ""
	p.PasswordStore = ""
}

// Resolve applies precedence: flags > env > named profile > defaultProfile > built-ins.
//
// Stored profile passwords (keychain or config file) are origin-bound: they
// are only used when the final server URL shares scheme/host/port with the
// profile URL. Invocation-scoped passwords (OMRS_PASSWORD / Overrides.Password)
// may be sent to any URL the caller chose for this process.
func Resolve(o Overrides) (Resolved, error) {
	cfg, err := Load()
	if err != nil {
		return Resolved{}, err
	}

	// Only the URL has a built-in default. Credentials must come from a
	// profile, env vars, flags, or `omrs login` — silent default
	// credentials would make logout meaningless.
	res := Resolved{URL: DefaultURL}

	profileName := o.Profile
	if profileName == "" {
		profileName = os.Getenv("OMRS_PROFILE")
	}
	if profileName == "" {
		profileName = cfg.DefaultProfile
	}

	// Profile secret is held separately until we know the final URL origin.
	var profileURL string
	var profilePassword string
	keychainProfile := ""
	var keychainReadErr error
	if profileName != "" {
		p, ok := cfg.Profiles[profileName]
		if !ok {
			// An explicitly requested profile must exist; a missing default is fine.
			if o.Profile != "" || os.Getenv("OMRS_PROFILE") != "" {
				return Resolved{}, fmt.Errorf("profile %q not found in %s", profileName, Path())
			}
		} else {
			res.Profile = profileName
			if p.URL != "" {
				res.URL = p.URL
				profileURL = p.URL
			}
			if p.User != "" {
				res.User = p.User
			}
			if p.PasswordStore == "keychain" {
				keychainProfile = profileName
				pw, err := secrets.Get(profileName)
				switch {
				case err == nil:
					profilePassword = pw
				case errors.Is(err, secrets.ErrNotFound):
					// Leave empty; hard-fail below if still unset after overrides.
				default:
					// Store unavailable (common on headless Linux CI with no
					// Secret Service). Do not abort yet — OMRS_PASSWORD or
					// flags may still supply credentials.
					keychainReadErr = err
				}
			} else if p.Password != "" {
				profilePassword = p.Password
			}
		}
	}

	if v := os.Getenv("OMRS_SERVER"); v != "" {
		res.URL = v
	}
	if v := os.Getenv("OMRS_USER"); v != "" {
		res.User = v
	}
	if o.Server != "" {
		res.URL = o.Server
	}
	if o.User != "" {
		res.User = o.User
	}

	// Invocation-scoped password (env or internal override) is not
	// origin-bound: the caller supplied it for this process.
	invocationPassword := ""
	if v := os.Getenv("OMRS_PASSWORD"); v != "" {
		invocationPassword = v
	}
	if o.Password != "" {
		invocationPassword = o.Password
	}

	norm, err := NormalizeServerURL(res.URL)
	if err != nil {
		return Resolved{}, err
	}
	res.URL = norm

	if invocationPassword != "" {
		res.Password = invocationPassword
	} else if profilePassword != "" {
		if profileURL == "" {
			return Resolved{}, fmt.Errorf("%w: profile %q has credentials but no server URL; run 'omrs login' or set OMRS_PASSWORD",
				ErrCredentialOrigin, profileName)
		}
		profNorm, err := NormalizeServerURL(profileURL)
		if err != nil {
			return Resolved{}, err
		}
		same, err := SameOrigin(profNorm, res.URL)
		if err != nil {
			return Resolved{}, err
		}
		if !same {
			return Resolved{}, fmt.Errorf("%w: refusing to send credentials for profile %q to a different server (profile is %s; request targets %s). Run 'omrs login' for the new server, or set OMRS_PASSWORD for one-shot use",
				ErrCredentialOrigin, profileName, profNorm, res.URL)
		}
		res.Password = profilePassword
	}

	// Profile said keychain but we still have no password after all
	// overrides — fail loudly so agents do not issue anonymous requests.
	if keychainProfile != "" && res.Password == "" {
		if keychainReadErr != nil {
			return Resolved{}, fmt.Errorf("%w: credential store unavailable for profile %q: %v; set OMRS_PASSWORD or run 'omrs login' where a keyring is available",
				ErrCredentialStore, keychainProfile, keychainReadErr)
		}
		return Resolved{}, fmt.Errorf("%w: profile %q expects a credential-store entry but none was found; run 'omrs login' or set OMRS_PASSWORD",
			ErrCredentialStore, keychainProfile)
	}

	return res, nil
}
