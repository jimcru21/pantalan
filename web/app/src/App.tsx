import { useEffect, useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter } from "react-router";
import { RouterProvider } from "react-router/dom";
import { onSignedOut, SignedOutError } from "@/api/client";
import { TooltipProvider } from "@/components/ui/tooltip";
import { loginPath } from "@/lib/return-to";
import { AppPage } from "@/pages/AppPage";
import { AppsPage } from "@/pages/AppsPage";
import { DeploymentPage } from "@/pages/DeploymentPage";
import { OverviewPage } from "@/pages/OverviewPage";
import { SignInPage } from "@/pages/SignInPage";
import { NotFoundPage } from "@/shell/Page";
import { Shell } from "@/shell/Shell";

/** The Dashboard's pages, by address. */
const routes = [
  { path: "login", element: <SignInPage /> },
  {
    element: <Shell />,
    children: [
      { index: true, element: <OverviewPage /> },
      { path: "apps", element: <AppsPage /> },
      { path: "apps/:appId", element: <AppPage /> },
      { path: "apps/:appId/deployments/:deploymentId", element: <DeploymentPage /> },
      {
        path: "*",
        element: (
          <NotFoundPage title="Page not found" back={{ to: "/", label: "Back to the Overview" }}>
            No Dashboard page has this address.
          </NotFoundPage>
        ),
      },
    ],
  },
];

/**
 * The whole Dashboard. Tests render it as it is, passing their own
 * QueryClient so no cache leaks between them.
 */
export function App({ queryClient }: { queryClient?: QueryClient }) {
  const [client] = useState(() => queryClient ?? newQueryClient());
  const [router] = useState(() => createBrowserRouter(routes));

  // Any 401 behind sign-in means the session ended (expired, signed out
  // elsewhere, reset-password): go to sign in, remembering this address,
  // so a stale tab doesn't silently stop updating.
  useEffect(
    () =>
      onSignedOut(() => {
        const { pathname, search, hash } = router.state.location;
        if (pathname === "/login") return; // another request got there first
        void router.navigate(loginPath(pathname + search + hash), { replace: true });
      }),
    [router],
  );

  return (
    <QueryClientProvider client={client}>
      <TooltipProvider>
        <RouterProvider router={router} />
      </TooltipProvider>
    </QueryClientProvider>
  );
}

function newQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        // Retrying a signed-out request can't help.
        retry: (failures, err) => !(err instanceof SignedOutError) && failures < 3,
      },
    },
  });
}
