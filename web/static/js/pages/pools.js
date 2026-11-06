import { get } from '../api.js';
import { html, setHTML, load, pageHead, table, num, compact, price, asset, ledgerLink, expertURL, icon, tile, short, raw } from '../ui.js';

export default async function pools(view, _p, query) {
  setHTML(view, html`
    ${pageHead('Liquidity pools', { sub: html`Classic constant-product AMM pools (CAP-38) touched since indexing began, ordered by number of participants. Soroban DEX pools (Soroswap, Aquarius, CLMM) are under <a href="#/protocols">Protocols</a>.` })}
    <div class="tiles" id="tiles"></div>
    <div class="card mkt-pin"><div class="card-head"><h2>Pools</h2><input type="search" id="f" class="mkt-pool-filter" placeholder="Filter by asset code" aria-label="Filter pools by asset code or pool id" value="${query.id || ''}"></div><div class="card-body flush" id="list"></div></div>`);
  let rows = [];
  const draw = () => {
    const q = view.querySelector('#f').value.trim().toUpperCase();
    const list = rows.filter((p) => !q || p.pool_id.toUpperCase() === q || p.asset_a.toUpperCase().includes(q) || p.asset_b.toUpperCase().includes(q) || (q === 'XLM' && (p.asset_a === 'native' || p.asset_b === 'native')));
    setHTML(view.querySelector('#list'), table([
      { h: 'Pool', v: (p) => html`<span class="pair">${asset(p.asset_a)}<span class="slash">/</span>${asset(p.asset_b)}</span>` },
      { h: 'Reserve A', cls: 'num', v: (p) => compact(p.reserve_a) },
      { h: 'Reserve B', cls: 'num', v: (p) => compact(p.reserve_b) },
      { h: 'Spot price (B per A)', cls: 'num', v: (p) => price(p.price_b_per_a) },
      { h: 'Fee', cls: 'num', v: (p) => `${p.fee_bp / 100}%` },
      { h: 'Participants', cls: 'num', v: (p) => num(p.trustline_count) },
      { h: 'Updated', cls: 'num', v: (p) => ledgerLink(p.last_modified) },
      { h: 'Chart', v: (p) => html`<a href="#/pair/${encodeURIComponent(p.asset_a)}/${encodeURIComponent(p.asset_b)}">Price</a>` },
      { h: 'Id', v: (p) => { const u = expertURL('pool', p.pool_id); return u ? html`<a class="ext mono" href="${u}" target="_blank" rel="noopener">${short(p.pool_id, 6, 4)} ${icon('ext')}</a>` : html`<span class="mono">${short(p.pool_id, 6, 4)}</span>`; } },
    ], list, { emptyTitle: rows.length ? 'No pool matches' : 'No pools indexed yet', emptyText: 'Pools appear after a ledger touches their entry (a trade, deposit or withdrawal).' }));
  };
  await load(view.querySelector('#list'), () => get('/pools?limit=500'), (r) => {
    rows = r;
    const xlm = r.reduce((s, p) => s + (p.asset_a === 'native' ? p.reserve_a : 0) + (p.asset_b === 'native' ? p.reserve_b : 0), 0);
    setHTML(view.querySelector('#tiles'), html`${tile('Pools indexed', num(r.length))}${tile('XLM locked (indexed pools)', compact(xlm))}${tile('Participants', compact(r.reduce((s, p) => s + p.trustline_count, 0)))}`);
    return raw('');
  }, { after: draw });
  view.querySelector('#f').addEventListener('input', draw);
}
