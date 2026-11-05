// Thin client for the stellar-indexer JSON API.

const KEY = 'apiBase';

export function apiBase() {
  try { return (localStorage.getItem(KEY) || '').replace(/\/+$/, ''); } catch { return ''; }
}

export function setApiBase(v) {
  try { v ? localStorage.setItem(KEY, v.replace(/\/+$/, '')) : localStorage.removeItem(KEY); } catch {}
}

// Absolute URL of an API path, for display (curl) and fetching.
export function apiURL(path) {
  const b = apiBase();
  return (b || location.origin) + path;
}

// headers adds the optional API key (settings dialog) to a request.
export function headers(extra = {}) {
  const h = { Accept: 'application/json', ...extra };
  try { const k = localStorage.getItem('apiKey'); if (k) h['X-API-Key'] = k; } catch {}
  return h;
}

export class ApiError extends Error {
  constructor(message, { status = 0, url = '', body = null } = {}) {
    super(message);
    this.status = status; this.url = url; this.body = body;
  }
}

// get fetches JSON; it throws ApiError with the server's {"error"} message.
export async function get(path, { signal } = {}) {
  const url = apiURL(path);
  let res;
  try {
    res = await fetch(url, { signal, headers: headers() });
  } catch (e) {
    if (e.name === 'AbortError') throw e;
    throw new ApiError(`Network error: cannot reach ${apiBase() || location.origin} (${e.message})`, { url });
  }
  const text = await res.text();
  let body = null;
  try { body = text ? JSON.parse(text) : null; } catch { /* non-JSON */ }
  if (!res.ok) {
    const msg = body?.error || text.slice(0, 200) || res.statusText;
    throw new ApiError(`HTTP ${res.status}: ${msg}`, { status: res.status, url, body });
  }
  if (body === null && text) throw new ApiError('Response is not JSON', { status: res.status, url });
  return body;
}

// raw fetches without throwing, for the playground (status, timing, size).
export async function raw(path, { signal } = {}) {
  const url = apiURL(path);
  const t0 = performance.now();
  const res = await fetch(url, { signal, headers: headers() });
  const text = await res.text();
  const ms = performance.now() - t0;
  let body = null;
  try { body = JSON.parse(text); } catch {}
  return { url, status: res.status, ok: res.ok, ms, bytes: new Blob([text]).size, text, body,
    type: res.headers.get('content-type') || '' };
}

// qs builds a query string, skipping empty values.
export function qs(params) {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params || {})) {
    if (v !== undefined && v !== null && v !== '') p.set(k, v);
  }
  const s = p.toString();
  return s ? '?' + s : '';
}

// send performs a write (POST/DELETE) with the optional labels token.
export async function send(method, path, body) {
  let token = '';
  try { token = localStorage.getItem('labelsToken') || ''; } catch {}
  const h = headers(body !== undefined ? { 'Content-Type': 'application/json' } : {});
  if (token) h.Authorization = `Bearer ${token}`;
  const url = apiURL(path);
  let res;
  try {
    res = await fetch(url, { method, headers: h, body: body === undefined ? undefined : JSON.stringify(body) });
  } catch (e) {
    throw new ApiError(`Network error: ${e.message}`, { url });
  }
  const data = await res.json().catch(() => null);
  if (!res.ok) throw new ApiError(`HTTP ${res.status}: ${data?.error || res.statusText}`, { status: res.status, url, body: data });
  return data;
}
