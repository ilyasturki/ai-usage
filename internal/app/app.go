// Package app is the composition root: it wires providers, the clock, and the
// output writers into the orchestration and rendering logic. Everything is
// driven through Deps so the whole CLI can be exercised end-to-end in tests by
// asserting on the rendered bytes and exit code.
package app

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"ai-usage/internal/provider/claude"
	"ai-usage/internal/provider/codex"
	"ai-usage/internal/render"
	"ai-usage/internal/usage"
)

// Mode selects which providers run.
type Mode int

const (
	// ModeCombined shows both providers under headers with indented windows.
	ModeCombined Mode = iota
	// ModeClaude shows only Claude usage (no header, no indentation).
	ModeClaude
	// ModeCodex shows only Codex usage (no header, no indentation).
	ModeCodex
)

// Deps is the injectable dependency set wired in main and faked in tests.
type Deps struct {
	HTTPClient  *http.Client
	BaseURL     string // Claude API origin
	CredsPath   string // Claude credentials file
	SessionsDir string // Codex sessions directory
	Now         func() time.Time
	Out         io.Writer // normal output (stdout)
	Err         io.Writer // error output for single-provider modes (stderr)
}

// Run executes the requested mode and returns the process exit code.
func Run(d Deps, mode Mode) int {
	now := d.Now()
	claudeProvider := &claude.Provider{HTTP: d.HTTPClient, BaseURL: d.BaseURL, CredsPath: d.CredsPath}
	codexProvider := &codex.Provider{SessionsDir: d.SessionsDir}

	switch mode {
	case ModeClaude:
		return runSingle(d, claudeProvider, now)
	case ModeCodex:
		return runSingle(d, codexProvider, now)
	default:
		return runCombined(d, []usage.Provider{claudeProvider, codexProvider}, now)
	}
}

// runSingle renders one provider on its own: windows to Out, an error to Err.
// Exit is non-zero only when that provider failed.
func runSingle(d Deps, p usage.Provider, now time.Time) int {
	res, err := p.Fetch()
	if err != nil {
		fmt.Fprintln(d.Err, err)
		return 1
	}
	for _, line := range render.Lines(res, now) {
		fmt.Fprintln(d.Out, line)
	}
	return 0
}

// runCombined renders every provider under its header with two-space-indented
// lines, a blank line between providers. A failing provider shows its error
// message in place (so one failure never blanks out the others), and the exit
// code is non-zero only when every provider failed.
func runCombined(d Deps, providers []usage.Provider, now time.Time) int {
	anyOK := false
	for i, p := range providers {
		if i > 0 {
			fmt.Fprintln(d.Out)
		}
		fmt.Fprintln(d.Out, p.Name())

		var lines []string
		if res, err := p.Fetch(); err != nil {
			lines = []string{err.Error()}
		} else {
			anyOK = true
			lines = render.Lines(res, now)
		}
		for _, line := range lines {
			fmt.Fprintln(d.Out, "  "+line)
		}
	}
	if anyOK {
		return 0
	}
	return 1
}
