import { get } from '../api.js';
import { html, setHTML, load, pageHead, card, tile, kv, table, num, amount, compact, dt, ago, addr, txLink, okBadge, tag, expert, copyBtn, icon, dec } from '../ui.js';

export default async function ledger(view, [seq]) {
  const n = +seq;
  setHTML(view, html`
    ${pageHead(`Ledger ${num(n)}`, {
      crumbs: html`<a href="#/ledgers">Ledgers</a><span aria-hidden="true">/</span><span>${num(n)}</span>`,
      actions: html`<a class="btn" href="#/ledger/${n - 1}" aria-label="Previous ledger, ${num(n - 1)}">Previous <span class="chain-btn-num">${num(n - 1)}</span></a><a class="btn" href="#/ledger/${n + 1}" aria-label="Next ledger, ${num(n + 1)}">Next <span class="chain-btn-num">${num(n + 1)}</span></a>${expert('ledger', n)}`,
    })}
    <div id="body"></div>`);
  await load(view.querySelector('#body'), () => get(`/ledgers/${n}`), (l) => html`
    <div class="chain-id"><span class="chain-id-label">Hash</span><span class="mono chain-hash">${l.hash}</span>${copyBtn(l.hash)}</div>
    <div class="tiles chain-tally">
      ${tile('Transactions', num(l.tx_count), { foot: `${num(l.tx_success)} ok, ${num(l.tx_failed)} failed` })}
      ${tile('Operations', num(l.op_count))}
      ${tile('Soroban txs', num(l.soroban_tx_count))}
      ${tile('Fees charged', html`${dec(amount(l.fees_charged / 1e7, 4))} <span class="muted">XLM</span>`)}
      ${tile('Closed', ago(l.closed_at), { foot: dt(l.closed_at) })}
    </div>
    <div class="stack chain-ledger">
      ${card('Transactions', table([
        { h: '#', cls: 'num', v: (t) => String(t.tx_index) },
        { h: 'Hash', v: (t) => txLink(t.hash) },
        { h: 'Status', v: (t) => okBadge(t.successful) },
        { h: 'Source', v: (t) => addr(t.source) },
        { h: 'Ops', cls: 'num', v: (t) => num(t.op_count) },
        { h: 'Call', v: (t) => t.contract_id ? html`<span class="chain-call">${addr(t.contract_id)}<span class="muted">.</span><b>${t.function || '?'}</b></span>` : html`<span class="muted">Classic</span>` },
        { h: 'Fee (XLM)', cls: 'num', v: (t) => dec(amount(t.fee_charged / 1e7, 5)) },
      ], l.transactions, { rowHref: (t) => `#/tx/${t.hash}`, emptyTitle: 'No transactions stored', emptyText: l.tx_count ? 'This ledger had only failed transactions and the indexer runs with INDEX_FAILED_TXS=false.' : 'Empty ledger.' }), { flush: true, hint: html`<span>${l.transactions.length} indexed</span>` })}
      ${html`<div class="chain-header">${card('Header', kv([
        ['Previous', html`<a class="mono" href="#/ledger/${n - 1}">${l.prev_hash}</a>`],
        ['Closed at', dt(l.closed_at)],
        ['Protocol', String(l.protocol)],
        ['Base fee', `${num(l.base_fee)} stroops`],
        ['Base reserve', `${amount(l.base_reserve / 1e7)} XLM`],
        ['Total coins', `${compact(l.total_coins / 1e7)} XLM`],
        ['Fee pool', `${compact(l.fee_pool / 1e7)} XLM`],
      ]))}</div>`}
    </div>`);
}
