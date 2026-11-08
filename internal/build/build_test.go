package build

import (
	"bytes"
	"testing"

	"github.com/pantalan/pantalan/internal/service"
)

func TestANixpacksBuildOnANodeWithoutNixpacksFailsSayingSo(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // the installer never installs nixpacks
	var logs bytes.Buffer

	err := Nixpacks{}.Build(t.Context(), service.BuildRequest{Dir: t.TempDir(), Image: "app:1", Logs: &logs})

	if err == nil || err.Error() != "nixpacks not installed on this Node" {
		t.Errorf("Build = %v, want %q", err, "nixpacks not installed on this Node")
	}
	if got := logs.String(); got != "nixpacks not installed on this Node\n" {
		t.Errorf("build log = %q, want the same message", got)
	}
}
