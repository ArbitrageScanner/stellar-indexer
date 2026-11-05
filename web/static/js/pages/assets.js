import { get, qs } from '../api.js';
import { html, setHTML, load, pageHead, seg, bindSeg, table, num, compact, asset, addr, tag, card, hbars } from '../ui.js';

export default async function assets(view, _p, query, ctx) {
  let hours = query.hours || '24';
  setHTML(view, html`
    ${pageHead('Assets', {
      sub: 'Most active assets by trades and token transfers. Classic assets, their Stellar Asset Contract (SAC) ids and custom Soroban tokens share one key, so activity from every venue adds up.',
      actions: seg('hours', [['1', '1h'], ['24', '24h'], ['168', '7d']], hours),
    })}
    <div class="grid g2" style="margin-bottom:16px">
      ${card('Most traded', html`<div id="b1"></div>`, { hint: 'fills touching the asset' })}
      ${card('Most transferred', html`<div id="b2"></div>`, { hint: 'transfer / mint / burn events' })}
    </div>
    <div class="card"><div class="card-body flush" id="list"></div></div>`);
  const run = () => load(view.querySelector('#list'), () => get('/assets' + qs({ hours, limit: 200 })), (rows) => {
    const bar = (k) => hbars(rows.slice().sort((a, b) => b[k] - a[k]).slice(0, 8).map((a) => ({ label: a.code || a.asset_key.slice(0, 8), value: a[k], href: `#/asset/${encodeURIComponent(a.asset_key)}`, title: a.asset_key })));
    setHTML(view.querySelector('#b1'), bar('trades'));
    setHTML(view.querySelector('#b2'), bar('transfers'));
    return table([
      { h: 'Asset', v: (a) => asset(a.asset_key, { issuer: true }) },
      { h: 'Type', v: (a) => tag(a.type) },
      { h: 'Trades', cls: 'num', v: (a) => num(a.trades) },
      { h: 'Volume', cls: 'num', v: (a) => compact(a.volume) },
      { h: 'Transfers', cls: 'num', v: (a) => num(a.transfers) },
      { h: 'Holders indexed', cls: 'num', v: (a) => num(a.holders_indexed) },
      { h: 'Contract', v: (a) => addr(a.contract_id) },
    ], rows, { rowHref: (a) => `#/asset/${encodeURIComponent(a.asset_key)}`, emptyTitle: 'No asset activity in this window' });
  });
  run();
  bindSeg(view, 'hours', (h) => { hours = h; ctx.setQuery({ hours }); run(); });
}
