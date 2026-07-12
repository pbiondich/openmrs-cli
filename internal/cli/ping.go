package cli

import (
	"fmt"
	"net/url"
	"time"

	"github.com/spf13/cobra"

	"github.com/pbiondich/openmrs-cli/internal/output"
)

var pingCmd = &cobra.Command{
	Use:   "ping",
	Short: "Check connectivity to the OpenMRS server",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		start := time.Now()
		data, err := c.Get("session", url.Values{})
		if err != nil {
			return err
		}
		elapsed := time.Since(start).Milliseconds()
		authenticated, _ := data["authenticated"].(bool)

		result := map[string]any{
			"url":           c.BaseURL(),
			"authenticated": authenticated,
			"responseMs":    elapsed,
		}
		if outputMode() == output.ModeJSON {
			return output.Print(result, output.ModeJSON, "")
		}
		fmt.Printf("Connected to %s (%dms, authenticated: %v)\n", c.BaseURL(), elapsed, authenticated)
		return nil
	},
}

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Show authentication status and current user",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := newClient()
		if err != nil {
			return err
		}
		data, err := c.Get("session", url.Values{})
		if err != nil {
			return err
		}
		return output.Print(data, outputMode(), "")
	},
}

func init() {
	rootCmd.AddCommand(pingCmd, sessionCmd)
}
