//go:build installtest

package installtest

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestUninstallKeepsDataAndAReinstallBringsItBack(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, DockerOK)
		first := installAs(t, n, "alice")
		app := plantAppState(t, n)
		envBefore := sh(t, n, "cat /etc/pantalan/pantalan.env")

		r := installer{}.run(t, n, "--uninstall")
		if r.Code != 0 {
			t.Fatalf("--uninstall exited %d", r.Code)
		}
		assertServiceGone(t, n)
		assertFirewallAfterRemoval(t, n, r, []int{22})
		for _, path := range []string{"/etc/pantalan/pantalan.env", "/var/lib/pantalan/pantalan.db", "/var/lib/pantalan/tls/key.pem", dropIn} {
			if n.Run(t, Cmd{Script: "test -e " + path}).Code != 0 {
				t.Errorf("--uninstall removed %s, want it kept", path)
			}
		}
		assertAppStatePresent(t, n, app)
		if n.Run(t, Cmd{Script: "getent passwd pantalan"}).Code != 0 {
			t.Errorf("--uninstall removed the pantalan user, want it kept")
		}
		for _, kept := range []string{"/etc/pantalan", "/var/lib/pantalan", "pantalan.service.d", "pantalan.app", "pantalan/*", "pantalan user", "| sudo sh -s -- --purge"} {
			if !strings.Contains(r.Output(), kept) {
				t.Errorf("--uninstall's output doesn't mention %q among what it kept", kept)
			}
		}

		if again := (installer{}).run(t, n, "--uninstall"); again.Code != 0 {
			t.Errorf("a second --uninstall exited %d, want 0: there's nothing left to remove", r.Code)
		}

		// A reinstall needs no --admin-user: the Admin is still there.
		r = installer{}.run(t, n)
		if r.Code != 0 {
			t.Fatalf("reinstall after --uninstall exited %d", r.Code)
		}
		s := parseKeptAdminSummary(t, r.Output())
		assertFirewallLine(t, r.Output(), "ufw active: allows SSH 22, 80, 443, 8443; added 80, 443, 8443")
		if s.Fingerprint != first.Fingerprint {
			t.Errorf("the reinstall's certificate is %s, want the kept %s", s.Fingerprint, first.Fingerprint)
		}
		assertInstalled(t, n, installed{
			Version:       tag,
			DashboardPort: 8443,
			Fingerprint:   first.Fingerprint,
			AdminUser:     "alice",
			AdminPassword: first.AdminPassword,
		})
		if got := sh(t, n, "cat /etc/pantalan/pantalan.env"); got != envBefore {
			t.Errorf("the reinstall changed the env file:\nbefore:\n%s\nafter:\n%s", envBefore, got)
		}
		if apps := apiGet(t, n, "/api/v1/apps"); !strings.Contains(apps, app.ID) {
			t.Errorf("after the reinstall the API lists %s, want the kept app %s", apps, app.ID)
		}
		if got := sh(t, n, "systemctl show pantalan --property DropInPaths --value"); !strings.Contains(got, dropIn) {
			t.Errorf("the reinstalled unit's drop-ins are %q, want the kept %s", got, dropIn)
		}
	})
}

func TestPurgeNeedsYesWithNoTTYThenRemovesAllButDocker(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		// From Bare, so the Docker that must survive is the one the
		// installer put there.
		n := substrate.Boot(t, d, Bare)
		installAs(t, n, "alice")
		app := plantAppState(t, n)
		before := hostState(t, n)

		r := installer{}.run(t, n, "--purge")
		assertFailedNaming(t, r, "--purge --yes", "terminal")
		assertUntouched(t, n, before)
		assertServiceRunning(t, n)

		r = installer{}.run(t, n, "--purge", "--yes")
		if r.Code != 0 {
			t.Fatalf("--purge --yes exited %d", r.Code)
		}
		assertPurged(t, n, app)
		assertFirewallAfterRemoval(t, n, r, []int{22})
		assertDockerStillRuns(t, n)
		sh(t, n, "command -v git")
	})
}

func TestPurgeAtATerminalNeedsPurgeTyped(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, DockerOK)
		installAs(t, n, "alice")
		app := plantAppState(t, n)
		before := hostState(t, n)

		r := installer{typed: []string{"yes"}}.run(t, n, "--purge")
		assertFailedNaming(t, r, "Type purge", "wasn't confirmed")
		assertUntouched(t, n, before)

		r = installer{typed: []string{"purge"}}.run(t, n, "--purge")
		if r.Code != 0 {
			t.Fatalf("--purge with purge typed exited %d", r.Code)
		}
		assertPurged(t, n, app)
		assertFirewallAfterRemoval(t, n, r, []int{22})
		assertDockerStillRuns(t, n)
	})
}

func TestYesWithoutPurgeIsRejectedBeforeAnyChange(t *testing.T) {
	t.Parallel()
	forEachDistro(t, func(t *testing.T, d Distro) {
		n := substrate.Boot(t, d, Bare)
		before := hostState(t, n)

		for _, flags := range [][]string{{"--yes"}, {"--uninstall", "--yes"}, {"--admin-user", "alice", "--yes"}} {
			r := installer{}.run(t, n, flags...)
			assertFailedNaming(t, r, "--yes", "--purge")
		}
		assertUntouched(t, n, before)
	})
}

// installAs installs with --admin-user user, failing the test unless it
// worked, and returns the summary.
func installAs(t *testing.T, n Node, user string) summary {
	t.Helper()
	r := installer{}.run(t, n, "--admin-user", user)
	if r.Code != 0 {
		t.Fatalf("install exited %d", r.Code)
	}
	return parseSummary(t, r.Output())
}

// dropIn is an operator's unit drop-in, which only --purge removes.
const dropIn = "/etc/systemd/system/pantalan.service.d/local.conf"

// appState is what an installed Pantalan accumulates besides its own
// files: an app in the database, its image and container in Docker, and
// the operator's drop-in.
type appState struct {
	ID        string // the app's ID
	Image     string
	Container string
}

// plantAppState gives a running install an app (through the API), an
// image and a container labelled as that app's, and a unit drop-in. The
// image is an empty import and the container is only created, so nothing
// is pulled or run.
func plantAppState(t *testing.T, n Node) appState {
	t.Helper()
	out := sh(t, n, `tok=$(sed -n 's/^PANTALAN_ADMIN_TOKEN=//p' /etc/pantalan/pantalan.env)
		curl -ksSf -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
			-d '{"name":"demo","repo_url":"https://github.com/jimcru21/pantalan.git","branch":"main","build_type":"dockerfile","domain":"demo.example.com","port":8080}' \
			https://127.0.0.1:8443/api/v1/apps`)
	var app struct{ ID string }
	if err := json.Unmarshal([]byte(out), &app); err != nil || app.ID == "" {
		t.Fatalf("creating an app through the API answered %q (%v)", out, err)
	}
	s := appState{ID: app.ID, Image: "pantalan/" + app.ID + ":planted", Container: "pantalan-planted-" + app.ID}
	sh(t, n, "tar -cf - -T /dev/null | docker import - "+s.Image+
		" >/dev/null && docker create --name "+s.Container+" --label pantalan.app="+s.ID+" "+s.Image+" /app")
	sh(t, n, "install -d /etc/systemd/system/pantalan.service.d && printf '[Service]\\nEnvironment=PLANTED=1\\n' > "+dropIn)
	return s
}

func assertAppStatePresent(t *testing.T, n Node, s appState) {
	t.Helper()
	if got := sh(t, n, "docker ps -a --filter label=pantalan.app --format '{{.Names}}'"); got != s.Container {
		t.Errorf("app containers are %q, want the kept %s", got, s.Container)
	}
	if n.Run(t, Cmd{Script: "docker image inspect " + s.Image}).Code != 0 {
		t.Errorf("the app image %s is gone, want it kept", s.Image)
	}
}

// assertServiceGone: no unit, no binary, nothing running or listening.
func assertServiceGone(t *testing.T, n Node) {
	t.Helper()
	for _, path := range []string{"/etc/systemd/system/pantalan.service", "/usr/local/bin/pantalan"} {
		if n.Run(t, Cmd{Script: "test -e " + path}).Code == 0 {
			t.Errorf("%s is still there", path)
		}
	}
	if got := sh(t, n, "systemctl list-unit-files --no-legend pantalan.service || :"); got != "" {
		t.Errorf("systemd still knows pantalan.service: %q", got)
	}
	if got := sh(t, n, "systemctl is-active pantalan || :"); got == "active" {
		t.Errorf("pantalan.service is still active")
	}
	if got := sh(t, n, "ss -Hltn 'sport = :8443'"); got != "" {
		t.Errorf("something still listens on the Dashboard port: %s", got)
	}
}

// assertFirewallAfterRemoval: --uninstall and --purge (run r) delete every
// `pantalan` rule but keep the `pantalan-ssh` ones (for ports ssh) and ufw
// itself active, since dropping an SSH rule from an active ufw locks out
// the next login; and they say how to remove the kept rules.
func assertFirewallAfterRemoval(t *testing.T, n Node, r Result, ssh []int) {
	t.Helper()
	rules := ufwRules(t, n)
	if len(rules["pantalan"]) != 0 {
		t.Errorf("ufw still allows %v/tcp with comment pantalan, want none", rules["pantalan"])
	}
	if !slices.Equal(rules["pantalan-ssh"], ssh) {
		t.Errorf("ufw allows %v/tcp with comment pantalan-ssh, want %v kept", rules["pantalan-ssh"], ssh)
	}
	if got := ufwStatus(t, n); got != "active" {
		t.Errorf("ufw is %q after the removal, want still active", got)
	}
	for _, want := range []string{"pantalan-ssh", "sudo ufw status numbered", "sudo ufw delete"} {
		if !strings.Contains(r.Output(), want) {
			t.Errorf("the removal's output doesn't mention %q about the kept SSH rules", want)
		}
	}
}

// assertPurged: everything --uninstall removes, plus everything it keeps.
func assertPurged(t *testing.T, n Node, s appState) {
	t.Helper()
	assertServiceGone(t, n)
	for _, path := range []string{"/etc/systemd/system/pantalan.service.d", "/etc/pantalan", "/var/lib/pantalan"} {
		if n.Run(t, Cmd{Script: "test -e " + path}).Code == 0 {
			t.Errorf("--purge left %s", path)
		}
	}
	if got := sh(t, n, "docker ps -aq --filter label=pantalan.app"); got != "" {
		t.Errorf("--purge left app containers %s", got)
	}
	if got := sh(t, n, "docker images --format '{{.Repository}}:{{.Tag}}' | grep '^pantalan/' || :"); got != "" {
		t.Errorf("--purge left images %s", got)
	}
	for _, db := range []string{"passwd", "group"} {
		if n.Run(t, Cmd{Script: "getent " + db + " pantalan"}).Code == 0 {
			t.Errorf("--purge left pantalan in %s", db)
		}
	}
}

func assertDockerStillRuns(t *testing.T, n Node) {
	t.Helper()
	if got := sh(t, n, "dpkg-query -W -f '${db:Status-Abbrev}' docker-ce"); !strings.HasPrefix(got, "ii") {
		t.Errorf("docker-ce is %q after the removal, want still installed", got)
	}
	if got := sh(t, n, "systemctl is-active docker || :"); got != "active" {
		t.Errorf("docker.service is %q after the removal, want active", got)
	}
	if r := n.Run(t, Cmd{Script: "docker version --format '{{.Server.Version}}'"}); r.Code != 0 {
		t.Errorf("the Docker daemon doesn't answer after the removal: %s", r.Output())
	}
}

// apiGet GETs path from the Dashboard listener with the Admin Token, on
// the Node itself.
func apiGet(t *testing.T, n Node, path string) string {
	t.Helper()
	return sh(t, n, `tok=$(sed -n 's/^PANTALAN_ADMIN_TOKEN=//p' /etc/pantalan/pantalan.env)
		curl -ksSf -H "Authorization: Bearer $tok" https://127.0.0.1:8443`+path)
}
