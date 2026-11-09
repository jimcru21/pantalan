import { render } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient } from "@tanstack/react-query";
import { App } from "@/App";
import { status, stubEventSource, stubFetch, type Api } from "./network";

/**
 * Renders the whole app at a path, as a browser opening that address
 * would, with only fetch and EventSource stubbed (see network.ts).
 *
 *   const { user, calls } = renderApp("/apps", {
 *     api: { "GET /api/v1/apps": () => json<App[]>([app]) },
 *   });
 */
export function renderApp(path: string, { api = {} }: { api?: Api } = {}) {
  window.history.replaceState(null, "", path);
  const calls = stubFetch(api, liveSession);
  const eventSources = stubEventSource();
  const user = userEvent.setup();
  let view = render(<App queryClient={testQueryClient()} />);

  return {
    user,
    /** Every request the app made, in order. */
    calls,
    /** The EventSource stand-in: eventSources.latest(path) is a stream the app opened. */
    eventSources,
    /** Reloads the page: a fresh app at the current address; localStorage survives. */
    reload() {
      view.unmount();
      view = render(<App queryClient={testQueryClient()} />);
    },
  };
}

// The shell's session check finds the session live, unless a test's api
// answers it otherwise.
const liveSession: Api = { "GET /api/v1/sessions/current": () => status(204) };

// No retries, so an error shows at once instead of after the backoff.
function testQueryClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}
