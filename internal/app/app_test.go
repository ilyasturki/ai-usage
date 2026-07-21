package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-usage/internal/render"
	"ai-usage/internal/usage"
)

// clock is the fixed "now" all reset countdowns are computed against.
var clock = time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

// hdr is the section rule the combined view prints above each provider. Its
// exact form is pinned by the render package's own tests; here it just keeps the
// expected output readable without embedding a wall of box-drawing characters.
func hdr(name string) string { return render.New(false).Header(name) }

const healthyClaudeJSON = `{
	"five_hour":        {"utilization": 42.5, "resets_at": "2026-06-30T14:30:00Z"},
	"seven_day":        {"utilization": 10.0, "resets_at": "2026-07-05T00:00:00Z"},
	"seven_day_opus":   {"utilization": 0.0,  "resets_at": "2026-07-05T00:00:00Z"},
	"seven_day_sonnet": {"utilization": null}
}`

// codexSnapshot builds one token_count session line whose resets are anchored
// to the fixed clock so countdowns are deterministic.
func codexSnapshot() string {
	primaryReset := clock.Add(1 * time.Hour).Unix()
	secondaryReset := clock.Add(48*time.Hour + 15*time.Minute).Unix()
	return fmt.Sprintf(`{"timestamp":"2026-06-30T11:00:00Z","payload":{"type":"token_count","rate_limits":{`+
		`"primary":{"used_percent":55.0,"resets_at":%d,"window_minutes":300},`+
		`"secondary":{"used_percent":8.0,"resets_at":%d,"window_minutes":10080},`+
		`"credits":{"unlimited":false,"balance":42}}}}`, primaryReset, secondaryReset)
}

// claudeServer serves a canned status + body and records the request it saw.
func claudeServer(t *testing.T, status int, body string) (url string, gotAuth, gotBeta *string) {
	t.Helper()
	auth := new(string)
	beta := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*auth = r.Header.Get("Authorization")
		*beta = r.Header.Get("anthropic-beta")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, auth, beta
}

// writeCreds writes a credentials file containing the given token and returns
// its path.
func writeCreds(t *testing.T, token string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")
	body := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q}}`, token)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writeCreds: %v", err)
	}
	return path
}

// sessionsWith writes named session files into a fresh dir and stamps each with
// an mtime offset (older files get larger negative offsets) so ordering is
// deterministic. Returns the directory.
func sessionsWith(t *testing.T, files map[string]string, order []string) string {
	t.Helper()
	dir := t.TempDir()
	for i, name := range order {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(files[name]), 0o600); err != nil {
			t.Fatalf("sessionsWith write: %v", err)
		}
		// order[0] is newest: give it mtime = base, later entries get older.
		mtime := clock.Add(time.Duration(-i) * time.Hour)
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("sessionsWith chtimes: %v", err)
		}
	}
	return dir
}

func baseDeps(out, errOut *bytes.Buffer) Deps {
	return Deps{
		HTTPClient: http.DefaultClient,
		// Fail the live read by default so these tests exercise the session-log
		// fallback against their fixtures instead of spawning a real
		// `codex app-server` and asserting on the machine's actual usage.
		CodexLive: func() (json.RawMessage, error) { return nil, errors.New("live disabled in tests") },
		Now:       func() time.Time { return clock },
		Out:       out,
		Err:       errOut,
	}
}

func TestCombinedHealthy(t *testing.T) {
	url, gotAuth, gotBeta := claudeServer(t, http.StatusOK, healthyClaudeJSON)
	var out, errOut bytes.Buffer
	d := baseDeps(&out, &errOut)
	d.BaseURL = url
	d.CredsPath = writeCreds(t, "tok-123")
	d.SessionsDir = sessionsWith(t, map[string]string{"a.jsonl": codexSnapshot()}, []string{"a.jsonl"})

	code := Run(d, ModeCombined)

	want := join(
		hdr("Claude"),
		"  5-hour        ████████░░░░░░░░░░░░     42.5%   resets in 2 hours 30 minutes",
		"  Weekly        ██░░░░░░░░░░░░░░░░░░     10.0%   resets in 4 days 12 hours",
		"  Weekly Opus   ░░░░░░░░░░░░░░░░░░░░      0.0%   resets in 4 days 12 hours",
		"",
		hdr("Codex"),
		"  5-hour        ███████████░░░░░░░░░     55.0%   resets in 1 hour",
		"  Weekly        █░░░░░░░░░░░░░░░░░░░      8.0%   resets in 2 days",
		"  Credits       42",
	)
	if out.String() != want {
		t.Errorf("combined output mismatch:\n got:\n%s\nwant:\n%s", out.String(), want)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want empty", errOut.String())
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if *gotAuth != "Bearer tok-123" {
		t.Errorf("Authorization header = %q, want %q", *gotAuth, "Bearer tok-123")
	}
	if *gotBeta != "oauth-2025-04-20" {
		t.Errorf("anthropic-beta header = %q, want %q", *gotBeta, "oauth-2025-04-20")
	}
}

func TestClaudeOnly(t *testing.T) {
	url, _, _ := claudeServer(t, http.StatusOK, healthyClaudeJSON)
	var out, errOut bytes.Buffer
	d := baseDeps(&out, &errOut)
	d.BaseURL = url
	d.CredsPath = writeCreds(t, "tok")
	d.SessionsDir = t.TempDir() // unused in this mode

	code := Run(d, ModeClaude)

	want := join(
		"5-hour        ████████░░░░░░░░░░░░     42.5%   resets in 2 hours 30 minutes",
		"Weekly        ██░░░░░░░░░░░░░░░░░░     10.0%   resets in 4 days 12 hours",
		"Weekly Opus   ░░░░░░░░░░░░░░░░░░░░      0.0%   resets in 4 days 12 hours",
	)
	if out.String() != want {
		t.Errorf("claude-only output mismatch:\n got:\n%s\nwant:\n%s", out.String(), want)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}

func TestCodexOnly(t *testing.T) {
	var out, errOut bytes.Buffer
	d := baseDeps(&out, &errOut)
	d.SessionsDir = sessionsWith(t, map[string]string{"a.jsonl": codexSnapshot()}, []string{"a.jsonl"})

	code := Run(d, ModeCodex)

	want := join(
		"5-hour        ███████████░░░░░░░░░     55.0%   resets in 1 hour",
		"Weekly        █░░░░░░░░░░░░░░░░░░░      8.0%   resets in 2 days",
		"Credits       42",
	)
	if out.String() != want {
		t.Errorf("codex-only output mismatch:\n got:\n%s\nwant:\n%s", out.String(), want)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}

func TestCodexStaleSnapshotShowsAgeNote(t *testing.T) {
	// The user's real situation: the newest snapshot is days old and both
	// windows have since reset. Rather than a confident flat 0% that reads like
	// fresh "no usage", the section must flag the data as stale and name when it
	// was last seen. The credit balance (not a window) still shows.
	stale := fmt.Sprintf(`{"timestamp":"2026-06-16T20:34:24Z","payload":{"type":"token_count","rate_limits":{`+
		`"primary":{"used_percent":100.0,"resets_at":%d,"window_minutes":300},`+
		`"secondary":{"used_percent":42.0,"resets_at":%d,"window_minutes":10080},`+
		`"credits":{"has_credits":false,"unlimited":false,"balance":"0"}}}}`,
		clock.Add(-13*24*time.Hour).Unix(), clock.Add(-6*24*time.Hour).Unix())

	var out, errOut bytes.Buffer
	d := baseDeps(&out, &errOut)
	d.SessionsDir = sessionsWith(t, map[string]string{"a.jsonl": stale}, []string{"a.jsonl"})

	code := Run(d, ModeCodex)

	want := join(
		"no recent session — last seen Jun 16 (13 days ago)",
		"Credits       0",
	)
	if out.String() != want {
		t.Errorf("stale codex output mismatch:\n got:\n%s\nwant:\n%s", out.String(), want)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}

// Codex 0.144 emits a single weekly window, whose seven-day reset can outlive
// the last session by days, so nothing resets and the result is not stale. The
// weekly is a rolling window that decays as usage ages out, which makes a
// five-day-old reading a ceiling rather than a measurement: it must render with
// a "≤" and its age, not as a confident number beside a live countdown.
func TestCodexOldWeeklyWindowRendersAsUpperBound(t *testing.T) {
	old := fmt.Sprintf(`{"timestamp":"2026-06-25T12:00:00Z","payload":{"type":"token_count","rate_limits":{`+
		`"limit_id":"codex","primary":{"used_percent":18.0,"window_minutes":10080,"resets_at":%d},`+
		`"secondary":null,"credits":null}}}`,
		clock.Add(22*time.Hour).Unix())

	var out, errOut bytes.Buffer
	d := baseDeps(&out, &errOut)
	d.SessionsDir = sessionsWith(t, map[string]string{"a.jsonl": old}, []string{"a.jsonl"})

	code := Run(d, ModeCodex)

	want := join(
		"Weekly        ███░░░░░░░░░░░░░░░░░    ≤18.0%",
		"as of Jun 25 (5 days ago) — actual is lower",
	)
	if out.String() != want {
		t.Errorf("bounded codex output mismatch:\n got:\n%s\nwant:\n%s", out.String(), want)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}

func TestExpiredTokenInCombinedStillShowsCodex(t *testing.T) {
	url, _, _ := claudeServer(t, http.StatusUnauthorized, `{"error":"expired"}`)
	var out, errOut bytes.Buffer
	d := baseDeps(&out, &errOut)
	d.BaseURL = url
	d.CredsPath = writeCreds(t, "stale")
	d.SessionsDir = sessionsWith(t, map[string]string{"a.jsonl": codexSnapshot()}, []string{"a.jsonl"})

	code := Run(d, ModeCombined)

	want := join(
		hdr("Claude"),
		"  claude-usage: token may be expired - open Claude once to refresh",
		"",
		hdr("Codex"),
		"  5-hour        ███████████░░░░░░░░░     55.0%   resets in 1 hour",
		"  Weekly        █░░░░░░░░░░░░░░░░░░░      8.0%   resets in 2 days",
		"  Credits       42",
	)
	if out.String() != want {
		t.Errorf("output mismatch:\n got:\n%s\nwant:\n%s", out.String(), want)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0 (Codex succeeded)", code)
	}
}

func TestMissingCredentialsInCombined(t *testing.T) {
	var out, errOut bytes.Buffer
	d := baseDeps(&out, &errOut)
	d.BaseURL = "http://127.0.0.1:0" // never reached; token read fails first
	missing := filepath.Join(t.TempDir(), "nope", ".credentials.json")
	d.CredsPath = missing
	d.SessionsDir = sessionsWith(t, map[string]string{"a.jsonl": codexSnapshot()}, []string{"a.jsonl"})

	code := Run(d, ModeCombined)

	want := join(
		hdr("Claude"),
		"  claude-usage: "+missing+" not found",
		"",
		hdr("Codex"),
		"  5-hour        ███████████░░░░░░░░░     55.0%   resets in 1 hour",
		"  Weekly        █░░░░░░░░░░░░░░░░░░░      8.0%   resets in 2 days",
		"  Credits       42",
	)
	if out.String() != want {
		t.Errorf("output mismatch:\n got:\n%s\nwant:\n%s", out.String(), want)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}

func TestClaudeOnlyMissingCredentialsExitsNonZero(t *testing.T) {
	var out, errOut bytes.Buffer
	d := baseDeps(&out, &errOut)
	missing := filepath.Join(t.TempDir(), "nope.json")
	d.CredsPath = missing
	d.SessionsDir = t.TempDir()

	code := Run(d, ModeClaude)

	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
	wantErr := "claude-usage: " + missing + " not found\n"
	if errOut.String() != wantErr {
		t.Errorf("stderr = %q, want %q", errOut.String(), wantErr)
	}
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

func TestCodexOnlyNoSnapshotExitsNonZero(t *testing.T) {
	var out, errOut bytes.Buffer
	d := baseDeps(&out, &errOut)
	// Directory exists but has no usable snapshot.
	d.SessionsDir = sessionsWith(t, map[string]string{"a.jsonl": `{"payload":{"type":"other"}}`}, []string{"a.jsonl"})

	code := Run(d, ModeCodex)

	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
	if errOut.String() != "codex-usage: no Codex rate-limit snapshot found\n" {
		t.Errorf("stderr = %q", errOut.String())
	}
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
}

func TestBothProvidersFailExitsNonZero(t *testing.T) {
	var out, errOut bytes.Buffer
	d := baseDeps(&out, &errOut)
	d.CredsPath = filepath.Join(t.TempDir(), "nope.json")
	d.SessionsDir = filepath.Join(t.TempDir(), "no-such-dir")

	code := Run(d, ModeCombined)

	if code != 1 {
		t.Errorf("exit = %d, want 1 when both providers fail", code)
	}
	// Both headers and both error lines should still be present.
	for _, want := range []string{"Claude", "Codex", "not found"} {
		if !bytes.Contains(out.Bytes(), []byte(want)) {
			t.Errorf("combined output missing %q:\n%s", want, out.String())
		}
	}
}

func TestCodexPicksNewestFileWithSnapshot(t *testing.T) {
	// Newest file (a.jsonl) has a snapshot at 99%; an older file has 11%.
	newest := `{"timestamp":"2026-06-30T11:00:00Z","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":99.0,"window_minutes":300}}}}`
	older := `{"timestamp":"2026-06-30T11:00:00Z","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":11.0,"window_minutes":300}}}}`
	var out, errOut bytes.Buffer
	d := baseDeps(&out, &errOut)
	d.SessionsDir = sessionsWith(t,
		map[string]string{"a.jsonl": newest, "b.jsonl": older},
		[]string{"a.jsonl", "b.jsonl"}, // a is newest
	)

	if code := Run(d, ModeCodex); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !bytes.Contains(out.Bytes(), []byte(" 99.0%")) {
		t.Errorf("expected the newest file's 99%% snapshot:\n%s", out.String())
	}
	if bytes.Contains(out.Bytes(), []byte(" 11.0%")) {
		t.Errorf("older file's snapshot leaked into output:\n%s", out.String())
	}
}

func TestCodexFallsThroughToOlderFileWhenNewestHasNoSnapshot(t *testing.T) {
	newestNoSnap := `{"payload":{"type":"agent_message"}}`
	older := `{"timestamp":"2026-06-30T11:00:00Z","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":11.0,"window_minutes":300}}}}`
	var out, errOut bytes.Buffer
	d := baseDeps(&out, &errOut)
	d.SessionsDir = sessionsWith(t,
		map[string]string{"a.jsonl": newestNoSnap, "b.jsonl": older},
		[]string{"a.jsonl", "b.jsonl"},
	)

	if code := Run(d, ModeCodex); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !bytes.Contains(out.Bytes(), []byte(" 11.0%")) {
		t.Errorf("expected fallback to the older file's snapshot:\n%s", out.String())
	}
}

// fakeProvider answers after a fixed delay, so a test can control which
// provider finishes first independently of where it sits in the list.
type fakeProvider struct {
	name  string
	delay time.Duration
}

func (f fakeProvider) Name() string { return f.name }

func (f fakeProvider) Fetch() (usage.Result, error) {
	time.Sleep(f.delay)
	return usage.Result{Windows: []usage.Window{{Label: "Weekly", Utilization: 1}}}, nil
}

// The combined view fetches providers concurrently, so it must cost the slower
// of them rather than the sum, and must still lay them out in list order rather
// than in the order their answers arrived.
func TestCombinedFetchesConcurrentlyAndKeepsOrder(t *testing.T) {
	const delay = 150 * time.Millisecond
	var out, errOut bytes.Buffer
	d := baseDeps(&out, &errOut)

	start := time.Now()
	code := runCombined(d, render.New(false), []usage.Provider{
		fakeProvider{name: "Slow", delay: delay},
		fakeProvider{name: "Fast", delay: 0},
	}, clock)
	elapsed := time.Since(start)

	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if elapsed >= 2*delay {
		t.Errorf("took %v, want under %v: providers were fetched in sequence", elapsed, 2*delay)
	}
	slowAt := strings.Index(out.String(), "Slow")
	fastAt := strings.Index(out.String(), "Fast")
	if slowAt < 0 || fastAt < 0 || slowAt > fastAt {
		t.Errorf("sections out of order (Slow at %d, Fast at %d); want list order, not completion order:\n%s",
			slowAt, fastAt, out.String())
	}
}

// join renders lines exactly as the program writes them: each followed by a
// newline (fmt.Fprintln semantics).
func join(lines ...string) string {
	var b bytes.Buffer
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return b.String()
}
