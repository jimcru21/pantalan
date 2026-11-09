// The sign-in API (`GET /api/v1/login-info`, `POST /api/v1/sessions`,
// `GET` and `DELETE /api/v1/sessions/current`). Every sign-in rule is the
// server's.
import { useQuery } from "@tanstack/react-query";
import type { LoginInfo, SignInRequest } from "@/api/generated/httpapi";
import { ApiError, request, requestPublic } from "@/api/client";

const currentSession = "/api/v1/sessions/current";

export function fetchLoginInfo(): Promise<LoginInfo> {
  return requestPublic<LoginInfo>("/api/v1/login-info");
}

/**
 * Starts a session; the server sets its cookie. Rejects with an ApiError:
 * 401 wrong username or password, 429 throttled (retryAfter), 403 no
 * Admin, 503 too many sign-ins at once.
 */
export function signIn(credentials: SignInRequest): Promise<void> {
  return requestPublic<void>("/api/v1/sessions", { method: "POST", json: credentials });
}

/** Ends this browser's session on the server and clears its cookie. */
export function signOut(): Promise<void> {
  return requestPublic<void>(currentSession, { method: "DELETE" });
}

/** How often an open Dashboard page checks that its session is still live. */
export const sessionCheckMs = 30_000;

/**
 * Checks the session every 30 s while the tab is shown, and again as soon
 * as it's shown after being hidden, so a page that never polls (the env
 * editor, a finished Deployment) still goes to sign in within a minute of
 * its session ending: signed out elsewhere, expired, or ended by `admin
 * reset-password`. A 401 goes through `request`'s redirect to /login. The
 * check (GET /api/v1/sessions/current) never extends the session, so an
 * idle tab still signs out after an hour. The shell runs it for every
 * page behind sign-in.
 */
export function useSessionCheck() {
  useQuery({
    queryKey: ["session"],
    queryFn: () => request<void>(currentSession).then(() => null),
    // The page gate checked the session to serve this page: no check on
    // mount, the first one is 30 s in.
    initialData: null,
    refetchOnMount: false,
    refetchInterval: sessionCheckMs,
    refetchOnWindowFocus: "always",
    retry: false,
  });
}

/**
 * Whether this browser's session is live, for a caller that says what it
 * means itself: no redirect to /login on a 401. "unreachable" when the
 * server didn't answer, or answered something else. Like the shell's
 * check, it doesn't extend the session.
 */
export async function sessionState(): Promise<"live" | "signed-out" | "unreachable"> {
  try {
    await requestPublic<void>(currentSession);
    return "live";
  } catch (err) {
    return err instanceof ApiError && err.status === 401 ? "signed-out" : "unreachable";
  }
}
