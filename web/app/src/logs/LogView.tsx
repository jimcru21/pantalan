// A streamed log on screen: both logs (the Deployment Log and the App Log)
// show their LogStream with this.
import { useLayoutEffect, useRef, type ReactNode } from "react";
import type { LogStream } from "@/api/streams";

// Why a stream ended for good without done.
const refusals: Partial<Record<LogStream["state"], string>> = {
  "signed-out": "Signed out, reload to sign in.",
  refused: "The server refused this log stream. Reload to try again.",
  unreachable: "Can't reach Pantalan to check the session. Reload to try again.",
};

/**
 * The log's text in a scroll box (role log, named by `label`) that stays
 * pinned to the newest line unless the Admin has scrolled up to read.
 * Starts with an "Earlier output omitted" line when earlier output isn't
 * shown, ends with "End of the log." once the stream is done (`end`
 * replaces that line), and says why when the server refused the stream:
 * signed out, or refused with the session live, or unreachable. `empty`
 * shows while there's no output yet.
 */
export function LogView({
  stream,
  label,
  empty = "No output yet.",
  end = "End of the log.",
}: {
  stream: LogStream;
  label: string;
  empty?: ReactNode;
  end?: ReactNode;
}) {
  const box = useRef<HTMLDivElement>(null);
  const refusal = refusals[stream.state];
  // Whether the view follows new output: until the Admin scrolls up.
  const pinned = useRef(true);

  useLayoutEffect(() => {
    const el = box.current;
    if (el && pinned.current) el.scrollTop = el.scrollHeight;
  }, [stream.text, stream.omitted]);

  const onScroll = () => {
    const el = box.current;
    if (el) pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight <= 4;
  };

  return (
    <div className="grid">
      <div
        ref={box}
        role="log"
        aria-label={label}
        // Focusable, so the keyboard can scroll it.
        tabIndex={0}
        onScroll={onScroll}
        className="max-h-[60vh] min-h-24 overflow-auto bg-secondary px-4 py-3 font-mono text-[12px] leading-[1.55]"
      >
        {stream.omitted && (
          <p className="m-0 text-muted-foreground italic">Earlier output omitted</p>
        )}
        {stream.text ? (
          <pre className="m-0 font-[inherit] break-all whitespace-pre-wrap">{stream.text}</pre>
        ) : (
          stream.state === "open" && <p className="m-0 text-muted-foreground">{empty}</p>
        )}
        {stream.state === "done" && <p className="m-0 text-muted-foreground">{end}</p>}
      </div>
      {refusal && (
        <p role="status" className="m-0 border-t px-4 py-3 text-destructive">
          {refusal}
        </p>
      )}
    </div>
  );
}
