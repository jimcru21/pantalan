import { act, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { App, AppEnv, AppListItem, LoginInfo, SignInRequest } from "@/api/generated/httpapi";
import { anAppListItem } from "@/test/app-fixtures";
import { aLoginInfo, anApp } from "@/test/fixtures";
import { json, status, type Api, type ApiCall } from "@/test/network";
import { overviewApi } from "@/test/node-fixtures";
import { renderApp } from "@/test/render";
import { setHidden } from "@/test/visibility";

// The sign-in API as a Node with one Admin answers it.
function signInApi(signIn: Api[string] = () => status(204)): Api {
  return {
    "GET /api/v1/login-info": () => json<LoginInfo>(aLoginInfo()),
    "POST /api/v1/sessions": signIn,
  };
}

// Fills in the form and submits it, as the Admin would.
async function signInAs(
  user: ReturnType<typeof renderApp>["user"],
  username: string,
  password = "s3cret",
) {
  await user.type(await screen.findByLabelText("Username"), username);
  await user.type(screen.getByLabelText("Password"), password);
  await user.click(screen.getByRole("button", { name: "Sign in" }));
}

describe("the sign-in screen", () => {
  it("names the Node and keeps the certificate fingerprint hint", async () => {
    renderApp("/login", { api: signInApi() });

    expect(await screen.findByText("203.0.113.7:8443")).toBeInTheDocument();
    expect(screen.getByText(/node-1/)).toBeInTheDocument();
    expect(screen.getByText("sudo pantalan tls fingerprint")).toBeInTheDocument();
    expect(document.querySelectorAll("style"), "the CSP blocks <style>").toHaveLength(0);
  });

  it("lets a password manager fill and save the username and password", async () => {
    renderApp("/login", { api: signInApi() });

    const username = await screen.findByLabelText("Username");
    const password = screen.getByLabelText("Password");
    expect(username).toHaveAttribute("autocomplete", "username");
    expect(username).toHaveAttribute("name", "username");
    expect(password).toHaveAttribute("autocomplete", "current-password");
    expect(password).toHaveAttribute("type", "password");
    expect(password).toHaveAttribute("name", "password");
    expect(username.closest("form")).toBe(password.closest("form"));
  });

  it("says the same thing for any wrong username or password", async () => {
    const { user } = renderApp("/login", {
      api: signInApi(() => json({ error: "incorrect username or password" }, { status: 401 })),
    });

    await signInAs(user, "mallory", "guess");

    expect(await screen.findByRole("alert")).toHaveTextContent("Incorrect username or password.");
    expect(window.location.pathname).toBe("/login");
    expect(screen.getByLabelText("Username")).toHaveValue("mallory");
  });

  it("counts down a throttled wait, still naming the reset-password command", async () => {
    // Only the clock and setInterval are fake: Testing Library's waits and
    // userEvent keep using the real setTimeout.
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
    const { user } = renderApp("/login", {
      api: signInApi(() =>
        json(
          { error: "too many failed sign-ins, try again later" },
          { status: 429, headers: { "Retry-After": "65" } },
        ),
      ),
    });

    await signInAs(user, "alice");

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Too many attempts. Try again in 1 min 5 s");
    expect(alert).toHaveTextContent("sudo pantalan admin reset-password");
    expect(screen.getByRole("button", { name: "Sign in" })).toBeDisabled();

    act(() => vi.advanceTimersByTime(6_000));
    expect(alert).toHaveTextContent("Try again in 59 s");
    expect(alert).toHaveTextContent("sudo pantalan admin reset-password");

    act(() => vi.advanceTimersByTime(59_000));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeEnabled();
  });

  it("tells a busy Node apart from a wrong password", async () => {
    const { user } = renderApp("/login", {
      api: signInApi(() =>
        json(
          { error: "too many sign-ins in progress" },
          { status: 503, headers: { "Retry-After": "5" } },
        ),
      ),
    });

    await signInAs(user, "alice");

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/busy.*try again/i);
    expect(alert).not.toHaveTextContent(/incorrect/i);
  });

  it("shows the admin create command instead of the form when there's no Admin", async () => {
    renderApp("/login", {
      api: { "GET /api/v1/login-info": () => json(aLoginInfo({ admin_exists: false })) },
    });

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "sudo pantalan admin create --username NAME",
    );
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Sign in" })).not.toBeInTheDocument();
  });

  it("switches to the admin create command when the Admin is gone by sign-in time", async () => {
    const { user } = renderApp("/login", {
      api: signInApi(() => json({ error: "no admin exists" }, { status: 403 })),
    });

    await signInAs(user, "alice");

    expect(await screen.findByRole("alert")).toHaveTextContent("sudo pantalan admin create");
    expect(screen.queryByLabelText("Password")).not.toBeInTheDocument();
  });
});

describe("an ended session", () => {
  it("sends the browser to sign in on any 401, then back to where it was", async () => {
    let signedIn = false;
    const { user } = renderApp("/apps?sort=name", {
      api: {
        ...signInApi(() => {
          signedIn = true;
          return status(204);
        }),
        "GET /api/v1/apps": () =>
          signedIn
            ? json<AppListItem[]>([anAppListItem({ name: "hello" })])
            : new Response("unauthorized\n", { status: 401 }),
      },
    });

    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/login");
    expect(new URLSearchParams(window.location.search).get("next")).toBe("/apps?sort=name");

    await signInAs(user, "alice");

    expect(await screen.findByText("hello")).toBeInTheDocument();
    expect(window.location.pathname + window.location.search).toBe("/apps?sort=name");
  });
});

// admin reset-password must sign out every open page within a minute,
// even one that never polls, like the env editor.
describe("the session check", () => {
  const app = anApp({ id: "app7", name: "shop" });
  // The env editor: a page that asks the server nothing once it's shown.
  function envEditorApi(session: () => Response): Api {
    return {
      ...signInApi(),
      "GET /api/v1/apps/app7": () => json<App>(app),
      "GET /api/v1/apps/app7/env": () => json<AppEnv>({ env: "A=1\n" }),
      "GET /api/v1/sessions/current": session,
    };
  }
  const checks = (calls: ApiCall[]) =>
    calls.filter((c) => c.path === "/api/v1/sessions/current").length;

  it("sends a page that never polls to sign in within 30 s of its session ending", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let live = true;
    renderApp("/apps/app7/env", {
      api: envEditorApi(() =>
        live ? status(204) : json({ error: "not signed in" }, { status: 401 }),
      ),
    });
    expect(await screen.findByLabelText("Env vars")).toBeInTheDocument();

    live = false; // admin reset-password
    await act(() => vi.advanceTimersByTimeAsync(30_000));

    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(new URLSearchParams(window.location.search).get("next")).toBe("/apps/app7/env");
  });

  it("leaves a live session's page where it is", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { calls } = renderApp("/apps/app7/env", { api: envEditorApi(() => status(204)) });
    expect(await screen.findByLabelText("Env vars")).toBeInTheDocument();

    await act(() => vi.advanceTimersByTimeAsync(60_000));

    expect(checks(calls)).toBe(2);
    expect(window.location.pathname).toBe("/apps/app7/env");
    expect(screen.getByLabelText("Env vars")).toBeInTheDocument();
  });

  it("pauses in a hidden tab and checks again as soon as it's shown", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let live = true;
    const { calls } = renderApp("/apps/app7/env", {
      api: envEditorApi(() =>
        live ? status(204) : json({ error: "not signed in" }, { status: 401 }),
      ),
    });
    expect(await screen.findByLabelText("Env vars")).toBeInTheDocument();

    await act(async () => setHidden(true));
    live = false;
    await act(() => vi.advanceTimersByTimeAsync(5 * 60_000));
    expect(checks(calls)).toBe(0);

    await act(async () => setHidden(false));

    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(checks(calls)).toBe(1);
  });
});

describe("signing out", () => {
  it("ends the session on the server, then shows the sign-in screen", async () => {
    const { user, calls } = renderApp("/apps", {
      api: {
        ...signInApi(),
        "GET /api/v1/apps": () => json<AppListItem[]>([]),
        "DELETE /api/v1/sessions/current": () => status(204),
      },
    });

    await user.click(await screen.findByRole("button", { name: "Sign out" }));

    expect(await screen.findByRole("button", { name: "Sign in" })).toBeInTheDocument();
    expect(window.location.pathname + window.location.search).toBe("/login");
    expect(calls.map((c) => `${c.method} ${c.path}`)).toContain("DELETE /api/v1/sessions/current");
  });

  it("stays signed in, and says so, when the server can't end the session", async () => {
    const { user } = renderApp("/", {
      api: {
        ...overviewApi(),
        "DELETE /api/v1/sessions/current": () => json({ error: "internal error" }, { status: 500 }),
      },
    });

    await user.click(await screen.findByRole("button", { name: "Sign out" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/sign-out failed/i);
    expect(window.location.pathname).toBe("/");
  });
});

describe("signing in", () => {
  it("returns to the address the Admin asked for", async () => {
    const { user, calls } = renderApp("/login?next=%2Fapps", {
      api: { ...signInApi(), "GET /api/v1/apps": () => json<AppListItem[]>([]) },
    });

    await signInAs(user, "alice");

    expect(await screen.findByRole("heading", { name: "Apps" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/apps");
    const signIn = calls.find((c) => c.method === "POST")!;
    expect(await signIn.request.json()).toEqual<SignInRequest>({
      username: "alice",
      password: "s3cret",
    });
  });

  it("never follows a requested address off the Node", async () => {
    const { user } = renderApp("/login?next=%2F%2Fevil.example%2F", {
      api: { ...signInApi(), ...overviewApi() },
    });

    await signInAs(user, "alice");

    expect(await screen.findByRole("heading", { name: "Overview" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/");
  });
});
