// The log streams: Server-Sent Events read with EventSource, never through
// TanStack Query. Both logs (the Deployment Log and the App Log) use this.
//
// Streams live in a store that <StreamsProvider> (in <App>) owns, not in
// the component showing them, so a stream opened with `keepRunning` goes
// on while the Admin is on another page and a page showing it again picks
// up the text it has gathered: nothing lost, nothing repeated. The browser
// reconnects a dropped stream on its own, sending the last event's id as
// Last-Event-ID; the server resumes from there.
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { StreamDone, StreamOmitted } from "@/api/generated/httpapi";
import { sessionState } from "@/api/sessions";

/**
 * How much text a log keeps: 1 MiB, the server's first replay. Older
 * output is dropped from the top, a whole line at a time.
 */
export const maxLogText = 1 << 20;

export type LogStream = {
  /** The output so far: at most maxLogText, starting at a line's start. */
  text: string;
  /** Earlier output isn't in `text`: the server skipped it, or it was dropped here. */
  omitted: boolean;
  /**
   * open: connected (or reconnecting after a drop); done: the server said
   * the log is complete. The other three end the stream for good: the
   * server refused a (re)connect, and the session check said why.
   * signed-out: the session has ended (a 401); refused: the session is
   * live, so the server refused the stream itself (its App or Deployment
   * is gone, say); unreachable: the session check failed too.
   */
  state: "open" | "done" | "signed-out" | "refused" | "unreachable";
  /** The data of the done event, e.g. the Deployment's final status. */
  done?: string;
  /** The last data of each of the extra named events asked for. */
  events: Record<string, string>;
};

export type StreamOptions = {
  /** Keep the stream going, until done, when no page shows it. */
  keepRunning?: boolean;
  /** Named events besides message, omitted and done to record in `events`. */
  events?: string[];
};

type Entry = {
  source: EventSource;
  snapshot: LogStream;
  listeners: Set<() => void>;
  keepRunning: boolean;
  events: string[];
};

/** The open streams, by URL. */
class Streams {
  private entries = new Map<string, Entry>();

  /** Subscribes to url's stream, opening it if it isn't open. */
  subscribe(url: string, options: StreamOptions, listener: () => void): () => void {
    const entry = this.entries.get(url) ?? this.open(url, options);
    entry.listeners.add(listener);
    return () => {
      entry.listeners.delete(listener);
      // After this commit: React may unmount and mount the same view at
      // once (StrictMode), which shouldn't reconnect.
      queueMicrotask(() => {
        if (entry.listeners.size > 0) return;
        if (entry.keepRunning && entry.snapshot.state === "open") return;
        this.drop(url, entry);
      });
    };
  }

  snapshot(url: string): LogStream | undefined {
    return this.entries.get(url)?.snapshot;
  }

  /**
   * Follows url's stream afresh, from the server's first answer: for a
   * stream that is done, to follow what comes next (the App Log of a new
   * Deployment). Whoever shows the old stream shows the new one.
   */
  restart(url: string): void {
    const entry = this.entries.get(url);
    if (!entry) return;
    entry.source.close();
    entry.source = new EventSource(url);
    entry.snapshot = notOpen;
    this.listen(url, entry);
    entry.listeners.forEach((l) => l());
  }

  closeAll(): void {
    for (const [url, entry] of this.entries) this.drop(url, entry);
  }

  private open(url: string, { keepRunning = false, events = [] }: StreamOptions): Entry {
    const entry: Entry = {
      source: new EventSource(url),
      snapshot: notOpen,
      listeners: new Set(),
      keepRunning,
      events,
    };
    this.entries.set(url, entry);
    this.listen(url, entry);
    return entry;
  }

  // Gathers what entry's EventSource sends into its snapshot.
  private listen(url: string, entry: Entry): void {
    const source = entry.source;
    const update = (change: Partial<LogStream>) => {
      if (entry.source !== source) return; // restarted since
      entry.snapshot = { ...entry.snapshot, ...change };
      entry.listeners.forEach((l) => l());
      // Nobody is looking and nothing more will come: let it go.
      if (entry.listeners.size === 0 && entry.snapshot.state !== "open") this.drop(url, entry);
    };

    source.addEventListener("message", (e) => update(append(entry.snapshot, e.data)));
    source.addEventListener(StreamOmitted, () => update({ omitted: true }));
    source.addEventListener(StreamDone, (e) => {
      source.close(); // or the browser would reconnect
      update({ state: "done", done: e.data });
    });
    for (const name of entry.events) {
      source.addEventListener(name, (e) =>
        update({ events: { ...entry.snapshot.events, [name]: e.data } }),
      );
    }
    source.addEventListener("error", () => {
      // CONNECTING: the browser is retrying a drop. CLOSED: the server
      // refused the stream (not 200 text/event-stream). An EventSource
      // can't read the status, so ask the session check whether it was a
      // 401 or the stream itself.
      if (source.readyState !== EventSource.CLOSED) return;
      void sessionState().then((session) =>
        update({ state: session === "live" ? "refused" : session }),
      );
    });
  }

  private drop(url: string, entry: Entry): void {
    entry.source.close();
    if (this.entries.get(url) === entry) this.entries.delete(url);
  }
}

// Appends text, dropping whole lines from the top past maxLogText.
function append(log: LogStream, text: string): Partial<LogStream> {
  const all = log.text + text;
  if (all.length <= maxLogText) return { text: all };
  const cut = all.indexOf("\n", all.length - maxLogText - 1);
  return { text: all.slice(cut < 0 ? all.length - maxLogText : cut + 1), omitted: true };
}

const StreamsContext = createContext<Streams | null>(null);

/** Owns the app's streams; closes them all when the app goes (a reload). */
export function StreamsProvider({ children }: { children: ReactNode }) {
  const [streams] = useState(() => new Streams());
  useEffect(() => () => streams.closeAll(), [streams]);
  return <StreamsContext.Provider value={streams}>{children}</StreamsContext.Provider>;
}

// What a stream shows before it has opened.
const notOpen: LogStream = { text: "", omitted: false, state: "open", events: {} };

/**
 * Follows the event stream at `url` (none while undefined): its text, as
 * the unnamed message events carry it, and its state. See StreamOptions.
 */
export function useLogStream(url: string | undefined, options: StreamOptions = {}): LogStream {
  const streams = useContext(StreamsContext);
  if (!streams) throw new Error("useLogStream needs a <StreamsProvider>");
  const { keepRunning } = options;
  const events = options.events?.join(" ") ?? ""; // a stable dependency
  const subscribe = useCallback(
    (listener: () => void) =>
      url
        ? streams.subscribe(url, { keepRunning, events: events ? events.split(" ") : [] }, listener)
        : () => {},
    [streams, url, keepRunning, events],
  );
  const getSnapshot = useCallback(
    () => (url ? streams.snapshot(url) : undefined) ?? notOpen,
    [streams, url],
  );
  return useSyncExternalStore(subscribe, getSnapshot);
}

/**
 * A function that follows the stream at `url` afresh (see
 * Streams.restart), for a view that shows it with useLogStream.
 */
export function useRestartLogStream(url: string | undefined): () => void {
  const streams = useContext(StreamsContext);
  if (!streams) throw new Error("useRestartLogStream needs a <StreamsProvider>");
  return useCallback(() => {
    if (url) streams.restart(url);
  }, [streams, url]);
}
