// Deployment config for the explorer. Served as-is by the gateway (same
// origin, nothing to set). When the UI is hosted as static files elsewhere
// (CDN, nginx), point it at the public API here; a value saved in the
// settings dialog overrides it per browser.
window.STELLAR_INDEXER = window.STELLAR_INDEXER || {
  apiBase: '', // e.g. 'https://api.example.org'
  blocked: [], // API paths not served by that endpoint (the gateway fills this in)
};
