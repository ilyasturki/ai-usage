## Problem Statement

I check my Claude and Codex subscription usage from the terminal with a command (`ai-usage`, aliased `cu`) that wraps `claude-usage` and `codex-usage`. Today these are fish functions with ~100 lines of Python embedded inside Nix heredoc strings in my home-manager config. That embedding is the pain: the Python can't be linted, type-checked, syntax-highlighted, or unit-tested; it's escaped inside a `.nix` file, split across two languages (fish + Python), and run as a `curl | jq | python3` pipeline. Adding a provider, or fixing the parsing when an upstream response shape shifts, means editing stringly-typed code with no safety net. I want a real, owned tool I can test and extend — not a script trapped in my Nix config.

## Solution

Replace the three fish functions with a single standalone **Go** CLI — `ai-usage` — that I own in its own repository and consume in my NixOS config as a flake input (the same pattern as `nosleep`). It reproduces today's exact behavior — Claude and Codex usage windows rendered as labeled 20-character bars with percentages and reset countdowns — but as typed, tested Go: one process instead of a `curl | jq | python3` pipeline, parsing behind a `Provider` interface so a third provider is a new file rather than a new heredoc, and a test suite that pins the rendered output. When the Claude OAuth token is stale (401), it prints a clear "open Claude once to refresh" message instead of dumping a raw response. The fish abbreviations (`cu`, `ccu`, `cxu`) keep working.

## User Stories

1. As a subscription user, I want a single `ai-usage` command that shows both my Claude and Codex usage, so that I can check all my AI rate limits in one place.
2. As a subscription user, I want my Claude 5-hour window shown, so that I know how close I am to the short-term limit before a big session.
3. As a subscription user, I want my Claude weekly (all-models) window shown, so that I can pace my week.
4. As a subscription user, I want my Claude weekly Opus window shown, so that I can tell how much Opus budget remains.
5. As a subscription user, I want my Claude weekly Sonnet window shown, so that I can tell how much Sonnet budget remains.
6. As a subscription user, I want each window rendered as a labeled 20-character bar with a percentage, so that I can read utilization at a glance.
7. As a subscription user, I want a "resets in Xh YYm" countdown next to each window, so that I know when my budget refreshes.
8. As a subscription user, I want windows with no data (null utilization) silently omitted, so that the output only shows what's real.
9. As a subscription user, I want my Codex primary (5-hour) window shown, so that I can see my short-term Codex limit.
10. As a subscription user, I want my Codex secondary (weekly) window shown, so that I can see my longer Codex limit.
11. As a subscription user, I want Codex window labels derived from the window length, so that labels stay correct if Codex changes its windows.
12. As a subscription user, I want my Codex credit balance (or "unlimited") shown when present, so that I know whether I'm on credits.
13. As a subscription user, I want Codex usage read from my most recent local session logs without starting a new Codex session, so that checking usage doesn't itself consume a session.
14. As a subscription user, I want the latest rate-limit snapshot chosen across all my session files, so that the numbers reflect my most recent activity.
15. As a subscription user, I want Claude usage read by reusing the OAuth token Claude Code already stores, so that I don't authenticate separately.
16. As a subscription user, I want a clear "open Claude once to refresh" message when my token is expired (401), so that I know exactly how to fix it.
17. As a subscription user, I want a clear message when my credentials file is missing, so that I understand why Claude usage can't be shown.
18. As a subscription user, I want a clear message when no Codex snapshot exists yet, so that I understand why Codex usage is empty.
19. As a subscription user, I want one provider failing to not blank out the other, so that I still see Claude usage when Codex is unavailable and vice versa.
20. As a subscription user, I want to run just the Claude half (`ccu`) or just the Codex half (`cxu`), so that I can check one provider quickly.
21. As a subscription user, I want my existing `cu`/`ccu`/`cxu` abbreviations to keep working, so that the change is invisible to my muscle memory.
22. As a subscription user, I want the combined output grouped under "Claude" and "Codex" headers with indented bars, so that it reads the same as it does today.
23. As a subscription user, I want a non-zero exit code when usage can't be fetched, so that I can script around failures.
24. As a developer, I want each provider behind a `Provider` interface, so that adding a new agent (opencode, antigravity, …) is a new file, not a rewrite.
25. As a developer, I want each provider's response-to-windows parsing exposed as a pure function, so that I can unit-test the squishy JSON parsing directly and fast.
26. As a developer, I want the program driven from a composition root with injectable dependencies (HTTP, sessions dir, clock, credentials path, output), so that I can test end-to-end behavior by asserting on rendered output.
27. As a developer, I want a fixed/injectable clock, so that reset-countdown output is deterministic in tests.
28. As a developer, I want oversized and malformed session-log lines skipped rather than crashing the scan, so that one bad line doesn't break Codex usage.
29. As a developer, I want a single typed binary with no curl/jq/python runtime dependency, so that there are fewer moving parts to break.
30. As a developer, I want parsing to tolerate missing/optional fields without panicking, so that partial upstream responses degrade gracefully.
31. As a NixOS user, I want the tool packaged with `buildGoModule` in its own flake, so that it builds reproducibly.
32. As a NixOS user, I want to consume it as a flake input in nix-config (like `nosleep`), so that it integrates with my existing setup.
33. As a NixOS user, I want the old inline-Python fish functions removed once the binary is wired in, so that there's a single source of truth.
34. As a developer, I want a test suite covering the healthy, expired-token, missing-credentials, no-snapshot, multi-file-latest, and malformed-line cases, so that behavior is pinned against regressions.

## Implementation Decisions

- **Standalone Go module in its own repository**, packaged with `buildGoModule`. The implementation is expected to be pure standard library (`net/http`, `encoding/json`, `bufio`, `path/filepath`, `time`), so `vendorHash = null` should hold. nix-config consumes it as a flake input (the `nosleep` precedent) and references the binary from the home-manager Claude/Codex config; the existing `claude-usage`/`codex-usage`/`ai-usage` fish functions are removed and the `cu`/`ccu`/`cxu` abbreviations repoint to the binary.
- **`Provider` abstraction:** each provider yields a list of usage windows (label, utilization percent, optional reset time) plus optional extra lines (e.g. Codex credits). The CLI orchestrates providers and renders uniformly. Two providers ship: Claude and Codex.
- **Claude provider — API contract:** read the OAuth access token from the Claude Code credentials file (`claudeAiOauth.accessToken` in `~/.claude/.credentials.json`); issue `GET https://api.anthropic.com/api/oauth/usage` with headers `Authorization: Bearer <token>` and `anthropic-beta: oauth-2025-04-20`. Response fields consumed: `five_hour`, `seven_day`, `seven_day_opus`, `seven_day_sonnet`, each `{utilization: number, resets_at: ISO-8601}`. Windows with null utilization are omitted. A 401 (or a response missing `five_hour`) is treated as an expired token and yields the "open Claude once to refresh" message. Base URL and HTTP client are injectable.
- **Codex provider — data contract:** walk the Codex sessions directory (`~/.codex/sessions`) for `*.jsonl`, newest file by mtime first; within each, find `token_count`-type events carrying a `rate_limits` object and keep the one with the latest event timestamp; the first file yielding a snapshot wins. From `rate_limits`, render `primary` and `secondary` windows (`used_percent`, `resets_at` as epoch seconds, `window_minutes`) and an optional `credits` line (`unlimited` or `balance`). Window labels derive from `window_minutes`: 300 → "5-hour", 10080 → "Weekly", whole hours → "N-hour", else "Nm". Sessions directory path is injectable.
- **Rendering:** each window renders as `{label padded} [{20-char bar}] {percent}%{ optional "  resets in Xh YYm"}`, the bar filling one block per 5% (clamped 0–20). This reproduces today's output. The combined view prints a "Claude" header then indented Claude windows, a blank line, a "Codex" header then indented Codex windows. The binary reproduces three behaviors — combined (default), Claude-only, Codex-only — to which `cu`/`ccu`/`cxu` map.
- **Token refresh is explicitly not performed:** the tool reads the on-disk token and reports a clear refresh instruction on 401; it does not replicate Claude's OAuth refresh flow.
- **Composition root / dependency injection:** the program is constructed from a dependency set — HTTP client/base URL, Codex sessions directory, clock (`now`), credentials path, output writer — wired in `main` and substituted in tests.
- **Session-log scanning** sets an enlarged read buffer so long jsonl lines (which exceed the 64 KB scanner default) are read rather than erroring; malformed lines are skipped.

## Testing Decisions

- **What makes a good test here:** assert on externally-observable behavior — the exact rendered output (and exit status) for a given set of inputs — never on internal function calls or struct layout. Tests feed canned external data and check what the user would see.
- **Two seams (by decision):**
  1. **Composition-root seam (end-to-end):** construct the app with fake dependencies — an `httptest.Server` serving canned Claude JSON, a temp directory of fixture `.jsonl` files for Codex, a fixed clock, a fixture/missing credentials file, and a buffer for stdout — then assert on rendered bytes. Covers parsing, label/window logic, reset math, rendering, multi-provider orchestration, and the indented combined view, all through real code.
  2. **Parser unit seam:** each provider's "raw bytes/events → usage windows" parsing is a pure function tested directly, for fast focused coverage of the squishy/optional JSON (missing fields, null utilization, the Codex window-label mapping, choosing the latest snapshot across files).
- **Scenarios to pin:** healthy Claude response; 401/expired token → refresh message; missing credentials → clean error; Codex picking the latest `token_count` across several files; no Codex snapshot → clean message; malformed and oversized jsonl lines skipped without crashing; Codex credits (unlimited and balance); one provider failing while the other renders; the combined indented output.
- **Modules tested:** both providers (via the parser seam) and the whole CLI (via the composition-root seam).
- **Prior art:** no existing Go tests; the patterns are idiomatic Go — `httptest.Server` for the HTTP edge, `t.TempDir()` + fixture files for the filesystem edge, an injected clock for time, `bytes.Buffer` for captured output — organized as table-driven tests.

## Out of Scope

- Token refresh / replicating Claude's OAuth refresh flow.
- Providers beyond Claude and Codex (opencode, antigravity, pi, forgecode, t3code) — the `Provider` interface leaves the door open, but none ship now.
- A live/watch TUI, a `--json` machine-readable mode, historical tracking/sparklines, and threshold notifications — all deferred; one-shot text output is the whole surface for now.
- Cross-shell packaging beyond the existing fish abbreviations.
- Any change to how Claude Code or Codex store usage data; this tool only reads existing on-disk artifacts and the existing endpoint.
- Forking or contributing to `caut` — a from-scratch tool is the chosen path (see Further Notes).

## Further Notes

- This PRD is filed on the `nix-config` tracker because the standalone tool's repository does not exist yet. nix-config's role is the flake wiring, the home-manager reference, and removal of the old fish functions; the Go source lives in the new repo.
- The driver is maintainability, ownership, and extensibility — **not** immunity to upstream breakage. The Claude endpoint is undocumented and has shifted between Claude Code releases, and the Codex log shape is an internal format; both can change and would break any client regardless of language. The tool mitigates this with clear error messages and a fast edit-test cycle, not by pretending the contracts are stable. The real robustness gains: collapsing `curl | jq | python3` into one typed process, optional-tolerant parsing, and the test suite.
- `caut` (`Dicklesworthstone/coding_agent_usage_tracker`) is the closest existing tool and was evaluated and removed previously: its Codex fetcher probes non-existent CLI subcommands, whereas this tool reads Codex session logs (the approach that works). A from-scratch Go tool was chosen over forking/PRing caut deliberately, to avoid an upstream-merge dependency.
- Output parity with today's script is a goal: the bar format, labels, and indented combined view should match.
