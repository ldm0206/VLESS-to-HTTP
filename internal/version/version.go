// Package version carries build metadata injected at link time.
package version

import (
	"runtime"
	"strings"

	"github.com/xtls/xray-core/core"
)

// Set with -ldflags "-X .../internal/version.Version=...".
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Xray is the embedded core version.
func Xray() string { return core.Version() }

// Full renders the build identifier. CI publishes every commit with the short
// hash as the version, so "abc1234 (abc1234def…)" would just repeat itself.
func Full() string {
	if Commit == "none" || Commit == "" {
		return Version
	}
	if Version != "" && strings.HasPrefix(Commit, Version) {
		return Version
	}
	return Version + " (" + Commit + ")"
}

// GoRuntime is the Go version the binary was built with.
func GoRuntime() string { return runtime.Version() }
