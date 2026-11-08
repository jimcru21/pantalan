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
})();
