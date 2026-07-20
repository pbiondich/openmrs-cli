// Package output renders results as JSON (for pipes/agents) or aligned
// tables (for interactive terminals), and writes structured errors to stderr.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/pbiondich/openmrs-cli/internal/client"
)

type Mode int

const (
	ModeJSON Mode = iota
	ModeTable
)

// Detect picks the output mode: explicit flags win, otherwise JSON when
// stdout is not a terminal (piped/redirected — the agent case).
func Detect(jsonFlag, tableFlag bool) Mode {
	switch {
	case jsonFlag:
		return ModeJSON
	case tableFlag:
		return ModeTable
	case stdoutIsTTY():
		return ModeTable
	default:
		return ModeJSON
	}
}

func stdoutIsTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// Column defines one table column: header label + dot-path into the record.
type Column struct {
	Label string
	Path  string
}

// Columns maps a resource hint to its default table columns.
var Columns = map[string][]Column{
	"patient": {
		{"UUID", "uuid"}, {"NAME", "person.display"}, {"GENDER", "person.gender"},
		{"AGE", "person.age"}, {"BIRTHDATE", "person.birthdate"}, {"ID", "display"},
	},
	"concept": {
		{"UUID", "uuid"}, {"DISPLAY", "display"},
		{"DATATYPE", "datatype.display"}, {"CLASS", "conceptClass.display"},
	},
	"encounter": {
		{"UUID", "uuid"}, {"TYPE", "encounterType.display"},
		{"DATETIME", "encounterDatetime"}, {"LOCATION", "location.display"},
	},
	"obs": {
		{"UUID", "uuid"}, {"CONCEPT", "concept.display"},
		{"VALUE", "value.display|value"}, {"DATETIME", "obsDatetime"},
	},
	"visit": {
		{"UUID", "uuid"}, {"TYPE", "visitType.display"}, {"PATIENT", "patient.display"},
		{"START", "startDatetime"}, {"STOP", "stopDatetime"},
	},
	"location": {
		{"UUID", "uuid"}, {"NAME", "name|display"}, {"DESCRIPTION", "description"},
	},
	"user": {
		{"UUID", "uuid"}, {"DISPLAY", "display"}, {"USERNAME", "username"}, {"SYSTEMID", "systemId"},
	},
	"provider": {
		{"UUID", "uuid"}, {"DISPLAY", "display"},
	},
}

var fallbackColumns = []Column{{"UUID", "uuid"}, {"DISPLAY", "display|name"}}

// Print renders data. Objects containing a "results" array render as a table
// in table mode; other objects render as key/value records.
func Print(data map[string]any, mode Mode, resource string) error {
	if mode == ModeJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(data)
	}
	if results, ok := data["results"].([]any); ok {
		printTable(results, resource)
		return nil
	}
	printRecord(data)
	return nil
}

func printTable(results []any, resource string) {
	cols, ok := Columns[resource]
	if !ok {
		cols = fallbackColumns
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = c.Label
	}
	fmt.Fprintln(w, strings.Join(headers, "\t"))
	for _, r := range results {
		rec, _ := r.(map[string]any)
		vals := make([]string, len(cols))
		for i, c := range cols {
			vals[i] = truncate(Extract(rec, c.Path), 60)
		}
		fmt.Fprintln(w, strings.Join(vals, "\t"))
	}
	w.Flush()
	fmt.Fprintf(os.Stdout, "\n%d result(s)\n", len(results))
}

func printRecord(rec map[string]any) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, k := range sortedKeys(rec) {
		if k == "links" || k == "resourceVersion" {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\n", k, truncate(formatValue(rec[k]), 100))
	}
	w.Flush()
}

func sortedKeys(m map[string]any) []string {
	// Show identity fields first, then the rest alphabetically.
	priority := []string{"uuid", "display", "name"}
	seen := map[string]bool{}
	var keys []string
	for _, p := range priority {
		if _, ok := m[p]; ok {
			keys = append(keys, p)
			seen[p] = true
		}
	}
	var rest []string
	for k := range m {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	// insertion sort is fine at this scale
	for i := 1; i < len(rest); i++ {
		for j := i; j > 0 && rest[j] < rest[j-1]; j-- {
			rest[j], rest[j-1] = rest[j-1], rest[j]
		}
	}
	return append(keys, rest...)
}

// Extract resolves a dot-path (e.g. "person.age") within a decoded JSON
// object. Alternate paths may be separated by "|": the first non-empty wins.
func Extract(rec map[string]any, path string) string {
	for _, alt := range strings.Split(path, "|") {
		if v := lookup(rec, alt); v != "" {
			return v
		}
	}
	return ""
}

func lookup(rec map[string]any, path string) string {
	var cur any = rec
	for _, part := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[part]
	}
	return formatValue(cur)
}

func formatValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case map[string]any:
		if d, ok := t["display"].(string); ok {
			return d
		}
		if n, ok := t["name"].(string); ok {
			return n
		}
		b, _ := json.Marshal(t)
		return string(b)
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, formatValue(e))
		}
		return strings.Join(parts, ", ")
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

// truncate shortens s to at most n bytes plus an ellipsis, never
// cutting a multibyte rune in half (invalid UTF-8 corrupts terminals
// and downstream JSON logs).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - 1
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// usagePrefixes identify cobra's own parse/usage errors (unknown
// command, bad flag, wrong arg count) so they can be labeled USAGE and
// given a help hint rather than the generic UNKNOWN.
var usagePrefixes = []string{
	"unknown command", "unknown flag", "unknown shorthand",
	"flag needs", "invalid argument", "accepts ", "requires ",
}

func isUsageError(msg string) bool {
	for _, p := range usagePrefixes {
		if strings.HasPrefix(msg, p) {
			return true
		}
	}
	return false
}

// Warn emits an advisory one-line JSON warning to stderr. Always
// marshaled — never hand-built — so embedded quotes can't produce
// invalid JSON on the wire agents parse.
func Warn(format string, args ...any) {
	b, _ := json.Marshal(map[string]string{"warning": fmt.Sprintf(format, args...)})
	fmt.Fprintln(os.Stderr, string(b))
}

// PrintError writes the error to stderr and returns the process exit
// code. Agents (stderr piped, or --json) get one-line structured JSON;
// humans at a terminal get plain readable text.
func PrintError(err error, forceJSON bool) int {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		code := client.CodeUnknown
		if isUsageError(err.Error()) {
			code = client.CodeUsage
		}
		apiErr = &client.APIError{Message: err.Error(), Code: code}
	}

	if forceJSON || !stderrIsTTY() {
		b, _ := json.Marshal(apiErr)
		fmt.Fprintln(os.Stderr, string(b))
		return client.ExitCode(apiErr.Code)
	}

	fmt.Fprintf(os.Stderr, "Error: %s\n", strings.TrimRight(apiErr.Message, "\n"))
	if apiErr.Detail != "" {
		fmt.Fprintf(os.Stderr, "  %s\n", apiErr.Detail)
	}
	if apiErr.Code == client.CodeUsage {
		fmt.Fprintln(os.Stderr, "\nRun 'omrs --help' for usage.")
	}
	return client.ExitCode(apiErr.Code)
}

func stderrIsTTY() bool {
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
