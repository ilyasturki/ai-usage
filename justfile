# Version injected into the binary, read from the single-source-of-truth VERSION
# file (matches what flake.nix injects).
version := trim(`cat VERSION`)
ldflags := "-X ai-usage/internal/version.Version=" + version

# Compile the binary (standard library only)
build:
    go build -ldflags "{{ldflags}}" -o ai-usage .

# Run from source, passing args through (e.g. `just run claude`)
run *ARGS:
    go run -ldflags "{{ldflags}}" . {{ARGS}}

# Run the compiled binary, building it first if needed
exec *ARGS: build
    ./ai-usage {{ARGS}}

# Run the test suite
tests:
    go test ./...

# Build with Nix -> ./result/bin/ai-usage
nix-build:
    nix build

# Run via Nix, passing args through (e.g. `just nix-run claude`)
nix-run *ARGS:
    nix run . -- {{ARGS}}
