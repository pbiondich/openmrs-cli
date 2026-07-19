// Package config manages named server profiles stored in ~/.config/omrs/config.json
// and resolves the effective server/credentials from flags, environment, and profiles.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

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

// Resolve applies precedence: flags > env > named profile > defaultProfile > built-ins.
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

	// Track whether the profile expected a keychain entry so a missing
	// secret can hard-fail after flag/env overrides are considered.
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
			}
			if p.User != "" {
				res.User = p.User
			}
			if p.PasswordStore == "keychain" {
				keychainProfile = profileName
				pw, err := secrets.Get(profileName)
				switch {
				case err == nil:
					res.Password = pw
				case errors.Is(err, secrets.ErrNotFound):
					// Leave empty; hard-fail below if still unset after overrides.
				default:
					// Store unavailable (common on headless Linux CI with no
					// Secret Service). Do not abort yet — OMRS_PASSWORD or
					// flags may still supply credentials.
					keychainReadErr = err
				}
			} else if p.Password != "" {
				res.Password = p.Password
			}
		}
	}

	if v := os.Getenv("OMRS_SERVER"); v != "" {
		res.URL = v
	}
	if v := os.Getenv("OMRS_USER"); v != "" {
		res.User = v
	}
	if v := os.Getenv("OMRS_PASSWORD"); v != "" {
		res.Password = v
	}

	if o.Server != "" {
		res.URL = o.Server
	}
	if o.User != "" {
		res.User = o.User
	}
	if o.Password != "" {
		res.Password = o.Password
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
