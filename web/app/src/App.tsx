import { useState } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createBrowserRouter } from "react-router";
import { RouterProvider } from "react-router/dom";
import { TooltipProvider } from "@/components/ui/tooltip";
import { AppsPage } from "@/pages/AppsPage";
import { OverviewPage } from "@/pages/OverviewPage";
import { Shell } from "@/shell/Shell";

/** The Dashboard's pages, by address. */
const routes = [
  {
    element: <Shell />,
    children: [
      { index: true, element: <OverviewPage /> },
      { path: "apps", element: <AppsPage /> },
    ],
  },
];

/**
 * The whole Dashboard. Tests render it as it is, passing their own
 * QueryClient so no cache leaks between them.
 */
export function App({ queryClient }: { queryClient?: QueryClient }) {
  const [client] = useState(() => queryClient ?? new QueryClient());
  const [router] = useState(() => createBrowserRouter(routes));
  return (
    <QueryClientProvider client={client}>
      <TooltipProvider>
        <RouterProvider router={router} />
      </TooltipProvider>
    </QueryClientProvider>
  );
}
