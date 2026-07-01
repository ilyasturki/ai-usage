package render

import (
	"strings"
	"testing"
	"time"

	"ai-usage/internal/usage"
)

func ptr[T any](v T) *T { return &v }

var clock = time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

func TestRenderWindow(t *testing.T) {
	tests := []struct {
		name string
		win  usage.Window
		want string
	}{
		{
			name: "hours and minutes",
			win:  usage.Window{Label: "5-hour", Utilization: 42.5, ResetsAt: ptr(clock.Add(2*time.Hour + 30*time.Minute))},
			want: "5-hour        ████████░░░░░░░░░░░░     42.5%   resets in 2 hours 30 minutes",
		},
		{
			name: "zero utilization, no reset",
			win:  usage.Window{Label: "Weekly Sonnet", Utilization: 0.0},
			want: "Weekly Sonnet ░░░░░░░░░░░░░░░░░░░░      0.0%",
		},
		{
			name: "full bar at 100%",
			win:  usage.Window{Label: "5-hour", Utilization: 100.0},
			want: "5-hour        ████████████████████    100.0%",
		},
		{
			name: "over 100% clamps the bar",
			win:  usage.Window{Label: "Weekly", Utilization: 150.0},
			want: "Weekly        ████████████████████    150.0%",
		},
		{
			name: "reset in the past reads 'resets now'",
			win:  usage.Window{Label: "Weekly", Utilization: 42.0, ResetsAt: ptr(clock.Add(-time.Hour))},
			want: "Weekly        ████████░░░░░░░░░░░░     42.0%   resets now",
		},
		{
			name: "multi-day countdown shows days and hours",
			win:  usage.Window{Label: "Weekly Opus", Utilization: 10.0, ResetsAt: ptr(clock.Add(108 * time.Hour))},
			want: "Weekly Opus   ██░░░░░░░░░░░░░░░░░░     10.0%   resets in 4 days 12 hours",
		},
		{
			name: "singular day and hour",
			win:  usage.Window{Label: "Weekly", Utilization: 10.0, ResetsAt: ptr(clock.Add(25 * time.Hour))},
			want: "Weekly        ██░░░░░░░░░░░░░░░░░░     10.0%   resets in 1 day 1 hour",
		},
		{
			name: "whole days drop a zero hour",
			win:  usage.Window{Label: "Weekly", Utilization: 8.0, ResetsAt: ptr(clock.Add(48*time.Hour + 15*time.Minute))},
			want: "Weekly        █░░░░░░░░░░░░░░░░░░░      8.0%   resets in 2 days",
		},
		{
			name: "whole hour drops zero minutes",
			win:  usage.Window{Label: "5-hour", Utilization: 55.0, ResetsAt: ptr(clock.Add(time.Hour))},
			want: "5-hour        ███████████░░░░░░░░░     55.0%   resets in 1 hour",
		},
		{
			name: "minutes only",
			win:  usage.Window{Label: "5-hour", Utilization: 14.0, ResetsAt: ptr(clock.Add(45 * time.Minute))},
			want: "5-hour        ██░░░░░░░░░░░░░░░░░░     14.0%   resets in 45 minutes",
		},
		{
			name: "one minute is singular",
			win:  usage.Window{Label: "5-hour", Utilization: 14.0, ResetsAt: ptr(clock.Add(time.Minute))},
			want: "5-hour        ██░░░░░░░░░░░░░░░░░░     14.0%   resets in 1 minute",
		},
		{
			name: "floors to whole cells at 49%",
			win:  usage.Window{Label: "Weekly", Utilization: 49.0},
			want: "Weekly        █████████░░░░░░░░░░░     49.0%",
		},
	}
	rr := New(false)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rr.Window(tt.win, clock); got != tt.want {
				t.Errorf("Window()\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestRenderExtra(t *testing.T) {
	tests := []struct {
		extra usage.Extra
		want  string
	}{
		{usage.Extra{Label: "Credits", Value: "unlimited"}, "Credits       unlimited"},
		{usage.Extra{Label: "Credits", Value: "0"}, "Credits       0"},
		{usage.Extra{Label: "Credits", Value: "12.5"}, "Credits       12.5"},
	}
	rr := New(false)
	for _, tt := range tests {
		if got := rr.Extra(tt.extra); got != tt.want {
			t.Errorf("Extra(%+v)\n got: %q\nwant: %q", tt.extra, got, tt.want)
		}
	}
}

func TestHeader(t *testing.T) {
	rr := New(false)
	if got, want := rr.Header("Claude"), "━━━ Claude "+strings.Repeat("━", 39); got != want {
		t.Errorf("Header(Claude)\n got: %q\nwant: %q", got, want)
	}
	if got, want := rr.Header("Codex"), "━━━ Codex "+strings.Repeat("━", 40); got != want {
		t.Errorf("Header(Codex)\n got: %q\nwant: %q", got, want)
	}
}

func TestLinesOrdersWindowsThenExtras(t *testing.T) {
	r := usage.Result{
		Windows: []usage.Window{{Label: "5-hour", Utilization: 10}},
		Extras:  []usage.Extra{{Label: "Credits", Value: "unlimited"}},
	}
	got := New(false).Lines(r, clock)
	want := []string{
		"5-hour        ██░░░░░░░░░░░░░░░░░░     10.0%",
		"Credits       unlimited",
	}
	if len(got) != len(want) {
		t.Fatalf("Lines() returned %d lines, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Lines()[%d]\n got: %q\nwant: %q", i, got[i], want[i])
		}
	}
}

func TestLinesStaleShowsNoteInPlaceOfWindows(t *testing.T) {
	asOf := time.Date(2026, 6, 16, 20, 34, 24, 0, time.UTC)
	r := usage.Result{
		Windows: []usage.Window{ // zeroed by the provider; must not be rendered
			{Label: "5-hour", Utilization: 0},
			{Label: "Weekly", Utilization: 0},
		},
		Extras: []usage.Extra{{Label: "Credits", Value: "0"}},
		Stale:  true,
		AsOf:   &asOf,
	}
	got := New(false).Lines(r, clock)
	want := []string{
		"no recent session — last seen Jun 16 (13 days ago)",
		"Credits       0",
	}
	if len(got) != len(want) {
		t.Fatalf("Lines() = %d lines, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Lines()[%d]\n got: %q\nwant: %q", i, got[i], want[i])
		}
	}
}

func TestLinesStaleWithoutAsOfOmitsAge(t *testing.T) {
	r := usage.Result{
		Windows: []usage.Window{{Label: "5-hour", Utilization: 0}},
		Stale:   true, // AsOf nil: capture time unknown
	}
	got := New(false).Lines(r, clock)
	if len(got) != 1 || got[0] != "no recent session" {
		t.Errorf("Lines() = %q, want a single bare \"no recent session\" note", got)
	}
}

func TestAgo(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{time.Minute, "1 minute ago"},
		{45 * time.Minute, "45 minutes ago"},
		{time.Hour, "1 hour ago"},
		{5 * time.Hour, "5 hours ago"},
		{24 * time.Hour, "1 day ago"},
		{13*24*time.Hour + 15*time.Hour, "13 days ago"}, // extra hours floor away
	}
	for _, tt := range tests {
		if got := ago(clock.Add(-tt.d), clock); got != tt.want {
			t.Errorf("ago(-%s) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// TestColorStyling checks that an enabled Renderer colors the filled bar by
// level and dims the empty run and countdown, with matching resets.
func TestColorStyling(t *testing.T) {
	rr := New(true)
	got := rr.Window(usage.Window{Label: "5-hour", Utilization: 42.5, ResetsAt: ptr(clock.Add(90 * time.Minute))}, clock)
	if !strings.Contains(got, ansiGreen) { // 42.5% < 50 → green filled run
		t.Errorf("Window() = %q, want a green run", got)
	}
	if !strings.Contains(got, ansiDim) { // empty run and countdown dimmed
		t.Errorf("Window() = %q, want a dim run", got)
	}
	if !strings.Contains(got, ansiReset) {
		t.Errorf("Window() = %q, want reset codes", got)
	}
	if !strings.Contains(got, "resets in 1 hour 30 minutes") {
		t.Errorf("Window() = %q, want the countdown text intact", got)
	}
}

func TestColorLevels(t *testing.T) {
	rr := New(true)
	cases := []struct {
		util float64
		code string
		name string
	}{
		{10, ansiGreen, "green"},
		{50, ansiYellow, "yellow at 50"},
		{80, ansiYellow, "yellow at 80"},
		{80.1, ansiRed, "red just past 80"},
		{95, ansiRed, "red"},
	}
	for _, c := range cases {
		if got := rr.bar(c.util); !strings.Contains(got, c.code) {
			t.Errorf("bar(%g) = %q, want %s", c.util, got, c.name)
		}
	}
}

func TestNoEscapesWhenColorDisabled(t *testing.T) {
	rr := New(false)
	got := rr.Window(usage.Window{Label: "5-hour", Utilization: 90, ResetsAt: ptr(clock.Add(time.Hour))}, clock)
	if strings.ContainsRune(got, '\x1b') {
		t.Errorf("Window() = %q, want no escape codes", got)
	}
	if strings.ContainsRune(rr.Header("Claude"), '\x1b') {
		t.Error("Header() leaked escape codes with color disabled")
	}
	if strings.ContainsRune(rr.Notice("boom"), '\x1b') {
		t.Error("Notice() leaked escape codes with color disabled")
	}
}
