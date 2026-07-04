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

# Bump the VERSION file, then commit and annotate-tag (e.g. `just bump patch`).
# PART is major, minor, or patch. Requires a clean working tree.
bump PART:
    #!/usr/bin/env bash
    set -euo pipefail
    case "{{PART}}" in
      major|minor|patch) ;;
      *) echo "usage: just bump major|minor|patch" >&2; exit 1 ;;
    esac
    if [ -n "$(git status --porcelain)" ]; then
      echo "working tree not clean; commit or stash first" >&2
      exit 1
    fi
    IFS=. read -r major minor patch < VERSION
    case "{{PART}}" in
      major) major=$((major + 1)); minor=0; patch=0 ;;
      minor) minor=$((minor + 1)); patch=0 ;;
      patch) patch=$((patch + 1)) ;;
    esac
    new="${major}.${minor}.${patch}"
    echo "{{version}} -> ${new}"
    printf '%s\n' "${new}" > VERSION
    git commit -am "chore: bump version to ${new}"
    git tag -a "v${new}" -m "v${new}"
