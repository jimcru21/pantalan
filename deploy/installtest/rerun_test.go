//go:build installtest

package installtest

import (
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// Re-running the installer: the same version repairs, a newer one upgrades,
// an older one is refused. Every re-run here has no flags and no terminal,
// the way an unattended upgrade runs.

// A same-version re-run puts back what's missing (here the unit) and
// leaves everything else as it was: the Admin, the env file, the
// certificate and the operator's drop-ins.
func TestSameVersionReRunRestoresADeletedUnit(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, DockerOK)
		first := installAs(t, n, "alice")
		sh(t, n, "install -d /etc/systemd/system/pantalan.service.d && printf '[Service]\\nEnvironment=PLANTED=1\\n' > "+dropIn)
		envBefore := sh(t, n, "cat /etc/pantalan/pantalan.env")
		sh(t, n, "rm /etc/systemd/system/pantalan.service && systemctl daemon-reload")

		// A flag that disagrees with the env file stops the run, and says
		// how to change the setting instead.
		before := hostState(t, n)
		r := installer{}.run(t, n, "--dashboard-port", "9443")
		assertFailedNaming(t, r, "PANTALAN_DASHBOARD_ADDR=:8443", "edit /etc/pantalan/pantalan.env, then systemctl restart pantalan")
		assertUntouched(t, n, before)

		r = installer{}.run(t, n)
		if r.Code != 0 {
			t.Fatalf("same-version re-run exited %d", r.Code)
		}
		s := parseKeptAdminSummary(t, r.Output())
		if s.Version != tag {
			t.Errorf("the summary says %s, want %s", s.Version, tag)
		}
		if s.Fingerprint != first.Fingerprint {
			t.Errorf("the re-run's certificate is %s, want the kept %s", s.Fingerprint, first.Fingerprint)
		}
		assertInstalled(t, n, installed{
			Version:       tag,
			DashboardPort: 8443,
			Fingerprint:   first.Fingerprint,
			AdminUser:     "alice",
			AdminPassword: first.AdminPassword,
		})
		if got := sh(t, n, "cat /etc/pantalan/pantalan.env"); got != envBefore {
			t.Errorf("the re-run changed the env file:\nbefore:\n%s\nafter:\n%s", envBefore, got)
		}
		if got := sh(t, n, "systemctl show pantalan --property DropInPaths --value"); !strings.Contains(got, dropIn) {
			t.Errorf("the restored unit's drop-ins are %q, want the kept %s", got, dropIn)
		}
	})
}

// Upgrading N to N+1 is the same command with the newer install.sh: the
// binary is replaced and the service restarted, and the Admin, its
// sessions and the database's rows all carry over.
func TestUpgradeKeepsTheAdminSessionsAndData(t *testing.T) {
	t.Parallel()
	needNewerRelease(t)
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, DockerOK)
		first := installAs(t, n, "alice")
		app := plantAppState(t, n)
		dashboard := net.JoinHostPort(n.Addr(), "8443")
		session := signIn(t, dashboard, first.Fingerprint, "alice", first.AdminPassword)
		envBefore := sh(t, n, "cat /etc/pantalan/pantalan.env")

		r := installer{release: newerTag}.run(t, n)
		if r.Code != 0 {
			t.Fatalf("upgrade exited %d", r.Code)
		}
		from, to := parseUpgradeSummary(t, r.Output())
		if from != tag || to != newerTag {
			t.Errorf("the summary says Upgraded %s → %s, want %s → %s", from, to, tag, newerTag)
		}
		assertInstalled(t, n, installed{
			Version:       newerTag,
			DashboardPort: 8443,
			Fingerprint:   first.Fingerprint,
			AdminUser:     "alice",
			AdminPassword: first.AdminPassword,
		})
		if got := openDashboard(t, dashboard, first.Fingerprint, session); got != http.StatusOK {
			t.Errorf("the session from before the upgrade opens the Dashboard with %d, want 200", got)
		}
		if apps := apiGet(t, n, "/api/v1/apps"); !strings.Contains(apps, app.ID) {
			t.Errorf("after the upgrade the API lists %s, want the kept app %s", apps, app.ID)
		}
		assertAppStatePresent(t, n, app)
		if got := sh(t, n, "cat /etc/pantalan/pantalan.env"); got != envBefore {
			t.Errorf("the upgrade changed the env file:\nbefore:\n%s\nafter:\n%s", envBefore, got)
		}
		if got := sh(t, n, "systemctl show pantalan --property DropInPaths --value"); !strings.Contains(got, dropIn) {
			t.Errorf("the upgraded unit's drop-ins are %q, want the kept %s", got, dropIn)
		}
	})
}

// Installing N over N+1 is refused before any change, whether it's N's
// install.sh or N+1's with --version, and the refusal says how to roll back
// from a database snapshot.
func TestOlderVersionIsRefusedWithTheSnapshotHint(t *testing.T) {
	t.Parallel()
	needNewerRelease(t)
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, DockerOK)
		r := installer{release: newerTag}.run(t, n, "--admin-user", "alice")
		if r.Code != 0 {
			t.Fatalf("installing %s exited %d", newerTag, r.Code)
		}
		before := hostState(t, n) + sh(t, n, "sha256sum /usr/local/bin/pantalan /var/lib/pantalan/pantalan.db; ufw show added")

		for name, in := range map[string]struct {
			installer
			flags []string
		}{
			"N's install.sh":               {installer{release: tag}, nil},
			"N+1's install.sh --version N": {installer{release: newerTag}, []string{"--version", tag}},
		} {
			r := in.run(t, n, in.flags...)
			if r.Code == 0 {
				t.Errorf("%s over %s exited 0, want it refused", name, newerTag)
			}
			assertFailedNaming(t, r, newerTag+" is installed", tag+" is older", "Nothing was changed",
				"pantalan.db.bak-<version>", "--uninstall", "--version "+tag)
		}
		if after := hostState(t, n) + sh(t, n, "sha256sum /usr/local/bin/pantalan /var/lib/pantalan/pantalan.db; ufw show added"); after != before {
			t.Errorf("a refused downgrade changed the host:\nbefore:\n%s\nafter:\n%s", before, after)
		}
		assertServiceRunning(t, n)
		assertBinaryVersion(t, n, newerTag)

		// The hint's way back, with no snapshot to restore (N+1 has N's
		// schema): --uninstall, then install N. The Admin is kept.
		if r := (installer{release: newerTag}).run(t, n, "--uninstall"); r.Code != 0 {
			t.Fatalf("--uninstall exited %d", r.Code)
		}
		r = installer{release: newerTag}.run(t, n, "--version", tag)
		if r.Code != 0 {
			t.Fatalf("installing %s after --uninstall exited %d", tag, r.Code)
		}
		s := parseKeptAdminSummary(t, r.Output())
		if s.Version != tag {
			t.Errorf("the rollback's summary says %s, want %s", s.Version, tag)
		}
		assertBinaryVersion(t, n, tag)
		assertServiceRunning(t, n)
	})
}

// needNewerRelease skips paths that need N+1 when only one release is
// served (INSTALLTEST_RELEASE_DIR).
func needNewerRelease(t *testing.T) {
	t.Helper()
	if newerTag == "" {
		t.Skip("needs a release newer than " + tag + "; only " + tag + " is served")
	}
}

var upgradedLine = regexp.MustCompile(`(?m)^Upgraded (v\S+) → (v\S+)$`)

// parseUpgradeSummary reads an upgrade's summary: its first line is
// `Upgraded vA → vB` instead of the install's "is running" line, and it has
// no Admin or password lines. It returns vA and vB.
func parseUpgradeSummary(t *testing.T, out string) (from, to string) {
	t.Helper()
	m := upgradedLine.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("the upgrade's summary has no Upgraded vA → vB line:\n%s", out)
	}
	if summaryLines["version"].MatchString(out) {
		t.Errorf("the upgrade's summary has the install's \"is running\" line, want Upgraded vA → vB first")
	}
	// The Upgraded line opens the summary, right after its blank line.
	if !strings.Contains(out, "\n\n"+m[0]+"\n") {
		t.Errorf("Upgraded %s → %s isn't the summary's first line", m[1], m[2])
	}
	for _, name := range []string{"admin", "admin password"} {
		if summaryLines[name].MatchString(out) {
			t.Errorf("the upgrade's summary has an %s line, want none", name)
		}
	}
	summaryField(t, out, "dashboard")
	summaryField(t, out, "fingerprint")
	return m[1], m[2]
}

// signIn signs in to the Dashboard at addr and returns the session cookie.
func signIn(t *testing.T, addr, fp, user, password string) *http.Cookie {
	t.Helper()
	resp, err := pinned(fp).PostForm("https://"+addr+"/login", url.Values{"username": {user}, "password": {password}})
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, "__Host-") {
			return c
		}
	}
	t.Fatalf("sign in as %s got %d and no __Host- cookie", user, resp.StatusCode)
	return nil
}

// openDashboard GETs the Dashboard's / with session and returns the status.
func openDashboard(t *testing.T, addr, fp string, session *http.Cookie) int {
	t.Helper()
	req, _ := http.NewRequest("GET", "https://"+addr+"/", nil)
	req.AddCookie(session)
	resp, err := pinned(fp).Do(req)
	if err != nil {
		t.Errorf("open the Dashboard: %v", err)
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}
