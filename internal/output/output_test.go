package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

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
