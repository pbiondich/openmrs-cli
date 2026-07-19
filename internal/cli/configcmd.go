package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/pbiondich/openmrs-cli/internal/config"
	"github.com/pbiondich/openmrs-cli/internal/output"
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

var setProfileURL, setProfileUser, setProfilePassword string

var configSetProfileCmd = &cobra.Command{
	Use:   "set-profile <name>",
	Short: "Create or update a named profile",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		p := cfg.Profiles[name]
		if setProfileURL != "" {
			p.URL = setProfileURL
		}
		if setProfileUser != "" {
			p.User = setProfileUser
		}
		if setProfilePassword != "" {
			p.Password = setProfilePassword
		}
		if p.URL == "" {
			return fmt.Errorf("--url is required for a new profile")
		}
		cfg.Profiles[name] = p
		if cfg.DefaultProfile == "" {
			cfg.DefaultProfile = name
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("Saved profile %q\n", name)
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
	configSetProfileCmd.Flags().StringVar(&setProfilePassword, "password", "", "password")
	configCmd.AddCommand(configInitCmd, configShowCmd, configSetProfileCmd, configRemoveProfileCmd, configUseCmd)
	rootCmd.AddCommand(configCmd)
}
