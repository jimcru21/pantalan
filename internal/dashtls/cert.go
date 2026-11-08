// Package dashtls is the Dashboard listener's TLS: the Node's self-signed
// certificate, kept in $PANTALAN_DATA_DIR/tls and renewed before it
// expires, and a listener that turns plain HTTP away to HTTPS.
package dashtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// Validity is how long a generated certificate lasts: the longest
	// lifetime browsers have accepted, which keeps renewals rare.
	Validity = 825 * 24 * time.Hour
	// backdate absorbs clock skew between the Node and a client.
	backdate = time.Hour
	// RenewWithin is how close to expiry a certificate is replaced.
	RenewWithin = 30 * 24 * time.Hour

	certFile = "cert.pem"
	keyFile  = "key.pem"
)

// ErrNoCertificate is returned by Load when the directory holds no
// certificate yet.
var ErrNoCertificate = errors.New("no TLS certificate")

// Cert is a certificate and its private key, ready to serve.
type Cert struct {
	tls tls.Certificate
}

// Leaf is the parsed certificate.
func (c *Cert) Leaf() *x509.Certificate { return c.tls.Leaf }

// Fingerprint is the certificate's SHA-256 fingerprint as the Admin compares
// it: SHA256: then uppercase colon-separated hex over the DER.
func (c *Cert) Fingerprint() string {
	sum := sha256.Sum256(c.tls.Leaf.Raw)
	var b strings.Builder
	b.WriteString("SHA256:")
	for i, x := range sum {
		if i > 0 {
			b.WriteByte(':')
		}
		fmt.Fprintf(&b, "%02X", x)
	}
	return b.String()
}

// Load reads the certificate and key from dir. It returns an error
// wrapping ErrNoCertificate when either file is missing.
func Load(dir string) (*Cert, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, certFile))
	if err == nil {
		var keyPEM []byte
		keyPEM, err = os.ReadFile(filepath.Join(dir, keyFile))
		if err == nil {
			pair, err := tls.X509KeyPair(certPEM, keyPEM)
			if err != nil {
				return nil, fmt.Errorf("load TLS certificate from %s: %w", dir, err)
			}
			return &Cert{tls: pair}, nil
		}
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w in %s", ErrNoCertificate, dir)
	}
	return nil, err
}

// Generate creates a new self-signed certificate valid from now, writes it
// to dir (replacing any there) and returns it. It covers loopback, every
// global IP on the Node's interfaces and the Node's hostname.
func Generate(dir string, now time.Time) (*Cert, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	hostname, _ := os.Hostname()
	ips, err := nodeIPs()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Pantalan Dashboard"},
		NotBefore:             now.Add(-backdate),
		NotAfter:              now.Add(-backdate).Add(Validity),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           ips,
	}
	if hostname != "" {
		tmpl.DNSNames = []string{hostname}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, fmt.Errorf("create TLS certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	// A crash between the two renames leaves a pair that doesn't match,
	// which Load refuses rather than serving.
	if err := writeAtomic(dir, keyFile, keyPEM, 0o600); err != nil {
		return nil, err
	}
	if err := writeAtomic(dir, certFile, certPEM, 0o644); err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &Cert{tls: pair}, nil
}

// nodeIPs are the addresses a client can reach the Node on: loopback and
// every global unicast address on its interfaces right now. Private
// ranges count, since a Node in a VPC is reached on one.
func nodeIPs() ([]net.IP, error) {
	ips := []net.IP{net.IPv4(127, 0, 0, 1).To4(), net.IPv6loopback}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("list interface addresses: %w", err)
	}
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || !n.IP.IsGlobalUnicast() {
			continue
		}
		ip := n.IP
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		ips = append(ips, ip)
	}
	return ips, nil
}

// writeAtomic replaces dir/name with data so that a reader sees either the
// old file or the new one, never a partial write.
func writeAtomic(dir, name string, data []byte, perm fs.FileMode) error {
	f, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once renamed
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
