import { get, qs } from '../api.js';
import { html, setHTML, pageHead, card, table, num, compact, amount, pct, ago, addr, asset, icon, empty, errorBox, raw } from '../ui.js';
import { chip } from '../tags.js';

const usd = (v) => v === null || v === undefined ? '—' : '$' + compact(v);
const signed = (v) => v === null || v === undefined ? '—' : html`<span class="${v > 0 ? 'up' : v < 0 ? 'down' : ''}">${v > 0 ? '+' : ''}${compact(v)}</span>`;

async function profile(a, hours) {
  const [pf, st, tags] = await Promise.all([
    get(`/accounts/${a}/portfolio?history=false`), get(`/accounts/${a}/stats` + qs({ hours })), get(`/accounts/${a}/tags`)]);
  return { a, pf, st, tags };
}

export default async function compare(view, _p, query, ctx) {
  const a = query.a || '', b = query.b || '';
  setHTML(view, html`
    ${pageHead('Compare addresses', { sub: 'Side-by-side portfolio, trading indicators and tags for two addresses, with the assets and counterparties they share.' })}
    <form class="acct-pick" id="f">
      <input name="a" placeholder="Address A" aria-label="Address A" autocomplete="off" spellcheck="false" value="${a}" required pattern="[GCM][A-Z2-7]{55,68}">
      <span class="muted acct-vs">vs</span>
      <input name="b" placeholder="Address B" aria-label="Address B" autocomplete="off" spellcheck="false" value="${b}" required pattern="[GCM][A-Z2-7]{55,68}">
      <button type="submit" class="btn primary">${icon('swap')}Compare</button>
    </form>
    <div id="out"></div>`);
  view.querySelector('#f').addEventListener('submit', (e) => {
    e.preventDefault();
    const d = new FormData(e.target);
    location.hash = `/compare?a=${d.get('a').trim()}&b=${d.get('b').trim()}`;
  });
  const out = view.querySelector('#out');
  if (!a || !b) { setHTML(out, empty('Pick two addresses', 'Paste two G… or C… addresses above, or use “Compare” on an address page or the watchlist.', 'swap')); return; }
  setHTML(out, html`<div class="skel acct-skel"></div>`);
  let A, B;
  try { [A, B] = await Promise.all([profile(a, 168), profile(b, 168)]); } catch (e) { setHTML(out, errorBox(e, false)); return; }
  if (!ctx.alive()) return;
  const rows = [
    ['Net worth (USD)', (x) => usd(x.pf.value_usd), (x) => x.pf.value_usd],
    ['Net worth (XLM)', (x) => compact(x.pf.value_xlm), (x) => x.pf.value_xlm],
    ['24h change', (x) => x.pf.change_24h_pct === null ? '—' : pct(x.pf.change_24h_pct, 2, true), (x) => x.pf.change_24h_pct],
    ['Assets held', (x) => num(x.pf.holdings.length), (x) => x.pf.holdings.length],
    ['Volume 7d (XLM)', (x) => compact(x.st.trading.volume_xlm), (x) => x.st.trading.volume_xlm],
    ['Fills 7d', (x) => num(x.st.trading.trades), (x) => x.st.trading.trades],
    ['Realized PnL 7d (XLM)', (x) => signed(x.st.trading.pnl.realized_xlm), (x) => x.st.trading.pnl.realized_xlm],
    ['Arbitrage round trips', (x) => num(x.st.trading.cycles), (x) => x.st.trading.cycles],
    ['Transactions 7d', (x) => num(x.st.txs.txs), (x) => x.st.txs.txs],
    ['Fees paid 7d (XLM)', (x) => amount(x.st.txs.fees_xlm, 4), (x) => x.st.txs.fees_xlm],
    ['Last seen', (x) => x.st.seen.last_seen ? ago(x.st.seen.last_seen) : '—', () => null],
  ];
  const assetsA = new Set(A.pf.holdings.map((h) => h.asset)), common = B.pf.holdings.filter((h) => assetsA.has(h.asset)).map((h) => h.asset);
  const cpA = new Map(A.st.counterparties.map((c) => [c.address, c])), cpCommon = B.st.counterparties.filter((c) => cpA.has(c.address));
  const direct = A.st.counterparties.find((c) => c.address === b) || B.st.counterparties.find((c) => c.address === a);
  const win = (fa, fb) => (fa ?? -Infinity) > (fb ?? -Infinity) ? 'a' : (fb ?? -Infinity) > (fa ?? -Infinity) ? 'b' : '';
  setHTML(out, html`
    ${direct ? html`<div class="notice">${icon('arrows')}<div>These addresses transacted with each other directly: ${num(direct.transfers)} transfers in the last 7 days.</div></div>` : ''}
    <div class="cmp acct-row">
      ${[A, B].map((x, i) => card(html`<span class="acct-side">${i ? 'B' : 'A'}</span>${addr(x.a)}`, html`<div class="big-value">${x.pf.value_usd !== null ? usd(x.pf.value_usd) : `${compact(x.pf.value_xlm)} XLM`}</div>
        <div class="acct-mix">${x.pf.holdings.slice(0, 5).map((h) => html`<span><b>${h.asset === 'native' ? 'XLM' : h.asset.split(':')[0].slice(0, 12)}</b>${h.share ? html` ${pct(h.share * 100, 0)}` : ''}</span>`)}</div>
        <div class="chips acct-cmp-tags">${x.tags.length ? x.tags.map((t) => chip(t)) : html`<span class="muted">No tags</span>`}</div>`, { actions: html`<a class="btn sm" href="#/account/${x.a}">Open</a>` }))}
    </div>
    <div class="grid g-main acct-cmp-body">
    ${card('Side by side', html`<div class="table-wrap"><table class="t"><thead><tr><th>Indicator</th><th class="num">A</th><th class="num">B</th></tr></thead><tbody>
      ${rows.map(([label, f, v]) => { const w = win(v(A), v(B)); return html`<tr><td>${label}</td><td class="num ${w === 'a' ? 'acct-win' : ''}">${f(A)}</td><td class="num ${w === 'b' ? 'acct-win' : ''}">${f(B)}</td></tr>`; })}
    </tbody></table></div>`, { flush: true, hint: 'Bold is higher' })}
    <div class="stack">
      ${card(`Shared assets (${common.length})`, common.length ? html`<div class="chips">${common.map((x) => asset(x))}</div>` : html`<div class="muted">No asset in common.</div>`)}
      ${card(`Shared counterparties (${cpCommon.length})`, table([
        { h: 'Address', v: (c) => addr(c.address) },
        { h: 'Transfers with A', cls: 'num', v: (c) => num(cpA.get(c.address).transfers) },
        { h: 'Transfers with B', cls: 'num', v: (c) => num(c.transfers) },
      ], cpCommon, { emptyTitle: 'None among the top counterparties' }), { flush: true })}
    </div>
    </div>`);
}
