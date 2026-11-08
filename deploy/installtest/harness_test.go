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
	tag       string // the version the fake release serves (N)
	// newerTag is the release after tag (N+1), for the upgrade and
	// downgrade paths; empty when only one release is served (see
	// startReleaseServer).
	newerTag string
	// previousTag is the newest published release older than tag, which
	// the VM tier upgrades from; empty (with noPreviousReason saying why)
	// while none is known.
	previousTag, noPreviousReason string
)

func TestMain(m *testing.M) {
	os.Exit(func() int {
		switch name := os.Getenv("INSTALLTEST_SUBSTRATE"); name {
		case "", "containers":
			c, err := newContainers()
			if err != nil {
				fmt.Fprintln(os.Stderr, "installtest:", err)
				return 1
			}
			substrate = c
		case "vm":
			v := newVMs()
			substrate = v
			if v.unavailable != "" {
				if os.Getenv("INSTALLTEST_VM_REQUIRED") != "" {
					fmt.Fprintln(os.Stderr, "installtest: INSTALLTEST_VM_REQUIRED is set, but the VM tier can't run here:", v.unavailable)
					return 1
				}
				// Every Boot skips, so no release is needed. Paths that
				// skip before booting say why too.
				fmt.Fprintln(os.Stderr, "installtest: the VM tier can't run here, so its paths skip:", v.unavailable)
				noPreviousReason = "VM tier unavailable: " + v.unavailable
				return m.Run()
			}
		default:
			fmt.Fprintf(os.Stderr, "installtest: INSTALLTEST_SUBSTRATE=%s: want containers or vm\n", name)
			return 1
		}
		root, err := os.MkdirTemp("", "pantalan-installtest-")
		if err != nil {
			fmt.Fprintln(os.Stderr, "installtest:", err)
			return 1
		}
		defer os.RemoveAll(root)
		// INSTALLTEST_PUBLISHED_TAGS lists the published releases
		// (space-separated; CI asks GitHub). The fake releases are tagged
		// after the newest of them, so there's one to upgrade from.
		published, publishedKnown := os.LookupEnv("INSTALLTEST_PUBLISHED_TAGS")
		start := time.Now()
		var tags []string
		release, tags, err = startReleaseServer(root, substrate.HostAddr(), fakeReleaseTags(strings.Fields(published)))
		if err != nil {
			fmt.Fprintln(os.Stderr, "installtest: fake release:", err)
			return 1
		}
		defer release.Close()
		tag = tags[0]
		if len(tags) > 1 {
			newerTag = tags[1]
		}
		previousTag = previousRelease(strings.Fields(published), tag)
		switch {
		case !publishedKnown:
			noPreviousReason = "INSTALLTEST_PUBLISHED_TAGS is unset, so no published release is known to upgrade from"
		case previousTag == "":
			noPreviousReason = "no release older than " + tag + " is published yet"
		}
		fmt.Fprintf(os.Stderr, "installtest: fake releases %s for %s ready at %s in %s\n",
			strings.Join(tags, ", "), strings.Join(arches, "+"), release.addr, time.Since(start).Round(time.Second))
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
	// release is whose install.sh runs, and so the version it installs by
	// default; empty means tag.
	release string
	// published runs this published release's install.sh from GitHub
	// instead, with no base-URL override: exactly what an operator ran.
	published string
	// downloaded saves install.sh to this path first and runs it from
	// there (`sh <path> <flags>`), the way the README shows, instead of
	// piping it into sh.
	downloaded string
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
	version := in.release
	if version == "" {
		version = tag
	}
	shell := "sh"
	if in.asUser != "" {
		uid, gid, _ := strings.Cut(in.asUser, ":")
		shell = "setpriv --reuid=" + uid + " --regid=" + gid + " --clear-groups sh"
	}
	url, env := base+"/"+version+"/install.sh", []string{"PANTALAN_RELEASE_BASE_URL=" + base}
	if in.published != "" {
		url, env = publishedReleases+"/"+in.published+"/install.sh", nil
	}
	script := fmt.Sprintf(`curl -fsSL %s | %s -s --%s`, shellQuote(url), shell, quoteAll(flags))
	if in.downloaded != "" {
		p := shellQuote(in.downloaded)
		script = fmt.Sprintf(`curl -fsSL -o %s %s && chmod a+r %s && %s %s%s`,
			p, shellQuote(url), p, shell, p, quoteAll(flags))
	}
	var stdin string
	if in.typed != nil {
		// stdout and stderr both arrive on stdout, through the pty.
		script = "script --quiet --return --command " + shellQuote(script) + " /dev/null"
		stdin = strings.Join(in.typed, "\n") + "\n"
	}
	start := time.Now()
	r := n.Run(t, Cmd{
		Script:  script,
		Env:     env,
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
	"firewall":       regexp.MustCompile(`(?m)^  Firewall +(.+)$`),
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

// assertFirewallLine fails unless the summary's Firewall line is want.
func assertFirewallLine(t *testing.T, out, want string) {
	t.Helper()
	if got := summaryField(t, out, "firewall"); got != want {
		t.Errorf("the summary's Firewall line is\n  %s\nwant\n  %s", got, want)
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
