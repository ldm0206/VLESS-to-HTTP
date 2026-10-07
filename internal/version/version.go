// Package version carries build metadata injected at link time.
package version

import (
	"runtime"

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

// Full renders "v1.2.3 (abc1234)".
func Full() string {
	if Commit == "none" || Commit == "" {
		return Version
	}
	return Version + " (" + Commit + ")"
}

// GoRuntime is the Go version the binary was built with.
func GoRuntime() string { return runtime.Version() }
