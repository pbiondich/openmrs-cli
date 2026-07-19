package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pbiondich/openmrs-cli/internal/output"
)

var relativeRe = regexp.MustCompile(`^(\d+)([dwmy])$`)

// parseWhen turns a user-supplied date expression into a time plus the
// string form to send to the server. endOfDay controls how a date-only
// value is anchored: --until 2026-01-02 should include that whole day.
//
// Accepted forms: 2026-01-02, 2026-01-02T14:30[:05], 7d, 4w, 6m, 1y
// (that long ago), today, yesterday.
func parseWhen(s string, endOfDay bool) (time.Time, string, error) {
	s = strings.TrimSpace(s)
	now := time.Now()

	anchor := func(t time.Time) (time.Time, string, error) {
		if endOfDay {
			t = time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, t.Location())
		} else {
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
		}
		return t, t.Format("2006-01-02T15:04:05"), nil
	}

	switch strings.ToLower(s) {
	case "today":
		return anchor(now)
	case "yesterday":
		return anchor(now.AddDate(0, 0, -1))
	}

	if m := relativeRe.FindStringSubmatch(strings.ToLower(s)); m != nil {
		n, _ := strconv.Atoi(m[1])
		var t time.Time
		switch m[2] {
		case "d":
			t = now.AddDate(0, 0, -n)
		case "w":
			t = now.AddDate(0, 0, -7*n)
		case "m":
			t = now.AddDate(0, -n, 0)
		case "y":
			t = now.AddDate(-n, 0, 0)
		}
		return anchor(t)
	}

	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, t.Format("2006-01-02T15:04:05"), nil
		}
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return anchor(t)
	}

	return time.Time{}, "", fmt.Errorf(
		"invalid date %q (use YYYY-MM-DD, YYYY-MM-DDTHH:MM, 7d/4w/6m/1y, today, or yesterday)", s)
}

// obsDatetimeLayouts cover the formats OpenMRS emits for obsDatetime.
var obsDatetimeLayouts = []string{
	"2006-01-02T15:04:05.000-0700",
	"2006-01-02T15:04:05-0700",
	"2006-01-02T15:04:05.000Z07:00",
}

// filterResultsByDate keeps results whose dateField falls within
// [since, until]. Zero bounds are open. Used for resources (obs) whose
// REST search handler ignores date parameters, so filtering happens
// client-side after fetch.
func filterResultsByDate(data map[string]any, dateField string, since, until time.Time) map[string]any {
	results, _ := data["results"].([]any)
	kept := make([]any, 0, len(results))
	for _, r := range results {
		rec, _ := r.(map[string]any)
		raw, _ := rec[dateField].(string)
		t, ok := parseServerDatetime(raw)
		if !ok {
			continue
		}
		if !since.IsZero() && t.Before(since) {
			continue
		}
		if !until.IsZero() && t.After(until) {
			continue
		}
		kept = append(kept, r)
	}
	data["results"] = kept
	return data
}

func parseServerDatetime(s string) (time.Time, bool) {
	for _, layout := range obsDatetimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// warnClientSideFilter tells the user that filtering happened after
// fetch, so a page-limited result set may hide matches on later pages.
func warnClientSideFilter(resource string) {
	if !flags.all {
		output.Warn("%s date filtering is applied client-side after fetch; add --all to filter the complete result set", resource)
	}
}
