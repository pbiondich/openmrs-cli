package cli

import (
	"net/url"

	"github.com/spf13/cobra"
)

var patientByIdentifier bool

var patientCmd = &cobra.Command{
	Use:   "patient",
	Short: "Search and retrieve patients",
	RunE:  groupRunE,
}

var patientSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search patients by name or identifier",
	Example: `  omrs patient search "john"
  omrs patient search 1001HPV --identifier
  omrs patient search "mary" --fields uuid,display,person.age --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		params := url.Values{}
		if patientByIdentifier {
			params.Set("identifier", args[0])
		} else {
			params.Set("q", args[0])
		}
		return fetchList(cmd.Context(), "patient", params, "patient")
	},
}

func init() {
	patientSearchCmd.Flags().BoolVar(&patientByIdentifier, "identifier", false, "search by patient identifier instead of name")
	patientCmd.AddCommand(patientSearchCmd, getCmd("patient", "patient"))
	rootCmd.AddCommand(patientCmd)
}
