// The one way the SPA talks to /api/v1. Requests carry the session cookie
// (fetch sends it to the same origin by default). Use `request` for every
// call behind sign-in: a 401 from it means the session has ended, and the
// browser is sent to the sign-in screen (see onSignedOut). Only the
// sign-in screen itself uses `requestPublic`, where a 401 is a wrong
// password, not a signed-out browser.

/**
 * An API answer other than 2xx: its status, its {"error"} message, the
 * request body field the error is about (if any), and Retry-After.
 */
export class ApiError extends Error {
  readonly status: number;
  /** Retry-After, in seconds, when the response had one. */
  readonly retryAfter?: number;
  /** The JSON name of the body field the error is about, e.g. "domain". */
  readonly field?: string;

  constructor(status: number, message: string, retryAfter?: number, field?: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.retryAfter = retryAfter;
    this.field = field;
  }
}

/** A 401 behind sign-in: the session ended. The browser is already on its way to /login. */
export class SignedOutError extends ApiError {
  constructor() {
    super(401, "signed out");
    this.name = "SignedOutError";
  }
}

let signedOutHandlers: (() => void)[] = [];

/**
 * Runs `handler` on every 401 from `request`. The app registers the one
 * that sends the browser to /login; the return value unregisters it.
 */
export function onSignedOut(handler: () => void): () => void {
  signedOutHandlers.push(handler);
  return () => {
    signedOutHandlers = signedOutHandlers.filter((h) => h !== handler);
  };
}

/** A call behind sign-in. Resolves to the JSON body (undefined for 204). */
export async function request<T>(path: string, init?: RequestOptions): Promise<T> {
  try {
    return await requestPublic<T>(path, init);
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      signedOutHandlers.forEach((handler) => handler());
      throw new SignedOutError();
    }
    throw err;
  }
}

/** A JSON body to send, or none. */
export type RequestOptions = Omit<RequestInit, "body"> & { json?: unknown };

/** A call that needs no session: only the sign-in routes. */
export async function requestPublic<T>(
  path: string,
  { json, headers, ...init }: RequestOptions = {},
): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: {
      Accept: "application/json",
      ...(json === undefined ? {} : { "Content-Type": "application/json" }),
      ...headers,
    },
    body: json === undefined ? undefined : JSON.stringify(json),
  });
  if (!res.ok) {
    const { message, field } = await errorBody(res);
    throw new ApiError(res.status, message, retryAfter(res), field);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

// The API's ErrorResponse body, {"error": "...", "field"?: "..."}, or the
// status text when there's none (e.g. the plain-text 401 or the
// cross-origin 403).
async function errorBody(res: Response): Promise<{ message: string; field?: string }> {
  try {
    const body = (await res.json()) as { error?: unknown; field?: unknown };
    if (typeof body.error === "string") {
      const field = typeof body.field === "string" && body.field !== "" ? body.field : undefined;
      return { message: body.error, field };
    }
  } catch {
    // not JSON
  }
  return { message: res.statusText || `HTTP ${res.status}` };
}

function retryAfter(res: Response): number | undefined {
  const value = res.headers.get("Retry-After");
  if (value === null) return undefined;
  const seconds = Number(value);
  return Number.isFinite(seconds) && seconds >= 0 ? seconds : undefined;
}
