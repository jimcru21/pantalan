// Hides or shows the browser tab, as switching to another tab would.
// TanStack Query pauses its intervals while hidden and refetches what is
// due on return. The event must bubble: TanStack listens on window.
export function setHidden(hidden: boolean) {
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => (hidden ? "hidden" : "visible"),
  });
  Object.defineProperty(document, "hidden", { configurable: true, get: () => hidden });
  document.dispatchEvent(new Event("visibilitychange", { bubbles: true }));
}
