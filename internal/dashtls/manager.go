package dashtls

import (
	"crypto/tls"
	"errors"
	"sync/atomic"
	"time"
)

// Manager serves the certificate in a directory and replaces it, without
// a restart, when it nears expiry.
type Manager struct {
	dir string
	cur atomic.Pointer[Cert]
}

// Open loads the certificate in dir, generating one first when there is
// none. generated reports whether it did.
func Open(dir string, now time.Time) (m *Manager, generated bool, err error) {
	c, err := Load(dir)
	if errors.Is(err, ErrNoCertificate) {
		c, err = Generate(dir, now)
		generated = true
	}
	if err != nil {
		return nil, false, err
	}
	m = &Manager{dir: dir}
	m.cur.Store(c)
	return m, generated, nil
}

// Current is the certificate being served.
func (m *Manager) Current() *Cert { return m.cur.Load() }

// GetCertificate serves the current certificate; it is meant for
// tls.Config.GetCertificate.
func (m *Manager) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return &m.cur.Load().tls, nil
}

// RenewIfExpiring replaces the certificate, on disk and in service, when
// fewer than RenewWithin remain at now. renewed reports whether it did. On
// error the current certificate stays in service.
func (m *Manager) RenewIfExpiring(now time.Time) (renewed bool, err error) {
	if m.Current().Leaf().NotAfter.Sub(now) >= RenewWithin {
		return false, nil
	}
	c, err := Generate(m.dir, now)
	if err != nil {
		return false, err
	}
	m.cur.Store(c)
	return true, nil
}
