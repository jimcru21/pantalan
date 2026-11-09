import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Boxes, ChevronLeft, LayoutDashboard, LogOut, type LucideIcon } from "lucide-react";
import { NavLink, Outlet, useNavigate } from "react-router";
import { signOut as endSession, useSessionCheck } from "@/api/sessions";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

// A sidebar entry: a page link or the sign-out button.
const navItem = cn(
  "flex items-center gap-3 rounded-md px-3.5 py-2.5 whitespace-nowrap text-sidebar-foreground no-underline outline-none",
  "hover:bg-sidebar-accent hover:text-sidebar-accent-foreground focus-visible:bg-sidebar-accent focus-visible:text-sidebar-accent-foreground",
);

// `end`: only that exact address is the entry's page. Apps stays current
// on the pages under it (an App, a Deployment).
const nav: { to: string; label: string; icon: LucideIcon; end: boolean }[] = [
  { to: "/", label: "Overview", icon: LayoutDashboard, end: true },
  { to: "/apps", label: "Apps", icon: Boxes, end: false },
];

/**
 * The frame around every signed-in page: a dark sidebar that collapses to
 * an icon rail (remembered across reloads), then the page.
 */
export function Shell() {
  useSessionCheck();
  const [collapsed, setCollapsed] = useRememberedCollapse();
  const navigate = useNavigate();
  // Ends the session on the server, so the cookie can't be reused; only
  // then is the Admin signed out.
  const signOut = useMutation({
    mutationFn: endSession,
    onSuccess: () => navigate("/login", { replace: true }),
  });

  return (
    <div
      className={cn(
        "grid min-h-screen grid-cols-[var(--sidebar-width)_minmax(0,1fr)] transition-[grid-template-columns] duration-200",
        collapsed ? "[--sidebar-width:4rem]" : "[--sidebar-width:13.75rem]",
      )}
    >
      <aside className="sticky top-0 flex h-screen flex-col overflow-hidden bg-sidebar text-sidebar-foreground">
        <div className="flex h-14 items-center gap-2.5 px-4 font-extrabold whitespace-nowrap text-sidebar-accent-foreground">
          <span className="grid size-8 flex-none place-items-center rounded-lg bg-sidebar-primary text-sidebar-primary-foreground">
            P
          </span>
          <Label collapsed={collapsed}>Pantalan</Label>
        </div>
        <nav aria-label="Dashboard" className="grid gap-0.5 p-2">
          {nav.map(({ to, label, icon: Icon, end }) => (
            <Tooltip key={to}>
              <TooltipTrigger asChild>
                <NavLink
                  to={to}
                  end={end}
                  className={({ isActive }) =>
                    cn(navItem, isActive && "bg-sidebar-accent text-sidebar-accent-foreground")
                  }
                >
                  <Icon aria-hidden className="size-[18px] flex-none" />
                  <Label collapsed={collapsed}>{label}</Label>
                </NavLink>
              </TooltipTrigger>
              {collapsed && <TooltipContent side="right">{label}</TooltipContent>}
            </Tooltip>
          ))}
          <Tooltip>
            <TooltipTrigger asChild>
              <button
                type="button"
                disabled={signOut.isPending}
                onClick={() => signOut.mutate()}
                className={cn(navItem, "w-full cursor-pointer text-left")}
              >
                <LogOut aria-hidden className="size-[18px] flex-none" />
                <Label collapsed={collapsed}>Sign out</Label>
              </button>
            </TooltipTrigger>
            {collapsed && <TooltipContent side="right">Sign out</TooltipContent>}
          </Tooltip>
        </nav>
        <button
          type="button"
          aria-expanded={!collapsed}
          aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
          onClick={() => setCollapsed(!collapsed)}
          className="mt-auto flex cursor-pointer items-center gap-3 border-t border-sidebar-border px-[1.3rem] py-3.5 whitespace-nowrap text-sidebar-foreground hover:text-sidebar-accent-foreground"
        >
          <ChevronLeft
            aria-hidden
            className={cn("size-[18px] flex-none transition-transform", collapsed && "rotate-180")}
          />
          <Label collapsed={collapsed}>Collapse</Label>
        </button>
      </aside>
      <div className="min-w-0">
        <Outlet />
      </div>
      {signOut.isError && (
        <div
          role="alert"
          className="fixed right-4 bottom-4 rounded-md bg-destructive-soft px-3 py-2.5 text-[13px] text-destructive shadow-sm"
        >
          Sign-out failed: you are still signed in. Try again.
        </div>
      )}
    </div>
  );
}

// A sidebar label: hidden from sight on the icon rail, but still the
// accessible name of its link or button.
function Label({ collapsed, children }: { collapsed: boolean; children: string }) {
  return <span className={cn(collapsed && "sr-only")}>{children}</span>;
}

const collapsedKey = "pantalan.sidebar.collapsed";

function useRememberedCollapse(): [boolean, (collapsed: boolean) => void] {
  const [collapsed, setCollapsed] = useState(() => {
    try {
      return localStorage.getItem(collapsedKey) === "1";
    } catch {
      return false; // storage blocked: start expanded
    }
  });
  return [
    collapsed,
    (next) => {
      setCollapsed(next);
      try {
        localStorage.setItem(collapsedKey, next ? "1" : "0");
      } catch {
        // storage blocked: the choice lasts until the reload
      }
    },
  ];
}
