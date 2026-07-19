package cli

import (
	"net/url"

	"github.com/spf13/cobra"
)

var encounterPatient, encounterType, encounterSince, encounterUntil string

var encounterCmd = &cobra.Command{
	Use:   "encounter",
	Short: "List and retrieve encounters",
	RunE:  groupRunE,
}

var encounterListCmd = &cobra.Command{
	Use:   "list",
	Short: "List encounters (OpenMRS returns an empty list without a filter)",
	Example: `  omrs encounter list --patient <uuid>
  omrs encounter list --patient <uuid> --since 30d
  omrs encounter list --patient <uuid> --since 2026-01-01 --until yesterday --all`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		params := url.Values{}
		if encounterPatient != "" {
			params.Set("patient", encounterPatient)
		}
		if encounterType != "" {
			params.Set("encounterType", encounterType)
		}
		if encounterSince != "" {
			_, s, err := parseWhen(encounterSince, false)
			if err != nil {
				return err
			}
			params.Set("fromdate", s)
		}
		if encounterUntil != "" {
			_, s, err := parseWhen(encounterUntil, true)
			if err != nil {
				return err
			}
			params.Set("todate", s)
		}
		warnIfNoFilter(params, "--patient <uuid>")
		return fetchList("encounter", params, "encounter")
	},
}

func init() {
	encounterListCmd.Flags().StringVar(&encounterPatient, "patient", "", "filter by patient UUID")
	encounterListCmd.Flags().StringVar(&encounterType, "type", "", "filter by encounter type UUID")
	encounterListCmd.Flags().StringVar(&encounterSince, "since", "", "only encounters on/after this date (YYYY-MM-DD, 7d, today, ...)")
	encounterListCmd.Flags().StringVar(&encounterUntil, "until", "", "only encounters on/before this date")
	encounterCmd.AddCommand(encounterListCmd, getCmd("encounter", "encounter"))
	rootCmd.AddCommand(encounterCmd)
}
