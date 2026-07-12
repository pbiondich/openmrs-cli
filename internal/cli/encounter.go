package cli

import (
	"net/url"

	"github.com/spf13/cobra"
)

var encounterPatient, encounterType string

var encounterCmd = &cobra.Command{
	Use:   "encounter",
	Short: "List and retrieve encounters",
}

var encounterListCmd = &cobra.Command{
	Use:   "list",
	Short: "List encounters (OpenMRS returns an empty list without a filter)",
	Example: `  omrs encounter list --patient <uuid>
  omrs encounter list --patient <uuid> --type <encounter-type-uuid> --all`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		params := url.Values{}
		if encounterPatient != "" {
			params.Set("patient", encounterPatient)
		}
		if encounterType != "" {
			params.Set("encounterType", encounterType)
		}
		warnIfNoFilter(params, "--patient <uuid>")
		return fetchList("encounter", params, "encounter")
	},
}

func init() {
	encounterListCmd.Flags().StringVar(&encounterPatient, "patient", "", "filter by patient UUID")
	encounterListCmd.Flags().StringVar(&encounterType, "type", "", "filter by encounter type UUID")
	encounterCmd.AddCommand(encounterListCmd, getCmd("encounter", "encounter"))
	rootCmd.AddCommand(encounterCmd)
}
