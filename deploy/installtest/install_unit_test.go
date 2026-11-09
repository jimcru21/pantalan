package installtest

// Unit tests of the installer's decisions: deploy/install.sh is sourced
// with its guard set, the probes it asks the host with are replaced by
// stubs, and a decision is called on its own. No container, root or ufw
// needed, so they carry no build tag and run in `make check`.

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// hostCommands fail loudly in the decisions' PATH, so a probe a case
// forgot to stub can't reach the real host.
var hostCommands = []string{"apt-get", "curl", "docker", "dpkg", "dpkg-query", "id", "install", "ip",
	"journalctl", "mktemp", "ss", "sshd", "systemctl", "ufw", "unshare", "useradd", "usermod"}

// decisionHost is the host a decision runs against: every probe reports a
// bare Node (nothing installed, listening or running, no terminal) unless
// the case stubs it. The installer's file probes read under HOST_ROOT,
// which the case fills with files.
const decisionHost = `
have_tty() { return 1; }
ask() { echo "not stubbed: ask $*" >&2; exit 97; }
pkg_installed() { return 1; }
pkg_version() { echo "not stubbed: pkg_version $*" >&2; exit 97; }
port_holder() { :; }
listening_tcp() { :; }
docker_responds() { return 1; }
docker_firewall_backend() { :; }
running_containers() { :; }
unit_active() { return 1; }
unit_enabled() { return 1; }
nftables_conf_is_stock() { return 1; }
ufw_active() { return 1; }
ufw_incoming_policy() { echo deny; }
ufw_show_added() { :; }
global_ipv6() { :; }
ssh_socket_listens() { :; }
sshd_test_config() { :; }
session_ssh_port() { :; }
`

// decisionResult is what a sourced script printed, and its exit status.
type decisionResult struct {
	Stdout, Stderr string
	Code           int
}

func (r decisionResult) Output() string { return r.Stdout + r.Stderr }

// installScript is deploy/install.sh's absolute path.
func installScript(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// runSourced runs script in dash after sourcing install.sh on
// decisionHost, with HOST_ROOT a temp dir holding files (path under the
// root → content; content starting with #! is made executable).
func runSourced(t *testing.T, files map[string]string, script string) decisionResult {
	t.Helper()
	dash, err := exec.LookPath("dash")
	if err != nil {
		t.Fatalf("the installer's unit tests need dash, the shell the installer runs under on Debian and Ubuntu: %v", err)
	}
	stubs := t.TempDir()
	for _, name := range hostCommands {
		stub := "#!/bin/sh\necho \"not stubbed: " + name + " $*\" >&2\nexit 97\n"
		if err := os.WriteFile(filepath.Join(stubs, name), []byte(stub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(content, "#!") {
			mode = 0o755
		}
		if err := os.WriteFile(full, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(dash, "-c", `. "$0"
HOST_ROOT=$1
`+decisionHost+script, installScript(t), root)
	cmd.Env = []string{"PATH=" + stubs + ":" + os.Getenv("PATH"), "LC_ALL=C", "PANTALAN_INSTALL_NO_MAIN=1"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	r := decisionResult{}
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running dash: %v", err)
		}
		r.Code = exit.ExitCode()
	}
	r.Stdout, r.Stderr = stdout.String(), stderr.String()
	if strings.Contains(r.Stderr, "not stubbed: ") {
		t.Fatalf("the decision reached the host:\n%s", r.Stderr)
	}
	return r
}

// assertDecision fails unless r exited with code and its output contains
// every one of want.
func assertDecision(t *testing.T, r decisionResult, code int, want ...string) {
	t.Helper()
	if r.Code != code {
		t.Errorf("exited %d, want %d; output:\n%s", r.Code, code, r.Output())
	}
	for _, w := range want {
		if !strings.Contains(r.Output(), w) {
			t.Errorf("output doesn't mention %q:\n%s", w, r.Output())
		}
	}
}

// assertPrints fails unless r exited 0 having printed exactly want (and a
// newline, when want isn't empty) on stdout.
func assertPrints(t *testing.T, r decisionResult, call, want string) {
	t.Helper()
	if got := strings.TrimSuffix(r.Stdout, "\n"); r.Code != 0 || got != want {
		t.Errorf("%s printed %q and exited %d, want %q; stderr:\n%s", call, got, r.Code, want, r.Stderr)
	}
}

func TestSourcingTheInstallerWithTheGuardSetRunsNothing(t *testing.T) {
	dash, err := exec.LookPath("dash")
	if err != nil {
		t.Fatalf("needs dash: %v", err)
	}
	// With a PATH that has no commands at all, anything sourcing ran
	// besides assignments (the root check's id, the log's mktemp, a
	// download's curl) would fail with "not found".
	cmd := exec.Command(dash, "-c", `. "$0" --admin-user alice && command -v main && command -v parse_args`, installScript(t))
	cmd.Env = []string{"PATH=" + t.TempDir(), "PANTALAN_INSTALL_NO_MAIN=1"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sourcing install.sh failed: %v\n%s", err, out)
	}
	if got := string(out); got != "main\nparse_args\n" {
		t.Errorf("sourcing install.sh printed %q, want only the functions it defined", got)
	}
}

// envFile is ENV_FILE under HOST_ROOT.
const envFile = "etc/pantalan/pantalan.env"

// The stubbed host for the Dashboard port cases: SSH on 22 and 2222, perl
// on 9000, nginx on 8444, and Pantalan itself (an earlier run) on 8443.
const portHolders = `
SSH_PORTS="22 2222"
port_holder() {
	case $1 in
	8443) echo pantalan ;;
	8444) echo nginx ;;
	9000) echo perl ;;
	esac
}
`

func TestEachRefusedDashboardPortSaysWhy(t *testing.T) {
	for _, c := range []struct {
		port, proxy, want string
	}{
		{"1023", "", "port 1023 is below 1024, where system services live"},
		{"80", "", "port 80 is the app proxy's"},
		{"443", "", "port 443 is kept for app HTTPS"},
		{"22", "", "SSH listens on port 22, and the Dashboard can't share it"},
		{"2222", "", "SSH listens on port 2222, and the Dashboard can't share it"},
		{"8081", ":8081", "port 8081 is the app proxy's"},
		{"80", "0.0.0.0:8081", "port 80 is below 1024, where system services live"},
		{"9000", "", "port 9000 is in use by perl"},
		{"8x443", "", "8x443 isn't a port number"},
		{"0443", "", "0443 isn't a port number"},
		{"123456", "", "123456 isn't a port number"},
		{"", "", " isn't a port number"},
		{"65536", "", "65536 isn't a port number: they go up to 65535"},
		// Usable: free, or Pantalan's own from an earlier run.
		{"65535", "", ""},
		{"9443", "", ""},
		{"8443", "", ""},
	} {
		files := map[string]string{}
		if c.proxy != "" {
			files[envFile] = "PANTALAN_PROXY_ADDR=" + c.proxy + "\n"
		}
		r := runSourced(t, files, portHolders+`dashboard_port_problem "`+c.port+`"`)
		assertPrints(t, r, "dashboard_port_problem "+c.port+" with the proxy on "+c.proxy, c.want)
	}
}

func TestNextDashboardPortSkipsEveryRefusedPort(t *testing.T) {
	for _, c := range []struct{ after, want string }{
		{"8443", "8446"}, // 8444 is nginx's, 8445 SSH's
		{"2221", "2223"},
		{"65535", ""},
	} {
		r := runSourced(t, nil, portHolders+`SSH_PORTS="$SSH_PORTS 8445"; next_dashboard_port `+c.after)
		assertPrints(t, r, "next_dashboard_port "+c.after, c.want)
	}
}

func TestTheDashboardPortComesFromTheFlagTheEnvFileOrTheDefault(t *testing.T) {
	for _, c := range []struct {
		name  string
		flag  string
		env   string // PANTALAN_DASHBOARD_ADDR
		taken string // a port perl holds
		code  int
		want  []string
	}{
		{name: "default", code: 0, want: []string{"DASHBOARD_PORT=8443"}},
		{name: "flag", flag: "9443", code: 0, want: []string{"DASHBOARD_PORT=9443"}},
		{name: "env file", env: ":9443", code: 0, want: []string{"DASHBOARD_PORT=9443"}},
		{name: "flag agreeing with the env file", flag: "9443", env: ":9443", code: 0, want: []string{"DASHBOARD_PORT=9443"}},
		{name: "refused flag", flag: "443", code: 1, want: []string{
			"--dashboard-port 443: port 443 is kept for app HTTPS. Choose another port.",
		}},
		{name: "flag disagreeing with the env file", flag: "8443", env: ":9443", code: 1, want: []string{
			"--dashboard-port 8443 disagrees with PANTALAN_DASHBOARD_ADDR=:9443 in /etc/pantalan/pantalan.env",
			"Nothing was changed.",
		}},
		{name: "env file port taken", env: ":9000", taken: "9000", code: 1, want: []string{
			"PANTALAN_DASHBOARD_ADDR=:9000 in /etc/pantalan/pantalan.env: port 9000 is in use by perl. Free the port, or edit /etc/pantalan/pantalan.env",
		}},
		{name: "default taken with no terminal", taken: "8443", code: 1, want: []string{
			"port 8443 is in use by perl, and it's the Dashboard's default port. Choose another with --dashboard-port N, e.g. --dashboard-port 8444",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{}
			if c.env != "" {
				files[envFile] = "PANTALAN_DASHBOARD_ADDR=" + c.env + "\n"
			}
			r := runSourced(t, files, `
				SSH_PORTS=22
				port_holder() { [ "$1" != "`+c.taken+`" ] || echo perl; }
				OPT_DASHBOARD_PORT=`+c.flag+`
				resolve_dashboard_port
				echo "DASHBOARD_PORT=$DASHBOARD_PORT"`)
			assertDecision(t, r, c.code, c.want...)
		})
	}
}

func TestTheInstalledVersionMakesARunAnUpgradeARepairOrARefusal(t *testing.T) {
	const bin = "usr/local/bin/pantalan"
	reports := func(v string) map[string]string {
		return map[string]string{bin: "#!/bin/sh\necho " + v + "\n"}
	}
	for _, c := range []struct {
		name     string
		files    map[string]string
		code     int
		want     []string
		upgraded string
	}{
		{name: "nothing installed", code: 0},
		{name: "same version", files: reports("v1.2.0"), code: 0, want: []string{
			"Pantalan v1.2.0 is installed: repairing anything missing.",
		}},
		{name: "upgrade", files: reports("v1.1.9"), code: 0, upgraded: "v1.1.9", want: []string{
			"Pantalan v1.1.9 is installed: upgrading to v1.2.0.",
		}},
		{name: "upgrade by version, not by string", files: reports("v0.10.3"), code: 0, upgraded: "v0.10.3", want: []string{
			"upgrading to v1.2.0",
		}},
		{name: "a development build", files: reports("dev"), code: 0, want: []string{
			"warning: the installed /usr/local/bin/pantalan reports dev, not a release version; replacing it with v1.2.0",
		}},
		{name: "a pre-release", files: reports("v1.3.0-rc.1"), code: 0, want: []string{
			"reports v1.3.0-rc.1, not a release version; replacing it with v1.2.0",
		}},
		{name: "a binary that fails to run", files: map[string]string{bin: "#!/bin/sh\nexit 2\n"}, code: 0, want: []string{
			"reports nothing: it fails to run, not a release version",
		}},
		{name: "downgrade with no snapshot", files: reports("v1.10.0"), code: 1, want: []string{
			"Pantalan v1.10.0 is installed, and v1.2.0 is older. Nothing was changed.",
			"(here: none)",
			"--uninstall",
			"--version v1.2.0",
		}},
		{name: "downgrade with snapshots", files: map[string]string{
			bin:                                  "#!/bin/sh\necho v1.3.0\n",
			"var/lib/pantalan/pantalan.db.bak-3": "",
			"var/lib/pantalan/pantalan.db.bak-4": "",
		}, code: 1, want: []string{
			"(here: pantalan.db.bak-3, pantalan.db.bak-4)",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runSourced(t, c.files, `
				VERSION=v1.2.0
				compare_installed_version
				echo "UPGRADED_FROM=$UPGRADED_FROM"`)
			assertDecision(t, r, c.code, c.want...)
			if c.code == 0 && !strings.Contains(r.Stdout, "UPGRADED_FROM="+c.upgraded+"\n") {
				t.Errorf("UPGRADED_FROM isn't %q:\n%s", c.upgraded, r.Stdout)
			}
		})
	}
}

func TestOnlyAnAnsweringDockerCE28OrNewerIsUsedAsItIs(t *testing.T) {
	const (
		ce28 = "5:28.1.1-1~debian.12~bookworm"
		ce27 = "5:27.5.1-1~debian.12~bookworm"
	)
	for _, c := range []struct {
		name     string
		pkg      string // the one Docker package installed, at version
		version  string
		responds bool
		files    map[string]string
		code     int
		want     []string
	}{
		{name: "no Docker", code: 0, want: []string{
			"Docker: not installed; Docker CE will be installed from Docker's apt repository.", "DOCKER_STATE=missing",
		}},
		{name: "acceptable docker-ce", pkg: "docker-ce", version: ce28, responds: true, code: 0, want: []string{
			"Docker: docker-ce " + ce28 + " is installed and running; using it as it is.", "DOCKER_STATE=ok",
		}},
		{name: "docker-ce without an epoch", pkg: "docker-ce", version: "29.0.0-1~ubuntu.24.04~noble", responds: true, code: 0, want: []string{
			"DOCKER_STATE=ok",
		}},
		{name: "docker-ce too old", pkg: "docker-ce", version: ce27, responds: true, code: 1, want: []string{
			"docker-ce " + ce27 + " is installed, and Pantalan needs 28 or newer. Pantalan never upgrades an existing Docker",
			"https://docs.docker.com/engine/install/debian/",
		}},
		{name: "docker-ce whose daemon doesn't answer", pkg: "docker-ce", version: ce28, code: 1, want: []string{
			"docker-ce " + ce28 + " is installed but the Docker daemon doesn't answer. Check `systemctl status docker`",
		}},
		{name: "docker-ce of no version", pkg: "docker-ce", version: "", code: 1, want: []string{
			"can't tell which docker-ce  is",
		}},
		{name: "docker.io", pkg: "docker.io", responds: true, code: 1, want: []string{
			"docker.io is installed. Pantalan needs Docker CE from Docker's apt repository (28 or newer) and never removes a Docker for you.",
		}},
		{name: "podman-docker", pkg: "podman-docker", code: 1, want: []string{"podman-docker is installed."}},
		{name: "docker-engine", pkg: "docker-engine", code: 1, want: []string{"docker-engine is installed."}},
		{name: "snap", files: map[string]string{"snap/bin/docker": "#!/bin/sh\n"}, code: 1, want: []string{
			"a Docker that isn't Docker CE from Docker's apt repository is installed (snap, or installed by hand)",
		}},
		{name: "installed by hand", responds: true, code: 1, want: []string{
			"a Docker that isn't Docker CE from Docker's apt repository is installed",
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			responds := "return 1"
			if c.responds {
				responds = "return 0"
			}
			r := runSourced(t, c.files, `
				OS_ID=debian
				pkg_installed() { [ "$1" = "`+c.pkg+`" ]; }
				pkg_version() { printf '%s\n' "`+c.version+`"; }
				docker_responds() { `+responds+`; }
				classify_docker
				echo "DOCKER_STATE=$DOCKER_STATE"`)
			assertDecision(t, r, c.code, c.want...)
		})
	}
}

func TestTheStoredFirewallModeWinsAndNoFirewallSetsItOnce(t *testing.T) {
	for _, c := range []struct {
		name, stored, flag string
		code               int
		want               string
	}{
		{name: "first install", code: 0, want: "FIREWALL_MODE=ufw"},
		{name: "first install with --no-firewall", flag: "1", code: 0, want: "FIREWALL_MODE=off"},
		{name: "stored ufw", stored: "ufw", code: 0, want: "FIREWALL_MODE=ufw"},
		{name: "stored off", stored: "off", code: 0, want: "FIREWALL_MODE=off"},
		{name: "stored off with --no-firewall", stored: "off", flag: "1", code: 0, want: "FIREWALL_MODE=off"},
		{name: "stored ufw with --no-firewall", stored: "ufw", flag: "1", code: 1,
			want: "--no-firewall disagrees with PANTALAN_FIREWALL=ufw in /etc/pantalan/pantalan.env. Nothing was changed."},
		{name: "stored nonsense", stored: "iptables", code: 1,
			want: "PANTALAN_FIREWALL=iptables in /etc/pantalan/pantalan.env must be ufw or off."},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{}
			if c.stored != "" {
				files[envFile] = "PANTALAN_ADMIN_TOKEN=x\nPANTALAN_FIREWALL=" + c.stored + "\n"
			}
			r := runSourced(t, files, `
				OPT_NO_FIREWALL=`+c.flag+`
				resolve_firewall_mode
				echo "FIREWALL_MODE=$FIREWALL_MODE"`)
			assertDecision(t, r, c.code, c.want)
		})
	}
}

func TestRiskyHostsHaveAReasonToStageUfw(t *testing.T) {
	const ipv6 = "2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500\n    inet6 2001:db8::5/64 scope global\n"
	for _, c := range []struct {
		name  string
		files map[string]string
		stubs string
		want  string
	}{
		{name: "nothing risky", want: ""},
		{name: "no ufw config yet", files: map[string]string{}, want: ""},
		{name: "MANAGE_BUILTINS", files: map[string]string{"etc/default/ufw": "IPV6=yes\nMANAGE_BUILTINS=yes\n"},
			want: "MANAGE_BUILTINS=yes in /etc/default/ufw"},
		{name: "IPV6=no with a global IPv6 address", files: map[string]string{"etc/default/ufw": "IPV6=\"no\"\nMANAGE_BUILTINS=no\n"},
			stubs: "global_ipv6() { printf '" + ipv6 + "'; }",
			want:  "IPV6=no in /etc/default/ufw on a Node with a global IPv6 address"},
		{name: "IPV6=no with no global IPv6 address", files: map[string]string{"etc/default/ufw": "IPV6=no\n"}, want: ""},
		{name: "Docker's nftables backend in daemon.json", files: map[string]string{"etc/docker/daemon.json": "{\n  \"firewall-backend\" : \"nftables\"\n}\n"},
			want: "Docker uses its nftables firewall backend"},
		{name: "Docker's iptables backend in daemon.json", files: map[string]string{"etc/docker/daemon.json": "{\"firewall-backend\": \"iptables\"}\n"},
			stubs: "docker_responds() { return 0; }; docker_firewall_backend() { echo iptables; }",
			want:  ""},
		{name: "Docker reporting its nftables backend", stubs: "docker_responds() { return 0; }; docker_firewall_backend() { echo nftables; }",
			want: "Docker uses its nftables firewall backend"},
		{name: "no SSH", stubs: "SSH_PORTS=", want: "no SSH server found"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := runSourced(t, c.files, "SSH_PORTS=22\n"+c.stubs+"\nstage_reason")
			assertPrints(t, r, "stage_reason", c.want)
		})
	}
}

func TestTheFirewallActionFollowsTheModeAndTheHostsFirewall(t *testing.T) {
	const published = `docker_responds() { return 0; }
running_containers() { printf '%s\n' 'web (0.0.0.0:8080->80/tcp, [::]:8080->80/tcp)' 'db (127.0.0.1:5432->5432/tcp)' 'idle ()'; }`
	for _, c := range []struct {
		name   string
		stored string // PANTALAN_FIREWALL
		stubs  string
		action string
		reason string
		want   []string // in the output
		warns  []string // in FW_WARNINGS
		quiet  []string // nowhere in the output
	}{
		{name: "off", stored: "off", action: "off", want: []string{"Firewall: off (PANTALAN_FIREWALL=off): left alone."}},
		{name: "off with a published container", stored: "off", stubs: published, action: "off",
			warns: []string{"these running containers publish ports on all addresses, which Docker forwards ahead of any firewall on this Node: web (0.0.0.0:8080->80/tcp, [::]:8080->80/tcp)"},
			quiet: []string{"db (", "idle ("}},
		{name: "firewalld", stubs: `unit_active() { [ "$1" = firewalld.service ]; }`, action: "skip", reason: "firewalld is active",
			want: []string{"warning: firewalld is active, so Pantalan leaves ufw alone: open 80, 443, 8443 in it"}},
		{name: "nftables with the operator's ruleset", stubs: `unit_enabled() { [ "$1" = nftables.service ]; }`, action: "skip",
			reason: "nftables.service is enabled with its own ruleset"},
		{name: "nftables with the stock ruleset", stubs: `unit_enabled() { [ "$1" = nftables.service ]; }; nftables_conf_is_stock() { return 0; }`,
			action: "enable"},
		{name: "another manager with a published container", stubs: `unit_active() { [ "$1" = firewalld.service ]; }` + "\n" + published,
			action: "skip", reason: "firewalld is active", warns: []string{"which Docker forwards ahead of any firewall on this Node: web"}},
		{name: "inactive ufw", action: "enable"},
		{name: "inactive ufw with a published container", stubs: published, action: "enable",
			warns: []string{"these running containers publish ports on all addresses, which bypass ufw: web"}},
		{name: "active ufw denying", stubs: "ufw_active() { return 0; }", action: "add", quiet: []string{"incoming policy"}},
		{name: "active ufw rejecting", stubs: "ufw_active() { return 0; }; ufw_incoming_policy() { echo reject; }", action: "add",
			quiet: []string{"incoming policy"}},
		{name: "active ufw allowing", stubs: "ufw_active() { return 0; }; ufw_incoming_policy() { echo allow; }", action: "add",
			want:  []string{"warning: ufw's default incoming policy is allow: ports with no rule are open too"},
			warns: []string{"ufw's default incoming policy is allow, so ports with no rule are open too. Pantalan leaves your policy as it is; `sudo ufw default deny incoming` changes it."}},
		{name: "risky host", stubs: "SSH_PORTS=", action: "stage", reason: "no SSH server found",
			want: []string{"warning: ufw won't be enabled (no SSH server found): its rules will be staged for you to enable after checking SSH"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{}
			if c.stored != "" {
				files[envFile] = "PANTALAN_FIREWALL=" + c.stored + "\n"
			}
			r := runSourced(t, files, `
				DASHBOARD_PORT=8443
				SSH_PORTS=22
				`+c.stubs+`
				classify_firewall
				echo "FW_ACTION=$FW_ACTION"
				echo "FW_REASON=$FW_REASON"
				printf 'FW_WARNINGS=%s' "$FW_WARNINGS"`)
			assertDecision(t, r, 0, append(c.want, "FW_ACTION="+c.action+"\n", "FW_REASON="+c.reason+"\n")...)
			_, warnings, _ := strings.Cut(r.Stdout, "FW_WARNINGS=")
			for _, w := range c.warns {
				if !strings.Contains(warnings, w) {
					t.Errorf("FW_WARNINGS doesn't mention %q:\n%s", w, warnings)
				}
			}
			if len(c.warns) == 0 && warnings != "" {
				t.Errorf("FW_WARNINGS = %q, want none", warnings)
			}
			for _, q := range c.quiet {
				if strings.Contains(r.Output(), q) {
					t.Errorf("output mentions %q:\n%s", q, r.Output())
				}
			}
		})
	}
}

func TestTheFirewallPlanAndItsSummaryLine(t *testing.T) {
	// ufw's rules, as `ufw show added` prints them.
	const (
		earlierRun = "ufw allow 80/tcp comment 'pantalan'\nufw allow 443/tcp comment 'pantalan'\nufw allow 8443/tcp comment 'pantalan'\nufw allow 22/tcp comment 'pantalan-ssh'\n"
		operators  = "ufw allow OpenSSH\nufw allow 443/tcp comment 'web'\nufw deny 3306/tcp\nufw allow from 10.0.0.0/8 to any port 9000 proto tcp\n"
	)
	for _, c := range []struct {
		name      string
		action    string
		reason    string
		dashboard string // default 8443
		ssh       string // SSH_PORTS; default 22
		noSSH     bool   // SSH_PORTS empty
		rules     string
		line      string
		// "FW_ADD|FW_ADD_SSH|FW_REMOVE", each as a port set; "||" changes
		// nothing, and an empty plan isn't checked (off and skip).
		plan string
	}{
		{name: "off", action: "off", line: "off (PANTALAN_FIREWALL=off)"},
		{name: "skipped", action: "skip", reason: "firewalld is active",
			line: "skipped: firewalld is active; open 80, 443, 8443 in it"},
		{name: "enabled fresh", action: "enable",
			line: "ufw enabled: allows SSH 22, 80, 443, 8443", plan: "80 443 8443|22|"},
		{name: "enabled again unchanged", action: "enable", rules: earlierRun,
			line: "ufw enabled: allows SSH 22, 80, 443, 8443; unchanged", plan: "||"},
		{name: "enabled again on a new Dashboard port", action: "enable", dashboard: "9443", rules: earlierRun,
			line: "ufw enabled: allows SSH 22, 80, 443, 9443; added 9443, removed 8443", plan: "9443||8443"},
		{name: "enabled again with a new SSH port", action: "enable", ssh: "22 2222", rules: earlierRun,
			line: "ufw enabled: allows SSH 22, 2222, 80, 443, 8443; added SSH 2222", plan: "|2222|"},
		{name: "active again", action: "add", rules: operators + earlierRun,
			line: "ufw active: allows SSH 22, 80, 443, 8443; unchanged", plan: "||"},
		{name: "active again with changes", action: "add", dashboard: "9443", ssh: "22 2222", rules: earlierRun,
			line: "ufw active: allows SSH 22, 2222, 80, 443, 9443; added 9443 and SSH 2222, removed 8443", plan: "9443|2222|8443"},
		{name: "already active, one port taken", action: "add", rules: operators,
			line: "ufw already active: added 80, 8443 and SSH 22 (443 was already allowed); your existing rules are unchanged", plan: "80 8443|22|"},
		{name: "already active, ports taken", action: "add", rules: "ufw allow 22/tcp\nufw allow 80/tcp comment 'web'\nufw allow 443/tcp\n",
			line: "ufw already active: added 8443 (80, 443 were already allowed); your existing rules are unchanged", plan: "8443||"},
		{name: "already active, nothing taken", action: "add", rules: "ufw allow 22/tcp\n",
			line: "ufw already active: added 80, 443, 8443; your existing rules are unchanged", plan: "80 443 8443||"},
		{name: "already active, all allowed", action: "add", rules: "ufw allow 22/tcp\nufw allow 80/tcp\nufw allow 443/tcp\nufw allow 8443/tcp\n",
			line: "ufw already active: your rules already allow SSH 22, 80, 443, 8443, and are unchanged", plan: "||"},
		{name: "staged fresh", action: "stage", reason: "no SSH server found", noSSH: true,
			line: "ufw NOT enabled (no SSH server found): rules are staged; check SSH, then run: ufw --force enable", plan: "80 443 8443||"},
		{name: "staged again unchanged", action: "stage", reason: "MANAGE_BUILTINS=yes in /etc/default/ufw", rules: earlierRun,
			line: "ufw NOT enabled (MANAGE_BUILTINS=yes in /etc/default/ufw): rules are staged (unchanged); check SSH, then run: ufw --force enable", plan: "||"},
		{name: "staged again with changes", action: "stage", reason: "Docker uses its nftables firewall backend", dashboard: "9443", rules: earlierRun,
			line: "ufw NOT enabled (Docker uses its nftables firewall backend): rules are staged (added 9443, removed 8443); check SSH, then run: ufw --force enable", plan: "9443||8443"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dashboard, ssh := c.dashboard, c.ssh
			if dashboard == "" {
				dashboard = "8443"
			}
			if ssh == "" && !c.noSSH {
				ssh = "22"
			}
			r := runSourced(t, nil, `
				FW_ACTION=`+c.action+`
				FW_REASON="`+c.reason+`"
				DASHBOARD_PORT=`+dashboard+`
				SSH_PORTS="`+ssh+`"
				ufw_show_added() { printf '%s' "`+c.rules+`"; }
				plan_firewall
				echo "FW_LINE=$FW_LINE"
				echo "PLAN=$(port_set "$FW_ADD")|$(port_set "$FW_ADD_SSH")|$(port_set "$FW_REMOVE")"`)
			assertDecision(t, r, 0, "FW_LINE="+c.line+"\n")
			if c.plan != "" {
				assertDecision(t, r, 0, "PLAN="+c.plan+"\n")
			}
		})
	}
}

func TestApplyFirewallAddsBeforeRemovingAndEnablesLast(t *testing.T) {
	r := runSourced(t, nil, `
		FW_ACTION=enable
		FW_TARGETS="80 443 9443"
		FW_ADD=" 9443"
		FW_ADD_SSH=" 2222"
		FW_REMOVE=" 8443"
		SSH_PORTS="22 2222"
		quietly() { echo "$*"; }
		confirm_ufw_active() { echo "confirm $1"; }
		apply_firewall`)
	want := "ufw allow 9443/tcp comment pantalan\n" +
		"ufw allow 2222/tcp comment pantalan-ssh\n" +
		"ufw --force delete allow 8443/tcp\n" +
		"ufw --force enable\n" +
		"confirm 80 443 9443 22 2222\n"
	if r.Code != 0 || r.Stdout != want {
		t.Errorf("apply_firewall ran (exit %d):\n%s\nwant:\n%s", r.Code, r.Stdout, want)
	}
}

func TestDetectSSHPortsUnitesEverySource(t *testing.T) {
	r := runSourced(t, nil, `
		listening_tcp() {
			cat <<'EOF'
LISTEN 0      128          0.0.0.0:22        0.0.0.0:*    users:(("sshd",pid=812,fd=3))
LISTEN 0      128             [::]:22           [::]:*    users:(("sshd",pid=812,fd=4))
LISTEN 0      4096       127.0.0.1:5432      0.0.0.0:*    users:(("postgres",pid=901,fd=5))
LISTEN 0      4096         0.0.0.0:2022      0.0.0.0:*    users:(("systemd",pid=1,fd=50))
EOF
		}
		ssh_socket_listens() { printf '%s\n' '0.0.0.0:2022 (Stream)' '[::]:22 (Stream)' '/run/ssh-unix-local/socket (Stream)' '0.0.0.0:2023 (Datagram)'; }
		sshd_test_config() { printf '%s\n' 'port 22' 'port 2200' 'addressfamily any' 'permitrootlogin no' 'passwordauthentication no'; }
		session_ssh_port() { echo 2201; }
		detect_ssh_ports
		echo "SSH_PORTS=$SSH_PORTS"`)
	assertDecision(t, r, 0,
		"SSH: ports 22 2022 2200 2201 (listening: 22 2022; sshd -T: 22 2200; this session: 2201).\n",
		"SSH_PORTS=22 2022 2200 2201\n")

	r = runSourced(t, nil, `detect_ssh_ports; echo "SSH_PORTS=$SSH_PORTS"`)
	assertDecision(t, r, 0,
		"SSH: ports none found (listening: none; sshd -T: none; this session: not over SSH).\n",
		"SSH_PORTS=\n")
}
