//go:build installtest

package installtest

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// The fixtures every test shares, set up once by TestMain.
var (
	substrate Substrate
	release   *releaseServer
	tag       string // the version the fake release serves
)

func TestMain(m *testing.M) {
	os.Exit(func() int {
		c, err := newContainers()
		if err != nil {
			fmt.Fprintln(os.Stderr, "installtest:", err)
			return 1
		}
		substrate = c
		root, err := os.MkdirTemp("", "pantalan-installtest-")
		if err != nil {
			fmt.Fprintln(os.Stderr, "installtest:", err)
			return 1
		}
		defer os.RemoveAll(root)
		start := time.Now()
		release, tag, err = startReleaseServer(root, substrate.HostAddr())
		if err != nil {
			fmt.Fprintln(os.Stderr, "installtest: fake release:", err)
			return 1
		}
		defer release.Close()
		fmt.Fprintf(os.Stderr, "installtest: fake release %s for %s ready at %s in %s\n",
			tag, strings.Join(arches, "+"), release.addr, time.Since(start).Round(time.Second))
		return m.Run()
	}())
}

// forEachDistro runs path once per selected distro, in parallel, each on
// Nodes of its own. INSTALLTEST_DISTROS (comma-separated names, e.g.
// debian-12,ubuntu-26.04) selects distros; unset means all four. A CI matrix
// job sets it to its one distro.
func forEachDistro(t *testing.T, path func(t *testing.T, d Distro)) {
	t.Helper()
	selected := selectedDistros(t)
	for _, d := range distros {
		if !slices.Contains(selected, d.Name) {
			continue
		}
		t.Run(d.Name, func(t *testing.T) {
			t.Parallel()
			path(t, d)
		})
	}
}

func selectedDistros(t *testing.T) []string {
	env := os.Getenv("INSTALLTEST_DISTROS")
	if env == "" {
		var all []string
		for _, d := range distros {
			all = append(all, d.Name)
		}
		return all
	}
	names := strings.Split(env, ",")
	for _, name := range names {
		if !slices.ContainsFunc(distros, func(d Distro) bool { return d.Name == name }) {
			t.Fatalf("INSTALLTEST_DISTROS: unknown distro %q", name)
		}
	}
	return names
}

// installer is one run of install.sh, the way an operator runs it:
// `curl -fsSL <release>/install.sh | sudo sh -s -- <flags>`, with no
// terminal unless typed is set.
type installer struct {
	origin string // which fake release origin to download from; empty means good
	asUser string // run as this uid:gid instead of root, e.g. 65534:65534
	// typed runs it at a terminal (script(1)'s pty) where the operator has
	// typed these lines in advance; prompts read them in order.
	typed []string
}

// run runs the installer on n with flags and returns how it ended. The
// output is logged, so a failing test shows what the operator would have seen.
func (in installer) run(t *testing.T, n Node, flags ...string) Result {
	t.Helper()
	origin := in.origin
	if origin == "" {
		origin = goodOrigin
	}
	base := release.baseURL(origin)
	shell := "sh"
	if in.asUser != "" {
		uid, gid, _ := strings.Cut(in.asUser, ":")
		shell = "setpriv --reuid=" + uid + " --regid=" + gid + " --clear-groups sh"
	}
	script := fmt.Sprintf(`curl -fsSL %s | %s -s --%s`,
		shellQuote(base+"/"+tag+"/install.sh"), shell, quoteAll(flags))
	var stdin string
	if in.typed != nil {
		// stdout and stderr both arrive on stdout, through the pty.
		script = "script --quiet --return --command " + shellQuote(script) + " /dev/null"
		stdin = strings.Join(in.typed, "\n") + "\n"
	}
	start := time.Now()
	r := n.Run(t, Cmd{
		Script:  script,
		Env:     []string{"PANTALAN_RELEASE_BASE_URL=" + base},
		Stdin:   stdin,
		Timeout: 12 * time.Minute,
	})
	t.Logf("install.sh %s exited %d after %s:\n%s", strings.Join(flags, " "), r.Code,
		time.Since(start).Round(time.Second), r.Output())
	return r
}

// summary is what the installer's closing summary told the operator.
type summary struct {
	Version       string
	DashboardURL  string
	Fingerprint   string
	Admin         string
	AdminPassword string
}

var summaryLines = map[string]*regexp.Regexp{
	"version":        regexp.MustCompile(`(?m)^Pantalan (\S+) is running on this Node\.$`),
	"dashboard":      regexp.MustCompile(`(?m)^  Dashboard +(https://\S+)$`),
	"fingerprint":    regexp.MustCompile(`(?m)^  Fingerprint +(SHA256:[0-9A-F:]+)\b`),
	"admin":          regexp.MustCompile(`(?m)^  Admin +([a-z][a-z0-9._-]*)$`),
	"admin password": regexp.MustCompile(`(?m)^  Admin password +([A-Z2-7]{26})\b`),
}

// parseSummary reads the summary out of the installer's output, failing the
// test when a line is missing.
func parseSummary(t *testing.T, out string) summary {
	t.Helper()
	return summary{
		Version:       summaryField(t, out, "version"),
		DashboardURL:  summaryField(t, out, "dashboard"),
		Fingerprint:   summaryField(t, out, "fingerprint"),
		Admin:         summaryField(t, out, "admin"),
		AdminPassword: summaryField(t, out, "admin password"),
	}
}

// parseKeptAdminSummary reads the summary of a run that found an Admin
// already there: it has no Admin or password lines, and fails the test if
// it does.
func parseKeptAdminSummary(t *testing.T, out string) summary {
	t.Helper()
	for _, name := range []string{"admin", "admin password"} {
		if summaryLines[name].MatchString(out) {
			t.Errorf("the summary has an %s line, but the Admin was already there:\n%s", name, out)
		}
	}
	return summary{
		Version:      summaryField(t, out, "version"),
		DashboardURL: summaryField(t, out, "dashboard"),
		Fingerprint:  summaryField(t, out, "fingerprint"),
	}
}

func summaryField(t *testing.T, out, name string) string {
	t.Helper()
	m := summaryLines[name].FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("the install summary has no %s line:\n%s", name, out)
	}
	return m[1]
}

func quoteAll(args []string) string {
	var b strings.Builder
	for _, a := range args {
		b.WriteString(" " + shellQuote(a))
	}
	return b.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
