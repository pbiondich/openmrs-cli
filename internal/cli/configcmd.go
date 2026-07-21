package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pbiondich/openmrs-cli/internal/config"
	"github.com/pbiondich/openmrs-cli/internal/output"
	"github.com/pbiondich/openmrs-cli/internal/secrets"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage server profiles",
	Long: `Manage named server profiles stored in ` + config.Path() + `.

Profiles hold a server URL and credentials. The default profile is used
unless overridden by --profile, OMRS_PROFILE, or connection flags.`,
	RunE: groupRunE,
}

var configInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a starter config with 'local' and 'demo' profiles",
	RunE: func(cmd *cobra.Command, args []string) error {
		if existing, err := config.Load(); err == nil && len(existing.Profiles) > 0 {
			return fmt.Errorf("config already exists at %s (edit it directly or use set-profile)", config.Path())
		}
		if err := config.Save(config.Default()); err != nil {
			return err
		}
		fmt.Printf("Wrote %s (default profile: local)\n", config.Path())
		return nil
	},
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current config (passwords redacted)",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		profiles := map[string]any{}
		for name, p := range cfg.Profiles {
			redacted := ""
			if p.PasswordStore == "keychain" {
				redacted = "(keychain)"
			} else if p.Password != "" {
				redacted = "***"
			}
			profiles[name] = map[string]any{"url": p.URL, "user": p.User, "password": redacted}
		}
		return output.Print(map[string]any{
			"path":           config.Path(),
			"defaultProfile": cfg.DefaultProfile,
			"profiles":       profiles,
		}, output.ModeJSON, "")
	},
}

var (
	setProfileURL                string
	setProfileUser               string
	setProfilePasswordStdin      bool
	setProfileStorePasswordConfig bool
)

var configSetProfileCmd = &cobra.Command{
	Use:   "set-profile <name>",
	Short: "Create or update a named profile",
	Long: `Create or update a named profile's URL and username.

Prefer omrs login to store a password (OS credential store). To set a
password without a full login probe, use --password-stdin (never a
password flag — secrets must not appear in process listings):

  echo "$OMRS_PW" | omrs config set-profile local --url http://localhost/openmrs --user admin --password-stdin

If the OS store is unavailable, password save fails unless you pass
--store-password-in-config or set OMRS_ALLOW_CONFIG_PASSWORD=1.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		p := cfg.Profiles[name]
		clearedCreds := false
		if setProfileURL != "" {
			norm, err := config.NormalizeServerURL(setProfileURL)
			if err != nil {
				return err
			}
			// Changing origin must not leave a secret that Resolve would
			// refuse (or, before origin-binding, would send to the new host).
			if p.URL != "" && (p.PasswordStore == "keychain" || p.Password != "") {
				same, err := config.SameOrigin(p.URL, norm)
				if err != nil {
					return err
				}
				if !same {
					config.ClearProfileSecrets(name, &p)
					clearedCreds = true
				}
			}
			p.URL = norm
		}
		if setProfileUser != "" {
			p.User = setProfileUser
		}
		if setProfilePasswordStdin {
			reader := bufio.NewReader(os.Stdin)
			line, err := reader.ReadString('\n')
			if err != nil && err != io.EOF {
				return fmt.Errorf("reading password from stdin: %w", err)
			}
			password := strings.TrimRight(line, "\r\n")
			if password == "" {
				return fmt.Errorf("no password provided on stdin")
			}
			if _, err := storeProfilePassword(name, &p, password, setProfileStorePasswordConfig); err != nil {
				return err
			}
			clearedCreds = false // caller just set a password for the new URL
		}
		if p.URL == "" {
			return fmt.Errorf("--url is required for a new profile")
		}
		// Re-validate even if URL came from an existing profile only.
		if norm, err := config.NormalizeServerURL(p.URL); err != nil {
			return err
		} else {
			p.URL = norm
		}
		cfg.Profiles[name] = p
		if cfg.DefaultProfile == "" {
			cfg.DefaultProfile = name
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("Saved profile %q\n", name)
		if clearedCreds {
			fmt.Printf("Credentials cleared for %q (server origin changed); run 'omrs login' again.\n", name)
		}
		return nil
	},
}

var configRemoveProfileCmd = &cobra.Command{
	Use:   "remove-profile <name>",
	Short: "Delete a named profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if _, ok := cfg.Profiles[name]; !ok {
			return fmt.Errorf("profile %q not found", name)
		}
		if err := secrets.Delete(name); err != nil {
			output.Warn("could not remove credential-store entry: %v", err)
		}
		delete(cfg.Profiles, name)
		if cfg.DefaultProfile == name {
			cfg.DefaultProfile = ""
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("Removed profile %q\n", name)
		return nil
	},
}

var configUseCmd = &cobra.Command{
	Use:   "use <name>",
	Short: "Set the default profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if _, ok := cfg.Profiles[name]; !ok {
			return fmt.Errorf("profile %q not found (create it with: omrs config set-profile %s --url ...)", name, name)
		}
		cfg.DefaultProfile = name
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("Default profile is now %q\n", name)
		return nil
	},
}

func init() {
	configSetProfileCmd.Flags().StringVar(&setProfileURL, "url", "", "server URL, e.g. https://dev3.openmrs.org/openmrs")
	configSetProfileCmd.Flags().StringVar(&setProfileUser, "user", "", "username")
	configSetProfileCmd.Flags().BoolVar(&setProfilePasswordStdin, "password-stdin", false, "read password from stdin (never put passwords on the command line)")
	configSetProfileCmd.Flags().BoolVar(&setProfileStorePasswordConfig, "store-password-in-config", false, "if the OS credential store is unavailable, store the password in config.json (0600)")
	configCmd.AddCommand(configInitCmd, configShowCmd, configSetProfileCmd, configRemoveProfileCmd, configUseCmd)
	rootCmd.AddCommand(configCmd)
}
