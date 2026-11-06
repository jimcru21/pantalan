// Package execx runs external tools (git, docker, nixpacks) and returns errors
// that include the tool's own stderr, which is where the useful message is.
package execx

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Output runs cmd and returns its trimmed stdout.
func Output(cmd *exec.Cmd) (string, error) {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// Stream runs cmd with stdout and stderr written to logs.
func Stream(cmd *exec.Cmd, logs io.Writer) error {
	cmd.Stdout = logs
	cmd.Stderr = logs
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(cmd.Args, " "), err)
	}
	return nil
}
