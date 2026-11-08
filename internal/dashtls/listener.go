package dashtls

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// recordTypeHandshake is the first byte of every TLS connection: the
// ClientHello's record header.
const recordTypeHandshake = 0x16

// peekTimeout bounds how long a connection may stay silent before it says
// whether it speaks TLS.
const peekTimeout = 10 * time.Second

// RedirectPlainHTTP wraps ln so that Accept returns only connections that
// open with a TLS handshake. Anything else is taken for plain HTTP and
// answered, on the side, with a redirect to the same host, port and path
// over https. No HSTS header is sent.
//
// Serve the result with http.Server.ServeTLS. Closing it closes ln and
// drops the plain-HTTP connections still open.
func RedirectPlainHTTP(ln net.Listener) net.Listener {
	l := &splitListener{
		Listener:  ln,
		tls:       make(chan net.Conn),
		plain:     &chanListener{addr: ln.Addr(), conns: make(chan net.Conn), done: make(chan struct{})},
		done:      make(chan struct{}),
		acceptErr: make(chan error),
	}
	l.redirects = &http.Server{
		Handler:           http.HandlerFunc(redirectToHTTPS),
		ReadHeaderTimeout: peekTimeout,
		IdleTimeout:       peekTimeout,
	}
	go l.redirects.Serve(l.plain)
	go l.acceptLoop()
	return l
}

type splitListener struct {
	net.Listener
	tls       chan net.Conn
	plain     *chanListener
	redirects *http.Server
	acceptErr chan error
	done      chan struct{}
	closeOnce sync.Once
}

func (l *splitListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.tls:
		return c, nil
	case err := <-l.acceptErr:
		return nil, err
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *splitListener) Close() error {
	var err error
	l.closeOnce.Do(func() {
		close(l.done)
		err = l.Listener.Close()
		close(l.plain.done)
		l.redirects.Close()
	})
	return err
}

// acceptLoop accepts raw connections and sorts each one in its own
// goroutine, so that a client that never speaks can't hold up the rest.
func (l *splitListener) acceptLoop() {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			// Hand the error to Accept, which decides (as http.Server
			// does for temporary ones) whether to call again.
			select {
			case l.acceptErr <- err:
			case <-l.done:
				return
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go l.sort(c)
	}
}

func (l *splitListener) sort(c net.Conn) {
	c.SetReadDeadline(time.Now().Add(peekTimeout))
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	if err != nil {
		c.Close()
		return
	}
	c.SetReadDeadline(time.Time{})
	pc := &peekedConn{Conn: c, r: br}
	to := l.tls
	if first[0] != recordTypeHandshake {
		to = l.plain.conns
	}
	select {
	case to <- pc:
	case <-l.done:
		c.Close()
	}
}

// peekedConn is a connection whose first bytes were read into r.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// chanListener is a net.Listener fed from a channel.
type chanListener struct {
	addr  net.Addr
	conns chan net.Conn
	done  chan struct{}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *chanListener) Close() error   { return nil } // closed with its splitListener
func (l *chanListener) Addr() net.Addr { return l.addr }

// redirectToHTTPS sends the client to the same URL over https. The Host
// header carries the port the client used; when it has none, the
// listener's own port is added, since this port never serves on 443.
func redirectToHTTPS(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		local, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		if local == nil {
			http.Error(w, "use https", http.StatusBadRequest)
			return
		}
		localHost, port, _ := net.SplitHostPort(local.String())
		if host == "" {
			host = localHost
		}
		host = net.JoinHostPort(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"), port)
	}
	w.Header().Set("Connection", "close")
	http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusPermanentRedirect)
}
