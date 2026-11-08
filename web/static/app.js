// Pantalan Dashboard behaviour that htmx doesn't cover. Loaded as a file,
// never inline, because the CSP allows only same-origin scripts.
(function () {
  "use strict";

  const key = "pantalan.sidebar.collapsed";

  function remembered() {
    try { return localStorage.getItem(key) === "1"; } catch { return false; }
  }

  function remember(collapsed) {
    try { localStorage.setItem(key, collapsed ? "1" : "0"); } catch { /* private mode */ }
  }

  function setCollapsed(shell, button, collapsed) {
    shell.classList.toggle("collapsed", collapsed);
    button.setAttribute("aria-expanded", String(!collapsed));
    button.setAttribute("aria-label", collapsed ? "Expand sidebar" : "Collapse sidebar");
  }

  document.addEventListener("DOMContentLoaded", function () {
    const shell = document.querySelector("[data-shell]");
    const button = document.querySelector("[data-collapse]");
    if (!shell || !button) return;
    setCollapsed(shell, button, remembered());
    button.addEventListener("click", function () {
      const collapsed = !shell.classList.contains("collapsed");
      setCollapsed(shell, button, collapsed);
      remember(collapsed);
    });
  });

  // Polling. Regions poll with a plain hx-trigger="every 5s": the CSP rules
  // out htmx's eval-based trigger filters, so instead nothing is requested
  // while the tab is hidden, and every polled region refreshes once when
  // the tab is shown again. htmx keeps its poll timers running, so polling
  // resumes on its own.
  const polled = "[hx-get][hx-trigger^='every ']";

  document.addEventListener("htmx:beforeRequest", function (event) {
    if (document.hidden) event.preventDefault();
  });

  document.addEventListener("visibilitychange", function () {
    if (document.hidden || !window.htmx) return;
    document.querySelectorAll(polled).forEach(function (region) {
      // The region's own hx-target and hx-swap apply, as for a poll.
      window.htmx.ajax("GET", region.getAttribute("hx-get"), { source: region });
    });
  });
})();
