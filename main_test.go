package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"testing"

	"ai-usage/internal/version"
)

// TestRunVersion covers the three version forms, mirroring the -h/--help/help
// trio. The expected string is built from version.Version so the test holds
// whether or not the build injected a value via ldflags (it is "dev" here).
func TestRunVersion(t *testing.T) {
	want := fmt.Sprintf("ai-usage %s\n", version.Version)
	for _, arg := range []string{"-v", "--version", "version"} {
		out, code := captureStdout(t, func() int { return run([]string{arg}) })
		if code != 0 {
			t.Errorf("run(%q) exit = %d, want 0", arg, code)
		}
		if out != want {
			t.Errorf("run(%q) stdout = %q, want %q", arg, out, want)
		}
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it
// wrote along with fn's return value.
func captureStdout(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	code := fn()
	w.Close()
	os.Stdout = orig

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return buf.String(), code
}
