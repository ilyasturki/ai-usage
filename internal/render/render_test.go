package render

import (
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
			name: "with reset countdown",
			win:  usage.Window{Label: "5-hour", Utilization: 42.5, ResetsAt: ptr(clock.Add(2*time.Hour + 30*time.Minute))},
			want: "5-hour         [########------------]  42.5%  resets in 2h30m",
		},
		{
			name: "zero utilization, no reset",
			win:  usage.Window{Label: "Weekly Sonnet", Utilization: 0.0},
			want: "Weekly Sonnet  [--------------------]   0.0%",
		},
		{
			name: "full bar at 100%",
			win:  usage.Window{Label: "5-hour", Utilization: 100.0},
			want: "5-hour         [####################] 100.0%",
		},
		{
			name: "over 100% clamps the bar",
			win:  usage.Window{Label: "Weekly", Utilization: 150.0},
			want: "Weekly         [####################] 150.0%",
		},
		{
			name: "reset in the past reads 0h00m",
			win:  usage.Window{Label: "Weekly", Utilization: 42.0, ResetsAt: ptr(clock.Add(-time.Hour))},
			want: "Weekly         [########------------]  42.0%  resets in 0h00m",
		},
		{
			name: "multi-day countdown",
			win:  usage.Window{Label: "Weekly Opus", Utilization: 10.0, ResetsAt: ptr(clock.Add(108 * time.Hour))},
			want: "Weekly Opus    [##------------------]  10.0%  resets in 108h00m",
		},
		{
			name: "partial-block flooring (49% -> 9 blocks)",
			win:  usage.Window{Label: "Weekly", Utilization: 49.0},
			want: "Weekly         [#########-----------]  49.0%",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RenderWindow(tt.win, clock); got != tt.want {
				t.Errorf("RenderWindow()\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestRenderExtra(t *testing.T) {
	tests := []struct {
		extra usage.Extra
		want  string
	}{
		{usage.Extra{Label: "Credits", Value: "unlimited"}, "Credits        unlimited"},
		{usage.Extra{Label: "Credits", Value: "0"}, "Credits        0"},
		{usage.Extra{Label: "Credits", Value: "12.5"}, "Credits        12.5"},
	}
	for _, tt := range tests {
		if got := RenderExtra(tt.extra); got != tt.want {
			t.Errorf("RenderExtra(%+v)\n got: %q\nwant: %q", tt.extra, got, tt.want)
		}
	}
}

func TestLinesOrdersWindowsThenExtras(t *testing.T) {
	r := usage.Result{
		Windows: []usage.Window{{Label: "5-hour", Utilization: 10}},
		Extras:  []usage.Extra{{Label: "Credits", Value: "unlimited"}},
	}
	got := Lines(r, clock)
	want := []string{
		"5-hour         [##------------------]  10.0%",
		"Credits        unlimited",
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
