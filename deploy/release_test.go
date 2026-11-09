package deploy

import (
	"bytes"
	"debug/elf"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// These tests drive deploy/release.sh, the script release.yml builds the
// release assets with, the way the workflow runs it.

func TestCheckTagAcceptsOnlyPlainReleaseTags(t *testing.T) {
	for _, tag := range []string{"v0.1.0", "v1.2.3", "v10.20.30"} {
		if r := releaseScript(t, "", "check-tag", tag); r.code != 0 {
			t.Errorf("check-tag %q: exit %d, want 0; stderr: %s", tag, r.code, r.stderr)
		}
	}
	for _, tag := range []string{
		"v1.2.3-rc1", "v1.2.3+build", "1.2.3", "v1.2", "v1.2.3.4", "v1..3", "V1.2.3", "v1.2.3\nv1.2.3", "",
	} {
		r := releaseScript(t, "", "check-tag", tag)
		if r.code == 0 {
			t.Errorf("check-tag %q: exit 0, want a refusal", tag)
		}
		if !strings.Contains(r.stderr, "vX.Y.Z") {
			t.Errorf("check-tag %q: stderr %q doesn't name the vX.Y.Z form", tag, r.stderr)
		}
	}
}

func TestBuildWritesTheReleaseAssets(t *testing.T) {
	repo := taggedCheckout(t, "v1.2.3", nil)
	dir := filepath.Join(t.TempDir(), "release")

	r := releaseScript(t, repo, "build", "v1.2.3", dir)
	if r.code != 0 {
		t.Fatalf("build: exit %d; stderr:\n%s", r.code, r.stderr)
	}

	want := []string{"SHA256SUMS", "install.sh", "pantalan_v1.2.3_linux_amd64", "pantalan_v1.2.3_linux_arm64"}
	if got := fileNames(t, dir); !slices.Equal(got, want) {
		t.Fatalf("assets = %q, want %q", got, want)
	}

	t.Run("the frontend is built from the lockfile with install scripts off", func(t *testing.T) {
		// npm ci installs exactly the lockfile; --ignore-scripts keeps any
		// package from running code at install.
		if got, want := npmCalls(t, r), "web/app ci --ignore-scripts\nweb/app run build\n"; got != want {
			t.Errorf("npm calls:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("the binaries embed the built frontend", func(t *testing.T) {
		for _, arch := range []string{"amd64", "arm64"} {
			bin, err := os.ReadFile(filepath.Join(dir, "pantalan_v1.2.3_linux_"+arch))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(bin, []byte(builtIndexHTML)) {
				t.Errorf("%s: the binary doesn't hold the built index.html", arch)
			}
		}
	})

	t.Run("binaries", func(t *testing.T) {
		for arch, machine := range map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64} {
			bin := filepath.Join(dir, "pantalan_v1.2.3_linux_"+arch)
			f, err := elf.Open(bin)
			if err != nil {
				t.Fatalf("%s: %v", arch, err)
			}
			if f.Machine != machine {
				t.Errorf("%s: ELF machine %v, want %v", arch, f.Machine, machine)
			}
			if slices.ContainsFunc(f.Progs, func(p *elf.Prog) bool { return p.Type == elf.PT_INTERP }) {
				t.Errorf("%s: dynamically linked, want a static binary", arch)
			}
			if f.Section(".symtab") != nil {
				t.Errorf("%s: has a symbol table, want it stripped (-s)", arch)
			}
			if slices.ContainsFunc(f.Sections, func(s *elf.Section) bool {
				return strings.HasPrefix(s.Name, ".debug_") || strings.HasPrefix(s.Name, ".zdebug_")
			}) {
				t.Errorf("%s: has DWARF sections, want them stripped (-w)", arch)
			}
			f.Close()
			// Go leaves -ldflags out of the build info under -trimpath, so
			// the two checks above stand in for it.
			info := run(t, "", "go", "version", "-m", bin)
			for _, setting := range []string{
				"build\t-trimpath=true", "build\tCGO_ENABLED=0",
				"build\tGOOS=linux", "build\tGOARCH=" + arch, "mod\tgithub.com/pantalan/pantalan\tv1.2.3",
			} {
				if !strings.Contains(info, setting) {
					t.Errorf("%s: go version -m lacks %q:\n%s", arch, setting, info)
				}
			}
		}
		if runtime.GOOS == "linux" {
			got := run(t, "", filepath.Join(dir, "pantalan_v1.2.3_linux_"+runtime.GOARCH), "version")
			if got != "v1.2.3\n" {
				t.Errorf("pantalan version printed %q, want \"v1.2.3\\n\"", got)
			}
		}
	})

	t.Run("install.sh has the tag baked in", func(t *testing.T) {
		script := readFile(t, filepath.Join(dir, "install.sh"))
		if strings.Contains(script, "@VERSION@") {
			t.Error("install.sh still holds the @VERSION@ placeholder")
		}
		if !strings.Contains(script, "\nDEFAULT_VERSION=v1.2.3\n") {
			t.Error("install.sh doesn't set DEFAULT_VERSION=v1.2.3")
		}
		src := readFile(t, filepath.Join(repo, "deploy", "install.sh"))
		if want := strings.Replace(src, "@VERSION@", "v1.2.3", 1); script != want {
			t.Error("install.sh differs from deploy/install.sh by more than the baked version")
		}
	})

	t.Run("SHA256SUMS", func(t *testing.T) {
		sums := readFile(t, filepath.Join(dir, "SHA256SUMS"))
		var names []string
		for _, line := range strings.Split(strings.TrimSuffix(sums, "\n"), "\n") {
			_, name, ok := strings.Cut(line, "  ")
			if !ok {
				t.Fatalf("SHA256SUMS line %q isn't sha256sum output", line)
			}
			names = append(names, name)
		}
		slices.Sort(names)
		if want := want[1:]; !slices.Equal(names, want) {
			t.Errorf("SHA256SUMS lists %q, want %q", names, want)
		}
		run(t, dir, "sha256sum", "-c", "--strict", "SHA256SUMS")
		// What the installer does: one binary downloaded next to SHA256SUMS.
		node := t.TempDir()
		for _, name := range []string{"SHA256SUMS", "pantalan_v1.2.3_linux_arm64"} {
			copyFile(t, filepath.Join(dir, name), filepath.Join(node, name))
		}
		out := run(t, node, "sha256sum", "-c", "--ignore-missing", "SHA256SUMS")
		if out != "pantalan_v1.2.3_linux_arm64: OK\n" {
			t.Errorf("sha256sum -c --ignore-missing printed %q", out)
		}
	})
}

func TestBuildRefusesWhatWouldShipAWrongRelease(t *testing.T) {
	for _, c := range []struct {
		name     string
		checkout string                          // the tag the checkout is at
		edit     func(t *testing.T, repo string) // changes committed with the tag
		after    func(t *testing.T, repo string) // changes left uncommitted
		build    string                          // the tag passed to build
		stderr   string
		builds   bool // whether it gets as far as building
	}{
		{name: "pre-release tag", checkout: "v1.2.3-rc1", build: "v1.2.3-rc1", stderr: "vX.Y.Z"},
		{name: "checkout at another tag", checkout: "v1.2.3", build: "v1.2.4", stderr: "want v1.2.4", builds: true},
		{
			name: "uncommitted change", checkout: "v1.2.3", build: "v1.2.3", stderr: "want v1.2.3", builds: true,
			after: func(t *testing.T, repo string) {
				if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("changed\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "installer without the placeholder", checkout: "v1.2.3", build: "v1.2.3", stderr: "@VERSION@",
			edit: func(t *testing.T, repo string) {
				path := filepath.Join(repo, "deploy", "install.sh")
				script := strings.Replace(readFile(t, path), "@VERSION@", "v9.9.9", 1)
				if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			repo := taggedCheckout(t, c.checkout, c.edit)
			if c.after != nil {
				c.after(t, repo)
			}
			dir := filepath.Join(t.TempDir(), "release")
			r := releaseScript(t, repo, "build", c.build, dir)
			if r.code == 0 {
				t.Fatalf("build %s: exit 0, want a refusal", c.build)
			}
			if !strings.Contains(r.stderr, c.stderr) {
				t.Errorf("stderr doesn't say %q:\n%s", c.stderr, r.stderr)
			}
			if built := strings.Contains(r.stderr, "building"); built != c.builds {
				t.Errorf("built anything = %v, want %v:\n%s", built, c.builds, r.stderr)
			}
			if calls := npmCalls(t, r); !c.builds && calls != "" {
				t.Errorf("ran npm before refusing:\n%s", calls)
			}
			if names, _ := os.ReadDir(dir); !c.builds && len(names) > 0 {
				t.Errorf("left %d files in %s", len(names), dir)
			}
		})
	}
}

type result struct {
	code           int
	stdout, stderr string
	npmDir         string // the run's fake npm (fakeNPM)
}

// releaseScript runs deploy/release.sh with args, from the repository at
// repo ("" means this one).
func releaseScript(t *testing.T, repo string, args ...string) result {
	t.Helper()
	if repo == "" {
		abs, err := filepath.Abs("..")
		if err != nil {
			t.Fatal(err)
		}
		repo = abs
	}
	cmd := exec.Command("sh", append([]string{filepath.Join(repo, "deploy", "release.sh")}, args...)...)
	cmd.Dir = repo
	npm := fakeNPM(t)
	cmd.Env = append(os.Environ(), "PATH="+npm+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := result{stdout: stdout.String(), stderr: stderr.String(), npmDir: npm}
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("release.sh %s: %v", strings.Join(args, " "), err)
		}
		r.code = exit.ExitCode()
	}
	return r
}

// builtIndexHTML is what the fake npm's `run build` writes as
// web/dist/index.html, so a test can find it embedded in a binary.
const builtIndexHTML = "<!doctype html><title>fake vite build 7d1f0c</title>"

// fakeNPM is a directory holding an npm that stands in for the real one,
// so these tests need no Node or network. It appends each call, as
// "<dir relative to the checkout> <args>", to the npm.log next to it, and
// `run build` writes builtIndexHTML to ../dist/index.html, as vite build
// does from web/app.
func fakeNPM(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
top=$(git rev-parse --show-toplevel)
printf '%s %s\n' "${PWD#"$top"/}" "$*" >>"` + filepath.Join(dir, "npm.log") + `"
case "$*" in
"ci --ignore-scripts") ;;
"run build") mkdir -p ../dist && printf '%s' '` + builtIndexHTML + `' >../dist/index.html ;;
*) echo "fake npm: unexpected npm $*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// npmCalls is what the fake npm of the run r was asked to do, one call a
// line; "" when it wasn't called.
func npmCalls(t *testing.T, r result) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.npmDir, "npm.log"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

// taggedCheckout copies this working tree (tracked and untracked files as
// they are on disk, minus ignored ones) into a fresh repository, lets edit
// change the copy, then commits it and tags the commit, the way release.yml
// sees a pushed tag.
func taggedCheckout(t *testing.T, tag string, edit func(t *testing.T, repo string)) string {
	t.Helper()
	repo := t.TempDir()
	files := run(t, "..", "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	for _, name := range strings.Split(strings.TrimRight(files, "\x00"), "\x00") {
		info, err := os.Lstat(filepath.Join("..", name))
		if errors.Is(err, os.ErrNotExist) {
			continue // deleted in the working tree
		}
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().IsRegular() {
			copyFile(t, filepath.Join("..", name), filepath.Join(repo, name))
		}
	}
	if edit != nil {
		edit(t, repo)
	}
	vcs := []string{"-c", "user.name=test", "-c", "user.email=test@invalid", "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false"}
	run(t, repo, "git", "init", "--quiet")
	run(t, repo, "git", append(vcs, "add", "--all")...)
	run(t, repo, "git", append(vcs, "commit", "--quiet", "--message", "release "+tag)...)
	run(t, repo, "git", append(vcs, "tag", "--annotate", "--message", tag, tag)...)
	return repo
}

// run runs a command in dir and returns its stdout, failing the test unless
// it exits 0.
func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String()
}

func fileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
