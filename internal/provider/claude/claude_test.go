package claude

import (
	"errors"
	"testing"
	"time"

	"ai-usage/internal/usage"
)

func TestParseUsageHealthy(t *testing.T) {
	body := []byte(`{
		"five_hour":        {"utilization": 42.5, "resets_at": "2026-06-30T14:30:00Z"},
		"seven_day":        {"utilization": 10.0, "resets_at": "2026-07-05T00:00:00Z"},
		"seven_day_opus":   {"utilization": 0.0,  "resets_at": "2026-07-05T00:00:00Z"},
		"seven_day_sonnet": {"utilization": null}
	}`)

	res, err := ParseUsage(body)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}
	// seven_day_sonnet has null utilization and must be omitted.
	if len(res.Windows) != 3 {
		t.Fatalf("got %d windows, want 3: %+v", len(res.Windows), res.Windows)
	}

	want := []struct {
		label string
		util  float64
		reset string // RFC3339, or "" for none
	}{
		{"5-hour", 42.5, "2026-06-30T14:30:00Z"},
		{"Weekly", 10.0, "2026-07-05T00:00:00Z"},
		{"Weekly Opus", 0.0, "2026-07-05T00:00:00Z"},
	}
	for i, w := range want {
		got := res.Windows[i]
		if got.Label != w.label || got.Utilization != w.util {
			t.Errorf("window %d = {%q, %v}, want {%q, %v}", i, got.Label, got.Utilization, w.label, w.util)
		}
		if got.ResetsAt == nil {
			t.Errorf("window %d: ResetsAt is nil, want %s", i, w.reset)
			continue
		}
		wantTime, _ := time.Parse(time.RFC3339, w.reset)
		if !got.ResetsAt.Equal(wantTime) {
			t.Errorf("window %d: ResetsAt = %v, want %v", i, got.ResetsAt, wantTime)
		}
	}
}

func TestParseUsageScopedWeeklyLimit(t *testing.T) {
	// Real-account shape: the per-model weekly limits (Fable here) live only in
	// the "limits" array as weekly_scoped entries, while seven_day_opus/sonnet
	// are null. The session/weekly_all entries duplicate the top-level windows
	// and must not add extra lines.
	body := []byte(`{
		"five_hour":        {"utilization": 43.0, "resets_at": "2026-07-04T23:00:00.353045+00:00"},
		"seven_day":        {"utilization": 58.0, "resets_at": "2026-07-07T15:00:00.353062+00:00"},
		"seven_day_opus":   null,
		"seven_day_sonnet": null,
		"limits": [
			{"kind": "session",       "group": "session", "percent": 43, "resets_at": "2026-07-04T23:00:00.353045+00:00", "scope": null},
			{"kind": "weekly_all",    "group": "weekly",  "percent": 58, "resets_at": "2026-07-07T15:00:00.353062+00:00", "scope": null},
			{"kind": "weekly_scoped", "group": "weekly",  "percent": 76, "resets_at": "2026-07-07T15:00:00.353320+00:00", "scope": {"model": {"id": null, "display_name": "Fable"}}, "is_active": true}
		]
	}`)

	res, err := ParseUsage(body)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}
	// five_hour, seven_day, then the Fable window pulled from the limits array.
	if len(res.Windows) != 3 {
		t.Fatalf("got %d windows, want 3: %+v", len(res.Windows), res.Windows)
	}
	fable := res.Windows[2]
	if fable.Label != "Weekly Fable" || fable.Utilization != 76 {
		t.Errorf("scoped window = {%q, %v}, want {%q, 76}", fable.Label, fable.Utilization, "Weekly Fable")
	}
	if fable.ResetsAt == nil {
		t.Fatal("Weekly Fable ResetsAt is nil, want a parsed time")
	}
	wantReset, _ := time.Parse(time.RFC3339, "2026-07-07T15:00:00.353320+00:00")
	if !fable.ResetsAt.Equal(wantReset) {
		t.Errorf("Weekly Fable ResetsAt = %v, want %v", fable.ResetsAt, wantReset)
	}
}

func TestParseUsageScopedLimitDedupesTopLevelWindow(t *testing.T) {
	// A model reported both as a top-level window and as a weekly_scoped limit
	// must render exactly once (the top-level window wins).
	body := []byte(`{
		"five_hour":      {"utilization": 5.0},
		"seven_day_opus": {"utilization": 30.0},
		"limits": [
			{"kind": "weekly_scoped", "percent": 30, "scope": {"model": {"display_name": "Opus"}}}
		]
	}`)
	res, err := ParseUsage(body)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}
	opus := 0
	for _, w := range res.Windows {
		if w.Label == "Weekly Opus" {
			opus++
		}
	}
	if opus != 1 {
		t.Fatalf("got %d Weekly Opus windows, want 1: %+v", opus, res.Windows)
	}
}

func TestParseUsageIgnoresNonScopedAndMalformedLimits(t *testing.T) {
	// weekly_scoped without a model, non-weekly_scoped kinds, and a non-array
	// limits value must all be tolerated without adding windows or erroring.
	for name, body := range map[string]string{
		"scoped without model": `{"five_hour": {"utilization": 5.0}, "limits": [{"kind": "weekly_scoped", "percent": 50, "scope": null}]}`,
		"only session/weekly":  `{"five_hour": {"utilization": 5.0}, "limits": [{"kind": "session", "percent": 5}, {"kind": "weekly_all", "percent": 9}]}`,
		"limits not an array":  `{"five_hour": {"utilization": 5.0}, "limits": {"unexpected": "object"}}`,
	} {
		res, err := ParseUsage([]byte(body))
		if err != nil {
			t.Fatalf("%s: ParseUsage() error = %v", name, err)
		}
		if len(res.Windows) != 1 || res.Windows[0].Label != "5-hour" {
			t.Fatalf("%s: got %+v, want only the 5-hour window", name, res.Windows)
		}
	}
}

func TestParseUsageMissingFiveHourIsExpired(t *testing.T) {
	// A 401-style error body lacks five_hour entirely.
	body := []byte(`{"error": {"type": "authentication_error", "message": "expired"}}`)
	_, err := ParseUsage(body)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("ParseUsage() error = %v, want ErrExpired", err)
	}
}

func TestParseUsageUnparseable(t *testing.T) {
	_, err := ParseUsage([]byte("not json at all"))
	if err == nil || errors.Is(err, ErrExpired) {
		t.Fatalf("ParseUsage() error = %v, want a parse error", err)
	}
}

func TestParseUsageToleratesOptionalShapes(t *testing.T) {
	// five_hour present (so not expired), every other window absent, null, or
	// an empty object — all of which must be omitted without panicking.
	body := []byte(`{
		"five_hour":      {"utilization": 5.0},
		"seven_day":      null,
		"seven_day_opus": {}
	}`)
	res, err := ParseUsage(body)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v", err)
	}
	if len(res.Windows) != 1 || res.Windows[0].Label != "5-hour" {
		t.Fatalf("got %+v, want only the 5-hour window", res.Windows)
	}
	if res.Windows[0].ResetsAt != nil {
		t.Errorf("5-hour ResetsAt = %v, want nil (no resets_at field)", res.Windows[0].ResetsAt)
	}
}

func TestParseUsagePresentButNullFiveHourIsOmittedNotExpired(t *testing.T) {
	// The key is present (so not treated as expired) but its value is null, so
	// the window is simply omitted.
	body := []byte(`{"five_hour": null, "seven_day": {"utilization": 20.0}}`)
	res, err := ParseUsage(body)
	if err != nil {
		t.Fatalf("ParseUsage() error = %v, want success", err)
	}
	if len(res.Windows) != 1 || res.Windows[0].Label != "Weekly" {
		t.Fatalf("got %+v, want only the Weekly window", res.Windows)
	}
}

var _ usage.Provider = (*Provider)(nil)
