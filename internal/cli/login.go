package cli

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/pbiondich/openmrs-cli/internal/client"
	"github.com/pbiondich/openmrs-cli/internal/config"
	"github.com/pbiondich/openmrs-cli/internal/output"
	"github.com/pbiondich/openmrs-cli/internal/secrets"
)

var loginPasswordStdin bool

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate against an OpenMRS server and save the profile",
	Long: `Interactively prompts for server URL, username, and password, validates
the credentials against the server, and saves them to a profile.

The password is stored in the OS credential store — macOS Keychain,
Windows Credential Manager, or Secret Service (GNOME Keyring / KWallet)
on Linux — falling back to the config file (mode 0600) on headless
systems with no keyring. It is never echoed and never appears in
shell history.

For scripts and agents, use --password-stdin:
  echo "$OMRS_PW" | omrs login -s http://localhost/openmrs -u admin --password-stdin`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		// The target profile: --profile if given, else the active default.
		profileName := flags.profile
		if profileName == "" {
			profileName = os.Getenv("OMRS_PROFILE")
		}
		if profileName == "" {
			profileName = cfg.DefaultProfile
		}
		if profileName == "" {
			profileName = "local"
		}
		existing := cfg.Profiles[profileName]

		reader := bufio.NewReader(os.Stdin)

		serverURL := flags.server
		if serverURL == "" {
			def := existing.URL
			if def == "" {
				def = config.DefaultURL
			}
			serverURL = promptDefault(reader, fmt.Sprintf("Server URL [%s]: ", def), def)
		}
		serverURL = strings.TrimRight(serverURL, "/")

		username := flags.user
		if username == "" {
			def := existing.User
			if def == "" {
				def = config.DefaultUser
			}
			username = promptDefault(reader, fmt.Sprintf("Username [%s]: ", def), def)
		}

		password := flags.password
		if password == "" {
			if loginPasswordStdin {
				line, err := reader.ReadString('\n')
				if err != nil && err != io.EOF {
					return fmt.Errorf("reading password from stdin: %w", err)
				}
				password = strings.TrimRight(line, "\r\n")
			} else {
				fmt.Fprint(os.Stderr, "Password: ")
				pw, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(os.Stderr)
				if err != nil {
					return fmt.Errorf("reading password (use --password-stdin when not on a terminal): %w", err)
				}
				password = string(pw)
			}
		}
		if password == "" {
			return &client.APIError{Message: "no password provided", Code: client.CodeAuth}
		}

		// Validate against the server before saving anything.
		c := client.New(config.Resolved{URL: serverURL, User: username, Password: password})
		data, err := c.Get("session", url.Values{})
		if err != nil {
			return err
		}
		if auth, _ := data["authenticated"].(bool); !auth {
			return &client.APIError{
				Message: fmt.Sprintf("authentication failed for %q at %s", username, serverURL),
				Code:    client.CodeAuth,
			}
		}

		// Persist: URL/user in config; password in the OS credential store
		// when one is available (headless servers often have none).
		p := config.Profile{URL: serverURL, User: username}
		storage := secrets.StoreName()
		if err := secrets.Set(profileName, password); err != nil {
			fmt.Fprintf(os.Stderr, `{"warning":"OS credential store unavailable (%v); storing password in config file"}`+"\n", err)
			p.Password = password
			storage = "config file"
		} else {
			p.PasswordStore = "keychain"
		}
		cfg.Profiles[profileName] = p
		if cfg.DefaultProfile == "" {
			cfg.DefaultProfile = profileName
		}
		if err := config.Save(cfg); err != nil {
			return err
		}

		user, _ := data["user"].(map[string]any)
		display, _ := user["display"].(string)
		fmt.Printf("Logged in to %s as %s (profile %q, password in %s)\n", serverURL, display, profileName, storage)
		return nil
	},
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Remove stored credentials for a profile",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		profileName := flags.profile
		if profileName == "" {
			profileName = cfg.DefaultProfile
		}
		p, ok := cfg.Profiles[profileName]
		if !ok {
			return fmt.Errorf("profile %q not found", profileName)
		}
		if err := secrets.Delete(profileName); err != nil {
			fmt.Fprintf(os.Stderr, `{"warning":"could not remove credential-store entry: %v"}`+"\n", err)
		}
		p.Password = ""
		p.PasswordStore = ""
		cfg.Profiles[profileName] = p
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Printf("Removed credentials for profile %q\n", profileName)
		return nil
	},
}

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show the authenticated user (fails with exit 2 if not authenticated)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		data, err := c.Get("session", url.Values{})
		if err != nil {
			return err
		}
		auth, _ := data["authenticated"].(bool)
		if !auth {
			return &client.APIError{
				Message: fmt.Sprintf("not authenticated to %s", c.BaseURL()),
				Code:    client.CodeAuth,
			}
		}
		user, _ := data["user"].(map[string]any)
		result := map[string]any{
			"url":           c.BaseURL(),
			"authenticated": true,
			"user":          user["display"],
			"systemId":      user["systemId"],
			"uuid":          user["uuid"],
		}
		if outputMode() == output.ModeJSON {
			return output.Print(result, output.ModeJSON, "")
		}
		fmt.Printf("%s on %s\n", output.Extract(result, "user"), c.BaseURL())
		return nil
	},
}

func promptDefault(r *bufio.Reader, prompt, def string) string {
	fmt.Fprint(os.Stderr, prompt)
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func init() {
	loginCmd.Flags().BoolVar(&loginPasswordStdin, "password-stdin", false, "read the password from stdin (for scripts and agents)")
	rootCmd.AddCommand(loginCmd, logoutCmd, whoamiCmd)
}
