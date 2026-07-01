// Package usage defines the provider-agnostic data model that every AI
// provider produces and the renderer consumes. Keeping these types in their
// own leaf package lets providers, the renderer, and the app wire together
// without import cycles.
package usage

import "time"

// Window is a single usage window: a labeled budget with a utilization
// percentage (0–100) and, when known, the time it resets.
type Window struct {
	Label       string
	Utilization float64
	// ResetsAt is the absolute reset time, or nil when the provider reports
	// no reset for this window. The renderer turns it into a relative
	// "resets in 2 hours 30 minutes" countdown against the injected clock.
	ResetsAt *time.Time
}

// Extra is a labeled line that is not a usage window — e.g. the Codex credit
// balance. It renders with the same label column as a window but without a bar.
type Extra struct {
	Label string
	Value string
}

// Result is everything a provider has to show: its usage windows plus any
// extra labeled lines. A successful fetch may legitimately yield zero windows
// (every window had null utilization), which renders as nothing.
type Result struct {
	Windows []Window
	Extras  []Extra
	// AsOf is when this data was captured, for a provider that reads a cached
	// snapshot rather than live state (Codex). Nil means live/now (Claude) or
	// unknown. The renderer uses it to say how old a Stale result is.
	AsOf *time.Time
	// Stale reports that every usage window had already reset by the time it was
	// read, so the recorded percentages no longer describe a live budget. The
	// renderer shows a short "no recent session" note in place of the windows —
	// which, having all reset, would otherwise read as a misleading flat 0%.
	Stale bool
}

// Provider is one AI agent's usage source. Adding a third provider (opencode,
// antigravity, …) is a new type that satisfies this interface, not a rewrite.
type Provider interface {
	// Name is the header shown above this provider's windows in the combined
	// view, e.g. "Claude" or "Codex".
	Name() string
	// Fetch reads the provider's usage. A non-nil error carries a single-line,
	// user-facing message (its Error() is printed verbatim).
	Fetch() (Result, error)
}
