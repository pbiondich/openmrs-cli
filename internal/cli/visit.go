package cli

import (
	"net/url"

	"github.com/spf13/cobra"
)

var visitPatient, visitSince, visitUntil string

var visitCmd = &cobra.Command{
	Use:   "visit",
	Short: "List and retrieve visits",
	RunE:  groupRunE,
}

var visitListCmd = &cobra.Command{
	Use:   "list",
	Short: "List visits",
	Example: `  omrs visit list --patient <uuid>
  omrs visit list --patient <uuid> --since 6m`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		params := url.Values{}
		if visitPatient != "" {
			params.Set("patient", visitPatient)
		}
		if visitSince != "" {
			_, s, err := parseWhen(visitSince, false)
			if err != nil {
				return err
			}
			params.Set("fromStartDate", s)
		}
		if visitUntil != "" {
			_, s, err := parseWhen(visitUntil, true)
			if err != nil {
				return err
			}
			params.Set("toStartDate", s)
		}
		return fetchList(cmd.Context(), "visit", params, "visit")
	},
}

func init() {
	visitListCmd.Flags().StringVar(&visitPatient, "patient", "", "filter by patient UUID")
	visitListCmd.Flags().StringVar(&visitSince, "since", "", "only visits starting on/after this date (YYYY-MM-DD, 7d, today, ...)")
	visitListCmd.Flags().StringVar(&visitUntil, "until", "", "only visits starting on/before this date")
	visitCmd.AddCommand(visitListCmd, getCmd("visit", "visit"))
	rootCmd.AddCommand(visitCmd)
}
