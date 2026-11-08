package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"
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

// releaseVersion matches the only tags releases are cut from: vX.Y.Z.
var releaseVersion = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// assetVersion is the path segment the Dashboard's static files are served
// under and cached forever by: the release version, else the short VCS
// revision (dev builds and pseudo-versions), else, for binaries with no
// VCS stamp such as `go run` and tests, a digest of the files themselves.
func assetVersion(b binary, static fs.FS) string {
	if releaseVersion.MatchString(b.Version) {
		return b.Version
	}
	if b.Revision != "" {
		return b.Revision[:min(12, len(b.Revision))]
	}
	h := sha256.New()
	fs.WalkDir(static, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, err := fs.ReadFile(static, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", path, len(content))
		h.Write(content)
		return nil
	})
	return "dev-" + hex.EncodeToString(h.Sum(nil))[:12]
}

func versionCmd(_ context.Context, inv invocation) int {
	fmt.Fprintln(inv.stdio.Out, buildInfo().Version)
	return 0
}
