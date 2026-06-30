// Package version exposes the build version string. The repo-root VERSION file
// is the single source of truth: flake.nix and the justfile both read it and
// inject it here at link time with
//
//	-ldflags "-X ai-usage/internal/version.Version=<v>"
//
// Plain `go build`/`go run` (no ldflags) leave the "dev" fallback in place.
package version

// Version is the build version, overridden at link time. Defaults to "dev".
var Version = "dev"
