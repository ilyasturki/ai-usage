// Package render turns provider results into the exact text the user sees.
// All output formatting lives here so the rendered bytes are the single thing
// tests assert on, and so every provider shares one bar/countdown style.
package render

import (
	"fmt"
	"math"
	"strings"
	"time"

	"ai-usage/internal/usage"
)

// barWidth is the number of cells in a usage bar; one cell per 5%.
const barWidth = 20

// labelWidth left-pads every label column so bars and values line up.
const labelWidth = 14

// Lines renders a provider result to its display lines (windows then extras),
// without indentation or trailing newlines. now drives the reset countdowns.
func Lines(r usage.Result, now time.Time) []string {
	lines := make([]string, 0, len(r.Windows)+len(r.Extras))
	for _, w := range r.Windows {
		lines = append(lines, RenderWindow(w, now))
	}
	for _, e := range r.Extras {
		lines = append(lines, RenderExtra(e))
	}
	return lines
}

// RenderWindow formats one window as "{label} [{bar}] {percent}%{ countdown}".
func RenderWindow(w usage.Window, now time.Time) string {
	when := ""
	if w.ResetsAt != nil {
		when = countdown(*w.ResetsAt, now)
	}
	return fmt.Sprintf("%-*s [%s] %5.1f%%%s", labelWidth, w.Label, bar(w.Utilization), w.Utilization, when)
}

// RenderExtra formats a non-window line (e.g. credits) in the label column.
func RenderExtra(e usage.Extra) string {
	return fmt.Sprintf("%-*s %s", labelWidth, e.Label, e.Value)
}

// bar draws a barWidth-cell bar, one filled cell per 5% of utilization,
// clamped to the bar's range so over/under values can't break the layout.
func bar(util float64) string {
	filled := int(util / 5)
	if filled < 0 {
		filled = 0
	}
	if filled > barWidth {
		filled = barWidth
	}
	return strings.Repeat("#", filled) + strings.Repeat("-", barWidth-filled)
}

// countdown renders the time until reset as "  resets in Xh YYm", flooring to
// whole minutes and never going negative (a window past its reset reads 0h00m).
func countdown(resetsAt, now time.Time) string {
	mins := int(math.Floor(resetsAt.Sub(now).Minutes()))
	if mins < 0 {
		mins = 0
	}
	return fmt.Sprintf("  resets in %dh%02dm", mins/60, mins%60)
}
