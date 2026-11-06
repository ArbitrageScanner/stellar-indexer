// Address tags (auto heuristics + user labels) and the local watchlist.
import { get, send } from './api.js';
import { html, raw, icon, toast, short } from './ui.js';

const GROUP_COLOR = { size: 'var(--s4)', behavior: 'var(--s7)', role: 'var(--s3)', defi: 'var(--s5)', lifecycle: 'var(--s1)', user: 'var(--accent)' };

// Documented heuristics (mirrors internal/api/insights.go).
export const AUTO_TAGS = [
  ['whale', 'Whale', 'size', 'XLM balance ≥ 1M.'],
  ['shark', 'Large holder', 'size', 'XLM balance ≥ 100K.'],
  ['arbitrage_bot', 'Arbitrage bot', 'behavior', '≥ 3 same-transaction round trips (first sold asset = last bought) in 7 days, or one plus a flash loan.'],
  ['market_maker', 'Market maker', 'behavior', 'Two-sided SDEX offers (≥ 4 open), or ≥ 100 offer updates in 24h.'],
  ['hft', 'High-frequency', 'behavior', '≥ 500 transactions or ≥ 1000 fills in 24h.'],
  ['failing_bot', 'Mostly failing', 'behavior', '≥ 50 transactions in 24h, at least half failed.'],
  ['issuer', 'Asset issuer', 'role', 'Issued at least one asset seen in the index.'],
  ['token_contract', 'Token contract', 'role', 'Contract is a Stellar Asset Contract or token.'],
  ['protocol_*', 'Protocol contract', 'role', 'Contract emits decoded protocol events (pool, pair, router, lending pool).'],
  ['flash_loans', 'Flash loans', 'defi', 'Took Blend flash loans.'],
  ['lp', 'Liquidity provider', 'defi', 'Holds classic pool shares or made Soroban liquidity deposits.'],
  ['borrower', 'Blend borrower', 'defi', 'Borrowed on Blend.'],
  ['defi_user', 'DeFi user', 'defi', 'Has decoded protocol events as the user.'],
  ['new_account', 'New account', 'lifecycle', 'Created < 7 days ago (creation ledger = starting sequence number >> 32).'],
  ['dormant', 'Dormant', 'lifecycle', 'Known account with no transactions or transfers for 7 days (or none since indexing began).'],
];

export function chip(t, { removable = false, address = '' } = {}) {
  const color = GROUP_COLOR[t.group] || 'var(--muted)';
  const label = t.kind === 'user' ? `#${t.label}` : t.label;
  const href = t.kind === 'user' ? `#/tags?tag=${encodeURIComponent(t.id)}` : '#/tags?auto=1';
  return html`<span class="chip ${t.kind}" data-tip="${t.reason || (t.kind === 'user' ? 'User label' : '')}"><span class="sw" style="background:${raw(color)}"></span><a href="${href}">${label}</a>${removable && t.kind === 'user' ? html`<button type="button" class="chip-x" data-untag="${t.id}" data-address="${address}" title="Remove label" aria-label="Remove label ${label}">×</button>` : ''}</span>`;
}

export const labelChips = (labels) => labels?.length ? html`${labels.map((l) => chip({ id: l, label: l, kind: 'user', group: 'user' }))}` : '';

// tagBar renders auto + user tags with an inline "add tag" form into el.
export async function tagBar(el, address, { editable = true } = {}) {
  const draw = (tags) => {
    el.innerHTML = '';
    el.insertAdjacentHTML('beforeend', html`<div class="chips">${tags.length ? tags.map((t) => chip(t, { removable: editable, address })) : html`<span class="muted chips-empty">No tags yet.</span>`}
      ${editable ? html`<form class="tag-form" data-tagform><input name="tag" placeholder="Add tag" maxlength="32" aria-label="Add a tag" list="known-tags"><input name="note" placeholder="Note (optional)" maxlength="280" aria-label="Note (optional)"><button type="submit" class="btn sm">Add</button><datalist id="known-tags"></datalist></form>` : ''}</div>`.s);
    const f = el.querySelector('[data-tagform]');
    f?.addEventListener('submit', async (e) => {
      e.preventDefault();
      const d = new FormData(f);
      const tag = (d.get('tag') || '').trim();
      if (!tag) return;
      try { await send('POST', '/labels', { address, tag, note: (d.get('note') || '').trim() }); toast(`Tagged #${tag.replace(/^#/, '').toLowerCase()}`); refresh(); }
      catch (err) { toast(err.message); }
    });
    get('/labels/tags').then((ts) => { const dl = el.querySelector('#known-tags'); if (dl) dl.innerHTML = ts.map((t) => `<option value="${t.tag.replace(/"/g, '')}">`).join(''); }).catch(() => {});
    el.querySelectorAll('[data-untag]').forEach((b) => b.addEventListener('click', async (e) => {
      e.preventDefault();
      try { await send('DELETE', `/labels/${b.dataset.address}/${encodeURIComponent(b.dataset.untag)}`); toast('Label removed'); refresh(); }
      catch (err) { toast(err.message); }
    }));
  };
  const refresh = () => get(`/accounts/${address}/tags`).then(draw).catch((e) => { el.innerHTML = html`<span class="muted">${e.message}</span>`.s; });
  await refresh();
}

// ---- watchlist (per-browser) ----
const WKEY = 'watchlist';
export function watchlist() {
  try { return JSON.parse(localStorage.getItem(WKEY) || '[]').filter((a) => typeof a === 'string'); } catch { return []; }
}
function saveWatch(list) { try { localStorage.setItem(WKEY, JSON.stringify([...new Set(list)])); } catch {} }
export const isWatched = (a) => watchlist().includes(a);
export function toggleWatch(a) {
  const l = watchlist();
  const on = !l.includes(a);
  saveWatch(on ? [...l, a] : l.filter((x) => x !== a));
  toast(on ? `Watching ${short(a)}` : 'Removed from watchlist');
  return on;
}
export function addWatch(addrs) { saveWatch([...watchlist(), ...addrs]); }

export function watchBtn(a) {
  const on = isWatched(a);
  return html`<button type="button" class="btn ${on ? 'primary' : ''}" data-watch="${a}" aria-pressed="${on ? 'true' : 'false'}">${on ? 'Watching' : 'Watch'}</button>`;
}
export function bindWatch(root) {
  root.querySelectorAll('[data-watch]').forEach((b) => b.addEventListener('click', () => {
    const on = toggleWatch(b.dataset.watch);
    b.classList.toggle('primary', on);
    b.textContent = on ? 'Watching' : 'Watch'; b.setAttribute('aria-pressed', on ? 'true' : 'false');
  }));
}
