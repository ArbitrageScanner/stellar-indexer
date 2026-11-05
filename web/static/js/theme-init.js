// Apply the saved theme before first paint to avoid a flash (a file, not an
// inline script, so the UI's Content-Security-Policy can forbid inline code).
try { const t = localStorage.getItem('theme'); if (t === 'light' || t === 'dark') document.documentElement.dataset.theme = t; } catch {}
