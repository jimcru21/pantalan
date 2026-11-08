package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// cli is one in-process invocation of the pantalan entrypoint.
type cli struct {
	code           int
	stdout, stderr string
}

// testNode is a Node whose outside world lives in a temp dir.
func testNode(t *testing.T) Node {
	t.Helper()
	dir := t.TempDir()
	return Node{ProcRoot: filepath.Join(dir, "proc"), DockerSocket: filepath.Join(dir, "docker.sock")}
}

func runCLI(t *testing.T, ctx context.Context, env []string, args ...string) cli {
	t.Helper()
	// A command that unexpectedly starts serving is stopped instead of
	// hanging the test.
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	code := run(ctx, args, env, Stdio{In: strings.NewReader(""), Out: &stdout, Err: &stderr}, testNode(t))
	return cli{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func TestVersionPrintsOnlyTheBuildInfoVersion(t *testing.T) {
	got := runCLI(t, t.Context(), nil, "version")

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", got.code, got.stderr)
	}
	// Test binaries, like `go run`, carry no VCS stamp.
	if got.stdout != "(devel)\n" {
		t.Errorf("stdout = %q, want %q", got.stdout, "(devel)\n")
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty", got.stderr)
	}
}

// syncBuffer is a bytes.Buffer that a running serve can write to while the
// test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// running is a `pantalan serve` running in a goroutine.
type running struct {
	stdout *syncBuffer
	stop   context.CancelFunc
	done   chan struct{}
	code   int // valid once done is closed
}

// wait waits for serve to exit and returns its exit code.
func (s *running) wait(t *testing.T) int {
	t.Helper()
	select {
	case <-s.done:
		return s.code
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not stop")
		return 0
	}
}

func startServe(t *testing.T, env []string) *running {
	t.Helper()
	ctx, stop := context.WithCancel(t.Context())
	s := &running{stdout: &syncBuffer{}, stop: stop, done: make(chan struct{})}
	node := testNode(t)
	go func() {
		defer close(s.done)
		s.code = run(ctx, []string{"serve"}, env, Stdio{In: strings.NewReader(""), Out: s.stdout, Err: s.stdout}, node)
	}()
	t.Cleanup(func() {
		stop()
		<-s.done
	})
	return s
}

// addr waits for serve to log that the named server is listening and
// returns its address.
func (s *running) addr(t *testing.T, server string) string {
	t.Helper()
	re := regexp.MustCompile(`msg=listening server=` + server + ` addr=(\S+)`)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if m := re.FindStringSubmatch(s.stdout.String()); m != nil {
			return m[1]
		}
		select {
		case <-s.done:
			t.Fatalf("serve exited with %d before listening; output:\n%s", s.code, s.stdout)
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("serve never listened as %s; output:\n%s", server, s.stdout)
	return ""
}

func serveEnv(t *testing.T) []string {
	return []string{
		"PANTALAN_ADMIN_TOKEN=test-token",
		"PANTALAN_API_ADDR=127.0.0.1:0",
		"PANTALAN_PROXY_ADDR=127.0.0.1:0",
		"PANTALAN_DATA_DIR=" + t.TempDir(),
	}
}

func TestServeRunsTheServiceUntilStopped(t *testing.T) {
	s := startServe(t, serveEnv(t))
	api := s.addr(t, "api")

	req, _ := http.NewRequest(http.MethodGet, "http://"+api+"/api/v1/apps", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/v1/apps = %d %q, want 200", resp.StatusCode, body)
	}

	s.stop()
	if code := s.wait(t); code != 0 {
		t.Errorf("exit code after stop = %d, want 0; output:\n%s", code, s.stdout)
	}
}

func TestServeLogsVersionRevisionAndGoVersionAtStartup(t *testing.T) {
	s := startServe(t, serveEnv(t))
	s.addr(t, "api")

	// Test binaries carry no VCS stamp, so the revision is empty here.
	want := regexp.MustCompile(`msg=starting version=\(devel\) revision="" go=go1\.\d+`)
	if !want.MatchString(s.stdout.String()) {
		t.Errorf("startup log does not match %s; output:\n%s", want, s.stdout)
	}
}

func TestServeAsRootRefusesAMissingDataDir(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root; the non-root path is a no-op covered by every other serve test")
	}
	missing := filepath.Join(t.TempDir(), "absent")
	env := append(serveEnv(t), "PANTALAN_DATA_DIR="+missing)

	got := runCLI(t, t.Context(), env, "serve")

	if got.code != 1 {
		t.Errorf("exit code = %d, want 1", got.code)
	}
	if !strings.Contains(got.stderr, "does not exist") {
		t.Errorf("stderr = %q, want it to name the missing data directory", got.stderr)
	}
	if _, err := os.Stat(missing); err == nil {
		t.Error("serve as root created the data directory")
	}
}

func TestBareOrUnknownCommandPrintsUsageAndExits2(t *testing.T) {
	for name, args := range map[string][]string{
		"bare":    nil,
		"unknown": {"srve"},
	} {
		t.Run(name, func(t *testing.T) {
			got := runCLI(t, t.Context(), nil, args...)

			if got.code != 2 {
				t.Errorf("exit code = %d, want 2", got.code)
			}
			if !strings.Contains(got.stderr, "Usage: pantalan") {
				t.Errorf("stderr = %q, want usage", got.stderr)
			}
			if got.stdout != "" {
				t.Errorf("stdout = %q, want empty", got.stdout)
			}
		})
	}
}
