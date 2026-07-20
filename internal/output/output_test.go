package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pbiondich/openmrs-cli/internal/client"
)

func TestExtractDotAndAlternates(t *testing.T) {
	rec := map[string]any{
		"uuid": "u1",
		"person": map[string]any{
			"display": "Ada Lovelace",
			"age":     float64(36),
		},
		"value": map[string]any{"display": "positive"},
	}
	if got := Extract(rec, "person.display"); got != "Ada Lovelace" {
		t.Fatalf("got %q", got)
	}
	if got := Extract(rec, "person.age"); got != "36" {
		t.Fatalf("got %q", got)
	}
	if got := Extract(rec, "missing|person.display"); got != "Ada Lovelace" {
		t.Fatalf("alternate got %q", got)
	}
	if got := Extract(rec, "value.display|value"); got != "positive" {
		t.Fatalf("got %q", got)
	}
	if got := Extract(rec, "nope|also.nope"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestDetectMode(t *testing.T) {
	if Detect(true, false) != ModeJSON {
		t.Fatal("json flag")
	}
	if Detect(false, true) != ModeTable {
		t.Fatal("table flag")
	}
	// Without flags, mode depends on TTY; just ensure it returns a known mode.
	m := Detect(false, false)
	if m != ModeJSON && m != ModeTable {
		t.Fatalf("unexpected mode %v", m)
	}
}

func TestPrintErrorAPIError(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	code := PrintError(&client.APIError{Message: "denied", Code: client.CodeForbidden, HTTPStatus: 403}, true)
	_ = w.Close()
	os.Stderr = old
	if code != 6 {
		t.Fatalf("exit=%d want 6", code)
	}
	body, _ := io.ReadAll(r)
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if m["code"] != client.CodeForbidden {
		t.Fatalf("%v", m)
	}
}

func TestPrintErrorWrappedAPIError(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	inner := &client.APIError{Message: "bad login", Code: client.CodeAuth}
	code := PrintError(fmt.Errorf("wrap: %w", inner), true)
	_ = w.Close()
	os.Stderr = old
	if code != 2 {
		t.Fatalf("exit=%d want 2 (errors.As)", code)
	}
	body, _ := io.ReadAll(r)
	if !bytes.Contains(body, []byte(`"code":"AUTH"`)) {
		// Message may be outer wrap depending on As target fields — code must be AUTH.
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		if m["code"] != "AUTH" {
			t.Fatalf("body=%s", body)
		}
	}
}

func TestPrintErrorUsage(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	code := PrintError(errors.New("unknown command \"foo\" for \"omrs\""), true)
	_ = w.Close()
	os.Stderr = old
	if code != 1 {
		t.Fatalf("exit=%d", code)
	}
	body, _ := io.ReadAll(r)
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	if m["code"] != client.CodeUsage {
		t.Fatalf("%s", body)
	}
}

// captureFile swaps out *f (os.Stdout or os.Stderr) for a pipe while fn
// runs and returns everything written.
func captureFile(t *testing.T, f **os.File, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := *f
	*f = w
	defer func() { *f = old }()
	fn()
	_ = w.Close()
	body, _ := io.ReadAll(r)
	return string(body)
}

func TestPrintJSONModeEmitsValidJSON(t *testing.T) {
	data := map[string]any{"results": []any{map[string]any{"uuid": "u1", "display": `has "quotes"`}}}
	out := captureFile(t, &os.Stdout, func() {
		if err := Print(data, ModeJSON, "patient"); err != nil {
			t.Error(err)
		}
	})
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out)
	}
	if len(m["results"].([]any)) != 1 {
		t.Fatalf("%v", m)
	}
}

func TestPrintTableUsesResourceColumnsAndCounts(t *testing.T) {
	data := map[string]any{"results": []any{
		map[string]any{"uuid": "u1", "display": "OpenMRS ID = 1", "person": map[string]any{
			"display": "Ada Lovelace", "gender": "F", "age": float64(36), "birthdate": "1990-01-01"}},
		map[string]any{"uuid": "u2", "display": "OpenMRS ID = 2", "person": map[string]any{
			"display": "Grace Hopper", "gender": "F", "age": float64(45), "birthdate": "1981-01-01"}},
	}}
	out := captureFile(t, &os.Stdout, func() {
		_ = Print(data, ModeTable, "patient")
	})
	for _, want := range []string{"UUID", "NAME", "GENDER", "Ada Lovelace", "Grace Hopper", "2 result(s)"} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Fatalf("table missing %q:\n%s", want, out)
		}
	}
}

func TestPrintTableFallbackColumnsForUnknownResource(t *testing.T) {
	data := map[string]any{"results": []any{map[string]any{"uuid": "x", "display": "Thing"}}}
	out := captureFile(t, &os.Stdout, func() {
		_ = Print(data, ModeTable, "no-such-resource")
	})
	if !bytes.Contains([]byte(out), []byte("DISPLAY")) || !bytes.Contains([]byte(out), []byte("Thing")) {
		t.Fatalf("fallback columns not used:\n%s", out)
	}
}

func TestPrintRecordOrderingAndSkips(t *testing.T) {
	rec := map[string]any{
		"zeta":            "last",
		"uuid":            "u1",
		"display":         "Ada",
		"links":           []any{map[string]any{"rel": "self"}},
		"resourceVersion": "1.8",
		"alpha":           "early",
	}
	out := captureFile(t, &os.Stdout, func() {
		_ = Print(rec, ModeTable, "")
	})
	if bytes.Contains([]byte(out), []byte("links")) || bytes.Contains([]byte(out), []byte("resourceVersion")) {
		t.Fatalf("noise keys must be skipped:\n%s", out)
	}
	uuidPos := bytes.Index([]byte(out), []byte("uuid"))
	alphaPos := bytes.Index([]byte(out), []byte("alpha"))
	zetaPos := bytes.Index([]byte(out), []byte("zeta"))
	if uuidPos < 0 || alphaPos < 0 || zetaPos < 0 || !(uuidPos < alphaPos && alphaPos < zetaPos) {
		t.Fatalf("ordering wrong (uuid=%d alpha=%d zeta=%d):\n%s", uuidPos, alphaPos, zetaPos, out)
	}
}

func TestTruncateIsRuneSafe(t *testing.T) {
	// A long run of multibyte characters must never be cut mid-rune —
	// invalid UTF-8 in table output corrupts terminals and JSON logs.
	// Try many cut points so at least one lands mid-rune for 2-, 3-, and
	// 4-byte encodings.
	for _, sample := range []string{
		strings.Repeat("é", 100),  // 2 bytes
		strings.Repeat("ብ", 100),  // 3 bytes
		strings.Repeat("😀", 100), // 4 bytes
	} {
		for n := 10; n < 20; n++ {
			got := truncate(sample, n)
			if !utf8.ValidString(got) {
				t.Fatalf("truncate(%q-run, %d) produced invalid UTF-8: %q", sample[:4], n, got)
			}
			if !strings.HasSuffix(got, "…") {
				t.Fatalf("expected ellipsis suffix, got %q", got)
			}
		}
	}
	if truncate("short", 60) != "short" {
		t.Fatal("short strings must pass through")
	}
	// n is a rune budget: n-1 characters plus the ellipsis.
	got := truncate(strings.Repeat("é", 100), 10)
	if runeCount := len([]rune(got)); runeCount != 10 {
		t.Fatalf("want 10 runes (9 + ellipsis), got %d: %q", runeCount, got)
	}
}

func TestWarnEmitsValidJSONWithQuotes(t *testing.T) {
	out := captureFile(t, &os.Stderr, func() {
		Warn("profile %q failed: %s", "we\"ird", `detail with "quotes"`)
	})
	var m map[string]string
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("warning is not valid JSON: %v\n%s", err, out)
	}
	if m["warning"] == "" {
		t.Fatalf("%v", m)
	}
}
