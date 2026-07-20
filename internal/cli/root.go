// Package cli defines the omrs command tree.
package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

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
  0 success · 1 unknown · 2 auth · 3 connection · 4 not found · 5 bad request · 6 forbidden

Servers are configured as named profiles (see omrs config --help).
Defaults to http://localhost/openmrs; override with --server/--profile
or the OMRS_SERVER, OMRS_USER, OMRS_PASSWORD, OMRS_PROFILE env vars.

Passwords never appear on the command line (no -p flag). Use
omrs login, OMRS_PASSWORD, or --password-stdin on login.`,
	SilenceErrors: true,
	SilenceUsage:  true,
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.StringVarP(&flags.server, "server", "s", "", "OpenMRS server URL, e.g. https://dev3.openmrs.org/openmrs")
	pf.StringVarP(&flags.user, "user", "u", "", "username")
	pf.StringVar(&flags.profile, "profile", "", "named config profile to use")
	pf.BoolVar(&flags.jsonOut, "json", false, "force JSON output (default when piped)")
	pf.BoolVar(&flags.tableOut, "table", false, "force table output (default on a terminal)")
	pf.BoolVar(&flags.full, "full", false, "full representation (v=full)")
	pf.BoolVar(&flags.ref, "ref", false, "minimal representation (v=ref)")
	pf.StringVar(&flags.fields, "fields", "", "custom fields, comma-separated (v=custom), e.g. uuid,display,person.age")
	// Exactly one representation mode; previously --full silently won over
	// --ref/--fields, which is easy for an agent to miss.
	rootCmd.MarkFlagsMutuallyExclusive("full", "ref", "fields")
	pf.IntVarP(&flags.limit, "limit", "l", 25, "results per page")
	pf.IntVar(&flags.start, "start", 0, "start index for pagination")
	pf.BoolVar(&flags.all, "all", false, fmt.Sprintf("fetch all pages (cap %d)", PaginationCap))
}

// Execute runs the CLI and returns the process exit code.
// SIGINT/SIGTERM cancel the command context so in-flight HTTP stops.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := rootCmd.ExecuteContext(ctx); err != nil {
		// flags.jsonOut is unset when flag parsing itself failed, so also
		// honor a literal --json on the command line.
		return output.PrintError(err, flags.jsonOut || slices.Contains(os.Args[1:], "--json"))
	}
	return 0
}

// wrapResolveError maps config/credential failures to stable APIError codes.
func wrapResolveError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, config.ErrCredentialStore) {
		return &client.APIError{Message: err.Error(), Code: client.CodeAuth}
	}
	return err
}

// groupRunE is the RunE for parent commands like `omrs patient`: bare
// invocation shows help (exit 0); an unrecognized subcommand is a USAGE
// error on stderr with exit 1 — never exit-0 help on stdout, which an
// agent reads as success.
func groupRunE(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		return cmd.Help()
	}
	return &client.APIError{
		Message: fmt.Sprintf("unknown subcommand %q for %q", args[0], cmd.CommandPath()),
		Code:    client.CodeUsage,
	}
}

// newClient resolves connection settings and builds an API client.
// Passwords come from the profile credential store, config file, or
// OMRS_PASSWORD — never from a -p command-line flag.
// When ctx is non-nil, requests use that context (cancel on Ctrl-C).
func newClient(ctx context.Context) (*client.Client, error) {
	res, err := config.Resolve(config.Overrides{
		Server:  flags.server,
		User:    flags.user,
		Profile: flags.profile,
	})
	if err != nil {
		return nil, wrapResolveError(err)
	}
	c := client.New(res)
	if ctx != nil {
		c = c.WithContext(ctx)
	}
	return c, nil
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

// setRepresentation applies the v= parameter: explicit representation
// flags always win, but an inline v= the user supplied (e.g.
// `omrs get "concept?v=full"`) is preserved rather than overwritten.
func setRepresentation(params url.Values) {
	if flags.full || flags.ref || flags.fields != "" || !params.Has("v") {
		params.Set("v", representation())
	}
}

// fetchListData performs a list/search query honoring limit/start/--all.
func fetchListData(ctx context.Context, path string, params url.Values) (map[string]any, error) {
	c, err := newClient(ctx)
	if err != nil {
		return nil, err
	}
	setRepresentation(params)

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
func fetchList(ctx context.Context, path string, params url.Values, resource string) error {
	data, err := fetchListData(ctx, path, params)
	if err != nil {
		return err
	}
	return output.Print(data, outputMode(), resource)
}

// fetchOne gets a single resource by path and prints it (no list limit).
// Caller-supplied params (inline query or --param) are passed through.
func fetchOne(ctx context.Context, path, resource string, params url.Values) error {
	c, err := newClient(ctx)
	if err != nil {
		return err
	}
	if params == nil {
		params = url.Values{}
	}
	setRepresentation(params)
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
			return fetchOne(cmd.Context(), apiPath+"/"+args[0], resource, nil)
		},
	}
}

func warnIfNoFilter(params url.Values, hint string) {
	if len(params) == 0 && !flags.all {
		output.Warn("no filter given; OpenMRS may return an empty list — try %s", hint)
	}
}
