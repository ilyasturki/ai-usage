// Package codex reads Codex usage from the rate-limit snapshots Codex records
// in its session logs, so checking usage never starts a new Codex session. It
// walks ~/.codex/sessions newest-file-first and reports the most recent
// snapshot it finds.
package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ai-usage/internal/usage"
)

// maxLineBytes caps a single jsonl line. Codex lines routinely exceed bufio's
// 64 KB default, so the buffer is enlarged to read them; a line beyond even
// this is skipped rather than crashing the scan.
const maxLineBytes = 16 * 1024 * 1024

// ErrNoSnapshot is returned when no session file yields a rate-limit snapshot.
var ErrNoSnapshot = errors.New("codex-usage: no Codex rate-limit snapshot found")

// Provider scans the Codex sessions directory for the latest rate-limit
// snapshot. SessionsDir is injected so tests can point at a fixture tree.
type Provider struct {
	SessionsDir string
}

// Name is the combined-view header for this provider.
func (p *Provider) Name() string { return "Codex" }

// Fetch finds the newest session file (by mtime) that carries a rate-limit
// snapshot and renders it. The first file yielding a snapshot wins.
func (p *Provider) Fetch() (usage.Result, error) {
	info, err := os.Stat(p.SessionsDir)
	if err != nil || !info.IsDir() {
		return usage.Result{}, fmt.Errorf("codex-usage: %s not found", p.SessionsDir)
	}

	paths, err := jsonlFilesByMtimeDesc(p.SessionsDir)
	if err != nil {
		return usage.Result{}, fmt.Errorf("codex-usage: %s not found", p.SessionsDir)
	}

	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		raw, ok := latestRateLimits(f)
		f.Close()
		if ok {
			return ParseRateLimits(raw)
		}
	}
	return usage.Result{}, ErrNoSnapshot
}

// jsonlFilesByMtimeDesc lists every *.jsonl file under root, newest mtime
// first (ties broken by path, descending, to match the reference behavior).
// Unreadable directories and entries are skipped silently.
func jsonlFilesByMtimeDesc(root string) ([]string, error) {
	type entry struct {
		path  string
		mtime time.Time
	}
	var entries []entry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		entries = append(entries, entry{path, info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if !entries[i].mtime.Equal(entries[j].mtime) {
			return entries[i].mtime.After(entries[j].mtime)
		}
		return entries[i].path > entries[j].path
	})
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.path
	}
	return paths, nil
}

// latestRateLimits scans one session file and returns the rate_limits object
// from the token_count event with the latest timestamp. Malformed lines are
// skipped; an oversized line ends the scan gracefully with whatever was found.
func latestRateLimits(r io.Reader) (json.RawMessage, bool) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	var current json.RawMessage
	var currentAt time.Time
	var haveCurrent, haveCurrentAt bool

	for sc.Scan() {
		var ev struct {
			Timestamp string          `json:"timestamp"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		var payload struct {
			Type       string          `json:"type"`
			RateLimits json.RawMessage `json:"rate_limits"`
		}
		if json.Unmarshal(ev.Payload, &payload) != nil {
			continue
		}
		if payload.Type != "token_count" || !isJSONObject(payload.RateLimits) {
			continue
		}

		at, atOK := parseTimestamp(ev.Timestamp)
		// Take the first snapshot unconditionally, then replace only with a
		// strictly newer, parseable timestamp.
		if !haveCurrent || (atOK && (!haveCurrentAt || at.After(currentAt))) {
			current = payload.RateLimits
			haveCurrent = true
			currentAt, haveCurrentAt = at, atOK
		}
	}
	// sc.Err() (e.g. a line past maxLineBytes) is intentionally ignored so one
	// bad line doesn't discard the snapshots already collected.
	return current, haveCurrent
}

// ParseRateLimits turns a rate_limits object into windows plus an optional
// credits line. Pure and tolerant: every field is optional and the wrong type
// is treated as absent rather than fatal.
func ParseRateLimits(raw json.RawMessage) (usage.Result, error) {
	obj, ok := asObject(raw)
	if !ok {
		return usage.Result{}, ErrNoSnapshot
	}

	var windows []usage.Window
	if w, ok := parseLimit(obj["primary"], "Primary"); ok {
		windows = append(windows, w)
	}
	if w, ok := parseLimit(obj["secondary"], "Weekly"); ok {
		windows = append(windows, w)
	}

	var extras []usage.Extra
	if e, ok := parseCredits(obj["credits"]); ok {
		extras = append(extras, e)
	}
	return usage.Result{Windows: windows, Extras: extras}, nil
}

// parseLimit reads one rate-limit window. A missing/non-numeric used_percent
// omits the window. The label derives from window_minutes, falling back to the
// given name. A zero/missing resets_at yields no countdown.
func parseLimit(raw json.RawMessage, fallback string) (usage.Window, bool) {
	obj, ok := asObject(raw)
	if !ok {
		return usage.Window{}, false
	}
	used, ok := jsonNumber(obj["used_percent"])
	if !ok {
		return usage.Window{}, false
	}
	w := usage.Window{Label: windowLabel(obj["window_minutes"], fallback), Utilization: used}
	if reset, ok := jsonNumber(obj["resets_at"]); ok && reset != 0 {
		sec := int64(reset)
		nsec := int64((reset - float64(sec)) * 1e9)
		t := time.Unix(sec, nsec).UTC()
		w.ResetsAt = &t
	}
	return w, true
}

// windowLabel maps a window length in minutes to a label: 300 → "5-hour",
// 10080 → "Weekly", whole hours → "N-hour", otherwise "Nm". A missing or
// non-positive length falls back to the given name.
func windowLabel(raw json.RawMessage, fallback string) string {
	minutes, ok := jsonNumber(raw)
	if !ok {
		return fallback
	}
	switch {
	case minutes == 300:
		return "5-hour"
	case minutes == 10080:
		return "Weekly"
	case minutes > 0:
		m := int64(minutes)
		if m%60 == 0 {
			return fmt.Sprintf("%d-hour", m/60)
		}
		return fmt.Sprintf("%dm", m)
	default:
		return fallback
	}
}

// parseCredits reads the optional credits line: "unlimited" when the flag is
// set, otherwise the raw balance value when present and non-null.
func parseCredits(raw json.RawMessage) (usage.Extra, bool) {
	obj, ok := asObject(raw)
	if !ok {
		return usage.Extra{}, false
	}
	if unlimited, ok := jsonBool(obj["unlimited"]); ok && unlimited {
		return usage.Extra{Label: "Credits", Value: "unlimited"}, true
	}
	if bal := obj["balance"]; len(bal) > 0 {
		var n json.Number
		if json.Unmarshal(bal, &n) == nil && n != "" {
			return usage.Extra{Label: "Credits", Value: n.String()}, true
		}
	}
	return usage.Extra{}, false
}

func parseTimestamp(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// isJSONObject reports whether raw is a JSON object (so null/array/scalar
// rate_limits are skipped just like a Python isinstance(_, dict) check).
func isJSONObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}

func asObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if !isJSONObject(raw) {
		return nil, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil, false
	}
	return m, true
}

// jsonNumber extracts a JSON number as a float. Strings, bools, null and
// absent values report not-ok.
func jsonNumber(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var n json.Number
	if json.Unmarshal(raw, &n) != nil {
		return 0, false
	}
	f, err := n.Float64()
	if err != nil {
		return 0, false
	}
	return f, true
}

func jsonBool(raw json.RawMessage) (bool, bool) {
	if len(raw) == 0 {
		return false, false
	}
	var b bool
	if json.Unmarshal(raw, &b) != nil {
		return false, false
	}
	return b, true
}
