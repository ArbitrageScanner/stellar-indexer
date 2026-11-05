import { get, qs } from '../api.js';
import { html, setHTML, feed, pageHead, seg, bindSeg, when, addr, txLink, asset, amt7, tag } from '../ui.js';

export default async function transfers(view, _p, query, ctx) {
  const f = { type: query.type || '', asset: query.asset || '' };
  setHTML(view, html`
    ${pageHead('Token transfers', { sub: 'Unified CAP-67 token flows for classic assets and Soroban tokens (SAC and SEP-41): transfers, mints, burns and clawbacks, decoded by the SDK token-transfer processor.' })}
    <form class="toolbar" id="filters">
      ${seg('type', [['', 'All'], ['transfer', 'Transfer'], ['mint', 'Mint'], ['burn', 'Burn'], ['clawback', 'Clawback']], f.type)}
      <input name="asset" type="search" placeholder="Asset: native, CODE:ISSUER or C…" value="${f.asset}" style="width:340px">
      <button class="btn primary">Apply</button>
    </form>
    <div class="card"><div class="card-body flush" id="list"></div></div>`);
  const run = () => {
    ctx.setQuery(f);
    feed(view.querySelector('#list'), {
      fetchPage: (before) => get('/transfers' + qs({ ...f, before, limit: 50 })),
      cursor: (r) => `${r.ledger}.${r.tx_index}.${r.transfer_index}`,
      rowHref: (r) => `#/tx/${r.tx_hash}`,
      cols: [
        { h: 'Time', v: (r) => when(r.closed_at) },
        { h: 'Type', v: (r) => tag(r.type) },
        { h: 'From', v: (r) => addr(r.from_addr) },
        { h: '', v: () => html`<span class="muted">→</span>` },
        { h: 'To', v: (r) => addr(r.to_addr) },
        { h: 'Amount', cls: 'num', v: (r) => html`<b>${amt7(r.amount)}</b>` },
        { h: 'Asset', v: (r) => asset(r.asset) },
        { h: 'Tx', v: (r) => txLink(r.tx_hash) },
      ],
      emptyTitle: 'No transfers match',
    });
  };
  run();
  bindSeg(view, 'type', (v) => { f.type = v; run(); });
  view.querySelector('#filters').addEventListener('submit', (e) => { e.preventDefault(); f.asset = new FormData(e.target).get('asset').trim(); run(); });
}
