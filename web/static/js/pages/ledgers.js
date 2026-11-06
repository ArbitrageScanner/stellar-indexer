import { get, qs } from '../api.js';
import { html, setHTML, feed, pageHead, num, amount, when, ledgerLink, short, raw, dec } from '../ui.js';

export default async function ledgers(view) {
  setHTML(view, html`
    ${pageHead('Ledgers', { sub: 'Every indexed ledger with its transaction set: successful vs failed transactions, operations, Soroban share and fees.' })}
    <section class="card chain-list chain-ledgers" aria-label="Indexed ledgers"><div class="card-body flush" id="list"></div></section>`);
  feed(view.querySelector('#list'), {
    fetchPage: (before) => get('/ledgers' + qs({ before, limit: 50 })),
    cursor: (l) => l.sequence,
    rowHref: (l) => `#/ledger/${l.sequence}`,
    cols: [
      { h: 'Ledger', v: (l) => html`<div class="chain-seq">${ledgerLink(l.sequence)}</div><div class="chain-when">${when(l.closed_at)}</div>` },
      { h: 'Txs', cls: 'num', v: (l) => num(l.tx_count) },
      { h: 'Success', v: (l) => {
        const ok = l.tx_count ? l.tx_success / l.tx_count * 100 : 0;
        return html`<div class="chain-okbar" title="${num(l.tx_success)} successful, ${num(l.tx_failed)} failed"><div class="chain-okbar-track"><span class="chain-okbar-ok" style="width:${raw(ok.toFixed(1))}%"></span><span class="chain-okbar-fail" style="width:${raw((100 - ok).toFixed(1))}%"></span></div><span class="chain-okbar-pct">${num(ok)}%</span></div>`;
      } },
      { h: 'Ops', cls: 'num', v: (l) => num(l.op_count) },
      { h: 'Soroban', cls: 'num', v: (l) => num(l.soroban_tx_count) },
      { h: 'Fees (XLM)', cls: 'num', v: (l) => dec(amount(l.fees_charged / 1e7, 4)) },
      { h: 'Protocol', cls: 'num', v: (l) => String(l.protocol) },
      { h: 'Hash', v: (l) => html`<span class="mono muted">${short(l.hash, 8, 6)}</span>` },
    ],
    emptyTitle: 'No ledgers indexed yet',
  });
}
