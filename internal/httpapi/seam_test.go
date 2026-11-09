package httpapi_test

import (
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/pantalan/pantalan/internal/dashboard"
	"github.com/pantalan/pantalan/internal/domain"
	"github.com/pantalan/pantalan/internal/httpapi"
	"github.com/pantalan/pantalan/internal/service"
	"github.com/pantalan/pantalan/internal/store/sqlite"
)

// The HTTP seam: httpapi.NewHandler with the Dashboard mounted, served
// over TLS by httptest, on the real services and a real SQLite store in a
// temp dir. Fakes stand in only at the edges (fakes_test.go). Tests drive
// it with HTTP requests and assert only on the status, headers and body
// of the response; the services and fakes are for setting a scene up.

// adminToken is the Admin Token the seam's handler is configured with.
const adminToken = "test-token"

type seam struct {
	t   *testing.T
	srv *httptest.Server
	// client follows no redirects and keeps no cookies, so a test sees
	// every 303 and sends exactly the credentials it means to.
	client *http.Client

	clock     *fakeClock
	hasher    *fakeHasher
	hashWait  *fakeTimer // AdminService's wait for a hash slot
	admins    *service.AdminService
	apps      *service.AppService
	deployer  *service.Deployer
	runtime   *fakeRuntime
	builder   *fakeBuilder
	router    *fakeRouter
	scheduler *fakeScheduler
	host      *fakeHost
	docker    *fakeDocker
	events    *fakeEvents
	dataDir   string // the Deployer's: Deployment Logs and build checkouts
}

// The built frontend the seam's Dashboard serves: what `vite build`
// writes to web/dist, in miniature. go test runs without Node, so the
// real web/dist holds only its placeholder.
const (
	indexHTML = `<!doctype html><html><head><script type="module" crossorigin src="/assets/index-B1x2y3z4.js"></script>` +
		`<link rel="stylesheet" crossorigin href="/assets/index-C5d6e7f8.css"></head><body><div id="root"></div></body></html>`
	bundleJS  = `console.log("the Dashboard");`
	bundleCSS = `body{margin:0}`
)

func builtFrontend() fs.FS {
	return fstest.MapFS{
		".gitkeep":                  {Data: []byte("placeholder\n")},
		"index.html":                {Data: []byte(indexHTML)},
		"assets/index-B1x2y3z4.js":  {Data: []byte(bundleJS)},
		"assets/index-C5d6e7f8.css": {Data: []byte(bundleCSS)},
	}
}

// seamOption changes the scene before the handler is built.
type seamOption func(*seamConfig)

type seamConfig struct {
	frontend fs.FS // the Dashboard's built SPA
}

// withFrontend serves files as the built SPA instead of builtFrontend.
func withFrontend(files fs.FS) seamOption {
	return func(c *seamConfig) { c.frontend = files }
}

func newSeam(t *testing.T, opts ...seamOption) *seam {
	t.Helper()
	cfg := seamConfig{frontend: builtFrontend()}
	for _, opt := range opts {
		opt(&cfg)
	}
	store, err := sqlite.Open(t.Context(), t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	log := slog.New(slog.DiscardHandler)

	s := &seam{
		t:         t,
		clock:     newFakeClock(),
		hasher:    &fakeHasher{},
		hashWait:  newFakeTimer(),
		runtime:   newFakeRuntime(),
		dataDir:   t.TempDir(),
		router:    newFakeRouter(),
		scheduler: &fakeScheduler{},
		host:      &fakeHost{},
		events:    &fakeEvents{},
	}
	s.builder = &fakeBuilder{docker: s.runtime}
	s.docker = &fakeDocker{clock: s.clock}
	s.admins = service.NewAdminService(service.AdminDeps{
		Credentials: store,
		Hasher:      s.hasher,
		Now:         s.clock.Now,
		After:       s.hashWait.After,
	})
	s.apps = service.NewAppService(store)
	s.deployer = service.NewDeployer(service.DeployerDeps{
		Apps:        store,
		Deployments: store,
		Source:      fakeSource{},
		Builders:    map[domain.BuildType]service.Builder{domain.BuildDockerfile: s.builder, domain.BuildNixpacks: s.builder},
		Runtime:     s.runtime,
		Health:      fakeHealth{},
		Router:      s.router,
		Scheduler:   s.scheduler,
		DataDir:     s.dataDir,
		Log:         log,
	})
	dash := dashboard.New(dashboard.Deps{
		Sessions: s.admins,
		Files:    cfg.frontend,
		Log:      log,
	})
	s.srv = httptest.NewTLSServer(httpapi.NewHandler(httpapi.Deps{
		Apps:          s.apps,
		Deployer:      s.deployer,
		Sessions:      s.admins,
		Hostname:      "node",
		Host:          s.host,
		Docker:        s.docker,
		Events:        s.events,
		Now:           s.clock.Now,
		AdminToken:    adminToken,
		WebhookSecret: "test-secret",
		Dashboard:     dash,
		Log:           log,
	}))
	t.Cleanup(s.srv.Close)
	s.client = s.srv.Client()
	s.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return s
}

// response is what a test may look at: status, headers and body.
type response struct {
	status int
	header http.Header
	body   string
}

// option changes a request before it is sent: a credential or a header.
type option func(*http.Request)

// withToken sends token as the bearer credential.
func withToken(token string) option {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

// withSession sends token as the session cookie, as the browser does.
func withSession(token string) option {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: httpapi.CookieName, Value: token}) }
}

func withHeader(name, value string) option {
	return func(r *http.Request) { r.Header.Set(name, value) }
}

// do sends method path with body (JSON when it isn't empty) and reads the
// whole response.
func (s *seam) do(method, path, body string, opts ...option) response {
	s.t.Helper()
	req, err := http.NewRequestWithContext(s.t.Context(), method, s.srv.URL+path, strings.NewReader(body))
	if err != nil {
		s.t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, opt := range opts {
		opt(req)
	}
	res, err := s.client.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		s.t.Fatalf("%s %s: read body: %v", method, path, err)
	}
	return response{status: res.StatusCode, header: res.Header, body: string(data)}
}

func (s *seam) get(path string, opts ...option) response {
	s.t.Helper()
	return s.do(http.MethodGet, path, "", opts...)
}

// adminPassword creates the Admin "alice", as `pantalan admin create`
// does, and returns the generated password.
func (s *seam) adminPassword() string {
	s.t.Helper()
	_, pw, err := s.admins.Create(s.t.Context(), "alice")
	if err != nil {
		s.t.Fatalf("create admin: %v", err)
	}
	return pw
}

// signedIn creates the Admin, signs them in and returns the session
// token a browser would hold in its cookie.
func (s *seam) signedIn() string {
	s.t.Helper()
	token, err := s.admins.SignIn(s.t.Context(), "alice", s.adminPassword())
	if err != nil {
		s.t.Fatalf("sign in: %v", err)
	}
	return token
}

// runQueued runs every Deployment queued so far to its end, as the
// queue's workers would.
func (s *seam) runQueued() {
	s.t.Helper()
	for _, id := range s.scheduler.take() {
		_ = s.deployer.Run(s.t.Context(), id) // a failed Deployment is stored as failed
	}
}
