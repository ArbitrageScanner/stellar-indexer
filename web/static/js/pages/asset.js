import { get, qs } from '../api.js';
import { html, setHTML, load, feed, card, tile, pageHead, table, kv, num, compact, amount, amt7, stroops, asset, addr, tag, when,
  txLink, pairLink, parseAsset, expert, ledgerLink, copyBtn, tabs, bindTabs } from '../ui.js';

export default async function assetPage(view, [key], _q, ctx) {
  const A = parseAsset(key);
  setHTML(view, html`
    ${pageHead(asset(key, { issuer: true, link: false }), { crumbs: html`<a href="#/assets">Assets</a> / ${A.code}`, actions: expert('asset', key) })}
    <div id="info"></div>`);
  await load(view.querySelector('#info'), () => get(`/assets/${encodeURIComponent(key)}`), (a) => {
    const s = a.stats_24h || {};
    return html`
      <div class="tiles">
        ${tile('Transfers (24h)', num(s.transfers))}
        ${tile('Transferred (24h)', compact(stroops(s.transfer_amount)), { foot: A.code })}
        ${tile('Minted (24h)', compact(stroops(s.minted)))}
        ${tile('Burned (24h)', compact(stroops(s.burned)))}
        ${tile('Trades (24h)', num(s.trades))}
        ${tile(A.native ? 'Accounts indexed' : 'Trustlines indexed', num(s.trustlines_indexed))}
      </div>
      <div class="grid g-main" style="margin-bottom:16px">
        ${card('Top pairs (24h)', table([
          { h: 'Pair', v: (p) => pairLink(p.base, p.counter) },
          { h: 'Trades', cls: 'num', v: (p) => num(p.trades) },
          { h: 'Base volume', cls: 'num', v: (p) => compact(p.base_volume) },
        ], a.top_pairs, { rowHref: (p) => `#/pair/${encodeURIComponent(p.base)}/${encodeURIComponent(p.counter)}`, emptyTitle: 'No trades in 24h' }), { flush: true })}
        ${card('Details', kv([
          ['Asset key', html`<code>${a.asset_key}</code>${copyBtn(a.asset_key)}`],
          ['Type', tag(a.type)],
          ['Code', a.code || '—'],
          a.issuer && ['Issuer', addr(a.issuer, { full: true, copy: true })],
          a.contract_id && ['Contract (SAC)', addr(a.contract_id, { full: true, copy: true })],
          ['First seen', ledgerLink(a.first_seen)],
        ]))}
      </div>`;
  });
  if (!ctx.alive()) return;
  view.insertAdjacentHTML('beforeend', `<div id="tabs"></div><div class="card"><div class="card-body flush" id="tab"></div></div>`);
  const tabEl = view.querySelector('#tab');
  setHTML(view.querySelector('#tabs'), tabs([['transfers', 'Recent transfers'], ['holders', A.native ? 'Top accounts' : 'Top holders']], 'transfers'));
  const show = (t) => {
    if (t === 'holders') {
      load(tabEl, () => get(`/assets/${encodeURIComponent(key)}/holders?limit=100`), (rows) => table([
        { h: '#', cls: 'num', v: (r) => String(rows.indexOf(r) + 1) },
        { h: 'Account', v: (r) => addr(r.account_id, { full: true }) },
        { h: 'Balance', cls: 'num', v: (r) => html`${amount(r.balance)} <span class="muted">${A.code}</span>` },
        { h: 'Limit', cls: 'num', v: (r) => r.trust_limit !== undefined ? compact(r.trust_limit) : '—' },
        { h: 'Updated', cls: 'num', v: (r) => ledgerLink(r.last_modified) },
      ], rows, { emptyTitle: 'No holders indexed', emptyText: 'Balances are known only for accounts touched since indexing began.' }));
    } else {
      feed(tabEl, {
        fetchPage: (before) => get('/transfers' + qs({ asset: key, before, limit: 50 })),
        cursor: (r) => `${r.ledger}.${r.tx_index}.${r.transfer_index}`,
        cols: [
          { h: 'Time', v: (r) => when(r.closed_at) },
          { h: 'Type', v: (r) => tag(r.type) },
          { h: 'From', v: (r) => addr(r.from_addr) },
          { h: 'To', v: (r) => addr(r.to_addr) },
          { h: 'Amount', cls: 'num', v: (r) => html`${amt7(r.amount)} <span class="muted">${A.code}</span>` },
          { h: 'Tx', v: (r) => txLink(r.tx_hash) },
        ],
        emptyTitle: 'No transfers indexed',
      });
    }
  };
  bindTabs(view.querySelector('#tabs'), show);
  show('transfers');
}
