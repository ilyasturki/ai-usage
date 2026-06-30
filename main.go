// Command ai-usage shows Claude and Codex subscription usage from the
// terminal: labeled 20-char bars with percentages and reset countdowns.
//
//	ai-usage          combined Claude + Codex view (default)
//	ai-usage claude   Claude usage only
//	ai-usage codex    Codex usage only
package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"ai-usage/internal/app"
	"ai-usage/internal/provider/claude"
)

const usageText = `usage: ai-usage [claude|codex]

  (no argument)   show combined Claude and Codex usage
  claude          show Claude usage only
  codex           show Codex usage only`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	mode := app.ModeCombined
	if len(args) > 0 {
		switch args[0] {
		case "claude":
			mode = app.ModeClaude
		case "codex":
			mode = app.ModeCodex
		case "-h", "--help", "help":
			fmt.Fprintln(os.Stdout, usageText)
			return 0
		default:
			fmt.Fprintf(os.Stderr, "ai-usage: unknown command %q\n\n%s\n", args[0], usageText)
			return 2
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ai-usage: cannot determine home directory: %v\n", err)
		return 1
	}

	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}

	deps := app.Deps{
		HTTPClient:  &http.Client{Timeout: 15 * time.Second},
		BaseURL:     claude.DefaultBaseURL,
		CredsPath:   filepath.Join(home, ".claude", ".credentials.json"),
		SessionsDir: filepath.Join(codexHome, "sessions"),
		Now:         time.Now,
		Out:         os.Stdout,
		Err:         os.Stderr,
	}
	return app.Run(deps, mode)
}
