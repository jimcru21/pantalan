package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pantalan/pantalan/internal/dashtls"
)

// dataDir is the PANTALAN_DATA_DIR an environment from serveEnv points at.
func dataDir(t *testing.T, env []string) string {
	t.Helper()
	return lookupIn(env)("PANTALAN_DATA_DIR")
}

var fingerprintLine = regexp.MustCompile(`^SHA256:([0-9A-F]{2}:){31}[0-9A-F]{2}$`)

// fingerprint runs `pantalan tls fingerprint` against env's data dir.
func fingerprint(t *testing.T, env []string) string {
	t.Helper()
	got := runCLI(t, t.Context(), env, "tls", "fingerprint")
	if got.code != 0 {
		t.Fatalf("tls fingerprint exit code = %d, stderr %q", got.code, got.stderr)
	}
	fp := strings.TrimSuffix(got.stdout, "\n")
	if !fingerprintLine.MatchString(fp) {
		t.Fatalf("tls fingerprint printed %q, want SHA256:AB:CD:... (32 uppercase hex pairs)", got.stdout)
	}
	return fp
}

// pinnedClient is an HTTPS client that trusts exactly the certificate
// `pantalan tls fingerprint` reports for env's data dir, the way an Admin
// is told to check it. It never follows redirects.
func pinnedClient(t *testing.T, env []string) *http.Client {
	t.Helper()
	want := fingerprint(t, env)
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // self-signed: verified by pinning below
			VerifyConnection: func(cs tls.ConnectionState) error {
				if got := sha256Fingerprint(cs.PeerCertificates[0].Raw); got != want {
					return fmt.Errorf("server certificate %s, want %s", got, want)
				}
				return nil
			},
		}},
	}
}

// sha256Fingerprint is the spec's format, written out independently of the
// code under test: SHA256: then uppercase colon-separated hex of the DER.
func sha256Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	pairs := make([]string, len(sum))
	for i, b := range sum {
		pairs[i] = fmt.Sprintf("%02X", b)
	}
	return "SHA256:" + strings.Join(pairs, ":")
}

func TestServeOnAnEmptyDataDirServesHTTPSWithAGeneratedCertificate(t *testing.T) {
	env := serveEnv(t)
	s := startServe(t, env)
	dashboard := s.addr(t, "dashboard")

	tlsDir := filepath.Join(dataDir(t, env), "tls")
	if _, err := os.Stat(filepath.Join(tlsDir, "cert.pem")); err != nil {
		t.Errorf("cert.pem: %v", err)
	}
	key, err := os.Stat(filepath.Join(tlsDir, "key.pem"))
	if err != nil {
		t.Fatalf("key.pem: %v", err)
	}
	if mode := key.Mode().Perm(); mode != 0o600 {
		t.Errorf("key.pem mode = %o, want 600", mode)
	}

	resp, err := pinnedClient(t, env).Get("https://" + dashboard + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz over pinned HTTPS: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200", resp.StatusCode)
	}
}

func TestPlainHTTPOnTheDashboardPortRedirectsToHTTPS(t *testing.T) {
	s := startServe(t, serveEnv(t))
	dashboard := s.addr(t, "dashboard")
	plain := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	resp, err := plain.Get("http://" + dashboard + "/login?next=%2Fapps")
	if err != nil {
		t.Fatalf("plain GET: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusPermanentRedirect {
		t.Errorf("status = %d, want 308", resp.StatusCode)
	}
	want := "https://" + dashboard + "/login?next=%2Fapps"
	if got := resp.Header.Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
	if hsts := resp.Header.Get("Strict-Transport-Security"); hsts != "" {
		t.Errorf("Strict-Transport-Security = %q, want none", hsts)
	}
}

func TestPlainHTTPWithoutAPortInHostRedirectsToTheListenersPort(t *testing.T) {
	s := startServe(t, serveEnv(t))
	dashboard := s.addr(t, "dashboard")
	_, port, _ := net.SplitHostPort(dashboard)

	conn, err := net.DialTimeout("tcp", dashboard, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(conn, "GET /x HTTP/1.1\r\nHost: node.example\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	want := "https://node.example:" + port + "/x"
	if got := resp.Header.Get("Location"); got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

func TestServeRenewsACertificateWithUnder30DaysLeftAndLogsTheNewFingerprint(t *testing.T) {
	env := serveEnv(t)
	// 825-day certificate made 800 days ago: 25 days left.
	old, err := dashtls.Generate(filepath.Join(dataDir(t, env), "tls"), time.Now().Add(-800*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	s := startServe(t, env)
	cert := servedCertificate(t, env, s.addr(t, "dashboard"))

	newFP := fingerprint(t, env)
	if newFP == old.Fingerprint() {
		t.Fatal("tls fingerprint still reports the expiring certificate")
	}
	if left := time.Until(cert.NotAfter); left < 824*24*time.Hour {
		t.Errorf("served certificate has %v left, want a fresh 825-day one", left)
	}
	if !strings.Contains(s.stdout.String(), `msg="tls certificate renewed" fingerprint=`+newFP) {
		t.Errorf("log does not record the renewal with fingerprint %s; output:\n%s", newFP, s.stdout)
	}
}

func TestTLSRegenerateReplacesTheCertificateAndSaysToRestart(t *testing.T) {
	env := serveEnv(t)
	tlsDir := filepath.Join(dataDir(t, env), "tls")
	old, err := dashtls.Generate(tlsDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	got := runCLI(t, t.Context(), env, "tls", "regenerate")

	if got.code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", got.code, got.stderr)
	}
	newFP := fingerprint(t, env)
	if newFP == old.Fingerprint() {
		t.Fatal("the certificate on disk was not replaced")
	}
	if got.stdout != newFP+"\n" {
		t.Errorf("stdout = %q, want the new fingerprint %s", got.stdout, newFP)
	}
	if !strings.Contains(got.stderr, "systemctl restart pantalan") {
		t.Errorf("stderr = %q, want the restart command", got.stderr)
	}
	key, err := os.Stat(filepath.Join(tlsDir, "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := key.Mode().Perm(); mode != 0o600 {
		t.Errorf("key.pem mode = %o, want 600", mode)
	}
}

func TestTLSFingerprintWithoutACertificateFails(t *testing.T) {
	got := runCLI(t, t.Context(), serveEnv(t), "tls", "fingerprint")

	if got.code != 1 {
		t.Errorf("exit code = %d, want 1", got.code)
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want empty", got.stdout)
	}
	if !strings.Contains(got.stderr, "no TLS certificate") {
		t.Errorf("stderr = %q, want it to say there is no certificate", got.stderr)
	}
}

// servedCertificate is the leaf the Dashboard presents to a pinned client.
func servedCertificate(t *testing.T, env []string, addr string) *x509.Certificate {
	t.Helper()
	resp, err := pinnedClient(t, env).Get("https://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz over pinned HTTPS: %v", err)
	}
	resp.Body.Close()
	return resp.TLS.PeerCertificates[0]
}

func TestGeneratedCertificateIsAServerLeafForTheNodesAddresses(t *testing.T) {
	env := serveEnv(t)
	started := time.Now()
	s := startServe(t, env)
	cert := servedCertificate(t, env, s.addr(t, "dashboard"))

	if key, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok || key.Curve != elliptic.P256() {
		t.Errorf("public key = %T, want ECDSA P-256", cert.PublicKey)
	}
	if cert.IsCA {
		t.Error("certificate is a CA, want a leaf")
	}
	if !slices.Equal(cert.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) {
		t.Errorf("ExtKeyUsage = %v, want [serverAuth]", cert.ExtKeyUsage)
	}
	if cert.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Errorf("KeyUsage = %v, want DigitalSignature only", cert.KeyUsage)
	}

	wantIPs := []string{"127.0.0.1", "::1"}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.IsGlobalUnicast() {
			wantIPs = append(wantIPs, n.IP.String())
		}
	}
	var gotIPs []string
	for _, ip := range cert.IPAddresses {
		gotIPs = append(gotIPs, ip.String())
	}
	slices.Sort(wantIPs)
	slices.Sort(gotIPs)
	if !slices.Equal(gotIPs, wantIPs) {
		t.Errorf("IP SANs = %v, want %v", gotIPs, wantIPs)
	}
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cert.DNSNames, []string{hostname}) {
		t.Errorf("DNS SANs = %v, want [%s]", cert.DNSNames, hostname)
	}

	// Valid 825 days, starting an hour before it was made.
	if d := cert.NotAfter.Sub(cert.NotBefore); d != 825*24*time.Hour {
		t.Errorf("validity = %v, want 825 days", d)
	}
	early := started.Sub(cert.NotBefore)
	if early < time.Hour-time.Minute || early > time.Hour+time.Minute {
		t.Errorf("NotBefore is %v before startup, want 1h", early)
	}
}
