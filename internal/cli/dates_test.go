package cli

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestParseWhenISOAndRelative(t *testing.T) {
	// Fixed-ish: relative forms should parse without error.
	for _, s := range []string{"7d", "4w", "6m", "1y", "today", "yesterday", "2026-01-02"} {
		tm, formatted, err := parseWhen(s, false)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if tm.IsZero() || formatted == "" {
			t.Fatalf("%q: zero result", s)
		}
	}

	_, _, err := parseWhen("not-a-date", false)
	if err == nil {
		t.Fatal("expected error")
	}

	// endOfDay anchors to 23:59:59
	tm, _, err := parseWhen("2026-03-15", true)
	if err != nil {
		t.Fatal(err)
	}
	if tm.Hour() != 23 || tm.Minute() != 59 || tm.Second() != 59 {
		t.Fatalf("endOfDay got %v", tm)
	}

	tm, _, err = parseWhen("2026-03-15", false)
	if err != nil {
		t.Fatal(err)
	}
	if tm.Hour() != 0 || tm.Minute() != 0 {
		t.Fatalf("start of day got %v", tm)
	}
}

func TestParseWhenDateTime(t *testing.T) {
	tm, formatted, err := parseWhen("2026-01-02T14:30", false)
	if err != nil {
		t.Fatal(err)
	}
	if tm.Hour() != 14 || tm.Minute() != 30 {
		t.Fatalf("%v", tm)
	}
	if formatted != "2026-01-02T14:30:00" {
		t.Fatalf("formatted=%q", formatted)
	}
}

func TestParseServerDatetime(t *testing.T) {
	samples := []string{
		"2026-01-02T14:30:05.000-0500",
		"2026-01-02T14:30:05-0500",
	}
	for _, s := range samples {
		if _, ok := parseServerDatetime(s); !ok {
			t.Errorf("failed to parse %q", s)
		}
	}
	if _, ok := parseServerDatetime("garbage"); ok {
		t.Fatal("expected failure")
	}
}

func TestFilterResultsByDate(t *testing.T) {
	data := map[string]any{
		"results": []any{
			map[string]any{"obsDatetime": "2026-01-01T10:00:00.000-0500", "id": "a"},
			map[string]any{"obsDatetime": "2026-02-15T10:00:00.000-0500", "id": "b"},
			map[string]any{"obsDatetime": "2026-03-20T10:00:00.000-0500", "id": "c"},
			map[string]any{"obsDatetime": "unparseable", "id": "drop"},
		},
	}
	since := time.Date(2026, 2, 1, 0, 0, 0, 0, time.Local)
	until := time.Date(2026, 3, 1, 23, 59, 59, 0, time.Local)
	out := filterResultsByDate(data, "obsDatetime", since, until)
	got := out["results"].([]any)
	if len(got) != 1 {
		t.Fatalf("got %d results: %v", len(got), got)
	}
	if got[0].(map[string]any)["id"] != "b" {
		t.Fatalf("%v", got[0])
	}
}

func TestWarnClientSideFilter(t *testing.T) {
	oldAll := flags.all
	t.Cleanup(func() { flags.all = oldAll })

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = oldStderr })

	flags.all = false
	warnClientSideFilter("obs")
	flags.all = true
	warnClientSideFilter("obs") // suppressed when --all already fetches everything

	_ = w.Close()
	body, _ := io.ReadAll(r)
	s := string(body)
	if !strings.Contains(s, "client-side") || !strings.Contains(s, "obs") {
		t.Fatalf("expected client-side warning, got %q", s)
	}
	if n := strings.Count(s, `"warning"`); n != 1 {
		t.Fatalf("want 1 warning (--all should suppress), got %d in %q", n, s)
	}
}
