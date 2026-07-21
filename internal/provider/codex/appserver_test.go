package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-usage/internal/usage"
)

// liveTestNow is the fixed clock the fallback fixtures are anchored to.
var liveTestNow = time.Unix(1785000000, 0).UTC()

// sessionLine builds one token_count session line, timestamped an hour before
// liveTestNow and resetting an hour after it — recent enough to render as a
// plain reading rather than an aged upper bound, so these tests isolate which
// source Fetch chose.
func sessionLine(t *testing.T, used float64, windowMinutes int) string {
	t.Helper()
	return fmt.Sprintf(`{"timestamp":%q,"payload":{"type":"token_count","rate_limits":{`+
		`"primary":{"used_percent":%.1f,"window_minutes":%d,"resets_at":%d}}}}`,
		liveTestNow.Add(-time.Hour).Format(time.RFC3339), used, windowMinutes,
		liveTestNow.Add(time.Hour).Unix())
}

func writeSessions(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("writeSessions: %v", err)
		}
	}
	return dir
}

// liveResult is a real account/rateLimits/read result, captured from
// `codex app-server` 0.144.6 — the reading that exposed the session-log path as
// wrong: 0% used, where the newest snapshot on disk still claimed 18%.
const liveResult = `{
  "rateLimits": {
    "limitId": "codex", "limitName": null,
    "primary": {"usedPercent": 0, "windowDurationMins": 10080, "resetsAt": 1785239706},
    "secondary": null,
    "credits": {"hasCredits": false, "unlimited": false, "balance": "0"},
    "individualLimit": null, "planType": "plus", "rateLimitReachedType": null
  },
  "rateLimitsByLimitId": {
    "codex": {
      "limitId": "codex",
      "primary": {"usedPercent": 0, "windowDurationMins": 10080, "resetsAt": 1785239706}
    }
  },
  "rateLimitResetCredits": {
    "availableCount": 2,
    "credits": [
      {"id": "RateLimitResetCredit_2dac", "resetType": "codexRateLimits", "status": "available",
       "grantedAt": 1782935551, "expiresAt": 1785527551, "title": "Full reset"},
      {"id": "RateLimitResetCredit_2ff4", "resetType": "codexRateLimits", "status": "available",
       "grantedAt": 1783963853, "expiresAt": 1786555853, "title": "Full reset"}
    ]
  }
}`

func TestParseLiveRateLimitsReadsRealResponse(t *testing.T) {
	got, err := ParseLiveRateLimits([]byte(liveResult))
	if err != nil {
		t.Fatalf("ParseLiveRateLimits: %v", err)
	}
	if len(got.Windows) != 1 {
		t.Fatalf("got %d windows, want 1 (primary only; secondary is null)", len(got.Windows))
	}
	w := got.Windows[0]
	if w.Label != "Weekly" || w.Utilization != 0 {
		t.Errorf("window = %+v, want Weekly at 0%%", w)
	}
	if want := time.Unix(1785239706, 0).UTC(); w.ResetsAt == nil || !w.ResetsAt.Equal(want) {
		t.Errorf("ResetsAt = %v, want %v", w.ResetsAt, want)
	}
	// The live reading is current by construction: nothing to age, nothing to bound.
	if got.Stale || got.AsOf != nil {
		t.Errorf("Stale=%v AsOf=%v, want a live result to be neither", got.Stale, got.AsOf)
	}
	want := []usage.Extra{
		{Label: "Credits", Value: "0"},
		{Label: "Reset credits", Value: "2 resets available"},
	}
	if len(got.Extras) != len(want) {
		t.Fatalf("extras = %+v, want %+v", got.Extras, want)
	}
	for i := range want {
		if got.Extras[i] != want[i] {
			t.Errorf("extras[%d] = %+v, want %+v", i, got.Extras[i], want[i])
		}
	}
}

func TestParseResetCreditsOmitsEmptyAndSingularizesOne(t *testing.T) {
	tests := []struct {
		name, raw, want string
	}{
		{"none", `{"availableCount":0}`, ""},
		{"one", `{"availableCount":1}`, "1 reset available"},
		{"several", `{"availableCount":3}`, "3 resets available"},
		{"absent", `null`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseResetCredits([]byte(tt.raw))
			if tt.want == "" {
				if ok {
					t.Errorf("got %+v, want the line omitted", got)
				}
				return
			}
			if !ok || got.Value != tt.want {
				t.Errorf("got %+v (ok=%v), want %q", got, ok, tt.want)
			}
		})
	}
}

// The server interleaves notifications and the initialize reply with the answer
// we want, so the reader must match on request id rather than take the first
// message it sees.
func TestReadLiveReplySkipsOtherMessages(t *testing.T) {
	stream := strings.Join([]string{
		`{"id":1,"result":{"userAgent":"ai-usage/0.144.6"}}`,
		`{"jsonrpc":"2.0","method":"account/rateLimits/updated","params":{"noise":true}}`,
		`not json at all`,
		`{"id":2,"result":{"rateLimits":{"primary":{"usedPercent":7}}}}`,
	}, "\n")

	raw, err := readLiveReply(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("readLiveReply: %v", err)
	}
	res, err := ParseLiveRateLimits(raw)
	if err != nil {
		t.Fatalf("ParseLiveRateLimits: %v", err)
	}
	if len(res.Windows) != 1 || res.Windows[0].Utilization != 7 {
		t.Errorf("windows = %+v, want the id:2 reply's 7%%", res.Windows)
	}
}

func TestReadLiveReplyReportsServerError(t *testing.T) {
	stream := `{"id":2,"error":{"code":-32603,"message":"not logged in"}}`
	if _, err := readLiveReply(strings.NewReader(stream)); err == nil ||
		!strings.Contains(err.Error(), "not logged in") {
		t.Errorf("err = %v, want it to carry the server's message", err)
	}
}

func TestReadLiveReplyEndsWithoutAnswer(t *testing.T) {
	stream := `{"id":1,"result":{}}`
	if _, err := readLiveReply(strings.NewReader(stream)); !errors.Is(err, errNoLiveReply) {
		t.Errorf("err = %v, want errNoLiveReply", err)
	}
}

// Live numbers win outright: the session logs here hold a snapshot claiming 55%
// that Fetch must not consult, let alone prefer.
func TestFetchPrefersLiveOverSessionLogs(t *testing.T) {
	p := &Provider{
		SessionsDir: writeSessions(t, map[string]string{"a.jsonl": sessionLine(t, 55, 300)}),
		Now:         func() time.Time { return liveTestNow },
		Live:        func() (json.RawMessage, error) { return []byte(liveResult), nil },
	}

	got, err := p.Fetch()
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Windows) != 1 || got.Windows[0].Utilization != 0 {
		t.Errorf("windows = %+v, want the live 0%%, not the log's 55%%", got.Windows)
	}
}

func TestFetchFallsBackToSessionLogsWhenLiveFails(t *testing.T) {
	p := &Provider{
		SessionsDir: writeSessions(t, map[string]string{"a.jsonl": sessionLine(t, 55, 300)}),
		Now:         func() time.Time { return liveTestNow },
		Live:        func() (json.RawMessage, error) { return nil, errors.New("codex not installed") },
	}

	got, err := p.Fetch()
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Windows) != 1 || got.Windows[0].Utilization != 55 {
		t.Errorf("windows = %+v, want the session log's 55%%", got.Windows)
	}
}

// A live reply that parses but carries no window is not an answer; Fetch must
// treat it as a failure and fall back rather than render an empty section.
func TestFetchFallsBackWhenLiveReplyHasNoWindows(t *testing.T) {
	p := &Provider{
		SessionsDir: writeSessions(t, map[string]string{"a.jsonl": sessionLine(t, 55, 300)}),
		Now:         func() time.Time { return liveTestNow },
		Live:        func() (json.RawMessage, error) { return []byte(`{"rateLimits":{}}`), nil },
	}

	got, err := p.Fetch()
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got.Windows) != 1 || got.Windows[0].Utilization != 55 {
		t.Errorf("windows = %+v, want the session log's 55%%", got.Windows)
	}
}
