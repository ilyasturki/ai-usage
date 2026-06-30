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
