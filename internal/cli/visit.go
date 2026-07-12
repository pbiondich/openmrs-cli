package cli

import (
	"net/url"

	"github.com/spf13/cobra"
)

var visitPatient string

var visitCmd = &cobra.Command{
	Use:   "visit",
	Short: "List and retrieve visits",
}

var visitListCmd = &cobra.Command{
	Use:     "list",
	Short:   "List visits",
	Example: `  omrs visit list --patient <uuid>`,
	Args:    cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		params := url.Values{}
		if visitPatient != "" {
			params.Set("patient", visitPatient)
		}
		return fetchList("visit", params, "visit")
	},
}

func init() {
	visitListCmd.Flags().StringVar(&visitPatient, "patient", "", "filter by patient UUID")
	visitCmd.AddCommand(visitListCmd, getCmd("visit", "visit"))
	rootCmd.AddCommand(visitCmd)
}
