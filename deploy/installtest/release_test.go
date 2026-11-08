//go:build installtest

package installtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// A fake release is laid out like a GitHub release download URL:
// <origin>/<tag>/<asset>, with the assets release.yml publishes
// (pantalan_<tag>_linux_<arch> for both arches, the baked install.sh and
// SHA256SUMS over all three). The installer is pointed at an origin with
// PANTALAN_RELEASE_BASE_URL and runs its real download and checksum path.
//
// Origins served by the release server, one directory each under its root:
const (
	goodOrigin    = "good"    // the release as built
	corruptOrigin = "corrupt" // the Node's binary altered after SHA256SUMS was written
)

// testTag is the version the release under test is built as. Go stamps it
// into the binary from the tag on a clean checkout, the way release.yml's
// builds get theirs.
const testTag = "v0.0.1"

var arches = []string{"amd64", "arm64"}

// nodeArch is the architecture of the Nodes: containers run natively.
var nodeArch = runtime.GOARCH

// releaseServer serves the fake release origins over plain HTTP.
type releaseServer struct {
	root string
	addr string // host:port, as Nodes reach it
	srv  *http.Server
}

// baseURL is what PANTALAN_RELEASE_BASE_URL is set to for origin.
func (s *releaseServer) baseURL(origin string) string {
	return "http://" + s.addr + "/" + origin
}

// startReleaseServer prepares every origin under root and serves it on
// listenIP, the address Nodes reach the test machine on.
//
// The release is built from the working tree, uncommitted changes
// included, unless INSTALLTEST_RELEASE_DIR names a directory that already
// holds the assets for INSTALLTEST_RELEASE_TAG (the VM tier and the release
// gate test the exact artifacts release.yml built).
func startReleaseServer(root, listenIP string) (*releaseServer, string, error) {
	tag := testTag
	good := filepath.Join(root, goodOrigin, tag)
	if dir := os.Getenv("INSTALLTEST_RELEASE_DIR"); dir != "" {
		tag = os.Getenv("INSTALLTEST_RELEASE_TAG")
		if tag == "" {
			return nil, "", errors.New("INSTALLTEST_RELEASE_DIR needs INSTALLTEST_RELEASE_TAG")
		}
		good = filepath.Join(root, goodOrigin, tag)
		if err := copyDir(dir, good); err != nil {
			return nil, "", err
		}
	} else if err := buildRelease(filepath.Join(root, "src"), good, tag); err != nil {
		return nil, "", err
	}
	if err := corruptRelease(good, filepath.Join(root, corruptOrigin, tag), tag); err != nil {
		return nil, "", err
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(listenIP, "0"))
	if err != nil {
		return nil, "", fmt.Errorf("release server: %w", err)
	}
	s := &releaseServer{
		root: root,
		addr: ln.Addr().String(),
		srv:  &http.Server{Handler: http.FileServer(http.Dir(root)), ReadHeaderTimeout: 10 * time.Second},
	}
	go s.srv.Serve(ln)
	return s, tag, nil
}

func (s *releaseServer) Close() { s.srv.Close() }

// buildRelease builds the release assets for tag into dir from a copy of the
// working tree committed and tagged in a scratch repo under src.
func buildRelease(src, dir, tag string) error {
	repo, err := repoRoot()
	if err != nil {
		return err
	}
	if err := copyWorkingTree(repo, src); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	git := func(args ...string) error {
		_, err := run(ctx, src, nil, "git", append([]string{
			"-c", "user.name=installtest", "-c", "user.email=installtest@invalid",
			"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false",
		}, args...)...)
		return err
	}
	for _, args := range [][]string{
		{"init", "--quiet"}, {"add", "--all"}, {"commit", "--quiet", "--message", "release " + tag}, {"tag", tag},
	} {
		if err := git(args...); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var assets []string
	for _, arch := range arches {
		asset := "pantalan_" + tag + "_linux_" + arch
		_, err := run(ctx, src, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=" + arch},
			"go", "build", "-trimpath", "-ldflags=-s -w", "-o", filepath.Join(dir, asset), "./cmd/pantalan")
		if err != nil {
			return err
		}
		assets = append(assets, asset)
	}
	// The same check release.yml makes: the binary knows its own tag.
	if runtime.GOOS == "linux" {
		out, err := run(ctx, src, nil, filepath.Join(dir, "pantalan_"+tag+"_linux_"+runtime.GOARCH), "version")
		if err != nil {
			return err
		}
		if got := strings.TrimSpace(out); got != tag {
			return fmt.Errorf("the release binary says its version is %q, want %q (does git see a clean tagged checkout?)", got, tag)
		}
	}
	script, err := os.ReadFile(filepath.Join(src, "deploy", "install.sh"))
	if err != nil {
		return err
	}
	if n := bytes.Count(script, []byte("@VERSION@")); n != 1 {
		return fmt.Errorf("install.sh has %d @VERSION@ placeholders, want exactly 1", n)
	}
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), bytes.ReplaceAll(script, []byte("@VERSION@"), []byte(tag)), 0o644); err != nil {
		return err
	}
	return writeSums(dir, append(assets, "install.sh"))
}

// corruptRelease copies the release at good to dir, then alters the Node's
// binary without updating SHA256SUMS, as a truncated or tampered download
// would look.
func corruptRelease(good, dir, tag string) error {
	if err := copyDir(good, dir); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "pantalan_"+tag+"_linux_"+nodeArch), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	if _, err := f.WriteString("tampered"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// writeSums writes SHA256SUMS in sha256sum's own format, bare file names.
func writeSums(dir string, names []string) error {
	slices.Sort(names)
	var b strings.Builder
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(content)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	return os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(b.String()), 0o644)
}

// repoRoot is the root of the repository the harness lives in.
func repoRoot() (string, error) {
	return filepath.Abs(filepath.Join("..", ".."))
}

// copyWorkingTree copies the files git would see (tracked plus untracked,
// minus ignored, as they are on disk) from repo to dst.
func copyWorkingTree(repo, dst string) error {
	out, err := run(context.Background(), repo, nil, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return err
	}
	for _, name := range strings.Split(strings.TrimRight(out, "\x00"), "\x00") {
		from := filepath.Join(repo, name)
		info, err := os.Lstat(from)
		if errors.Is(err, os.ErrNotExist) {
			continue // deleted in the working tree
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if err := copyFile(from, filepath.Join(dst, name), info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func copyDir(from, to string) error {
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(from, e.Name()), filepath.Join(to, e.Name()), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(from, to string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// run runs a command in dir with extra environment and returns its stdout.
func run(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
