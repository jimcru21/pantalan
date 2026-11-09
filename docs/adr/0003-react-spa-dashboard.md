---
status: accepted
---

# The Dashboard is a React SPA over the one `/api/v1`

The Dashboard becomes a single-page app in React, Vite and TypeScript, built from `web/app` into a gitignored `web/dist` that the binary embeds. This supersedes [ADR 0002](0002-server-rendered-dashboard-with-htmx.md). With Apps and Deployments, the Dashboard needs what ADR 0002 named as its reason to revisit: client-side state. Pages should change without a reload, actions should give feedback at once, a Deployment Log should keep streaming while the Admin moves to another page, and charts should answer hover. A web terminal should also be cheap to add later. The SPA calls the same `/api/v1` that automation uses. `/api/v1` now accepts either the Admin Token or the session cookie, so there is still one API, and anything the Dashboard can do, automation can do too.

## Considered Options

- **Keep htmx and add `hx-boost`, View Transitions and `hx-indicator`.** Rejected: it gives page changes without a reload and spinners on buttons, but not state that outlives a page or rich widgets, and the Dashboard needs both.
- **Svelte 5.** A close second, with smaller bundles and CSS compiled to files. Rejected: React has the larger ecosystem (charts, xterm.js) and agents write it more reliably. Most of this repo is written by agents.
- **Angular.** Rejected: heavy for a Dashboard with one Admin, and it injects `<style>` elements at runtime, so the CSP would need a nonce.
- **A JSON API just for the Dashboard, behind the cookie.** Rejected: it duplicates `/api/v1`, which was ADR 0002's main objection to an SPA.
- **Committing the built `web/dist`.** Rejected: every frontend change would carry a generated diff, and hashed bundles make miserable merge conflicts.

## Consequences

- Building the repo now needs Node. `make build`, `release.sh` and CI build the frontend first and install with `npm ci --ignore-scripts`. `go build` and `go test` without Node still compile and serve a "frontend not built" page, so Go work never waits on Node.
- The CSP is unchanged (`script-src 'self'; style-src 'self'`, no eval). That rules out CSS-in-JS libraries that inject `<style>`, inline scripts in `index.html`, and shadcn/ui's chart wrapper. React's `style` prop is fine, because it writes through the CSSOM. Tailwind compiles to a file. Playwright smoke tests against the built binary catch CSP violations.
- Sign-in is a React screen over `/api/v1`. It no longer works without JavaScript. Every sign-in rule (throttling, the same 401 for any wrong credential, no Admin, too many hashes at once) stays in `AdminService`.
- Requests that carry the cookie go through `http.CrossOriginProtection`, and token requests skip it. A page request without a session is redirected to `/login` before any app code loads.
- The `nodeinfo` exception, where Node overview reads skip the service layer, moves from `internal/dashboard` to `httpapi`. `internal/dashboard` keeps only serving the SPA, the security headers and the session gate.
- The API returns raw values (bytes, ratios, RFC 3339 times), and the browser formats them. Rules such as the Node's status level stay in Go.
- The TypeScript types for API responses are generated from the Go structs and checked for freshness in CI, so a renamed field breaks `tsc` rather than the page.
- A runtime npm dependency needs a stated reason in its pull request. This is the frontend's version of "standard library first".
