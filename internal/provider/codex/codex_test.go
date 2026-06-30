package codex

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ai-usage/internal/usage"
)

func TestParseRateLimitsWindowsAndCredits(t *testing.T) {
	raw := []byte(`{
		"primary":   {"used_percent": 55.0, "resets_at": 1782000000, "window_minutes": 300},
		"secondary": {"used_percent": 8.0,  "resets_at": 1782000000, "window_minutes": 10080},
		"credits":   {"unlimited": false, "balance": 42}
	}`)
	res, err := ParseRateLimits(raw)
	if err != nil {
		t.Fatalf("ParseRateLimits() error = %v", err)
	}
	if len(res.Windows) != 2 {
		t.Fatalf("got %d windows, want 2: %+v", len(res.Windows), res.Windows)
	}
	if res.Windows[0].Label != "5-hour" || res.Windows[0].Utilization != 55.0 {
		t.Errorf("primary = %+v, want 5-hour/55.0", res.Windows[0])
	}
	if res.Windows[1].Label != "Weekly" || res.Windows[1].Utilization != 8.0 {
		t.Errorf("secondary = %+v, want Weekly/8.0", res.Windows[1])
	}
	if len(res.Extras) != 1 || res.Extras[0].Value != "42" {
		t.Fatalf("extras = %+v, want one credits/42", res.Extras)
	}
}

func TestParseRateLimitsWindowLabels(t *testing.T) {
	tests := []struct {
		minutes string // raw JSON for window_minutes
		want    string
	}{
		{"300", "5-hour"},
		{"10080", "Weekly"},
		{"120", "2-hour"},
		{"90", "90m"},
		{"60", "1-hour"},
		{`"oops"`, "Primary"}, // non-numeric -> fallback
		{"null", "Primary"},   // null -> fallback
		{"0", "Primary"},      // non-positive -> fallback
	}
	for _, tt := range tests {
		t.Run(tt.minutes, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{"primary": {"used_percent": 10.0, "window_minutes": %s}}`, tt.minutes))
			res, err := ParseRateLimits(raw)
			if err != nil {
				t.Fatalf("ParseRateLimits() error = %v", err)
			}
			if len(res.Windows) != 1 {
				t.Fatalf("got %d windows, want 1", len(res.Windows))
			}
			if res.Windows[0].Label != tt.want {
				t.Errorf("label = %q, want %q", res.Windows[0].Label, tt.want)
			}
		})
	}
}

func TestParseRateLimitsCredits(t *testing.T) {
	tests := []struct {
		name    string
		credits string
		want    string // "" means no credits line
	}{
		{"unlimited", `{"unlimited": true}`, "unlimited"},
		{"unlimited wins over balance", `{"unlimited": true, "balance": 5}`, "unlimited"},
		{"balance zero shown", `{"unlimited": false, "balance": 0}`, "0"},
		{"balance float preserved", `{"balance": 100.0}`, "100.0"},
		{"balance null omitted", `{"unlimited": false, "balance": null}`, ""},
		{"empty credits omitted", `{}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{"primary": {"used_percent": 1.0}, "credits": %s}`, tt.credits))
			res, err := ParseRateLimits(raw)
			if err != nil {
				t.Fatalf("ParseRateLimits() error = %v", err)
			}
			if tt.want == "" {
				if len(res.Extras) != 0 {
					t.Fatalf("extras = %+v, want none", res.Extras)
				}
				return
			}
			if len(res.Extras) != 1 || res.Extras[0].Value != tt.want {
				t.Fatalf("extras = %+v, want credits %q", res.Extras, tt.want)
			}
		})
	}
}

func TestParseRateLimitsOmitsWindowWithoutUsedPercent(t *testing.T) {
	raw := []byte(`{"primary": {"window_minutes": 300}, "secondary": {"used_percent": 7.0, "window_minutes": 10080}}`)
	res, err := ParseRateLimits(raw)
	if err != nil {
		t.Fatalf("ParseRateLimits() error = %v", err)
	}
	if len(res.Windows) != 1 || res.Windows[0].Label != "Weekly" {
		t.Fatalf("got %+v, want only the Weekly window", res.Windows)
	}
}

func TestParseRateLimitsZeroResetGivesNoCountdown(t *testing.T) {
	raw := []byte(`{"primary": {"used_percent": 50.0, "resets_at": 0, "window_minutes": 300}}`)
	res, err := ParseRateLimits(raw)
	if err != nil {
		t.Fatalf("ParseRateLimits() error = %v", err)
	}
	if res.Windows[0].ResetsAt != nil {
		t.Errorf("ResetsAt = %v, want nil for resets_at == 0", res.Windows[0].ResetsAt)
	}
}

func TestLatestRateLimitsPicksNewestTimestamp(t *testing.T) {
	// Three token_count events, out of timestamp order; the middle one is
	// newest and must win.
	lines := strings.Join([]string{
		`{"timestamp":"2026-06-30T10:00:00Z","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":10.0}}}}`,
		`{"timestamp":"2026-06-30T12:00:00Z","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":99.0}}}}`,
		`{"timestamp":"2026-06-30T11:00:00Z","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":50.0}}}}`,
	}, "\n")

	raw, ok := latestRateLimits(strings.NewReader(lines))
	if !ok {
		t.Fatal("latestRateLimits() ok = false, want a snapshot")
	}
	res, err := ParseRateLimits(raw)
	if err != nil {
		t.Fatalf("ParseRateLimits() error = %v", err)
	}
	if res.Windows[0].Utilization != 99.0 {
		t.Errorf("utilization = %v, want 99.0 (the newest snapshot)", res.Windows[0].Utilization)
	}
}

func TestLatestRateLimitsSkipsMalformedAndIrrelevantLines(t *testing.T) {
	lines := strings.Join([]string{
		`this is not json`,
		`{"payload":"a string not an object"}`,
		`{"payload":{"type":"other_event","rate_limits":{"primary":{"used_percent":1.0}}}}`,
		`{"payload":{"type":"token_count","rate_limits":null}}`,
		`{"payload":{"type":"token_count"}}`,
		`{"timestamp":"2026-06-30T09:00:00Z","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":33.0}}}}`,
		``,
	}, "\n")

	raw, ok := latestRateLimits(strings.NewReader(lines))
	if !ok {
		t.Fatal("latestRateLimits() ok = false, want the one valid snapshot")
	}
	res, _ := ParseRateLimits(raw)
	if res.Windows[0].Utilization != 33.0 {
		t.Errorf("utilization = %v, want 33.0", res.Windows[0].Utilization)
	}
}

func TestLatestRateLimitsLargeLineIsRead(t *testing.T) {
	// A token_count line well past bufio's 64 KB default must still be read.
	padding := strings.Repeat("x", 200*1024)
	big := fmt.Sprintf(`{"timestamp":"2026-06-30T10:00:00Z","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":77.0}},"junk":%q}}`, padding)

	raw, ok := latestRateLimits(strings.NewReader(big))
	if !ok {
		t.Fatal("latestRateLimits() ok = false on a large valid line")
	}
	res, _ := ParseRateLimits(raw)
	if res.Windows[0].Utilization != 77.0 {
		t.Errorf("utilization = %v, want 77.0", res.Windows[0].Utilization)
	}
}

func TestScanRateLimitsOversizedLineSkippedScanContinues(t *testing.T) {
	// An over-cap line sits between an older and a newer snapshot. The scan
	// must skip it and still reach (and prefer) the newer snapshot after it —
	// not abort the file at the oversized line.
	older := `{"timestamp":"2026-06-30T10:00:00Z","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":10.0}}}}`
	newer := `{"timestamp":"2026-06-30T12:00:00Z","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":99.0}}}}`
	oversized := strings.Repeat("x", 512) // exceeds the 256-byte cap below
	content := older + "\n" + oversized + "\n" + newer + "\n"

	raw, ok := scanRateLimits(strings.NewReader(content), 256)
	if !ok {
		t.Fatal("scanRateLimits() ok = false; the scan aborted at the oversized line")
	}
	res, _ := ParseRateLimits(raw)
	if res.Windows[0].Utilization != 99.0 {
		t.Errorf("utilization = %v, want 99.0 (newer snapshot after the oversized line)", res.Windows[0].Utilization)
	}
}

func TestScanRateLimitsOversizedLineBeforeOnlySnapshot(t *testing.T) {
	// The newest file's first line is oversized; the snapshot after it must
	// still be found (so Fetch does not needlessly fall through to an older file).
	oversized := strings.Repeat("y", 512)
	snap := `{"timestamp":"2026-06-30T11:00:00Z","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":44.0}}}}`
	content := oversized + "\n" + snap + "\n"

	raw, ok := scanRateLimits(strings.NewReader(content), 256)
	if !ok {
		t.Fatal("scanRateLimits() ok = false; the oversized first line aborted the scan")
	}
	res, _ := ParseRateLimits(raw)
	if res.Windows[0].Utilization != 44.0 {
		t.Errorf("utilization = %v, want 44.0", res.Windows[0].Utilization)
	}
}

func TestLatestRateLimitsNoSnapshot(t *testing.T) {
	if _, ok := latestRateLimits(strings.NewReader("{}\n{\"payload\":{\"type\":\"x\"}}")); ok {
		t.Error("latestRateLimits() ok = true, want false when there is no token_count snapshot")
	}
}

func TestResetsAtConvertedFromEpoch(t *testing.T) {
	raw := []byte(`{"primary": {"used_percent": 50.0, "resets_at": 1782000000, "window_minutes": 300}}`)
	res, _ := ParseRateLimits(raw)
	if res.Windows[0].ResetsAt == nil {
		t.Fatal("ResetsAt is nil, want a time from the epoch")
	}
	want := time.Unix(1782000000, 0).UTC()
	if !res.Windows[0].ResetsAt.Equal(want) {
		t.Errorf("ResetsAt = %v, want %v", res.Windows[0].ResetsAt, want)
	}
}

func TestParseRateLimitsNonObjectIsNoSnapshot(t *testing.T) {
	if _, err := ParseRateLimits([]byte(`"not an object"`)); !errors.Is(err, ErrNoSnapshot) {
		t.Errorf("ParseRateLimits() error = %v, want ErrNoSnapshot", err)
	}
}

var _ usage.Provider = (*Provider)(nil)
