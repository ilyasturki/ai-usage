// Package render turns provider results into the exact text the user sees.
// All output formatting lives here so the rendered bytes are the single thing
// tests assert on, and so every provider shares one bar/header/countdown style.
//
// A Renderer carries one decision — whether ANSI color is enabled — so callers
// build it once with New(color) and every line it produces is styled
// consistently. With color off it emits plain bytes, which is what the tests
// assert on and what a piped or non-TTY invocation receives.
package render

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"ai-usage/internal/usage"
)

const (
	// barWidth is the number of cells in a usage bar; one cell per 5%.
	barWidth = 20
	// labelWidth left-pads every label so the bars (and an extra's value) line
	// up. It is wide enough that the longest label, "Weekly Sonnet", keeps a gap.
	labelWidth = 14
	// pctWidth is the column the percentage is right-aligned in: wide enough for
	// "100.0%" with the gap that follows a bar, and for the "≤" an upper bound
	// carries without shifting the numbers out of line.
	pctWidth = 10
	// ruleWidth is the total column width of a provider's section-header rule.
	ruleWidth = 50
)

// ANSI styling codes, applied only when a Renderer has color enabled.
const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
)

// Renderer formats provider results. The zero value renders without color; use
// New to be explicit at the call site.
type Renderer struct{ color bool }

// New returns a Renderer that emits ANSI color when color is true.
func New(color bool) Renderer { return Renderer{color: color} }

// Header renders a provider's section rule, e.g. "━━━ Claude ━━━━━…━━", with the
// name emphasized and the rule dimmed when color is enabled. The rule uses the
// heavy box-drawing line (U+2501) so it reads as a solid, thick divider.
func (rr Renderer) Header(name string) string {
	dashes := ruleWidth - 4 - utf8.RuneCountInString(name) - 1
	if dashes < 3 {
		dashes = 3
	}
	return rr.dim("━━━ ") + rr.bold(name) + rr.dim(" "+strings.Repeat("━", dashes))
}

// Lines renders a result's windows then extras, without indentation or trailing
// newlines. now drives the reset countdowns. A stale result — every window
// already reset by now — shows a single "no recent session" note in place of
// its windows, since they would otherwise read as a misleading flat 0%. Windows
// from a snapshot old enough to matter (AsOf set) render as upper bounds — a
// recorded percentage only decays as usage ages out — followed by an "as of"
// line naming the age.
func (rr Renderer) Lines(r usage.Result, now time.Time) []string {
	lines := make([]string, 0, len(r.Windows)+len(r.Extras)+1)
	if r.Stale {
		lines = append(lines, rr.staleNote(r.AsOf, now))
	} else {
		bounded := r.AsOf != nil
		for _, w := range r.Windows {
			lines = append(lines, rr.window(w, now, bounded))
		}
		if bounded && len(r.Windows) > 0 {
			lines = append(lines, rr.ageNote(*r.AsOf, now))
		}
	}
	for _, e := range r.Extras {
		lines = append(lines, rr.Extra(e))
	}
	return lines
}

// staleNote is shown in place of windows when a provider's data is stale: every
// window has reset since it was captured, so the recorded percentages no longer
// describe a live budget. It names how long ago the data was captured when that
// time is known, so an old reading is never mistaken for fresh 0% use. The date
// is shown in now's location so it reads in the user's own clock.
func (rr Renderer) staleNote(asOf *time.Time, now time.Time) string {
	if asOf == nil {
		return rr.dim("no recent session")
	}
	when := asOf.In(now.Location()).Format("Jan 2")
	return rr.dim(fmt.Sprintf("no recent session — last seen %s (%s)", when, ago(*asOf, now)))
}

// ageNote dates windows rendered as upper bounds, naming both when the reading
// was taken and which way it is wrong, so a ceiling is not read as a measurement.
// The date is shown in now's location so it reads in the user's own clock.
func (rr Renderer) ageNote(asOf, now time.Time) string {
	when := asOf.In(now.Location()).Format("Jan 2")
	return rr.dim(fmt.Sprintf("as of %s (%s) — actual is lower", when, ago(asOf, now)))
}

// Window formats one window as "{label}{bar}    {percent}{   countdown}".
func (rr Renderer) Window(w usage.Window, now time.Time) string {
	return rr.window(w, now, false)
}

// window renders one window, as a plain reading or — when bounded — as a ceiling.
//
// A bounded window carries a "≤" and drops its countdown. Both follow from the
// reading being old: a recorded percentage only decays as usage ages out of a
// rolling window, so the true figure is at or below it; and the reset time it
// was recorded with is not a fixed deadline (Codex slides the weekly anchor
// forward), so counting down to it would invent a precision the data lacks.
// This assumes nothing was spent from another machine or the web app in the
// meantime — usage the session logs never see, and the one way the real figure
// could instead be higher.
func (rr Renderer) window(w usage.Window, now time.Time, bounded bool) string {
	pct := fmt.Sprintf("%.1f%%", w.Utilization)
	if bounded {
		pct = "≤" + pct
	}
	line := fmt.Sprintf("%-*s%s%s", labelWidth, w.Label, rr.bar(w.Utilization), padLeft(pct, pctWidth))
	if w.ResetsAt != nil && !bounded {
		line += "   " + rr.dim(countdown(*w.ResetsAt, now))
	}
	return line
}

// padLeft right-aligns s in a field width columns wide, counting runes rather
// than bytes so a multi-byte prefix like "≤" lines up with plain percentages
// instead of shifting them by the extra bytes it occupies.
func padLeft(s string, width int) string {
	if n := width - utf8.RuneCountInString(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// Extra formats a non-window line (e.g. credits): a label plus a dimmed value,
// the value aligned to the column the bars start in.
func (rr Renderer) Extra(e usage.Extra) string {
	return fmt.Sprintf("%-*s%s", labelWidth, e.Label, rr.dim(e.Value))
}

// Notice styles a one-line, user-facing message — an error shown in place of a
// provider's windows — red when color is enabled.
func (rr Renderer) Notice(s string) string { return rr.wrap(ansiRed, s) }

// bar draws a barWidth-cell bar: one filled cell per whole 5% of utilization,
// the filled run colored by level and the empty run dimmed. Out-of-range values
// clamp so the layout can't break.
func (rr Renderer) bar(util float64) string {
	filled := int(util / 5)
	if filled < 0 {
		filled = 0
	}
	if filled > barWidth {
		filled = barWidth
	}
	return rr.level(util, strings.Repeat("█", filled)) + rr.dim(strings.Repeat("░", barWidth-filled))
}

// countdown renders the time until reset as "resets in <duration>" — e.g.
// "resets in 4 days 12 hours" or "resets in 45 minutes". It floors to whole
// minutes, shows at most the two most-significant non-zero units (days, hours,
// minutes), and reads "resets now" once a window is at or past its reset.
func countdown(resetsAt, now time.Time) string {
	mins := int(math.Floor(resetsAt.Sub(now).Minutes()))
	if mins <= 0 {
		return "resets now"
	}
	days, hours, minutes := mins/1440, mins%1440/60, mins%60

	var primary, secondary string
	switch {
	case days > 0:
		primary, secondary = plural(days, "day"), plural(hours, "hour")
	case hours > 0:
		primary, secondary = plural(hours, "hour"), plural(minutes, "minute")
	default:
		primary = plural(minutes, "minute")
	}
	if secondary != "" {
		return "resets in " + primary + " " + secondary
	}
	return "resets in " + primary
}

// ago renders how long before now a past time was, as "<n> <unit> ago" using
// the single largest whole unit (days, then hours, then minutes). It floors like
// countdown, so a moment just past reads "just now" rather than a zero count.
func ago(t, now time.Time) string {
	mins := int(math.Floor(now.Sub(t).Minutes()))
	switch {
	case mins < 1:
		return "just now"
	case mins < 60:
		return plural(mins, "minute") + " ago"
	case mins < 1440:
		return plural(mins/60, "hour") + " ago"
	default:
		return plural(mins/1440, "day") + " ago"
	}
}

// plural formats a count and its unit as "1 day" / "3 days", returning "" for a
// zero count so the caller can drop an empty trailing unit — a whole-hour reset
// reads "resets in 1 hour", not "resets in 1 hour 0 minutes".
func plural(n int, unit string) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "1 " + unit
	default:
		return fmt.Sprintf("%d %ss", n, unit)
	}
}

// --- color helpers: no-ops when color is disabled or the span is empty ---

func (rr Renderer) wrap(code, s string) string {
	if !rr.color || s == "" {
		return s
	}
	return code + s + ansiReset
}

func (rr Renderer) dim(s string) string  { return rr.wrap(ansiDim, s) }
func (rr Renderer) bold(s string) string { return rr.wrap(ansiBold, s) }

// level colors s by how full a window is: green below 50%, yellow through 80%,
// red above.
func (rr Renderer) level(util float64, s string) string {
	switch {
	case util < 50:
		return rr.wrap(ansiGreen, s)
	case util <= 80:
		return rr.wrap(ansiYellow, s)
	default:
		return rr.wrap(ansiRed, s)
	}
}
