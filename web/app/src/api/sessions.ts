// The sign-in API (`GET /api/v1/login-info`, `POST /api/v1/sessions`,
// `DELETE /api/v1/sessions/current`). Every sign-in rule is the server's.
import type { LoginInfo, SignInRequest } from "@/api/generated/httpapi";
import { requestPublic } from "@/api/client";

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
  return requestPublic<void>("/api/v1/sessions/current", { method: "DELETE" });
}
