package cli

import (
	"net/url"

	"github.com/spf13/cobra"
)

var providerQuery string

var providerCmd = &cobra.Command{
	Use:   "provider",
	Short: "List and retrieve providers",
	RunE:  groupRunE,
}

var providerListCmd = &cobra.Command{
	Use:   "list",
	Short: "List providers",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		params := url.Values{}
		if providerQuery != "" {
			params.Set("q", providerQuery)
		}
		return fetchList("provider", params, "provider")
	},
}

func init() {
	providerListCmd.Flags().StringVarP(&providerQuery, "query", "q", "", "search providers by name")
	providerCmd.AddCommand(providerListCmd, getCmd("provider", "provider"))
	rootCmd.AddCommand(providerCmd)
}
