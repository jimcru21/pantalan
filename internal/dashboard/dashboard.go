// Package dashboard serves the Dashboard: the login screen and the
// session-only pages behind it, rendered with html/template from the
// assets in package web and made live with htmx (ADR 0002). It calls the
// service layer directly, never /api/v1, and accepts only the session
// cookie: the Admin Token is refused here.
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
)

// Auth is what the Dashboard needs to sign the Admin in and out and to
// check sessions; service.AuthService provides it.
type Auth interface {
	AdminExists(ctx context.Context) (bool, error)
	// SignIn returns a new session token, or service.ErrNoAdmin or
	// service.ErrBadCredentials.
	SignIn(ctx context.Context, username, password string) (token string, err error)
	// Authenticate returns service.ErrNoSession unless token names a live
	// session, and extends it.
	Authenticate(ctx context.Context, token string) error
	SignOut(ctx context.Context, token string) error
}

type Deps struct {
	Auth Auth
	// Host is the Node's host health for the Node overview; nil shows it
	// as unavailable.
	Host Host
	// Docker is the Node's Docker for the Node overview; nil shows it as
	// unreachable.
	Docker Docker
	// Events is Docker's recent container events for the Events tab; nil
	// shows none.
	Events Events
	// Apps names the apps whose containers the Containers tab badges.
	Apps Apps
	// Templates and Static are web.Templates() and web.Static().
	Templates fs.FS
	Static    fs.FS
	// AssetVersion is the path segment static files are served under,
	// /static/<AssetVersion>/, and must change whenever they do.
	AssetVersion string
	// Hostname is the Node's, shown on the login card.
	Hostname string
	Log      *slog.Logger
}

// Dashboard is the http.Handler for every Dashboard route. Mount it at "/"
// next to /api/v1, /webhooks and /healthz.
type Dashboard struct {
	deps    Deps
	log     *slog.Logger
	views   *views
	pages   []page
	handler http.Handler
}

// page is one entry in the sidebar, served at path behind a session.
// Its template is templates/pages/<name>.html, which defines "main".
type page struct {
	name  string
	path  string // URL path; "/" is the Dashboard's home
	label string
	icon  string // templates/icons/<icon>.svg
	// data is what the page's "main" template gets as its dot.
	data func(*http.Request) (any, error)
	// fragments are the page's polled regions. Each is served at
	// GET /fragments/<name> from the template block of the same name in
	// the page's file, which "main" also renders, so the first load and
	// every poll share markup.
	fragments []fragment
}

// fragment is one polled region of a page.
type fragment struct {
	name string
	// data is the block's dot; nil means the page's data.
	data func(*http.Request) (any, error)
}

// New parses the templates and builds the routes. A template that fails
// to parse is an error here, so a broken template fails startup.
func New(deps Deps) (*Dashboard, error) {
	if deps.AssetVersion == "" {
		return nil, errors.New("dashboard: AssetVersion is required")
	}
	d := &Dashboard{deps: deps, log: deps.Log}
	if d.log == nil {
		d.log = slog.New(slog.DiscardHandler)
	}

	// The sidebar, in order. Add a page here and in templates/pages/.
	d.pages = []page{
		{
			name: "overview", path: "/", label: "Node overview", icon: "overview", data: d.overviewPage,
			fragments: []fragment{
				{name: "topbar", data: d.overviewData},
				{name: "band", data: d.overviewData},
				{name: "containers", data: d.containersData},
				{name: "images", data: d.imagesData},
				{name: "docker", data: d.dockerData},
				{name: "events", data: d.eventsData},
			},
		},
	}

	names := make([]string, len(d.pages))
	for i, p := range d.pages {
		names[i] = p.name
	}
	var err error
	if d.views, err = parseViews(deps.Templates, d.assetBase(), names); err != nil {
		return nil, err
	}
	for _, p := range d.pages {
		for _, f := range p.fragments {
			if d.views.pages[p.name].Lookup(f.name) == nil {
				return nil, fmt.Errorf("parse templates: pages/%s.html does not define fragment %q", p.name, f.name)
			}
		}
	}

	mux := http.NewServeMux()
	mux.Handle("GET /static/{version}/{file...}", d.static())

	mux.Handle("GET /login", noStore(http.HandlerFunc(d.loginPage)))
	mux.Handle("POST /login", noStore(http.HandlerFunc(d.login)))
	mux.Handle("POST /logout", noStore(http.HandlerFunc(d.logout)))
	for _, p := range d.pages {
		pattern := "GET " + p.path
		if p.path == "/" {
			pattern = "GET /{$}"
		}
		mux.Handle(pattern, noStore(d.requireSession(d.pageHandler(p))))
		for _, f := range p.fragments {
			mux.Handle("GET /fragments/"+f.name, noStore(d.requireSession(d.fragmentHandler(p, f))))
		}
	}
	// Anything else is a 404, but only once signed in, so the route list
	// isn't probed from outside.
	mux.Handle("/", noStore(d.requireSession(http.NotFoundHandler())))

	d.handler = securityHeaders(refuseAdminToken(http.NewCrossOriginProtection().Handler(mux)))
	return d, nil
}

func (d *Dashboard) ServeHTTP(w http.ResponseWriter, r *http.Request) { d.handler.ServeHTTP(w, r) }

// shellData is what shell.html renders around a page.
type shellData struct {
	Title string
	Nav   []navLink
	Page  any // the page's own data, the dot of its "main"
}

type navLink struct {
	Href, Label, Icon string
	Active            bool
}

func (d *Dashboard) pageHandler(p page) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := p.data(r)
		if err != nil {
			d.log.Error("dashboard page", "page", p.name, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		nav := make([]navLink, len(d.pages))
		for i, q := range d.pages {
			nav[i] = navLink{Href: q.path, Label: q.label, Icon: q.icon, Active: q.name == p.name}
		}
		d.render(w, http.StatusOK, d.views.pages[p.name], "base", shellData{Title: p.label, Nav: nav, Page: data})
	})
}

// fragmentHandler serves one polled region of page p on its own.
func (d *Dashboard) fragmentHandler(p page, f fragment) http.Handler {
	data := f.data
	if data == nil {
		data = p.data
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dot, err := data(r)
		if err != nil {
			d.log.Error("dashboard fragment", "fragment", f.name, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		d.render(w, http.StatusOK, d.views.pages[p.name], f.name, dot)
	})
}
