package cli

import (
	"net/url"
	"time"

	"github.com/spf13/cobra"

	"github.com/pbiondich/openmrs-cli/internal/output"
)

var obsPatient, obsEncounter, obsConcept, obsSince, obsUntil string

var obsCmd = &cobra.Command{
	Use:   "obs",
	Short: "List and retrieve observations",
	RunE:  groupRunE,
}

var obsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List observations (OpenMRS returns an empty list without a filter)",
	Example: `  omrs obs list --patient <uuid>
  omrs obs list --patient <uuid> --concept <concept-uuid> --all --json
  omrs obs list --patient <uuid> --since 90d --all`,
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

		// The obs REST search handler ignores date parameters, so date
		// bounds are applied client-side after fetch.
		var since, until time.Time
		var err error
		if obsSince != "" {
			if since, _, err = parseWhen(obsSince, false); err != nil {
				return err
			}
		}
		if obsUntil != "" {
			if until, _, err = parseWhen(obsUntil, true); err != nil {
				return err
			}
		}

		data, err := fetchListData(cmd.Context(), "obs", params)
		if err != nil {
			return err
		}
		if !since.IsZero() || !until.IsZero() {
			warnClientSideFilter("obs")
			data = filterResultsByDate(data, "obsDatetime", since, until)
		}
		return output.Print(data, outputMode(), "obs")
	},
}

func init() {
	obsListCmd.Flags().StringVar(&obsPatient, "patient", "", "filter by patient UUID")
	obsListCmd.Flags().StringVar(&obsEncounter, "encounter", "", "filter by encounter UUID")
	obsListCmd.Flags().StringVar(&obsConcept, "concept", "", "filter by concept UUID")
	obsListCmd.Flags().StringVar(&obsSince, "since", "", "only obs on/after this date (YYYY-MM-DD, 7d, today, ...; filtered client-side)")
	obsListCmd.Flags().StringVar(&obsUntil, "until", "", "only obs on/before this date (filtered client-side)")
	obsCmd.AddCommand(obsListCmd, getCmd("obs", "obs"))
	rootCmd.AddCommand(obsCmd)
}
