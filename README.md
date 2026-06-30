# ai-usage

Check your Claude and Codex subscription usage from the terminal — one command,
no new session, no `curl | jq | python3` pipeline.

```
$ ai-usage
Claude
  5-hour         [##------------------]  14.0%  resets in 1h48m
  Weekly         [#########-----------]  49.0%  resets in 1h58m
  Weekly Sonnet  [--------------------]   0.0%

Codex
  5-hour         [####################] 100.0%  resets in 0h00m
  Weekly         [########------------]  42.0%  resets in 0h00m
  Credits        unlimited
```

Each window is a 20-character bar (one block per 5%) with utilization and a
reset countdown. Windows with no data are omitted.

## Usage

```
ai-usage          combined Claude + Codex view (default)
ai-usage claude   Claude usage only
ai-usage codex    Codex usage only
ai-usage -v       print the version (also --version)
```

Exit status is non-zero when the usage you asked for couldn't be fetched
(combined mode is non-zero only when *both* providers fail), so it scripts
cleanly.

## How it reads usage

- **Claude** reuses the OAuth token Claude Code stores in
  `~/.claude/.credentials.json` and calls the same `oauth/usage` endpoint the
  `/usage` command hits. It never refreshes the token: when it's stale (401)
  you get a clear "open Claude once to refresh" message.
- **Codex** reads the rate-limit snapshots Codex already writes to its session
  logs under `~/.codex/sessions` (honoring `$CODEX_HOME`), newest file first —
  so checking usage doesn't start a session of its own.

Both upstreams are undocumented and have shifted between releases; this tool
trades clear errors and a fast test loop for that, not immunity.

## Build

```
go build -o ai-usage .   # standard library only
go test ./...
```

### Nix

```
nix build              # ./result/bin/ai-usage
nix run . -- claude
```

Packaged with `buildGoModule` (`vendorHash = null`). Consume it as a flake
input the way the rest of the config consumes its tools:

```nix
inputs.ai-usage.url = "github:ilyasturki/ai-usage";
# ...
home.packages = [ inputs.ai-usage.packages.${system}.default ];
```

## Adding a provider

A provider yields usage windows (label, utilization, optional reset) plus
optional extra lines, behind one interface in
[`internal/usage`](internal/usage/usage.go). Its raw-response → windows parsing
is a pure, separately-tested function. A third agent (opencode, antigravity, …)
is a new file under `internal/provider/`, not a rewrite.

## License

MIT
