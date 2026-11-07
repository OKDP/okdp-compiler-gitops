// Package buildinfo names the running program and its version, for the
// co-author trailer of the commits a user authors (gitops.Program).
package buildinfo

import (
	"path"
	"runtime/debug"
	"strings"
)

// Version is set at build time:
//
//	go build -ldflags "-X github.com/okdp/okdp-gitops/internal/buildinfo.Version=0.1.0"
//
// Empty, CurrentVersion falls back to the version Go records for the main
// module, then to "dev".
var Version = ""

// readBuildInfo is debug.ReadBuildInfo, replaced by the tests.
var readBuildInfo = debug.ReadBuildInfo

// Name returns the program name: the last element of the main module path
// ("okdp-gitops"), or fallback when the binary carries no
// build information.
func Name(fallback string) string {
	if info, ok := readBuildInfo(); ok && info != nil && info.Main.Path != "" {
		return path.Base(info.Main.Path)
	}
	return fallback
}

// CurrentVersion returns the version without a leading "v": the one set by
// the linker, else the main module version Go records (unless it is the
// "(devel)" placeholder), else "dev".
func CurrentVersion() string {
	if v := strings.TrimPrefix(strings.TrimSpace(Version), "v"); v != "" {
		return v
	}
	if info, ok := readBuildInfo(); ok && info != nil {
		if v := strings.TrimPrefix(info.Main.Version, "v"); v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}
