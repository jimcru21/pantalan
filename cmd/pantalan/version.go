package main

import (
	"context"
	"fmt"
	"runtime/debug"
)

// binary describes the running binary, read from the Go toolchain's build info
// rather than ldflags, so any `go build` of a tagged checkout reports the tag.
type binary struct {
	Version   string // module version: v1.2.3 for a release, (devel) for `go run`
	Revision  string // VCS commit, empty when the binary carries no VCS stamp
	GoVersion string
}

func buildInfo() binary {
	b := binary{Version: "(devel)"}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return b
	}
	if info.Main.Version != "" {
		b.Version = info.Main.Version
	}
	b.GoVersion = info.GoVersion
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			b.Revision = s.Value
		}
	}
	return b
}

func versionCmd(_ context.Context, inv invocation) int {
	fmt.Fprintln(inv.stdio.Out, buildInfo().Version)
	return 0
}
