// Package claude reads Claude subscription usage by reusing the OAuth token
// Claude Code already stores on disk and calling the same usage endpoint the
// /usage command hits internally. It does not perform token refresh: on a 401
// it tells the user to open Claude once to refresh.
package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"time"

	"ai-usage/internal/usage"
)

const (
	// DefaultBaseURL is the Anthropic API origin; injectable for tests.
	DefaultBaseURL = "https://api.anthropic.com"
	usagePath      = "/api/oauth/usage"
	oauthBetaValue = "oauth-2025-04-20"
)

// ErrExpired is returned when the token is rejected (401) or the response is
// missing the expected shape — both mean the on-disk token needs refreshing.
var ErrExpired = errors.New("claude-usage: token may be expired - open Claude once to refresh")

// Provider fetches Claude usage. HTTP, BaseURL and CredsPath are injected so
// the composition root can wire real dependencies and tests can fake them.
type Provider struct {
	HTTP      *http.Client
	BaseURL   string
	CredsPath string
}

// Name is the combined-view header for this provider.
func (p *Provider) Name() string { return "Claude" }

// Fetch reads the stored token and requests the usage snapshot.
func (p *Provider) Fetch() (usage.Result, error) {
	token, err := readToken(p.CredsPath)
	if err != nil {
		return usage.Result{}, err
	}

	base := p.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	req, err := http.NewRequest(http.MethodGet, base+usagePath, nil)
	if err != nil {
		return usage.Result{}, fmt.Errorf("claude-usage: request failed: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", oauthBetaValue)

	resp, err := p.HTTP.Do(req)
	if err != nil {
		return usage.Result{}, fmt.Errorf("claude-usage: request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return usage.Result{}, fmt.Errorf("claude-usage: request failed: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return usage.Result{}, ErrExpired
	}
	return ParseUsage(body)
}

// credentials mirrors the relevant slice of ~/.claude/.credentials.json.
type credentials struct {
	ClaudeAiOauth struct {
		AccessToken string `json:"accessToken"`
	} `json:"claudeAiOauth"`
}

func readToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("claude-usage: %s not found", path)
	}
	if err != nil {
		return "", fmt.Errorf("claude-usage: could not read credentials: %w", err)
	}
	var creds credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return "", errors.New("claude-usage: could not parse credentials")
	}
	return creds.ClaudeAiOauth.AccessToken, nil
}

// windowSpec pairs a display label with the response key holding that window.
type windowSpec struct {
	label string
	key   string
}

// windowSpecs is the fixed set of windows, in display order.
var windowSpecs = []windowSpec{
	{"5-hour", "five_hour"},
	{"Weekly", "seven_day"},
	{"Weekly Opus", "seven_day_opus"},
	{"Weekly Sonnet", "seven_day_sonnet"},
}

// ParseUsage turns a raw usage response into windows. It is pure (no clock, no
// I/O) so the squishy/optional JSON is fast to unit-test. A response that does
// not parse as JSON, or that lacks the five_hour window entirely, is treated
// as an expired/unexpected token.
func ParseUsage(body []byte) (usage.Result, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return usage.Result{}, errors.New("claude-usage: could not parse response")
	}
	if _, ok := raw["five_hour"]; !ok {
		return usage.Result{}, ErrExpired
	}

	windows := make([]usage.Window, 0, len(windowSpecs))
	for _, spec := range windowSpecs {
		if w, ok := parseWindow(spec.label, raw[spec.key]); ok {
			windows = append(windows, w)
		}
	}
	return usage.Result{Windows: windows}, nil
}

// parseWindow reads one window. It is tolerant: an absent key, a null value, a
// non-object, or a null/missing utilization all silently omit the window
// rather than panicking.
func parseWindow(label string, raw json.RawMessage) (usage.Window, bool) {
	if len(raw) == 0 {
		return usage.Window{}, false
	}
	var v struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    *string  `json:"resets_at"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return usage.Window{}, false
	}
	if v.Utilization == nil {
		return usage.Window{}, false
	}
	w := usage.Window{Label: label, Utilization: *v.Utilization}
	if v.ResetsAt != nil && *v.ResetsAt != "" {
		if t, err := time.Parse(time.RFC3339, *v.ResetsAt); err == nil {
			w.ResetsAt = &t
		}
	}
	return w, true
}
