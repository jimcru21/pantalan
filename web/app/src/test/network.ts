// The SPA's network seam: the only things tests stub are `fetch` and
// `EventSource`. Everything above them (routes, hooks, components, TanStack
// Query) is the real app.
import { vi } from "vitest";

/** A request the app made, as the stubbed server saw it. */
export type ApiCall = { method: string; path: string; request: Request };

/**
 * The stubbed API: "METHOD /path" (path without the query string) to a
 * handler. Build responses with json() so fixtures are checked against the
 * generated API types.
 */
export type Api = Record<string, (request: Request) => Response | Promise<Response>>;

/** A JSON response whose body must have the API type T. */
export function json<T>(body: T, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    ...init,
    headers: { "Content-Type": "application/json", ...init.headers },
  });
}

/** An empty response with a status, e.g. 204 or 401. */
export function status(code: number, headers: HeadersInit = {}): Response {
  return new Response(null, { status: code, headers });
}

// Requests no handler answered. setup.ts fails the test on any, so a page
// that calls an endpoint the test didn't expect can't pass by accident.
const unhandled: string[] = [];

export function takeUnhandled(): string[] {
  return unhandled.splice(0);
}

/**
 * Replaces fetch with the stubbed API and returns every call it served.
 * `defaults` answers what `api` has no handler for. Both are read at each
 * request, so a handler added later (a stateful fixture) is found.
 */
export function stubFetch(api: Api, defaults: Api = {}): ApiCall[] {
  const calls: ApiCall[] = [];
  vi.stubGlobal("fetch", async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = new Request(
      new URL(String(input instanceof Request ? input.url : input), window.location.href),
      init,
    );
    const url = new URL(request.url);
    const key = `${request.method} ${url.pathname}`;
    calls.push({ method: request.method, path: url.pathname + url.search, request });
    const handler = api[key] ?? defaults[key];
    if (!handler) {
      unhandled.push(key);
      return status(599);
    }
    return handler(request);
  });
  return calls;
}

/**
 * A stand-in EventSource the test drives: find the stream the app opened
 * with FakeEventSource.latest(url) and push events into it.
 */
export class FakeEventSource extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  static instances: FakeEventSource[] = [];

  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSED = 2;
  readonly url: string;
  readonly withCredentials: boolean;
  readyState = 0;
  onopen: ((ev: Event) => unknown) | null = null;
  onmessage: ((ev: MessageEvent) => unknown) | null = null;
  onerror: ((ev: Event) => unknown) | null = null;

  constructor(url: string | URL, init?: EventSourceInit) {
    super();
    this.url = new URL(String(url), window.location.href).toString();
    this.withCredentials = init?.withCredentials ?? false;
    FakeEventSource.instances.push(this);
  }

  /** The newest stream opened on a path (or full URL), if any. */
  static latest(path: string): FakeEventSource | undefined {
    return FakeEventSource.instances.findLast((s) => {
      const url = new URL(s.url);
      return s.url === path || url.pathname + url.search === path || url.pathname === path;
    });
  }

  close(): void {
    this.readyState = this.CLOSED;
  }

  /** The server accepted the connection. */
  open(): void {
    this.readyState = this.OPEN;
    this.fire(new Event("open"));
  }

  /** The server sent an event; type "message" is the unnamed default. */
  emit(data: string, { type = "message", id = "" }: { type?: string; id?: string } = {}): void {
    this.fire(new MessageEvent(type, { data, lastEventId: id, origin: new URL(this.url).origin }));
  }

  /** The connection dropped; `closed` is a failure the browser won't retry (e.g. a 401). */
  fail({ closed = false }: { closed?: boolean } = {}): void {
    this.readyState = closed ? this.CLOSED : this.CONNECTING;
    this.fire(new Event("error"));
  }

  private fire(event: Event): void {
    if (this.readyState === this.CLOSED && event.type !== "error") return;
    this.dispatchEvent(event);
    const handler = { open: this.onopen, message: this.onmessage, error: this.onerror }[event.type];
    handler?.call(this, event as MessageEvent);
  }
}

export function stubEventSource(): typeof FakeEventSource {
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
  return FakeEventSource;
}
