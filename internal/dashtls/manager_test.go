package dashtls_test

import (
	"testing"
	"time"

	"github.com/pantalan/pantalan/internal/dashtls"
)

const day = 24 * time.Hour

// served is the fingerprint-bearing leaf a TLS handshake would get now.
func served(t *testing.T, m *dashtls.Manager) []byte {
	t.Helper()
	c, err := m.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	return c.Leaf.Raw
}

func TestACertificateWithUnder30DaysLeftIsReplacedInService(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC)
	// Made 796 days ago: 29 days of its 825 left.
	old, err := dashtls.Generate(dir, now.Add(-796*day))
	if err != nil {
		t.Fatal(err)
	}
	m, generated, err := dashtls.Open(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if generated {
		t.Fatal("Open generated a certificate although one was on disk")
	}
	before := served(t, m)

	renewed, err := m.RenewIfExpiring(now)
	if err != nil {
		t.Fatal(err)
	}

	if !renewed {
		t.Fatal("RenewIfExpiring did not renew a certificate with 29 days left")
	}
	if string(served(t, m)) == string(before) {
		t.Error("the same certificate is still being served")
	}
	if got, want := m.Current().Leaf().NotAfter, now.Add(824*day+23*time.Hour); !got.Equal(want) {
		t.Errorf("new NotAfter = %v, want %v", got, want)
	}
	onDisk, err := dashtls.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if onDisk.Fingerprint() != m.Current().Fingerprint() {
		t.Errorf("certificate on disk %s, in service %s; want the same", onDisk.Fingerprint(), m.Current().Fingerprint())
	}
	if onDisk.Fingerprint() == old.Fingerprint() {
		t.Error("the old certificate is still on disk")
	}
}

func TestACertificateWith30DaysLeftIsKept(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC)
	// NotAfter is 825 days after NotBefore, which is an hour before creation.
	if _, err := dashtls.Generate(dir, now.Add(-795*day).Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	m, _, err := dashtls.Open(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	before := m.Current().Fingerprint()

	renewed, err := m.RenewIfExpiring(now)
	if err != nil {
		t.Fatal(err)
	}

	if renewed || m.Current().Fingerprint() != before {
		t.Error("a certificate with exactly 30 days left was replaced")
	}
}
