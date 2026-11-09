// The address a signed-out Admin asked for travels to the sign-in screen
// as `/login?next=<path>`, both from the server's page gate and from the
// SPA's own 401 redirect, and sign-in returns there.

/** The sign-in screen's address, remembering `path` (path, query and hash) to return to. */
export function loginPath(path: string): string {
  const next = sameOriginPath(path);
  return next === "/" ? "/login" : `/login?next=${encodeURIComponent(next)}`;
}

/** Where sign-in goes on to: the `next` in a sign-in screen's query, if it's ours. */
export function returnPath(search: string): string {
  return sameOriginPath(new URLSearchParams(search).get("next") ?? "/");
}

// A path on this origin, or "/": `next` is attacker-controlled (anyone can
// send a link to /login?next=...), so it must never lead off the Node,
// nor back to the sign-in screen.
function sameOriginPath(raw: string): string {
  if (!raw.startsWith("/")) return "/";
  let url: URL;
  try {
    url = new URL(raw, window.location.origin);
  } catch {
    return "/";
  }
  if (url.origin !== window.location.origin || url.pathname === "/login") return "/";
  return url.pathname + url.search + url.hash;
}
