//go:build installtest

package installtest

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// installed is what a finished install must look like from the outside.
type installed struct {
	Version       string // what `pantalan version` must print: the served release's tag
	DashboardPort int
	Fingerprint   string // the one the installer printed
	AdminUser     string
	AdminPassword string // one that signs in
	Firewall      firewall
}

// firewall is what the install must have left in ufw. The zero value is the
// common case: ufw enabled, allowing 80, 443, the Dashboard port and SSH 22.
type firewall struct {
	State firewallState
	// Pantalan is the ports with a `pantalan` rule; nil means 80, 443 and
	// the Dashboard port.
	Pantalan []int
	// SSH is the ports with a `pantalan-ssh` rule (in the ufw states), each
	// of which SSH from outside must reach; nil means 22.
	SSH []int
}

type firewallState int

const (
	// ufwEnabled: ufw is active with Pantalan's rules, and a port outside
	// them is unreachable from outside.
	ufwEnabled firewallState = iota
	// ufwStaged: Pantalan's rules are added, but ufw is inactive.
	ufwStaged
	// noUfw: the installer left the firewall alone, and didn't install ufw.
	noUfw
)

func (f firewall) pantalanPorts(dashboardPort int) []int {
	if f.Pantalan == nil {
		return []int{80, 443, dashboardPort}
	}
	return f.Pantalan
}

func (f firewall) sshPorts() []int {
	if f.SSH == nil {
		return []int{22}
	}
	return f.SSH
}

// assertInstalled is the shared post-install assertion every path that ends
// in a running install makes (fresh, repair, upgrade, reinstall), on any
// Substrate. Each check reports on its own, so one failure doesn't hide the
// others.
func assertInstalled(t *testing.T, n Node, want installed) {
	t.Helper()
	assertServiceRunning(t, n)
	assertBinaryVersion(t, n, want.Version)
	assertFileLayout(t, n)
	dashboard := net.JoinHostPort(n.Addr(), strconv.Itoa(want.DashboardPort))
	assertTLS(t, dashboard, want.Fingerprint)
	assertPlainHTTPRedirects(t, dashboard)
	assertAdminSignsIn(t, dashboard, want)
	assertAdminToken(t, n, dashboard, want.Fingerprint)
	assertFirewall(t, n, want.Firewall, want.DashboardPort)
	for _, port := range want.Firewall.sshPorts() {
		assertSSHFromOutside(t, n, port)
	}
}

// assertFirewall: ufw is in the wanted state, and the rules carrying
// Pantalan's two comments are exactly the wanted ones.
func assertFirewall(t *testing.T, n Node, want firewall, dashboardPort int) {
	t.Helper()
	if want.State == noUfw {
		if got := ufwRules(t, n); len(got["pantalan"])+len(got["pantalan-ssh"]) != 0 {
			t.Errorf("ufw has Pantalan's rules %v, want none", got)
		}
		if n.Run(t, Cmd{Script: "dpkg-query -W -f '${db:Status-Abbrev}' ufw | grep -q ^ii"}).Code == 0 {
			t.Errorf("ufw is installed, want it left alone (not installed)")
		}
		return
	}
	status := ufwStatus(t, n)
	if wantStatus := map[firewallState]string{ufwEnabled: "active", ufwStaged: "inactive"}[want.State]; status != wantStatus {
		t.Errorf("ufw is %q, want %s", status, wantStatus)
	}
	got := ufwRules(t, n)
	for comment, ports := range map[string][]int{"pantalan": want.pantalanPorts(dashboardPort), "pantalan-ssh": want.sshPorts()} {
		if !slices.Equal(got[comment], sorted(ports)) {
			t.Errorf("ufw allows %v/tcp with comment %q, want %v", got[comment], comment, sorted(ports))
		}
	}
	if want.State == ufwEnabled {
		assertPortFiltered(t, n)
	}
}

// ufwStatus is "active" or "inactive".
func ufwStatus(t *testing.T, n Node) string {
	t.Helper()
	return strings.TrimPrefix(strings.SplitN(sh(t, n, "ufw status"), "\n", 2)[0], "Status: ")
}

var ufwAllowLine = regexp.MustCompile(`^ufw allow (\d+)/tcp comment '([^']*)'$`)

// ufwRules maps each comment to the sorted TCP ports `ufw allow`ed with it,
// from `ufw show added`, which lists rules whether or not ufw is active.
// A rule of any other shape carrying Pantalan's comments fails the test.
func ufwRules(t *testing.T, n Node) map[string][]int {
	t.Helper()
	rules := map[string][]int{}
	if n.Run(t, Cmd{Script: "command -v ufw"}).Code != 0 {
		return rules
	}
	for _, line := range strings.Split(sh(t, n, "ufw show added"), "\n") {
		if m := ufwAllowLine.FindStringSubmatch(line); m != nil {
			port, _ := strconv.Atoi(m[1])
			rules[m[2]] = append(rules[m[2]], port)
		} else if strings.Contains(line, "comment 'pantalan") {
			t.Errorf("unexpected ufw rule with Pantalan's comment: %s", line)
		}
	}
	for c := range rules {
		slices.Sort(rules[c])
	}
	return rules
}

// operatorRules is `ufw show added` without Pantalan's rules: the
// operator's own, which the installer must never change.
func operatorRules(t *testing.T, n Node) string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(sh(t, n, "ufw show added"), "\n") {
		if !strings.Contains(line, "comment 'pantalan") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func sorted(ports []int) []int {
	s := slices.Clone(ports)
	slices.Sort(s)
	return s
}

// probePort is a port no rule allows, where assertPortFiltered listens.
const probePort = 2399

// assertPortFiltered: a port no rule allows is unreachable from outside
// even with something listening on it, so ufw really filters (rather than
// the rules merely being written down). The listener is a second sshd,
// which every Node with SSH has.
func assertPortFiltered(t *testing.T, n Node) {
	t.Helper()
	port := strconv.Itoa(probePort)
	sh(t, n, "install -d -m 0755 /run/sshd && /usr/sbin/sshd -p "+port+" -o PidFile=/run/pantalan-probe.pid && sleep 0.5 && ss -Hltn 'sport = :"+port+"' | grep -q .")
	defer n.Run(t, Cmd{Script: "kill $(cat /run/pantalan-probe.pid)"})
	if conn, err := net.DialTimeout("tcp", net.JoinHostPort(n.Addr(), port), 3*time.Second); err == nil {
		conn.Close()
		t.Errorf("port %s (no rule allows it) is reachable from outside: ufw isn't filtering", port)
	}
}

// assertServiceRunning: the unit is active and enabled, runs `pantalan
// serve`, hardened, as the pantalan user with the docker group.
func assertServiceRunning(t *testing.T, n Node) {
	t.Helper()
	if got := n.Run(t, Cmd{Script: "systemctl is-active pantalan"}); strings.TrimSpace(got.Stdout) != "active" {
		t.Errorf("pantalan.service is %q, want active", strings.TrimSpace(got.Output()))
	}
	if got := n.Run(t, Cmd{Script: "systemctl is-enabled pantalan"}); strings.TrimSpace(got.Stdout) != "enabled" {
		t.Errorf("pantalan.service is %q at boot, want enabled", strings.TrimSpace(got.Output()))
	}
	props := unitProperties(t, n, "ExecStart", "User", "AmbientCapabilities", "NoNewPrivileges", "ProtectSystem", "ReadWritePaths", "EnvironmentFiles")
	if !strings.Contains(props["ExecStart"], "argv[]=/usr/local/bin/pantalan serve ") {
		t.Errorf("ExecStart = %s, want /usr/local/bin/pantalan serve", props["ExecStart"])
	}
	for key, want := range map[string]string{
		"User":                "pantalan",
		"AmbientCapabilities": "cap_net_bind_service",
		"NoNewPrivileges":     "yes",
		"ProtectSystem":       "strict",
		"ReadWritePaths":      "/var/lib/pantalan",
	} {
		if props[key] != want {
			t.Errorf("unit %s = %q, want %q", key, props[key], want)
		}
	}
	if !strings.HasPrefix(props["EnvironmentFiles"], "/etc/pantalan/pantalan.env ") {
		t.Errorf("unit EnvironmentFiles = %q, want /etc/pantalan/pantalan.env", props["EnvironmentFiles"])
	}

	proc := sh(t, n, `pid=$(systemctl show --property MainPID --value pantalan)
		stat -c %U /proc/$pid
		sed -n 's/^Groups:\s*//p' /proc/$pid/status
		getent group docker | cut -d: -f3`)
	lines := strings.Split(proc, "\n")
	if len(lines) != 3 {
		t.Errorf("reading the service process: %q", proc)
		return
	}
	if lines[0] != "pantalan" {
		t.Errorf("the service runs as %q, want pantalan", lines[0])
	}
	if !slices.Contains(strings.Fields(lines[1]), lines[2]) {
		t.Errorf("the service's groups %q lack docker (gid %s)", lines[1], lines[2])
	}
	if got := sh(t, n, "getent passwd pantalan | cut -d: -f6,7"); got != "/var/lib/pantalan:/usr/sbin/nologin" {
		t.Errorf("pantalan user home:shell = %q, want /var/lib/pantalan:/usr/sbin/nologin", got)
	}
}

func unitProperties(t *testing.T, n Node, names ...string) map[string]string {
	t.Helper()
	out := sh(t, n, "systemctl show pantalan --property "+strings.Join(names, ","))
	props := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = v
		}
	}
	return props
}

func assertBinaryVersion(t *testing.T, n Node, want string) {
	t.Helper()
	if got := sh(t, n, "/usr/local/bin/pantalan version"); got != want {
		t.Errorf("pantalan version = %q, want the served release's %q", got, want)
	}
}

// fileLayout is the installed layout from the installer flow decision.
var fileLayout = []struct{ path, owner, mode, kind string }{
	{"/usr/local/bin/pantalan", "root:root", "755", "regular file"},
	{"/etc/pantalan/pantalan.env", "root:pantalan", "640", "regular file"},
	{"/var/lib/pantalan", "pantalan:pantalan", "750", "directory"},
	{"/var/lib/pantalan/pantalan.db", "pantalan:pantalan", "600", "regular file"},
	{"/var/lib/pantalan/tls/key.pem", "pantalan:pantalan", "600", "regular file"},
	{"/etc/systemd/system/pantalan.service", "root:root", "644", "regular file"},
	{"/var/log/pantalan-install.log", "root:root", "600", "regular file"},
}

func assertFileLayout(t *testing.T, n Node) {
	t.Helper()
	for _, f := range fileLayout {
		r := n.Run(t, Cmd{Script: "stat -c '%U:%G %a %F' " + f.path})
		want := f.owner + " " + f.mode + " " + f.kind
		if got := strings.TrimSpace(r.Stdout); r.Code != 0 || got != want {
			t.Errorf("%s: %q, want %q", f.path, strings.TrimSpace(r.Output()), want)
		}
	}
}

// pinned is an HTTPS client that trusts exactly the certificate whose
// fingerprint is fp, and never follows redirects.
func pinned(fp string) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // replaced by the pin below
			VerifyConnection: func(cs tls.ConnectionState) error {
				if got := fingerprint(cs.PeerCertificates[0]); got != fp {
					return fmt.Errorf("served certificate %s, want %s", got, fp)
				}
				return nil
			},
		}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// fingerprint is SHA256:AB:CD:… over the certificate's DER, worked out here
// rather than borrowed from the code under test.
func fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	hexes := make([]string, len(sum))
	for i, b := range sum {
		hexes[i] = fmt.Sprintf("%02X", b)
	}
	return "SHA256:" + strings.Join(hexes, ":")
}

func assertTLS(t *testing.T, addr, fp string) {
	t.Helper()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Errorf("TLS to the Dashboard at %s: %v", addr, err)
		return
	}
	defer conn.Close()
	if got := fingerprint(conn.ConnectionState().PeerCertificates[0]); got != fp {
		t.Errorf("the Dashboard serves certificate %s, but the installer printed %s", got, fp)
	}
}

func assertPlainHTTPRedirects(t *testing.T, addr string) {
	t.Helper()
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get("http://" + addr + "/login")
	if err != nil {
		t.Errorf("plain HTTP to the Dashboard: %v", err)
		return
	}
	resp.Body.Close()
	if want := "https://" + addr + "/login"; resp.StatusCode/100 != 3 || resp.Header.Get("Location") != want {
		t.Errorf("plain HTTP got %d to %q, want a redirect to %s", resp.StatusCode, resp.Header.Get("Location"), want)
	}
}

// assertAdminSignsIn: the printed credentials sign in over the pinned
// connection and get a __Host- session cookie that opens the Dashboard.
func assertAdminSignsIn(t *testing.T, addr string, want installed) {
	t.Helper()
	client := pinned(want.Fingerprint)
	resp, err := client.PostForm("https://"+addr+"/login", url.Values{"username": {want.AdminUser}, "password": {want.AdminPassword}})
	if err != nil {
		t.Errorf("sign in: %v", err)
		return
	}
	resp.Body.Close()
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, "__Host-") {
			session = c
		}
	}
	if resp.StatusCode != http.StatusSeeOther || session == nil {
		t.Errorf("sign in as %s got %d with cookies %v, want 303 and a __Host- cookie", want.AdminUser, resp.StatusCode, resp.Cookies())
		return
	}
	if !session.Secure || !session.HttpOnly {
		t.Errorf("session cookie %s is not Secure and HttpOnly", session.Name)
	}
	req, _ := http.NewRequest("GET", "https://"+addr+"/", nil)
	req.AddCookie(session)
	resp, err = client.Do(req)
	if err != nil {
		t.Errorf("open the Dashboard: %v", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the Dashboard with the session cookie got %d, want 200", resp.StatusCode)
	}
}

var adminTokenLine = regexp.MustCompile(`(?m)^PANTALAN_ADMIN_TOKEN=([0-9a-f]{64})$`)

// assertAdminToken: the token in the env file works on /api/v1 and is
// refused by the Dashboard.
func assertAdminToken(t *testing.T, n Node, addr, fp string) {
	t.Helper()
	m := adminTokenLine.FindStringSubmatch(sh(t, n, "cat /etc/pantalan/pantalan.env"))
	if m == nil {
		t.Errorf("/etc/pantalan/pantalan.env has no 32-byte hex PANTALAN_ADMIN_TOKEN")
		return
	}
	client := pinned(fp)
	for path, want := range map[string]int{"/api/v1/apps": http.StatusOK, "/": http.StatusUnauthorized} {
		req, _ := http.NewRequest("GET", "https://"+addr+path, nil)
		req.Header.Set("Authorization", "Bearer "+m[1])
		resp, err := client.Do(req)
		if err != nil {
			t.Errorf("GET %s with the Admin Token: %v", path, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("GET %s with the Admin Token got %d, want %d", path, resp.StatusCode, want)
		}
	}
}

// assertSSHFromOutside: an SSH session from the test machine to port still
// works, so the install didn't cut the operator off.
func assertSSHFromOutside(t *testing.T, n Node, port int) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	sh(t, n, "install -d -m 0700 /root/.ssh && echo "+shellQuote(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))))+" >> /root/.ssh/authorized_keys")
	client, err := ssh.Dial("tcp", net.JoinHostPort(n.Addr(), strconv.Itoa(port)), &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // a throwaway Node
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Errorf("SSH from outside to %s port %d: %v", n.Addr(), port, err)
		return
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		t.Errorf("SSH session: %v", err)
		return
	}
	defer session.Close()
	if out, err := session.CombinedOutput("echo connected"); err != nil || strings.TrimSpace(string(out)) != "connected" {
		t.Errorf("SSH command: %q, %v", out, err)
	}
}

// hostState is a digest of everything an install changes: packages, the
// apt sources it adds, Pantalan's paths, the service user and units. Two
// equal states mean the installer changed nothing on the host (its own log
// aside).
func hostState(t *testing.T, n Node) string {
	t.Helper()
	// Exit codes are part of what's compared, through the messages.
	return n.Run(t, Cmd{Script: `
		dpkg-query -W -f '${Package} ${Version} ${db:Status-Abbrev}\n'
		ls -la /etc/apt/sources.list.d /etc/apt/keyrings /etc/docker 2>&1
		ls -la /usr/local/bin /etc/pantalan /var/lib/pantalan /etc/systemd/system 2>&1
		getent passwd pantalan; getent group pantalan; getent group docker
		systemctl list-unit-files --no-legend pantalan.service docker.service 2>&1
	`}).Output()
}

// assertUntouched fails unless n is still in state before.
func assertUntouched(t *testing.T, n Node, before string) {
	t.Helper()
	if after := hostState(t, n); after != before {
		t.Errorf("the installer changed the host:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// assertFailedNaming fails unless r is a failed run whose output contains
// every one of want.
func assertFailedNaming(t *testing.T, r Result, want ...string) {
	t.Helper()
	if r.Code == 0 {
		t.Errorf("the installer exited 0, want a failure")
	}
	for _, w := range want {
		if !strings.Contains(r.Output(), w) {
			t.Errorf("the installer's output doesn't mention %q", w)
		}
	}
}
