import { get, qs } from '../api.js';
import { html, setHTML, feed, pageHead, num, amount, when, ledgerLink, short, raw } from '../ui.js';

export default async function ledgers(view) {
  setHTML(view, html`
    ${pageHead('Ledgers', { sub: 'Every indexed ledger with its transaction set: successful vs failed transactions, operations, Soroban share and fees.' })}
    <div class="card"><div class="card-body flush" id="list"></div></div>`);
  feed(view.querySelector('#list'), {
    fetchPage: (before) => get('/ledgers' + qs({ before, limit: 50 })),
    cursor: (l) => l.sequence,
    rowHref: (l) => `#/ledger/${l.sequence}`,
    cols: [
      { h: 'Ledger', v: (l) => ledgerLink(l.sequence) },
      { h: 'Closed', v: (l) => when(l.closed_at) },
      { h: 'Txs', cls: 'num', v: (l) => num(l.tx_count) },
      { h: 'Success', v: (l) => {
        const ok = l.tx_count ? l.tx_success / l.tx_count * 100 : 0;
        return html`<div style="display:flex;align-items:center;gap:8px;min-width:120px"><div class="minibar" style="flex:1;height:6px;border-radius:3px;display:flex;gap:1px;overflow:hidden;background:var(--surface-3)"><span style="width:${raw(ok.toFixed(1))}%;background:var(--s1)"></span><span style="width:${raw((100 - ok).toFixed(1))}%;background:var(--s2)"></span></div><span class="muted" style="font-size:12px">${num(ok)}%</span></div>`;
      } },
      { h: 'Ops', cls: 'num', v: (l) => num(l.op_count) },
      { h: 'Soroban', cls: 'num', v: (l) => num(l.soroban_tx_count) },
      { h: 'Fees (XLM)', cls: 'num', v: (l) => amount(l.fees_charged / 1e7, 4) },
      { h: 'Protocol', cls: 'num', v: (l) => String(l.protocol) },
      { h: 'Hash', v: (l) => html`<span class="mono muted">${short(l.hash, 8, 6)}</span>` },
    ],
    emptyTitle: 'No ledgers indexed yet',
  });
}
