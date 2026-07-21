package cli

import (
	"testing"

	"github.com/pbiondich/openmrs-cli/internal/config"
	"github.com/pbiondich/openmrs-cli/internal/secrets"
)

func TestSetProfileURLChangeClearsCredentials(t *testing.T) {
	secrets.MockInit()
	isolatedConfig(t)
	if err := secrets.Set("prod", "hospital-secret"); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		DefaultProfile: "prod",
		Profiles: map[string]config.Profile{
			"prod": {
				URL:           "https://hospital.example/openmrs",
				User:          "nurse",
				PasswordStore: "keychain",
			},
		},
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	oldURL, oldUser, oldStdin := setProfileURL, setProfileUser, setProfilePasswordStdin
	t.Cleanup(func() {
		setProfileURL, setProfileUser, setProfilePasswordStdin = oldURL, oldUser, oldStdin
	})
	setProfileURL = "https://attacker.example/openmrs"
	setProfileUser = ""
	setProfilePasswordStdin = false

	if err := configSetProfileCmd.RunE(configSetProfileCmd, []string{"prod"}); err != nil {
		t.Fatal(err)
	}
	saved, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := saved.Profiles["prod"]
	if p.URL != "https://attacker.example/openmrs" {
		t.Fatalf("url=%q", p.URL)
	}
	if p.Password != "" || p.PasswordStore != "" {
		t.Fatalf("credentials must be cleared on origin change: %+v", p)
	}
	if _, err := secrets.Get("prod"); err == nil {
		t.Fatal("keychain entry must be deleted")
	}

	// Same-origin path-only change keeps keychain when we re-set.
	if err := secrets.Set("prod", "hospital-secret"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.Load()
	p = cfg.Profiles["prod"]
	p.PasswordStore = "keychain"
	p.URL = "https://hospital.example/openmrs"
	cfg.Profiles["prod"] = p
	_ = config.Save(cfg)

	setProfileURL = "https://hospital.example/openmrs/"
	if err := configSetProfileCmd.RunE(configSetProfileCmd, []string{"prod"}); err != nil {
		t.Fatal(err)
	}
	saved, _ = config.Load()
	p = saved.Profiles["prod"]
	if p.PasswordStore != "keychain" {
		t.Fatalf("same origin must not clear credentials: %+v", p)
	}
	pw, err := secrets.Get("prod")
	if err != nil || pw != "hospital-secret" {
		t.Fatalf("secret=%q err=%v", pw, err)
	}
}
