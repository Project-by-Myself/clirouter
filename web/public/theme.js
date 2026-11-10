// Applies the saved theme before first paint. A file (not an inline script)
// so the dashboard can run under a strict Content-Security-Policy.
try { var t = localStorage.getItem("clr.theme"); if (t === "light" || t === "dark") document.documentElement.dataset.theme = t; } catch (e) {}
