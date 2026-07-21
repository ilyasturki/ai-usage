package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"ai-usage/internal/usage"
	"ai-usage/internal/version"
)

// liveTimeout bounds the whole app-server exchange — spawn, handshake, and
// reply. The call takes about a second in practice, almost all of it the
// server's own startup; past this the reading is not worth the wait and Fetch
// falls back to the session logs.
const liveTimeout = 5 * time.Second

// liveRequestID is the id of the rate-limit request. Responses are matched on
// it because the server may interleave notifications with replies.
const liveRequestID = 2

// errNoLiveReply reports that the app-server exited or timed out without
// answering the rate-limit request.
var errNoLiveReply = errors.New("codex-usage: app-server gave no rate-limit reply")

// liveRateLimits asks a freshly spawned `codex app-server` for the account's
// current rate limits over its stdio JSON-RPC protocol, returning the raw
// result object. This is the same call Codex's own UI makes: it reads the
// account's live limits and starts no session, so it neither creates a rollout
// nor spends model quota.
//
// The server is spawned per call rather than kept alive. It costs about a
// second, and a long-lived child would outlive a CLI that prints one line and
// exits.
func liveRateLimits() (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), liveTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "codex", "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("codex-usage: cannot run codex app-server: %w", err)
	}
	// Kill the server on every exit path: a reply that arrives after the
	// timeout, a parse failure, or a clean read all leave it running otherwise.
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	for _, req := range []any{
		rpcRequest{Version: "2.0", ID: 1, Method: "initialize", Params: initializeParams{
			ClientInfo: clientInfo{Name: "ai-usage", Version: version.Version},
		}},
		rpcRequest{Version: "2.0", Method: "initialized"},
		rpcRequest{Version: "2.0", ID: liveRequestID, Method: "account/rateLimits/read"},
	} {
		line, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		if _, err := stdin.Write(append(line, '\n')); err != nil {
			return nil, err
		}
	}
	return readLiveReply(stdout)
}

// readLiveReply scans newline-delimited JSON-RPC messages for the reply to the
// rate-limit request, skipping the notifications and the initialize reply that
// precede it. Unparseable lines are skipped rather than fatal, so a future
// server that emits something unexpected does not break the read.
func readLiveReply(r io.Reader) (json.RawMessage, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for sc.Scan() {
		var msg struct {
			ID     *int64          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil || msg.ID == nil || *msg.ID != liveRequestID {
			continue
		}
		if msg.Error != nil {
			return nil, fmt.Errorf("codex-usage: app-server: %s", msg.Error.Message)
		}
		return msg.Result, nil
	}
	return nil, errNoLiveReply
}

type rpcRequest struct {
	Version string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"` // omitted on notifications
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type initializeParams struct {
	ClientInfo clientInfo `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ParseLiveRateLimits turns an account/rateLimits/read result into windows plus
// credit lines. It mirrors ParseRateLimits but reads the app-server's own
// spelling — camelCase keys, and usedPercent as an integer — rather than the
// snake_case the session logs record. Like its sibling it is pure and tolerant:
// every field is optional and a wrong type is treated as absent.
//
// The "rateLimits" field is the backward-compatible single-bucket view, which
// matches what this tool displays. Its per-bucket sibling "rateLimitsByLimitId"
// is deliberately ignored for now; it carries the same numbers for a single
// metered limit, and rendering several buckets is a display question this has
// no answer for yet.
func ParseLiveRateLimits(raw json.RawMessage) (usage.Result, error) {
	obj, ok := asObject(raw)
	if !ok {
		return usage.Result{}, ErrNoSnapshot
	}
	snap, ok := asObject(obj["rateLimits"])
	if !ok {
		return usage.Result{}, ErrNoSnapshot
	}

	var windows []usage.Window
	if w, ok := parseLiveWindow(snap["primary"], "Primary"); ok {
		windows = append(windows, w)
	}
	if w, ok := parseLiveWindow(snap["secondary"], "Weekly"); ok {
		windows = append(windows, w)
	}

	var extras []usage.Extra
	if e, ok := parseLiveCredits(snap["credits"]); ok {
		extras = append(extras, e)
	}
	if e, ok := parseResetCredits(obj["rateLimitResetCredits"]); ok {
		extras = append(extras, e)
	}
	return usage.Result{Windows: windows, Extras: extras}, nil
}

// parseLiveWindow reads one window from the app-server's shape: usedPercent and
// windowDurationMins where the session logs say used_percent and window_minutes.
func parseLiveWindow(raw json.RawMessage, fallback string) (usage.Window, bool) {
	obj, ok := asObject(raw)
	if !ok {
		return usage.Window{}, false
	}
	used, ok := jsonNumber(obj["usedPercent"])
	if !ok {
		return usage.Window{}, false
	}
	w := usage.Window{Label: windowLabel(obj["windowDurationMins"], fallback), Utilization: used}
	if reset, ok := jsonNumber(obj["resetsAt"]); ok && reset != 0 {
		t := time.Unix(int64(reset), 0).UTC()
		w.ResetsAt = &t
	}
	return w, true
}

// parseLiveCredits reads the credit balance, which the app-server spells with
// camelCase keys and always returns as a string.
func parseLiveCredits(raw json.RawMessage) (usage.Extra, bool) {
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

// parseResetCredits summarizes the one-off "full reset" grants the account is
// holding — a budget the session logs never reported at all. A zero count is
// omitted rather than shown, so the line appears only when there is something
// to spend.
func parseResetCredits(raw json.RawMessage) (usage.Extra, bool) {
	obj, ok := asObject(raw)
	if !ok {
		return usage.Extra{}, false
	}
	n, ok := jsonNumber(obj["availableCount"])
	if !ok || n < 1 {
		return usage.Extra{}, false
	}
	unit := "resets"
	if n == 1 {
		unit = "reset"
	}
	return usage.Extra{Label: "Reset credits", Value: fmt.Sprintf("%d %s available", int64(n), unit)}, true
}
