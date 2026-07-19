package cli

import (
	"net/url"

	"github.com/spf13/cobra"
)

var locationTag string

var locationCmd = &cobra.Command{
	Use:   "location",
	Short: "List and retrieve locations",
	RunE:  groupRunE,
}

var locationListCmd = &cobra.Command{
	Use:   "list",
	Short: "List locations",
	Example: `  omrs location list
  omrs location list --tag "Login Location"`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		params := url.Values{}
		if locationTag != "" {
			params.Set("tag", locationTag)
		}
		return fetchList(cmd.Context(), "location", params, "location")
	},
}

func init() {
	locationListCmd.Flags().StringVar(&locationTag, "tag", "", "filter by location tag name or UUID")
	locationCmd.AddCommand(locationListCmd, getCmd("location", "location"))
	rootCmd.AddCommand(locationCmd)
}
