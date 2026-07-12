package cli

import (
	"net/url"

	"github.com/spf13/cobra"
)

var obsPatient, obsEncounter, obsConcept string

var obsCmd = &cobra.Command{
	Use:   "obs",
	Short: "List and retrieve observations",
}

var obsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List observations (OpenMRS returns an empty list without a filter)",
	Example: `  omrs obs list --patient <uuid>
  omrs obs list --patient <uuid> --concept <concept-uuid> --all --json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		params := url.Values{}
		if obsPatient != "" {
			params.Set("patient", obsPatient)
		}
		if obsEncounter != "" {
			params.Set("encounter", obsEncounter)
		}
		if obsConcept != "" {
			params.Set("concept", obsConcept)
		}
		warnIfNoFilter(params, "--patient <uuid>")
		return fetchList("obs", params, "obs")
	},
}

func init() {
	obsListCmd.Flags().StringVar(&obsPatient, "patient", "", "filter by patient UUID")
	obsListCmd.Flags().StringVar(&obsEncounter, "encounter", "", "filter by encounter UUID")
	obsListCmd.Flags().StringVar(&obsConcept, "concept", "", "filter by concept UUID")
	obsCmd.AddCommand(obsListCmd, getCmd("obs", "obs"))
	rootCmd.AddCommand(obsCmd)
}
