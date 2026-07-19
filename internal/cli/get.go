package cli

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

var getParams []string

// instanceUUIDRe matches a single path segment that looks like a UUID.
var instanceUUIDRe = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var genericGetCmd = &cobra.Command{
	Use:   "get <path>",
	Short: "GET any REST resource path (generic escape hatch)",
	Long: `GET an arbitrary path under /ws/rest/v1/ — every OpenMRS REST resource
is reachable this way, including ones without a dedicated subcommand.

Query parameters can be given inline or with repeatable --param flags.

A path of the form <resource>/<uuid> is fetched as a single record (no
list limit). Collection paths honor --limit / --all.`,
	Example: `  omrs get visittype
  omrs get encountertype --json
  omrs get program --limit 50
  omrs get patient/<uuid>
  omrs get patient/<uuid>/encounter
  omrs get "patient?q=john"
  omrs get obs --param patient=<uuid> --param concept=<uuid>`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := args[0]
		params := url.Values{}

		// Inline ?k=v query strings merge with --param flags.
		if i := strings.IndexByte(path, '?'); i >= 0 {
			inline, err := url.ParseQuery(path[i+1:])
			if err != nil {
				return fmt.Errorf("invalid query string in path: %w", err)
			}
			for k, vs := range inline {
				for _, v := range vs {
					params.Add(k, v)
				}
			}
			path = path[:i]
		}
		for _, kv := range getParams {
			k, v, found := strings.Cut(kv, "=")
			if !found || k == "" {
				return fmt.Errorf("--param must be key=value, got %q", kv)
			}
			params.Add(k, v)
		}

		resource := strings.SplitN(path, "/", 2)[0]
		if isInstancePath(path) {
			// Merge representation flags only; do not force limit.
			return fetchOne(cmd.Context(), path, resource)
		}
		return fetchList(cmd.Context(), path, params, resource)
	},
}

// isInstancePath reports paths of the form resource/<uuid> (exactly two
// segments, second a UUID). Nested lists like patient/<uuid>/encounter
// stay collection fetches.
func isInstancePath(path string) bool {
	path = strings.Trim(path, "/")
	if path == "" {
		return false
	}
	parts := strings.Split(path, "/")
	return len(parts) == 2 && instanceUUIDRe.MatchString(parts[1])
}

func init() {
	genericGetCmd.Flags().StringArrayVar(&getParams, "param", nil, "query parameter key=value (repeatable)")
	rootCmd.AddCommand(genericGetCmd)
}
