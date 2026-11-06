// App shell: hash router, live ingestion status, global search, theme, settings.

import { get, qs, apiBase, setApiBase } from './api.js';
import { html, setHTML, hydrate, icon, iconSVG, state, num, duration, short, errorBox, toast } from './ui.js';
import { disposeCharts } from './charts.js';
import { loadLogos } from './logos.js';

// [pattern, module, nav key]
const routes = [
  [/^$/, 'overview', 'overview'],
  [/^ledgers$/, 'ledgers', 'ledgers'],
  [/^ledger\/(\d+)$/, 'ledger', 'ledgers'],
  [/^txs$/, 'txs', 'txs'],
  [/^tx\/([0-9a-fA-F]{64})$/, 'tx', 'txs'],
  [/^ops$/, 'ops', 'ops'],
  [/^accounts$/, 'accounts', 'accounts'],
  [/^account\/([A-Z0-9]+)$/, 'account', 'accounts'],
  [/^transfers$/, 'transfers', 'transfers'],
  [/^markets$/, 'markets', 'markets'],
  [/^pair\/([^/]+)\/([^/]+)$/, 'pair', 'markets'],
  [/^trades$/, 'trades', 'trades'],
  [/^assets$/, 'assets', 'assets'],
  [/^asset\/([^/]+)$/, 'asset', 'assets'],
  [/^pools$/, 'pools', 'pools'],
  [/^protocols$/, 'protocols', 'protocols'],
  [/^protocol\/([a-z_]+)$/, 'protocol', 'protocols'],
  [/^contracts$/, 'contracts', 'contracts'],
  [/^contract\/(C[A-Z2-7]{55})$/, 'contract', 'contracts'],
  [/^events$/, 'events', 'events'],
  [/^api$/, 'playground', 'api'],
  [/^leaders$/, 'leaders', 'leaders'],
  [/^alerts$/, 'alerts', 'alerts'],
  [/^watchlist$/, 'watchlist', 'watchlist'],
  [/^tags$/, 'labels', 'tags'],
  [/^compare$/, 'compare', 'compare'],
];

const view = document.getElementById('view');
let cleanups = [];
let renderSeq = 0;

function parseHash() {
  const h = location.hash.replace(/^#\/?/, '');
  const [path, query = ''] = h.split('?');
  return { path: path.replace(/\/+$/, ''), query: Object.fromEntries(new URLSearchParams(query)) };
}

// ctx is passed to pages: timers and cleanups are torn down on navigation.
function makeCtx(seq) {
  return {
    alive: () => seq === renderSeq,
    onCleanup: (fn) => cleanups.push(fn),
    // every runs fn on an interval while the tab is visible and the page is current.
    every(ms, fn) {
      const id = setInterval(() => { if (!document.hidden && seq === renderSeq) fn(); }, ms);
      cleanups.push(() => clearInterval(id));
    },
    // setQuery updates the hash query without re-rendering the page.
    setQuery(q) {
      const { path } = parseHash();
      const s = qs(q);
      history.replaceState(null, '', `#/${path}${s}`);
    },
  };
}

async function render() {
  const seq = ++renderSeq;
  cleanups.forEach((f) => { try { f(); } catch {} });
  cleanups = [];
  disposeCharts();
  const { path, query } = parseHash();
  let match = null;
  for (const [re, mod, nav] of routes) {
    const m = path.match(re);
    if (m) { match = { mod, nav, params: m.slice(1).map(decodeURIComponent) }; break; }
  }
  document.querySelectorAll('#nav a').forEach((a) => a.classList.toggle('active', a.dataset.nav === match?.nav));
  closeMenu();
  if (!match) {
    setHTML(view, html`<div class="empty-state"><div class="icon">${icon('search')}</div><h3>Page not found</h3><p>No route for <code>#/${path}</code>. <a href="#/">Back to overview</a></p></div>`);
    return;
  }
  setHTML(view, html`<div class="skel" style="height:28px;width:240px;margin-bottom:20px"></div><div class="skel" style="height:320px"></div>`);
  try {
    const page = await import(`./pages/${match.mod}.js`);
    if (seq !== renderSeq) return;
    view.innerHTML = '';
    window.scrollTo(0, 0);
    await page.default(view, match.params, query, makeCtx(seq));
    hydrate(view);
  } catch (e) {
    if (seq !== renderSeq) return;
    console.error(e);
    setHTML(view, errorBox(e, false));
  }
}

// ---- live status -------------------------------------------------------------

const live = document.getElementById('live');
async function pollStatus() {
  try {
    const s = await get('/status');
    state.status = s;
    const firstNet = !state.network;
    state.network = s.network || '';
    const lag = s.lag_seconds ?? null;
    const tipLag = s.lag_ledgers ?? null;
    let cls = 'ok', text;
    if (!s.cursor) { cls = 'behind'; text = 'No data yet'; }
    else if (lag !== null && lag > 60) { cls = 'behind'; text = `Catching up, ${duration(lag)} behind`; }
    else text = `Live at #${num(s.cursor)}${lag !== null ? `, ${lag}s lag` : ''}`;
    live.className = 'live ' + cls;
    live.querySelector('.live-text').textContent = text;
    live.title = `Network: ${s.network}\nCursor: ${s.cursor}\nRPC tip: ${s.rpc_tip || 'n/a (API-only mode)'}${tipLag !== null ? `\nLedgers behind tip: ${tipLag}` : ''}\nIndexed ledgers: ${s.ledgers_indexed}`;
    setHTML(document.getElementById('sidebar-foot'), html`Network <b>${s.network}</b><br>Indexed <b>${num(s.ledgers_indexed)}</b> ledgers<br>from #${num(s.first_ledger)}<br><span class="muted">API ${apiBase() || location.host}</span>`);
    window.dispatchEvent(new CustomEvent('status', { detail: s }));
    if (firstNet && state.network) window.dispatchEvent(new CustomEvent('network'));
  } catch (e) {
    live.className = 'live down';
    live.querySelector('.live-text').textContent = 'API unreachable';
    live.title = e.message;
  }
}

// ---- search ------------------------------------------------------------------

const form = document.getElementById('search');
const input = document.getElementById('search-input');
const results = document.getElementById('search-results');
let searchTimer, searchItems = [], sel = -1;

const ROUTE = { ledger: 'ledger', transaction: 'tx', pool: 'pools', account: 'account', contract: 'contract', asset: 'asset' };
const hrefOf = (r) => r.kind === 'pool' ? `#/pools?id=${r.id}` : r.kind === 'tag' ? `#/tags?tag=${encodeURIComponent(r.id)}` : `#/${ROUTE[r.kind]}/${encodeURIComponent(r.id)}`;

function showResults(items, q) {
  searchItems = items; sel = items.length ? 0 : -1;
  results.hidden = false;
  setHTML(results, items.length
    ? html`${items.map((r, i) => html`<a href="${hrefOf(r)}" class="${i === 0 ? 'sel' : ''}"><span class="kind">${r.kind}</span><span>${r.label}</span><span class="muted mono">${r.kind === 'asset' ? r.id : short(r.id, 8, 6)}</span></a>`)}`
    : html`<div class="empty">No match for “${q}” in the index. Try a ledger number, a 64-char tx hash, a G… account, a C… contract, an asset code or a #tag.</div>`);
}

async function runSearch(q) {
  if (!q) { results.hidden = true; return; }
  try { showResults(await get('/search' + qs({ q })), q); } catch (e) { results.hidden = false; setHTML(results, html`<div class="empty">${e.message}</div>`); }
}

input.addEventListener('input', () => { clearTimeout(searchTimer); searchTimer = setTimeout(() => runSearch(input.value.trim()), 220); });
input.addEventListener('keydown', (e) => {
  if (results.hidden || !searchItems.length) return;
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault();
    sel = (sel + (e.key === 'ArrowDown' ? 1 : -1) + searchItems.length) % searchItems.length;
    results.querySelectorAll('a').forEach((a, i) => a.classList.toggle('sel', i === sel));
  } else if (e.key === 'Escape') { results.hidden = true; }
});
form.addEventListener('submit', async (e) => {
  e.preventDefault();
  const q = input.value.trim();
  if (!q) return;
  if (sel >= 0 && searchItems[sel]) { go(hrefOf(searchItems[sel])); return; }
  try {
    const r = await get('/search' + qs({ q }));
    if (r.length === 1) go(hrefOf(r[0])); else showResults(r, q);
  } catch (err) { toast(err.message); }
});
results.addEventListener('click', (e) => { if (e.target.closest('a')) { results.hidden = true; input.blur(); } });
document.addEventListener('click', (e) => { if (!form.contains(e.target)) results.hidden = true; });
document.addEventListener('keydown', (e) => {
  if (e.key === '/' && !/^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement?.tagName)) { e.preventDefault(); input.focus(); input.select(); }
});
function go(href) { results.hidden = true; input.value = ''; input.blur(); location.hash = href.slice(1); }

// ---- theme / menu / settings ----------------------------------------------------

const themeBtn = document.getElementById('theme-btn');
const THEMES = ['auto', 'light', 'dark'];
function applyTheme(t) {
  if (t === 'auto') delete document.documentElement.dataset.theme; else document.documentElement.dataset.theme = t;
  themeBtn.querySelector('i').innerHTML = iconSVG(t === 'dark' ? 'moon' : t === 'light' ? 'sun' : 'monitor');
  themeBtn.title = `Theme: ${t}`;
}
let theme = 'auto';
try { theme = localStorage.getItem('theme') || 'auto'; } catch {}
applyTheme(theme);
themeBtn.addEventListener('click', () => {
  theme = THEMES[(THEMES.indexOf(theme) + 1) % THEMES.length];
  try { theme === 'auto' ? localStorage.removeItem('theme') : localStorage.setItem('theme', theme); } catch {}
  applyTheme(theme);
  toast(`Theme: ${theme}`);
  window.dispatchEvent(new Event('resize'));
});

const sidebar = document.getElementById('sidebar'), scrim = document.getElementById('scrim');
function closeMenu() { sidebar.classList.remove('open'); scrim.classList.remove('open'); }
document.getElementById('menu-btn').addEventListener('click', () => { sidebar.classList.add('open'); scrim.classList.add('open'); });
scrim.addEventListener('click', closeMenu);

const dlg = document.getElementById('settings'), baseInput = document.getElementById('api-base'), tokenInput = document.getElementById('labels-token'),
  keyInput = document.getElementById('api-key');
document.getElementById('settings-btn').addEventListener('click', () => {
  baseInput.value = apiBase();
  try { tokenInput.value = localStorage.getItem('labelsToken') || ''; keyInput.value = localStorage.getItem('apiKey') || ''; } catch {}
  dlg.showModal();
});
dlg.addEventListener('close', () => {
  if (dlg.returnValue === 'save') {
    setApiBase(baseInput.value.trim());
    try {
      tokenInput.value.trim() ? localStorage.setItem('labelsToken', tokenInput.value.trim()) : localStorage.removeItem('labelsToken');
      keyInput.value.trim() ? localStorage.setItem('apiKey', keyInput.value.trim()) : localStorage.removeItem('apiKey');
    } catch {}
  } else if (dlg.returnValue === 'reset') setApiBase('');
  else return;
  state.network = '';
  Promise.all([pollStatus(), loadLogos()]).then(render);
  toast(`API: ${apiBase() || location.origin}`);
});

// ---- boot --------------------------------------------------------------------

hydrate(document);
window.addEventListener('hashchange', render);
// Logos never hold up the first paint for long; later views pick them up.
Promise.all([pollStatus(), Promise.race([loadLogos(), new Promise((r) => setTimeout(r, 1500))])]).finally(render);
setInterval(() => { if (!document.hidden) loadLogos(); }, 10 * 60 * 1000);
setInterval(() => { if (!document.hidden) pollStatus(); }, 5000);
