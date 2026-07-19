package cli

import (
	"net/url"

	"github.com/spf13/cobra"
)

var userQuery string

var userCmd = &cobra.Command{
	Use:   "user",
	Short: "List and retrieve users",
	RunE:  groupRunE,
}

var userListCmd = &cobra.Command{
	Use:   "list",
	Short: "List users",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		params := url.Values{}
		if userQuery != "" {
			params.Set("q", userQuery)
		}
		return fetchList("user", params, "user")
	},
}

func init() {
	userListCmd.Flags().StringVarP(&userQuery, "query", "q", "", "search users by name")
	userCmd.AddCommand(userListCmd, getCmd("user", "user"))
	rootCmd.AddCommand(userCmd)
}
