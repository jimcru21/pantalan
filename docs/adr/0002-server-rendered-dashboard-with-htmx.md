# Server-rendered Dashboard with htmx and no build step

The Dashboard is rendered by Go `html/template` and made live with htmx 2.x. Templates, one hand-written stylesheet, a small `app.js` and a vendored, minified htmx are embedded into the binary from `web/`, and the session-only handlers live in `internal/dashboard`. There is no Node toolchain, bundler or `package.json`. The v1 Dashboard is read-only and refreshes by polling server-rendered fragments, which is htmx's core feature. Cookies and same-origin requests already satisfy `http.CrossOriginProtection`, and keeping the frontend in Go means the binary and the repo need nothing beyond the Go toolchain.

## Considered Options

- **SPA (Svelte or React) building into `web/dist`.** Rejected: it adds a second toolchain, a build step before `go build`, and a JSON surface for the Dashboard that duplicates the token-only `/api/v1`, all for a read-only page.
- **Plain HTML and vanilla JS.** Rejected: it means hand-writing the fragment fetching, swapping and error handling that htmx already provides.

## Consequences

- The CSP is strict (`script-src 'self'; style-src 'self'`), so `htmx.config.allowEval` is false: there are no trigger filters or `hx-on`, and templates carry no `style=` attributes. Bars and sparklines are server-rendered SVG geometry or `<meter>`.
- Login and logout are plain form posts that work without JS. htmx is used only for reads.
- htmx 4.x changes attribute semantics, so moving to it is a deliberate upgrade, not a version bump.
- Revisit this choice if the Dashboard grows heavy client-side state, such as a web terminal or an interactive editor.
