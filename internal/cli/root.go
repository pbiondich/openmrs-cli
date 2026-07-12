// Package cli defines the omrs command tree.
package cli

import (
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/pbiondich/openmrs-cli/internal/client"
	"github.com/pbiondich/openmrs-cli/internal/config"
	"github.com/pbiondich/openmrs-cli/internal/output"
)

// Version is the release version, injected at build time by GoReleaser
// via -ldflags; "dev" for local builds.
var Version = "dev"

// PaginationCap bounds --all to avoid unbounded fetches.
const PaginationCap = 5000

var flags struct {
	server   string
	user     string
	password string
	profile  string
	jsonOut  bool
	tableOut bool
	full     bool
	ref      bool
	fields   string
	limit    int
	start    int
	all      bool
}

var rootCmd = &cobra.Command{
	Use:     "omrs",
	Short:   "Query OpenMRS servers from the command line",
	Version: Version,
	Long: `omrs — an AI-agent-friendly CLI for the OpenMRS REST Web Services API.

Read-only queries against any OpenMRS server: patients, encounters,
observations, concepts, visits, locations, users, and providers, plus a
generic escape hatch (omrs get <path>) for every other REST resource.

Output is a human-readable table on a terminal and JSON when piped;
force either with --json or --table. Errors are structured JSON on
stderr with stable exit codes:
  0 success · 1 unknown · 2 auth · 3 connection · 4 not found · 5 bad request

Servers are configured as named profiles (see omrs config --help).
Defaults to http://localhost/openmrs; override with --server/--profile
or the OMRS_SERVER, OMRS_USER, OMRS_PASSWORD, OMRS_PROFILE env vars.`,
	SilenceErrors: true,
	SilenceUsage:  true,
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVarP(&flags.server, "server", "s", "", "OpenMRS server URL, e.g. https://dev3.openmrs.org/openmrs")
	pf.StringVarP(&flags.user, "user", "u", "", "username")
	pf.StringVarP(&flags.password, "password", "p", "", "password")
	pf.StringVar(&flags.profile, "profile", "", "named config profile to use")
	pf.BoolVar(&flags.jsonOut, "json", false, "force JSON output (default when piped)")
	pf.BoolVar(&flags.tableOut, "table", false, "force table output (default on a terminal)")
	pf.BoolVar(&flags.full, "full", false, "full representation (v=full)")
	pf.BoolVar(&flags.ref, "ref", false, "minimal representation (v=ref)")
	pf.StringVar(&flags.fields, "fields", "", "custom fields, comma-separated (v=custom), e.g. uuid,display,person.age")
	pf.IntVarP(&flags.limit, "limit", "l", 25, "results per page")
	pf.IntVar(&flags.start, "start", 0, "start index for pagination")
	pf.BoolVar(&flags.all, "all", false, fmt.Sprintf("fetch all pages (cap %d)", PaginationCap))
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	if err := rootCmd.Execute(); err != nil {
		// flags.jsonOut is unset when flag parsing itself failed, so also
		// honor a literal --json on the command line.
		return output.PrintError(err, flags.jsonOut || slices.Contains(os.Args[1:], "--json"))
	}
	return 0
}

// newClient resolves connection settings and builds an API client.
func newClient() (*client.Client, error) {
	res, err := config.Resolve(config.Overrides{
		Server:   flags.server,
		User:     flags.user,
		Password: flags.password,
		Profile:  flags.profile,
	})
	if err != nil {
		return nil, err
	}
	return client.New(res), nil
}

// representation maps --full/--ref/--fields to the v= query parameter.
func representation() string {
	switch {
	case flags.full:
		return "full"
	case flags.ref:
		return "ref"
	case flags.fields != "":
		fs := strings.Split(flags.fields, ",")
		for i := range fs {
			fs[i] = strings.TrimSpace(fs[i])
		}
		return "custom:(" + strings.Join(fs, ",") + ")"
	default:
		return "default"
	}
}

func outputMode() output.Mode {
	return output.Detect(flags.jsonOut, flags.tableOut)
}

// fetchListData performs a list/search query honoring limit/start/--all.
func fetchListData(path string, params url.Values) (map[string]any, error) {
	c, err := newClient()
	if err != nil {
		return nil, err
	}
	params.Set("v", representation())

	if flags.all {
		return c.GetAll(path, params, PaginationCap)
	}
	params.Set("limit", fmt.Sprint(flags.limit))
	if flags.start > 0 {
		params.Set("startIndex", fmt.Sprint(flags.start))
	}
	return c.Get(path, params)
}

// fetchList fetches and prints a list/search query.
func fetchList(path string, params url.Values, resource string) error {
	data, err := fetchListData(path, params)
	if err != nil {
		return err
	}
	return output.Print(data, outputMode(), resource)
}

// fetchOne gets a single resource by path and prints it.
func fetchOne(path, resource string) error {
	c, err := newClient()
	if err != nil {
		return err
	}
	params := url.Values{}
	params.Set("v", representation())
	data, err := c.Get(path, params)
	if err != nil {
		return err
	}
	return output.Print(data, outputMode(), resource)
}

// getCmd builds a standard `<resource> get <uuid>` subcommand.
func getCmd(resource, apiPath string) *cobra.Command {
	return &cobra.Command{
		Use:   "get <uuid>",
		Short: fmt.Sprintf("Get a %s by UUID", resource),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return fetchOne(apiPath+"/"+args[0], resource)
		},
	}
}

func warnIfNoFilter(params url.Values, hint string) {
	if len(params) == 0 && !flags.all {
		fmt.Fprintf(os.Stderr, `{"warning":"no filter given; OpenMRS may return an empty list — try %s"}`+"\n", hint)
	}
}
