import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Lock } from "lucide-react";
import { useLocation, useNavigate } from "react-router";
import { ApiError } from "@/api/client";
import { fetchLoginInfo, signIn } from "@/api/sessions";
import { returnPath } from "@/lib/return-to";

/**
 * The sign-in screen: which Node this is, the username and password form
 * (or, with no Admin yet, the command that creates one), and the
 * certificate fingerprint hint.
 */
export function SignInPage() {
  // Without login-info the form still shows, only without the Node's name:
  // don't hold it back for retries.
  const info = useQuery({ queryKey: ["login-info"], queryFn: fetchLoginInfo, retry: false });
  // The Admin can also be deleted between loading the screen and signing in.
  const [adminGone, setAdminGone] = useState(false);
  const noAdmin = info.data?.admin_exists === false || adminGone;

  return (
    <main className="grid min-h-screen place-items-center px-4 py-6">
      <div className="grid w-full max-w-[380px] gap-[18px] rounded-xl border bg-card p-7 shadow-sm">
        <div className="flex items-center gap-2.5 text-lg font-bold">
          <span className="grid size-[30px] flex-none place-items-center rounded-lg bg-primary font-extrabold text-primary-foreground">
            P
          </span>
          Pantalan
        </div>
        <div>
          <h1 className="text-base font-semibold">Sign in to the Dashboard</h1>
          {info.data && (
            <p className="text-[13px] [overflow-wrap:anywhere] text-muted-foreground">
              {info.data.hostname} · <span className="font-mono">{info.data.address}</span>
            </p>
          )}
        </div>
        {info.isPending ? null : noAdmin ? (
          <Alert>
            No Admin has been created on this Node yet. On the server, run{" "}
            <Code>sudo pantalan admin create --username NAME</Code>, then sign in with the password
            it prints.
          </Alert>
        ) : (
          <SignInForm onNoAdmin={() => setAdminGone(true)} />
        )}
        <div className="grid gap-1.5 border-t pt-3.5 text-xs text-muted-foreground">
          <span>
            <Lock aria-hidden className="inline size-[13px] align-[-2px]" /> Before signing in,
            check that your browser shows the certificate fingerprint that{" "}
            <Code>sudo pantalan tls fingerprint</Code> prints on the server.
          </span>
          <span>
            Forgot the password? Run <Code>sudo pantalan admin reset-password</Code> on the server.
          </span>
        </div>
      </div>
    </main>
  );
}

function SignInForm({ onNoAdmin }: { onNoAdmin: () => void }) {
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [throttledFor, throttle] = useCountdown();

  const mutation = useMutation({
    mutationFn: signIn,
    onError: (err) => {
      setPassword("");
      if (isThrottled(err)) throttle(err.retryAfter ?? 60);
      if (err instanceof ApiError && err.status === 403) onNoAdmin();
    },
    onSuccess: () => {
      // Forget what the pages cached before the session ended.
      queryClient.removeQueries({ predicate: (q) => q.getObserversCount() === 0 });
      void navigate(returnPath(location.search), { replace: true });
    },
  });

  function submit(event: FormEvent) {
    event.preventDefault();
    mutation.mutate({ username, password });
  }

  return (
    <form className="grid gap-[18px]" method="post" onSubmit={submit}>
      {throttledFor > 0 ? (
        <Alert>
          Too many attempts. Try again in {formatWait(throttledFor)}, or run{" "}
          <Code>sudo pantalan admin reset-password</Code> on the server.
        </Alert>
      ) : (
        mutation.error &&
        !isThrottled(mutation.error) && <Alert>{failureMessage(mutation.error)}</Alert>
      )}
      <Field label="Username" htmlFor="username">
        <input
          id="username"
          name="username"
          className={inputClass}
          autoComplete="username"
          autoCapitalize="none"
          spellCheck={false}
          required
          autoFocus
          value={username}
          onChange={(e) => setUsername(e.target.value)}
        />
      </Field>
      <Field label="Password" htmlFor="password">
        <input
          id="password"
          name="password"
          type="password"
          className={inputClass}
          autoComplete="current-password"
          required
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
      </Field>
      <button
        type="submit"
        disabled={mutation.isPending || throttledFor > 0}
        className="inline-flex cursor-pointer items-center justify-center rounded-md bg-primary px-3.5 py-2.5 font-semibold text-primary-foreground disabled:cursor-default disabled:opacity-70"
      >
        Sign in
      </button>
    </form>
  );
}

function isThrottled(err: Error): err is ApiError {
  return err instanceof ApiError && err.status === 429;
}

/**
 * A wait that counts down each second: the whole seconds left (0 before it
 * starts and once it's over), and start(seconds).
 */
function useCountdown(): [number, (seconds: number) => void] {
  const [wait, setWait] = useState<{ until: number; now: number } | null>(null);
  const until = wait?.until;
  useEffect(() => {
    if (until === undefined) return;
    const tick = setInterval(() => {
      const now = Date.now();
      setWait((w) => w && { ...w, now });
      if (now >= until) clearInterval(tick);
    }, 1000);
    return () => clearInterval(tick);
  }, [until]);

  const left = wait ? Math.max(0, Math.ceil((wait.until - wait.now) / 1000)) : 0;
  const start = (seconds: number) => {
    const now = Date.now();
    setWait({ until: now + seconds * 1000, now });
  };
  return [left, start];
}

// "45 s", "1 min 5 s", "15 min".
function formatWait(seconds: number): string {
  const min = Math.floor(seconds / 60);
  const sec = seconds % 60;
  if (min === 0) return `${sec} s`;
  return sec === 0 ? `${min} min` : `${min} min ${sec} s`;
}

// What a refused sign-in says. Any wrong username or password gets the one
// message, so nobody can probe which usernames exist.
function failureMessage(err: Error): string {
  if (err instanceof ApiError && err.status === 401) return "Incorrect username or password.";
  if (err instanceof ApiError && err.status === 503) {
    return "The Node is busy checking other sign-ins. Try again in a few seconds.";
  }
  return "Sign-in failed. Try again.";
}

function Alert({ children }: { children: ReactNode }) {
  return (
    <div
      role="alert"
      className="rounded-md bg-destructive-soft px-3 py-2.5 text-[13px] text-destructive"
    >
      {children}
    </div>
  );
}

const inputClass =
  "w-full rounded-md border bg-card px-[11px] py-[9px] outline-none focus:border-primary focus:ring-3 focus:ring-accent";

function Field({
  label,
  htmlFor,
  children,
}: {
  label: string;
  htmlFor: string;
  children: ReactNode;
}) {
  return (
    <div className="grid gap-1.5">
      <label htmlFor={htmlFor} className="text-[13px] font-medium">
        {label}
      </label>
      {children}
    </div>
  );
}

function Code({ children }: { children: ReactNode }) {
  return <code className="font-mono text-[0.95em]">{children}</code>;
}
