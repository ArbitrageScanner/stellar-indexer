import { get } from '../api.js';
import { html, setHTML, load, pageHead, card, tile, kv, table, num, amount, compact, dt, ago, addr, txLink, okBadge, tag, expert, copyBtn, icon } from '../ui.js';

export default async function ledger(view, [seq]) {
  const n = +seq;
  setHTML(view, html`
    ${pageHead(`Ledger ${num(n)}`, {
      crumbs: html`<a href="#/ledgers">Ledgers</a> / ${num(n)}`,
      actions: html`<a class="btn" href="#/ledger/${n - 1}">← ${num(n - 1)}</a><a class="btn" href="#/ledger/${n + 1}">${num(n + 1)} →</a>${expert('ledger', n)}`,
    })}
    <div id="body"></div>`);
  await load(view.querySelector('#body'), () => get(`/ledgers/${n}`), (l) => html`
    <div class="tiles">
      ${tile('Transactions', num(l.tx_count), { foot: `${num(l.tx_success)} ok · ${num(l.tx_failed)} failed` })}
      ${tile('Operations', num(l.op_count))}
      ${tile('Soroban txs', num(l.soroban_tx_count))}
      ${tile('Fees charged', html`${amount(l.fees_charged / 1e7, 4)} <span class="muted" style="font-size:14px">XLM</span>`)}
      ${tile('Closed', ago(l.closed_at), { foot: dt(l.closed_at) })}
    </div>
    <div class="grid g-main">
      ${card(`Transactions (${l.transactions.length} indexed)`, table([
        { h: '#', cls: 'num', v: (t) => String(t.tx_index) },
        { h: 'Hash', v: (t) => txLink(t.hash) },
        { h: 'Status', v: (t) => okBadge(t.successful) },
        { h: 'Source', v: (t) => addr(t.source) },
        { h: 'Ops', cls: 'num', v: (t) => num(t.op_count) },
        { h: 'Call', v: (t) => t.contract_id ? html`${addr(t.contract_id)}.<b>${t.function || '?'}</b>` : html`<span class="muted">classic</span>` },
        { h: 'Fee (XLM)', cls: 'num', v: (t) => amount(t.fee_charged / 1e7, 5) },
      ], l.transactions, { rowHref: (t) => `#/tx/${t.hash}`, emptyTitle: 'No transactions stored', emptyText: l.tx_count ? 'This ledger had only failed transactions and the indexer runs with INDEX_FAILED_TXS=false.' : 'Empty ledger.' }), { flush: true })}
      ${card('Header', kv([
        ['Hash', html`<span class="mono">${l.hash}</span>${copyBtn(l.hash)}`],
        ['Previous', html`<a class="mono" href="#/ledger/${n - 1}">${l.prev_hash}</a>`],
        ['Closed at', dt(l.closed_at)],
        ['Protocol', String(l.protocol)],
        ['Base fee', `${num(l.base_fee)} stroops`],
        ['Base reserve', `${amount(l.base_reserve / 1e7)} XLM`],
        ['Total coins', `${compact(l.total_coins / 1e7)} XLM`],
        ['Fee pool', `${compact(l.fee_pool / 1e7)} XLM`],
      ]))}
    </div>`);
}
