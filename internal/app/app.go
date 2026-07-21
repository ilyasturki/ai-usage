// Package app is the composition root: it wires providers, the clock, and the
// output writers into the orchestration and rendering logic. Everything is
// driven through Deps so the whole CLI can be exercised end-to-end in tests by
// asserting on the rendered bytes and exit code.
package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
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
	// CodexLive reads Codex's live rate limits; nil spawns a real
	// `codex app-server`. Tests stub it to stay off the network.
	CodexLive func() (json.RawMessage, error)
	Now       func() time.Time
	Out       io.Writer // normal output (stdout)
	Err       io.Writer // error output for single-provider modes (stderr)
	Color     bool      // emit ANSI color (set by main from TTY detection)
}

// Run executes the requested mode and returns the process exit code.
func Run(d Deps, mode Mode) int {
	now := d.Now()
	rr := render.New(d.Color)
	claudeProvider := &claude.Provider{HTTP: d.HTTPClient, BaseURL: d.BaseURL, CredsPath: d.CredsPath}
	codexProvider := &codex.Provider{SessionsDir: d.SessionsDir, Now: d.Now, Live: d.CodexLive}

	switch mode {
	case ModeClaude:
		return runSingle(d, rr, claudeProvider, now)
	case ModeCodex:
		return runSingle(d, rr, codexProvider, now)
	default:
		return runCombined(d, rr, []usage.Provider{claudeProvider, codexProvider}, now)
	}
}

// runSingle renders one provider on its own: windows to Out, an error to Err.
// Exit is non-zero only when that provider failed.
func runSingle(d Deps, rr render.Renderer, p usage.Provider, now time.Time) int {
	res, err := p.Fetch()
	if err != nil {
		fmt.Fprintln(d.Err, rr.Notice(err.Error()))
		return 1
	}
	for _, line := range rr.Lines(res, now) {
		fmt.Fprintln(d.Out, line)
	}
	return 0
}

// runCombined renders every provider under its section header with two-space-
// indented lines, a blank line between providers. A failing provider shows its
// error message in place (so one failure never blanks out the others), and the
// exit code is non-zero only when every provider failed.
//
// Providers are fetched concurrently. Each one is a network round trip of its
// own — Claude's HTTP call and Codex's app-server query have nothing to say to
// each other — so fetching them in sequence made the combined view cost the sum
// of both rather than the slower of the two. Output is still written in
// provider order, from the collected results, so the layout does not depend on
// which call happened to answer first.
func runCombined(d Deps, rr render.Renderer, providers []usage.Provider, now time.Time) int {
	type outcome struct {
		res usage.Result
		err error
	}
	outcomes := make([]outcome, len(providers))

	var wg sync.WaitGroup
	for i, p := range providers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcomes[i].res, outcomes[i].err = p.Fetch()
		}()
	}
	wg.Wait()

	anyOK := false
	for i, p := range providers {
		if i > 0 {
			fmt.Fprintln(d.Out)
		}
		fmt.Fprintln(d.Out, rr.Header(p.Name()))

		var lines []string
		if outcomes[i].err != nil {
			lines = []string{rr.Notice(outcomes[i].err.Error())}
		} else {
			anyOK = true
			lines = rr.Lines(outcomes[i].res, now)
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
