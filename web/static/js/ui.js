// UI toolkit: safe HTML templating, formatting, shared components and
// async section loading with skeleton / empty / error states.

import { ApiError } from './api.js';

// ---- templating -------------------------------------------------------------

export class Safe { constructor(s) { this.s = s; } toString() { return this.s; } }
export const raw = (s) => new Safe(String(s));

export function esc(v) {
  return String(v).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
}

function part(v) {
  if (v === null || v === undefined || v === false) return '';
  if (v instanceof Safe) return v.s;
  if (Array.isArray(v)) return v.map(part).join('');
  return esc(v);
}

// html`...` escapes every interpolation unless it is Safe (or an array of them).
export function html(strings, ...vals) {
  let out = strings[0];
  for (let i = 0; i < vals.length; i++) out += part(vals[i]) + strings[i + 1];
  return new Safe(out);
}

export function setHTML(el, v) { el.innerHTML = part(v); hydrate(el); return el; }

// ---- icons ------------------------------------------------------------------

const P = {
  pulse: 'M3 12h4l3-8 4 16 3-8h4',
  layers: 'M12 3 2 8l10 5 10-5-10-5zM2 13l10 5 10-5M2 17.5l10 5 10-5',
  list: 'M8 6h13M8 12h13M8 18h13M3.5 6h.01M3.5 12h.01M3.5 18h.01',
  cog: 'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 0 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 0 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 0 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 0 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z',
  user: 'M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2M12 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8z',
  arrows: 'M7 7h13l-4-4M17 17H4l4 4',
  chart: 'M3 3v18h18M7 16V9M12 16V5M17 16v-4',
  swap: 'M16 3l4 4-4 4M20 7H4M8 21l-4-4 4-4M4 17h16',
  coin: 'M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM15 9.5c-.5-1-1.6-1.5-3-1.5-1.7 0-3 .8-3 2s1.3 1.7 3 2 3 .8 3 2-1.3 2-3 2c-1.4 0-2.5-.5-3-1.5M12 6v2M12 16v2',
  drop: 'M12 2.7l5.7 5.6a8 8 0 1 1-11.4 0z',
  grid: 'M3 3h7v7H3zM14 3h7v7h-7zM14 14h7v7h-7zM3 14h7v7H3z',
  code: 'M16 18l6-6-6-6M8 6l-6 6 6 6',
  bolt: 'M13 2 3 14h9l-1 8 10-12h-9z',
  terminal: 'M4 17l6-6-6-6M12 19h8',
  menu: 'M3 6h18M3 12h18M3 18h18',
  search: 'M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16zM21 21l-4.3-4.3',
  sun: 'M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10zM12 1v2M12 21v2M4.2 4.2l1.4 1.4M18.4 18.4l1.4 1.4M1 12h2M21 12h2M4.2 19.8l1.4-1.4M18.4 5.6l1.4-1.4',
  moon: 'M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z',
  monitor: 'M2 3h20v14H2zM8 21h8M12 17v4',
  sliders: 'M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6',
  copy: 'M9 9h13v13H9zM5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1',
  ext: 'M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6M15 3h6v6M10 14 21 3',
  check: 'M20 6 9 17l-5-5',
  x: 'M18 6 6 18M6 6l12 12',
  alert: 'M12 9v4M12 17h.01M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z',
  info: 'M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 16v-4M12 8h.01',
  play: 'M6 4l14 8-14 8z',
  refresh: 'M23 4v6h-6M1 20v-6h6M3.5 9a9 9 0 0 1 14.9-3.4L23 10M1 14l4.6 4.4A9 9 0 0 0 20.5 15',
  inbox: 'M22 12h-6l-2 3h-4l-2-3H2M5.5 5.1 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.5-6.9A2 2 0 0 0 16.8 4H7.2a2 2 0 0 0-1.7 1.1z',
  clock: 'M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 6v6l4 2',
  flip: 'M7 16V4M7 4 3 8M7 4l4 4M17 8v12M17 20l4-4M17 20l-4-4',
  trophy: 'M8 21h8M12 17v4M7 4h10v5a5 5 0 0 1-10 0zM17 5h3v2a3 3 0 0 1-3 3M7 5H4v2a3 3 0 0 0 3 3',
  bell: 'M18 8a6 6 0 0 0-12 0c0 7-3 9-3 9h18s-3-2-3-9M13.7 21a2 2 0 0 1-3.4 0',
  star: 'M12 2l3.1 6.3 6.9 1-5 4.9 1.2 6.8L12 17.8 5.8 21l1.2-6.8-5-4.9 6.9-1z',
  tag: 'M20.6 13.4 13.4 20.6a2 2 0 0 1-2.8 0L2 12V2h10l8.6 8.6a2 2 0 0 1 0 2.8zM7 7h.01',
};

export function iconSVG(name) {
  const d = P[name] || P.info;
  return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="${d}"/></svg>`;
}
export const icon = (name) => raw(`<i data-icon="${name}">${iconSVG(name)}</i>`);

// hydrate fills <i data-icon> placeholders and wires copy buttons.
export function hydrate(root = document) {
  root.querySelectorAll('i[data-icon]:empty').forEach((el) => { el.innerHTML = iconSVG(el.dataset.icon); });
}

document.addEventListener('click', (e) => {
  const b = e.target.closest('[data-copy]');
  if (!b) return;
  e.preventDefault(); e.stopPropagation();
  navigator.clipboard?.writeText(b.dataset.copy).then(() => toast('Copied'), () => toast('Copy failed'));
});

let toastTimer;
export function toast(msg) {
  const t = document.getElementById('toast');
  t.textContent = msg; t.classList.add('show');
  clearTimeout(toastTimer); toastTimer = setTimeout(() => t.classList.remove('show'), 1400);
}

// ---- formatting -------------------------------------------------------------

const nf = new Map();
function fmt(opts) {
  const k = JSON.stringify(opts);
  if (!nf.has(k)) nf.set(k, new Intl.NumberFormat('en-US', opts));
  return nf.get(k);
}

export function num(v, maxFrac = 0) {
  if (v === null || v === undefined || v === '' || isNaN(+v)) return '—';
  return fmt({ maximumFractionDigits: maxFrac }).format(+v);
}

export function compact(v) {
  if (v === null || v === undefined || isNaN(+v)) return '—';
  const n = +v;
  if (Math.abs(n) < 1000) return fmt({ maximumFractionDigits: Math.abs(n) < 10 ? 2 : 1 }).format(n);
  return fmt({ notation: 'compact', maximumFractionDigits: 1 }).format(n);
}

// amount formats a token amount (already in whole units) for display.
export function amount(v, maxFrac) {
  if (v === null || v === undefined || isNaN(+v)) return '—';
  const n = Math.abs(+v);
  if (maxFrac === undefined) maxFrac = n >= 1000 ? 2 : n >= 1 ? 4 : 7;
  if (n !== 0 && n < 1e-7) return fmt({ maximumSignificantDigits: 3 }).format(+v);
  return fmt({ maximumFractionDigits: maxFrac }).format(+v);
}

// stroops converts an integer amount string (7 decimals) to whole units,
// exactly, via BigInt; returns a Number for formatting.
export function stroops(s) {
  if (s === null || s === undefined || s === '') return null;
  try {
    const b = BigInt(String(s).split('.')[0]);
    const neg = b < 0n, a = neg ? -b : b;
    const v = Number(a / 10000000n) + Number(a % 10000000n) / 1e7;
    return neg ? -v : v;
  } catch { return +s / 1e7; }
}

export const amt7 = (s, maxFrac) => amount(stroops(s), maxFrac);

export function price(p) {
  if (p === null || p === undefined || isNaN(+p) || !isFinite(+p)) return '—';
  const n = Math.abs(+p);
  if (n === 0) return '0';
  if (n >= 1000) return fmt({ maximumFractionDigits: 2 }).format(+p);
  if (n >= 1) return fmt({ maximumFractionDigits: 4 }).format(+p);
  return fmt({ maximumSignificantDigits: 5 }).format(+p);
}

export function pct(v, digits = 2, signed = false) {
  if (v === null || v === undefined || isNaN(+v)) return '—';
  const s = fmt({ maximumFractionDigits: digits, minimumFractionDigits: digits }).format(+v) + '%';
  return signed && +v > 0 ? '+' + s : s;
}

export function bytes(n) {
  if (!n && n !== 0) return '—';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0, v = +n;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${u[i]}`;
}

export function ago(ts) {
  if (!ts) return '—';
  const s = Math.max(0, (Date.now() - new Date(ts).getTime()) / 1000);
  if (s < 60) return `${Math.floor(s)}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

export function duration(sec) {
  if (sec === null || sec === undefined) return '—';
  const s = Math.max(0, Math.round(sec));
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.floor(s / 60)}m ${s % 60}s`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ${Math.floor((s % 3600) / 60)}m`;
  return `${Math.floor(s / 86400)}d ${Math.floor((s % 86400) / 3600)}h`;
}

export function dt(ts, withDate = true) {
  if (!ts) return '—';
  const d = new Date(ts);
  const time = d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  return withDate ? `${d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })}, ${time}` : time;
}

export const when = (ts) => html`<span class="nowrap" title="${ts ? new Date(ts).toISOString() : ''}">${ago(ts)}</span>`;

export function short(s, head = 4, tail = 4) {
  if (!s) return '';
  s = String(s);
  return s.length <= head + tail + 1 ? s : `${s.slice(0, head)}…${s.slice(-tail)}`;
}

export const snake = (s) => String(s || '').replace(/_/g, ' ');

// ---- network / external links -------------------------------------------------

export const state = { network: '', status: null };

export function expertURL(kind, id) {
  const net = state.network === 'pubnet' ? 'public' : state.network === 'testnet' ? 'testnet' : null;
  if (!net || !id) return null;
  if (kind === 'asset') {
    const a = parseAsset(id);
    if (a.native) id = 'XLM';
    else if (a.contract) kind = 'contract';
    else id = `${a.code}-${a.issuer}`;
  }
  const path = { tx: 'tx', account: 'account', contract: 'contract', ledger: 'ledger', asset: 'asset', pool: 'liquidity-pool' }[kind];
  return `https://stellar.expert/explorer/${net}/${path}/${encodeURIComponent(id)}`;
}

export function expert(kind, id, label = 'stellar.expert') {
  const u = expertURL(kind, id);
  return u ? html`<a class="ext" href="${u}" target="_blank" rel="noopener">${label} ${icon('ext')}</a>` : '';
}

// ---- entities ---------------------------------------------------------------

export const copyBtn = (v) => html`<button class="copy" data-copy="${v}" title="Copy">${icon('copy')}</button>`;

// poolHex decodes a CAP-67 liquidity-pool strkey (L…) to its hex pool id.
export function poolHex(s) {
  if (!/^L[A-Z2-7]{55}$/.test(s)) return null;
  const A = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
  let bits = 0, val = 0;
  const out = [];
  for (const c of s) {
    val = (val << 5) | A.indexOf(c); bits += 5;
    if (bits >= 8) { out.push((val >>> (bits - 8)) & 255); bits -= 8; }
  }
  return out.slice(1, 33).map((b) => b.toString(16).padStart(2, '0')).join(''); // skip version byte, drop checksum
}

export function addr(a, { full = false, copy = false, label } = {}) {
  if (!a) return html`<span class="muted">—</span>`;
  const kind = a[0] === 'C' ? 'contract' : (a[0] === 'G' || a[0] === 'M') ? 'account' : null;
  const text = label || (full ? a : short(a, 4, 4));
  const pool = a[0] === 'L' ? poolHex(a) : null;
  const inner = kind ? html`<a class="addr" href="#/${kind}/${a}" title="${a}">${text}</a>`
    : pool ? html`<a class="addr" href="#/pools?id=${pool}" title="Liquidity pool ${a}">${text}</a>` : html`<span class="addr" title="${a}">${text}</span>`;
  return copy ? html`${inner}${copyBtn(a)}` : inner;
}

export const txLink = (h, full = false) => h ? html`<a class="addr" href="#/tx/${h}" title="${h}">${full ? h : short(h, 6, 4)}</a>` : '—';
export const ledgerLink = (n) => n ? html`<a href="#/ledger/${n}" class="nowrap">${num(n)}</a>` : '—';

export function parseAsset(key) {
  if (!key || key === 'native' || key === 'XLM') return { key: 'native', code: 'XLM', native: true };
  if (/^C[A-Z2-7]{55}$/.test(key)) return { key, code: short(key, 4, 3), contract: true };
  const i = key.indexOf(':');
  if (i > 0) return { key, code: key.slice(0, i), issuer: key.slice(i + 1) };
  return { key, code: key };
}

const AV = ['--s1', '--s2', '--s3', '--s7', '--s5', '--s6', '--s4', '--s8'];
function avColor(s) {
  let h = 0;
  for (const c of s) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return `var(${AV[h % AV.length]})`;
}

export function asset(key, { issuer = false, link = true } = {}) {
  const a = parseAsset(key);
  const av = a.native ? 'XL' : (a.contract ? 'SC' : a.code.slice(0, 2));
  const bg = a.native ? 'var(--ink)' : avColor(a.key);
  const iss = issuer && a.issuer ? html` <span class="iss" title="${a.issuer}">${short(a.issuer, 4, 4)}</span>` : '';
  const body = html`<span class="av" style="background:${raw(bg)};${raw(a.native ? 'color:var(--surface)' : '')}">${av}</span><span class="code">${a.code}</span>${iss}`;
  return link ? html`<a class="asset" href="#/asset/${encodeURIComponent(a.key)}" title="${a.key}">${body}</a>` : html`<span class="asset" title="${a.key}">${body}</span>`;
}

export function pairLink(base, counter) {
  return html`<a class="pair" href="#/pair/${encodeURIComponent(base)}/${encodeURIComponent(counter)}">${asset(base, { link: false })}<span class="slash">/</span>${asset(counter, { link: false })}</a>`;
}

// Fixed venue/protocol → categorical slot so an entity keeps its color everywhere.
const VENUES = { sdex: 1, amm: 2, soroswap: 3, aquarius: 4, clmm: 5, amm_generic: 6, blend: 7, phoenix: 8 };
export const VENUE_LABEL = { sdex: 'SDEX', amm: 'Classic AMM', soroswap: 'Soroswap', aquarius: 'Aquarius', clmm: 'CLMM', amm_generic: 'Generic AMM', blend: 'Blend' };
export const venueColor = (v) => `var(--s${VENUES[v] || 8})`;
export const venue = (v) => html`<span class="badge"><span class="sw" style="background:${raw(venueColor(v))}"></span>${VENUE_LABEL[v] || v}</span>`;

export const okBadge = (ok) => ok
  ? html`<span class="badge ok">${icon('check')}Success</span>`
  : html`<span class="badge fail">${icon('x')}Failed</span>`;

export const tag = (s) => html`<span class="tag">${s}</span>`;

// ---- JSON -------------------------------------------------------------------

// jsonHTML pretty-prints a value with syntax colors; strkeys become links.
export function jsonHTML(v, { max = 200000 } = {}) {
  let s = JSON.stringify(v, null, 2);
  if (s === undefined) s = 'null';
  if (s.length > max) s = s.slice(0, max) + '\n… truncated';
  s = esc(s);
  return raw(s.replace(/(&quot;(?:[^&]|&(?!quot;))*?&quot;)(\s*:)?|\b(true|false)\b|\bnull\b|-?\b\d+(?:\.\d+)?(?:e[+-]?\d+)?\b/g, (m, str, colon, bool) => {
    if (str) {
      if (colon) return `<span class="k">${str}</span>${colon}`;
      const inner = str.slice(6, -6);
      if (/^[GC][A-Z2-7]{55}$/.test(inner)) return `<span class="s">&quot;<a href="#/${inner[0] === 'C' ? 'contract' : 'account'}/${inner}">${inner}</a>&quot;</span>`;
      if (/^[0-9a-f]{64}$/.test(inner)) return `<span class="s">&quot;<a href="#/tx/${inner}">${inner}</a>&quot;</span>`;
      return `<span class="s">${str}</span>`;
    }
    if (bool) return `<span class="b">${m}</span>`;
    if (m === 'null') return `<span class="z">null</span>`;
    return `<span class="n">${m}</span>`;
  }));
}

export const json = (v, cls = '') => html`<pre class="json ${cls}">${jsonHTML(v)}</pre>`;

// ---- states -----------------------------------------------------------------

export function skeleton(rows = 6, height = 0) {
  if (height) return html`<div class="skel" style="height:${height}px;margin:12px 16px"></div>`;
  return html`<div class="skel-rows">${Array.from({ length: rows }, (_, i) => html`<div class="skel" style="width:${90 - (i * 7) % 30}%"></div>`)}</div>`;
}

export function empty(title = 'Nothing here yet', text = '', ic = 'inbox') {
  return html`<div class="empty-state"><div class="icon">${icon(ic)}</div><h3>${title}</h3>${text ? html`<p>${text}</p>` : ''}</div>`;
}

export function errorBox(err, retry = true) {
  const status = err instanceof ApiError && err.status ? `HTTP ${err.status}` : '';
  const url = err instanceof ApiError ? err.url : '';
  const hint = err instanceof ApiError && err.status === 404 ? 'Not found in the index. It may be outside the indexed range or pruned by retention.' : '';
  return html`<div class="error-box">${icon('alert')}<div class="msg"><b>${hint ? 'Not found' : 'Request failed'}</b>${hint || err.message}${url ? html`<br><code>${status} ${url}</code>` : ''}</div>${retry ? html`<button class="btn sm" data-retry>${icon('refresh')}Retry</button>` : ''}</div>`;
}

// load runs fetcher, rendering skeleton → content | error (with retry) into el.
// render(data) returns markup; after(el, data) wires behaviour.
export async function load(el, fetcher, render, { after, skel, quiet = false } = {}) {
  if (!el) return;
  if (!quiet) setHTML(el, skel ?? skeleton());
  try {
    const data = await fetcher();
    if (!el.isConnected) return data;
    setHTML(el, render(data));
    after?.(el, data);
    return data;
  } catch (e) {
    if (e.name === 'AbortError' || !el.isConnected) return;
    if (quiet && el.childElementCount) return; // keep stale content on background refresh
    setHTML(el, errorBox(e));
    el.querySelector('[data-retry]')?.addEventListener('click', () => load(el, fetcher, render, { after, skel }));
  }
}

// ---- table ------------------------------------------------------------------

// table renders rows with column specs {h, v(row), cls}; rowHref makes rows clickable.
export function table(cols, rows, { emptyTitle = 'No rows', emptyText = '', rowHref } = {}) {
  if (!rows?.length) return empty(emptyTitle, emptyText);
  return html`<div class="table-wrap"><table class="t">
    <thead><tr>${cols.map((c) => html`<th class="${c.cls || ''}">${c.h}</th>`)}</tr></thead>
    <tbody>${rows.map((r) => html`<tr ${raw(rowHref ? `class="click" data-href="${esc(rowHref(r))}"` : '')}>${cols.map((c) => html`<td class="${c.cls || ''}">${c.v(r)}</td>`)}</tr>`)}</tbody>
  </table></div>`;
}

document.addEventListener('click', (e) => {
  const tr = e.target.closest('tr[data-href]');
  if (!tr || e.target.closest('a,button,input,select')) return;
  if (window.getSelection()?.toString()) return;
  location.hash = tr.dataset.href.replace(/^#/, '');
});

// feed renders a keyset-paginated table with a "Load more" button.
// fetchPage(before) → rows; cursor(lastRow) → before value for the next page.
export function feed(el, { fetchPage, cols, cursor, pageSize = 50, rowHref, emptyTitle, emptyText }) {
  let rows = [];
  let done = false;
  const draw = () => {
    setHTML(el, html`${table(cols, rows, { rowHref, emptyTitle, emptyText })}${rows.length && !done ? html`<div class="more"><button class="btn" data-more>Load more</button></div>` : ''}`);
    el.querySelector('[data-more]')?.addEventListener('click', more);
  };
  async function more(e) {
    const btn = e?.currentTarget;
    if (btn) { btn.disabled = true; btn.textContent = 'Loading…'; }
    try {
      const page = await fetchPage(rows.length ? cursor(rows[rows.length - 1]) : '');
      rows = rows.concat(page);
      done = page.length < pageSize;
      draw();
    } catch (err) {
      if (btn) { btn.disabled = false; btn.textContent = 'Load more'; toast(err.message); return; }
      throw err;
    }
  }
  const start = async () => {
    rows = []; done = false;
    setHTML(el, skeleton(8));
    try {
      await more();
    } catch (err) {
      if (err.name === 'AbortError' || !el.isConnected) return;
      setHTML(el, errorBox(err));
      el.querySelector('[data-retry]')?.addEventListener('click', start);
    }
  };
  start();
  return { reload: start };
}

// ---- small layout helpers ------------------------------------------------------

export function card(title, body, { hint = '', actions = '', id = '', flush = false } = {}) {
  return html`<section class="card" ${raw(id ? `id="${esc(id)}"` : '')}>
    ${title ? html`<div class="card-head"><h2>${title}</h2>${hint ? html`<span class="hint">${hint}</span>` : ''}${actions}</div>` : ''}
    <div class="card-body ${flush ? 'flush' : ''}">${body}</div></section>`;
}

export function tile(label, value, { foot = '', hero = false, title = '' } = {}) {
  return html`<div class="tile" title="${title}"><div class="label">${label}</div><div class="value ${hero ? 'hero' : ''}">${value}</div>${foot ? html`<div class="foot">${foot}</div>` : ''}</div>`;
}

export function pageHead(title, { sub = '', crumbs = '', actions = '' } = {}) {
  return html`<div class="page-head"><div>${crumbs ? html`<div class="crumbs">${crumbs}</div>` : ''}<h1>${title}</h1>${sub ? html`<div class="sub">${sub}</div>` : ''}</div>${actions ? html`<div class="actions">${actions}</div>` : ''}</div>`;
}

// seg renders a segmented control; onPick(value) is wired by bindSeg.
export function seg(name, options, current) {
  return html`<div class="seg" data-seg="${name}">${options.map(([v, l]) => html`<button type="button" data-v="${v}" class="${String(v) === String(current) ? 'on' : ''}">${l}</button>`)}</div>`;
}

export function bindSeg(root, name, onPick) {
  const el = root.querySelector(`[data-seg="${name}"]`);
  el?.addEventListener('click', (e) => {
    const b = e.target.closest('button[data-v]');
    if (!b) return;
    el.querySelectorAll('button').forEach((x) => x.classList.toggle('on', x === b));
    onPick(b.dataset.v);
  });
}

// hbars renders horizontal HTML bars: items [{label, value, color, href, title}].
export function hbars(items, { fmt = compact, color = 'var(--s1)', max } = {}) {
  if (!items.length) return empty('No data in this window');
  const m = max ?? Math.max(...items.map((i) => +i.value || 0), 1);
  return html`<div class="bars">${items.map((i) => html`<div class="bar-row" title="${i.title || `${i.plain || ''} ${fmt(i.value)}`}">
    <div class="lbl">${i.href ? html`<a href="${i.href}">${i.label}</a>` : i.label}</div>
    <div class="track"><div class="fill" style="width:${raw(((+i.value || 0) / m * 100).toFixed(2))}%;background:${raw(i.color || color)}"></div></div>
    <div class="val">${fmt(i.value)}</div></div>`)}</div>`;
}

// stackbar renders one 100% bar from parts [{label, value, color}] with a legend.
export function stackbar(parts, { fmt = compact } = {}) {
  const total = parts.reduce((s, p) => s + (+p.value || 0), 0) || 1;
  return html`<div class="stackbar">${parts.filter((p) => +p.value > 0).map((p) => html`<span title="${p.label}: ${fmt(p.value)} (${pct(p.value / total * 100, 1)})" style="flex:${raw(String(+p.value))};background:${raw(p.color)}"></span>`)}</div>
    <div class="legend">${parts.map((p) => html`<span><i class="sw" style="background:${raw(p.color)}"></i>${p.label} <b>${fmt(p.value)}</b> <span class="muted">${pct(p.value / total * 100, 1)}</span></span>`)}</div>`;
}

export function kv(pairs) {
  return html`<div class="kv">${pairs.filter(Boolean).map(([k, v]) => html`<div>${k}</div><div>${v}</div>`)}</div>`;
}

export function tabs(items, current) {
  return html`<div class="tabs">${items.map(([id, label, count]) => html`<a href="javascript:void 0" data-tab="${id}" class="${id === current ? 'on' : ''}">${label}${count !== undefined ? html`<span class="count">${count}</span>` : ''}</a>`)}</div>`;
}

export function bindTabs(root, onPick) {
  root.querySelectorAll('[data-tab]').forEach((a) => a.addEventListener('click', (e) => {
    e.preventDefault();
    root.querySelectorAll('[data-tab]').forEach((x) => x.classList.toggle('on', x === a));
    onPick(a.dataset.tab);
  }));
}
