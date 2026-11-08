//go:build installtest

package installtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// publishedReleases is where the real releases are downloaded from, the
// installer's default base URL.
const publishedReleases = "https://github.com/jimcru21/pantalan/releases/download"

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
// listenIP, the address Nodes reach the test machine on. It returns the
// tags the good origin serves, oldest first.
//
// The working tree, uncommitted changes included, is built as the releases
// tags (N and N+1, see fakeReleaseTags), the second tagged on an empty
// commit after the first, so upgrades and downgrades can be tested; Go
// stamps each tag into its binary from the tag on a clean checkout, the way
// release.yml's builds get theirs. Unless INSTALLTEST_RELEASE_DIR names a
// directory that already holds the assets for INSTALLTEST_RELEASE_TAG (the
// release gate tests the exact artifacts release.yml built), which is then
// the only release served.
func startReleaseServer(root, listenIP string, tags []string) (*releaseServer, []string, error) {
	if dir := os.Getenv("INSTALLTEST_RELEASE_DIR"); dir != "" {
		tags = []string{os.Getenv("INSTALLTEST_RELEASE_TAG")}
		if tags[0] == "" {
			return nil, nil, errors.New("INSTALLTEST_RELEASE_DIR needs INSTALLTEST_RELEASE_TAG")
		}
		if err := copyDir(dir, filepath.Join(root, goodOrigin, tags[0])); err != nil {
			return nil, nil, err
		}
	} else if err := buildReleases(filepath.Join(root, "src"), filepath.Join(root, goodOrigin), tags); err != nil {
		return nil, nil, err
	}
	good := filepath.Join(root, goodOrigin, tags[0])
	if err := corruptRelease(good, filepath.Join(root, corruptOrigin, tags[0]), tags[0]); err != nil {
		return nil, nil, err
	}

	ln, err := net.Listen("tcp", net.JoinHostPort(listenIP, "0"))
	if err != nil {
		return nil, nil, fmt.Errorf("release server: %w", err)
	}
	s := &releaseServer{
		root: root,
		addr: ln.Addr().String(),
		srv:  &http.Server{Handler: http.FileServer(http.Dir(root)), ReadHeaderTimeout: 10 * time.Second},
	}
	go s.srv.Serve(ln)
	return s, tags, nil
}

func (s *releaseServer) Close() { s.srv.Close() }

// buildReleases builds the release assets for each of tags into dir/<tag>
// from a copy of the working tree committed in a scratch repo under src,
// with deploy/release.sh. Each tag goes on an empty commit after the
// previous one's, so the releases differ only in their version.
func buildReleases(src, dir string, tags []string) error {
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
	if err := git("init", "--quiet"); err != nil {
		return err
	}
	if err := git("add", "--all"); err != nil {
		return err
	}
	for _, tag := range tags {
		for _, args := range [][]string{
			{"commit", "--quiet", "--allow-empty", "--message", "release " + tag}, {"tag", tag},
		} {
			if err := git(args...); err != nil {
				return err
			}
		}
		// The script release.yml builds the real release with.
		if _, err := run(ctx, src, nil, "sh", "deploy/release.sh", "build", tag, filepath.Join(dir, tag)); err != nil {
			return err
		}
	}
	return nil
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
