package cli

import (
	"net/url"

	"github.com/spf13/cobra"
)

var conceptClass, conceptSource string

var conceptCmd = &cobra.Command{
	Use:   "concept",
	Short: "Search and retrieve concepts",
	RunE:  groupRunE,
}

var conceptSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search concepts by name",
	Example: `  omrs concept search "malaria"
  omrs concept search "blood pressure" --limit 5 --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := url.Values{}
		params.Set("q", args[0])
		if conceptClass != "" {
			params.Set("class", conceptClass)
		}
		if conceptSource != "" {
			params.Set("source", conceptSource)
		}
		return fetchList(cmd.Context(), "concept", params, "concept")
	},
}

func init() {
	conceptSearchCmd.Flags().StringVar(&conceptClass, "class", "", "filter by concept class UUID")
	conceptSearchCmd.Flags().StringVar(&conceptSource, "source", "", "filter by concept source UUID")
	conceptCmd.AddCommand(conceptSearchCmd, getCmd("concept", "concept"))
	rootCmd.AddCommand(conceptCmd)
}
