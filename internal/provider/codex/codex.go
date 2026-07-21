// Package codex reads Codex usage without ever starting a Codex session.
//
// It prefers live numbers: `codex app-server` answers account/rateLimits/read
// with the account's current limits, the same call Codex's own UI makes, which
// spends no model quota. When that is unavailable — codex not installed, no
// network, a protocol that has moved on — it falls back to the rate-limit
// snapshots Codex writes into its session logs, walking ~/.codex/sessions
// newest-file-first for the most recent one.
//
// The two sources are not equivalent, and the fallback is the weaker of them: a
// snapshot only describes the moment it was written, and Codex re-anchors its
// weekly window rather than holding the reset it recorded, so an old snapshot
// can claim usage the account no longer has. finalize therefore ages the
// fallback and the renderer presents it as a ceiling. The live path needs none
// of that.
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
// 64 KB default, so lines are read at any length up to this cap; a line beyond
// it is skipped and the scan continues rather than aborting the file.
const maxLineBytes = 16 * 1024 * 1024

// tailWindowBytes bounds the fast path. Session logs are append-only and the
// newest snapshot sits near the end, so only this many trailing bytes are
// scanned first; a file whose tail holds no snapshot falls back to a full scan.
// This trades a rare full read for not reading hundreds of MB on every call —
// real session files reach hundreds of MB, and a front-to-back scan of the
// newest one was the whole reason `codex` took seconds to print.
const tailWindowBytes = 4 * 1024 * 1024

// snapshotMaxAge is how old a snapshot may be before its age is surfaced next
// to the windows it produced. A snapshot only describes current usage while
// nothing has been spent since it was written, and the session logs are not a
// complete record of that: usage from another machine, the Codex web app, or an
// editor extension never reaches this directory at all. Codex's short window is
// 5 hours, so beyond that an entire window could have come and gone unobserved.
// Below this the reading is treated as current and shown bare.
const snapshotMaxAge = 6 * time.Hour

// ErrNoSnapshot is returned when no session file yields a rate-limit snapshot.
var ErrNoSnapshot = errors.New("codex-usage: no Codex rate-limit snapshot found")

// Provider reports Codex usage, live when it can and from the session logs
// when it cannot. SessionsDir is injected so tests can point at a fixture tree,
// Now so staleness correction is deterministic, and Live so the app-server
// exchange can be faked without spawning a subprocess.
type Provider struct {
	SessionsDir string
	Now         func() time.Time // current time for staleness; nil means time.Now
	// Live returns the raw account/rateLimits/read result. nil means spawn a
	// real `codex app-server`.
	Live func() (json.RawMessage, error)
}

// Name is the combined-view header for this provider.
func (p *Provider) Name() string { return "Codex" }

func (p *Provider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Fetch reports the account's usage, preferring the live app-server reading and
// falling back to the newest session-log snapshot when it cannot be had. A live
// failure is not surfaced: it is the ordinary case offline or without codex
// installed, and the fallback still has something worth showing. Only when both
// sources fail does an error reach the caller, and it is the session-log error,
// which names a path the user can check.
func (p *Provider) Fetch() (usage.Result, error) {
	if res, err := p.fetchLive(); err == nil {
		return res, nil
	}
	return p.fetchFromSessions()
}

// fetchLive reads the account's current limits from the app-server. Live
// numbers need no staleness correction — nothing about them is remembered.
func (p *Provider) fetchLive() (usage.Result, error) {
	read := p.Live
	if read == nil {
		read = liveRateLimits
	}
	raw, err := read()
	if err != nil {
		return usage.Result{}, err
	}
	res, err := ParseLiveRateLimits(raw)
	if err != nil {
		return usage.Result{}, err
	}
	if len(res.Windows) == 0 {
		return usage.Result{}, ErrNoSnapshot
	}
	return res, nil
}

// fetchFromSessions finds the newest session file (by mtime) that carries a
// rate-limit snapshot and renders it. The first file yielding a snapshot wins.
func (p *Provider) fetchFromSessions() (usage.Result, error) {
	info, err := os.Stat(p.SessionsDir)
	if err != nil || !info.IsDir() {
		return usage.Result{}, fmt.Errorf("codex-usage: %s not found", p.SessionsDir)
	}

	paths, err := jsonlFilesByMtimeDesc(p.SessionsDir)
	if err != nil {
		return usage.Result{}, fmt.Errorf("codex-usage: %s not found", p.SessionsDir)
	}

	for _, path := range paths {
		raw, at, atOK, ok := snapshotFromFile(path, tailWindowBytes)
		if !ok {
			continue
		}
		res, err := ParseRateLimits(raw)
		if err != nil {
			return usage.Result{}, err
		}
		return finalize(res, at, atOK, p.now()), nil
	}
	return usage.Result{}, ErrNoSnapshot
}

// snapshotFromFile returns the latest rate-limit snapshot in one session file
// along with its event timestamp (at, valid only when atOK). It scans only the
// final tailWindow bytes first — the newest snapshot sits near the end of an
// append-only log — and falls back to a full scan when that tail holds no
// snapshot. Seeking mid-file lands inside a line whose truncated JSON simply
// fails to parse and is skipped, so the partial first line is harmless.
// tailWindow is a parameter so tests drive both paths cheaply.
func snapshotFromFile(path string, tailWindow int64) (raw json.RawMessage, at time.Time, atOK, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, false, false
	}
	defer f.Close()

	// Fast path: the newest snapshot sits near EOF, so scan only the tail.
	if info, err := f.Stat(); err == nil && info.Size() > tailWindow {
		if _, err := f.Seek(info.Size()-tailWindow, io.SeekStart); err == nil {
			if raw, at, atOK, ok := latestRateLimits(f); ok {
				return raw, at, atOK, true
			}
		}
	}
	// Fallback: rewind and scan the whole file.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, time.Time{}, false, false
	}
	return latestRateLimits(f)
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
// from the token_count event with the latest timestamp, plus that timestamp (at,
// valid only when atOK). Lines are read at any length up to maxLineBytes;
// malformed lines and lines beyond that cap are both skipped while the scan
// continues, so one bad line never aborts the file or discards snapshots found
// before or after it.
func latestRateLimits(r io.Reader) (raw json.RawMessage, at time.Time, atOK, ok bool) {
	return scanRateLimits(r, maxLineBytes)
}

// scanRateLimits is latestRateLimits with an injectable line cap so the
// oversized-line path can be exercised in tests without a multi-megabyte fixture.
func scanRateLimits(r io.Reader, maxLine int) (raw json.RawMessage, at time.Time, atOK, ok bool) {
	br := bufio.NewReaderSize(r, 64*1024)

	var current json.RawMessage
	var currentAt time.Time
	var haveCurrent, haveCurrentAt bool

	for {
		line, tooLong, err := readLine(br, maxLine)
		if len(line) > 0 && !tooLong {
			if rl, evAt, evAtOK, ok := parseEventLine(line); ok {
				// Take the first snapshot unconditionally, then replace only
				// with a strictly newer, parseable timestamp.
				if !haveCurrent || (evAtOK && (!haveCurrentAt || evAt.After(currentAt))) {
					current = rl
					haveCurrent = true
					currentAt, haveCurrentAt = evAt, evAtOK
				}
			}
		}
		if err != nil {
			break
		}
	}
	return current, currentAt, haveCurrentAt, haveCurrent
}

// parseEventLine extracts the rate_limits object and event timestamp from one
// jsonl line, reporting ok=false for anything that is not a token_count event
// carrying a rate_limits object.
func parseEventLine(line []byte) (rl json.RawMessage, at time.Time, atOK, ok bool) {
	var ev struct {
		Timestamp string          `json:"timestamp"`
		Payload   json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return nil, time.Time{}, false, false
	}
	var payload struct {
		Type       string          `json:"type"`
		RateLimits json.RawMessage `json:"rate_limits"`
	}
	if json.Unmarshal(ev.Payload, &payload) != nil {
		return nil, time.Time{}, false, false
	}
	if payload.Type != "token_count" || !isJSONObject(payload.RateLimits) {
		return nil, time.Time{}, false, false
	}
	at, atOK = parseTimestamp(ev.Timestamp)
	return payload.RateLimits, at, atOK, true
}

// readLine reads the next newline-delimited line of any length. A line whose
// length would exceed maxLine is consumed and discarded (tooLong = true) so the
// caller can skip it and keep scanning. err is io.EOF once the stream is done.
func readLine(br *bufio.Reader, maxLine int) (line []byte, tooLong bool, err error) {
	for {
		frag, e := br.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(frag) > maxLine {
				tooLong = true
				line = nil
			} else {
				line = append(line, frag...)
			}
		}
		if e == bufio.ErrBufferFull {
			continue
		}
		return line, tooLong, e
	}
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

// finalize turns a freshly parsed snapshot into what the renderer shows: it
// adjusts each window for elapsed resets, flags the result stale when the
// snapshot no longer describes any live window, and records the capture time
// whenever that age is material.
//
// The two conditions are separate on purpose. A stale result — every window has
// reset since capture — has all its windows zeroed by adjustForReset, so without
// the flag it would render as a confident flat 0%, indistinguishable from a
// fresh reading of genuine zero use. But a result can also be plainly out of
// date while a window is still live: a lone weekly window keeps a future reset
// for up to seven days, so an old snapshot would otherwise render exactly like a
// current one, countdown and all. Age is therefore carried past snapshotMaxAge
// too, so the renderer can show the percentages with their age attached instead
// of implying they were read just now.
//
// at is carried only when atOK; a stale snapshot whose timestamp did not parse
// is still flagged, just without an age.
func finalize(r usage.Result, at time.Time, atOK bool, now time.Time) usage.Result {
	stale := len(r.Windows) > 0 && allResetsPast(r.Windows, now)
	r = adjustForReset(r, now)
	if stale {
		r.Stale = true
	}
	if atOK && (stale || now.Sub(at) >= snapshotMaxAge) {
		r.AsOf = &at
	}
	return r
}

// allResetsPast reports whether every window has a known reset that is at or
// before now. A window with no known reset (nil) counts as not-past, so a
// snapshot still carrying any live or countdown-less window is never stale.
func allResetsPast(ws []usage.Window, now time.Time) bool {
	for i := range ws {
		if rs := ws[i].ResetsAt; rs == nil || rs.After(now) {
			return false
		}
	}
	return true
}

// adjustForReset corrects for the snapshot being a point-in-time record. A
// window whose reset has already passed (relative to now) has rolled over to a
// fresh window since the snapshot was written; because any newer usage would
// have produced a newer snapshot, that window is now empty — reported as 0%
// with no pending countdown. This is why a 5-hour window last seen at 100%
// reads 0% once its reset has passed, rather than showing a stale 100% with a
// "resets now" countdown.
//
// A window that has not reset keeps its recorded utilization, but that figure
// is only an upper bound, not a current reading: Codex's weekly limit is a
// rolling window whose usage ages out continuously rather than a bucket held
// until resets_at. Observed directly in the session logs — the weekly fell from
// 26% (Jul 11) to 15% (Jul 15) with its recorded reset of Jul 18 still ahead —
// and its anchor slides too, having moved Jul 16 → Jul 18 → Jul 22 across
// consecutive snapshots. finalize therefore marks anything past snapshotMaxAge
// with an AsOf so the renderer can present it as a ceiling.
func adjustForReset(r usage.Result, now time.Time) usage.Result {
	for i := range r.Windows {
		w := &r.Windows[i]
		if w.ResetsAt != nil && !w.ResetsAt.After(now) {
			w.Utilization = 0
			w.ResetsAt = nil
		}
	}
	return r
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
// set, otherwise the balance value when present and non-null. Codex encodes the
// balance as either a JSON number or a JSON string (e.g. "0"), so both are
// accepted.
func parseCredits(raw json.RawMessage) (usage.Extra, bool) {
	obj, ok := asObject(raw)
	if !ok {
		return usage.Extra{}, false
	}
	if unlimited, ok := jsonBool(obj["unlimited"]); ok && unlimited {
		return usage.Extra{Label: "Credits", Value: "unlimited"}, true
	}
	if v, ok := scalarString(obj["balance"]); ok {
		return usage.Extra{Label: "Credits", Value: v}, true
	}
	return usage.Extra{}, false
}

// scalarString renders a balance the way the reference `f"{balance}"` did: a
// JSON string yields its contents, a number its literal text (so 100.0 stays
// "100.0"). null, absent, and non-scalar values report not-ok.
func scalarString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil && n != "" {
		return n.String(), true
	}
	return "", false
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
