import { get } from '../api.js';
import { html, setHTML, load, pageHead, card, tile, kv, table, num, amount, amt7, dt, ago, addr, ledgerLink, okBadge, tag, expert, copyBtn,
  asset, venue, price, json, snake, tabs, bindTabs, empty, VENUE_LABEL, unverified, dec } from '../ui.js';
import { opSummary } from '../ops.js';

export default async function tx(view, [hash]) {
  setHTML(view, html`
    ${pageHead('Transaction', {
      crumbs: html`<a href="#/txs">Transactions</a><span aria-hidden="true">/</span><span>${hash.slice(0, 10)}…</span>`,
      sub: html`<span class="chain-id"><span class="mono chain-hash">${hash}</span>${copyBtn(hash)}</span>`,
      actions: expert('tx', hash),
    })}
    <div id="body"></div>`);
  await load(view.querySelector('#body'), () => get(`/transactions/${hash}`), (t) => {
    const memo = t.memo_type && t.memo_type !== 'MemoTypeMemoNone' ? html`<span class="chain-memo chain-cap">${tag(t.memo_type.replace('MemoTypeMemo', '').toLowerCase())}<span class="mono">${t.memo}</span></span>` : html`<span class="muted">None</span>`;
    return html`
      <div class="tiles chain-tally chain-tally-tx">
        ${tile('Result', okBadge(t.successful), { foot: t.result_code.replace('TransactionResultCode', '') })}
        ${tile('Ledger', ledgerLink(t.ledger), { foot: `Tx #${t.tx_index}` })}
        ${tile('Closed', ago(t.closed_at), { foot: dt(t.closed_at) })}
        ${tile('Operations', num(t.op_count))}
        ${tile('Fee charged', html`${dec(amount(t.fee_charged / 1e7, 7))} <span class="muted">XLM</span>`, { foot: `Max ${amount(t.max_fee / 1e7, 7)} XLM` })}
      </div>
      <section class="card chain-flowcard" aria-label="Transaction contents"><div id="tabs" class="chain-tabs"></div><div class="card-body flush" id="tab"></div></section>
      <div class="grid g2 chain-facts">
          ${card('Summary', kv([
            ['Source', addr(t.source, { full: true, copy: true })],
            t.fee_account !== t.source && ['Fee account', addr(t.fee_account, { full: true })],
            ['Sequence', html`<span class="mono">${t.seq_num}</span>`],
            ['Memo', memo],
            t.inner_hash && ['Inner hash (fee bump)', html`<span class="mono">${t.inner_hash}</span>`],
          ]))}
          ${t.is_soroban ? card('Soroban', kv([
            ['Contract', addr(t.contract_id, { full: true, copy: true })],
            ['Function', html`<b>${t.function || '—'}</b>`],
            ['Resource fee', `${amount(t.resource_fee / 1e7, 7)} XLM`],
            ['Instructions', num(t.instructions)],
            ['Contract events', num(t.contract_events.length)],
            ['Protocol events', num(t.protocol_events.length)],
          ])) : card('Activity', kv([
            ['Operations', num(t.op_count)],
            ['Token transfers', num(t.transfers.length)],
            ['Trades', num(t.trades.length)],
          ]))}
      </div>`;
  }, {
    after: (el, t) => {
      const T = [['ops', 'Operations', t.operations.length], ['transfers', 'Token transfers', t.transfers.length], ['trades', 'Trades', t.trades.length],
        ['protocol', 'Protocol events', t.protocol_events.length], ['events', 'Contract events', t.contract_events.length], ['raw', 'Raw JSON']];
      const first = T.find((x) => x[2] > 0 && x[0] !== 'ops' && t.is_soroban)?.[0] || 'ops';
      setHTML(el.querySelector('#tabs'), tabs(T, first));
      const tab = el.querySelector('#tab');
      const show = (id) => setHTML(tab, render(id, t));
      bindTabs(el.querySelector('#tabs'), show);
      show(first);
    },
  });
}

function render(id, t) {
  switch (id) {
    case 'ops':
      return t.operations.length ? html`<div class="flow">${t.operations.map((o) => html`<div class="flow-item"><div class="n">${o.op_index + 1}</div><div>
        <div class="line"><b class="chain-optype">${snake(o.type)}</b>${o.successful ? '' : html`<span class="badge fail">${o.result_code || 'Failed'}</span>`}<span class="chain-src"><span class="muted">Source</span>${addr(o.source)}</span></div>
        <div class="line det">${opSummary(o.type, o.details)}</div>
        <details class="raw det"><summary>Details</summary>${json(o.details, 'inline')}</details></div></div>`)}</div>`
        : empty('No operations stored', 'Operations of failed transactions are skipped when INDEX_FAILED_TXS=false.');
    case 'transfers':
      return t.transfers.length ? html`<div class="flow">${t.transfers.map((x) => html`<div class="flow-item"><div class="n">${x.transfer_index}</div><div class="line">
        <span class="chain-cap">${tag(x.type)}</span><span class="chain-move">${addr(x.from_addr)}<span class="arrow" aria-hidden="true">→</span>${addr(x.to_addr)}</span><span class="chain-amt"><b>${amt7(x.amount)}</b>${asset(x.asset)}</span>${x.op_index ? html`<span class="muted">Op ${x.op_index}</span>` : ''}</div></div>`)}</div>`
        : empty('No token transfers', 'CAP-67 transfer / mint / burn / clawback events appear here.');
    case 'trades':
      return table([
        { h: 'Op', cls: 'num', v: (r) => String(r.op_index + 1) },
        { h: 'Venue', v: (r) => html`${venue(r.venue)}${unverified(r)}` },
        { h: 'Sold', cls: 'num', v: (r) => html`${amt7(r.sold_amount)} ${asset(r.sold_asset)}` },
        { h: 'Bought', cls: 'num', v: (r) => html`${amt7(r.bought_amount)} ${asset(r.bought_asset)}` },
        { h: 'Price', cls: 'num', v: (r) => html`${price(r.price)} <span class="muted">${asset(r.counter, { link: false })}/${asset(r.base, { link: false })}</span>` },
        { h: 'Pool / maker', v: (r) => addr(r.pool || r.maker) },
      ], t.trades, { emptyTitle: 'No trades in this transaction' });
    case 'protocol':
      return table([
        { h: 'Protocol', v: (r) => html`<a href="#/protocol/${r.protocol}">${VENUE_LABEL[r.protocol] || r.protocol}</a>` },
        { h: 'Action', v: (r) => html`<span class="chain-cap">${tag(r.action)}</span>${unverified(r)}` },
        { h: 'Contract', v: (r) => addr(r.contract_id) },
        { h: 'User', v: (r) => addr(r.user_addr) },
        { h: 'Asset', v: (r) => r.asset ? asset(r.asset) : '—' },
        { h: 'Amount 0', cls: 'num', v: (r) => r.amount0 ? amt7(r.amount0) : '—' },
        { h: 'Amount 1', cls: 'num', v: (r) => r.amount1 ? amt7(r.amount1) : '—' },
      ], t.protocol_events, { emptyTitle: 'No decoded protocol events' });
    case 'events':
      return t.contract_events.length ? html`<div class="flow">${t.contract_events.map((e) => html`<div class="flow-item"><div class="n">${e.event_index}</div><div>
        <div class="line">${addr(e.contract_id)}${e.protocol ? html`<span class="chain-cap">${tag(e.protocol)}${unverified(e)}</span>` : ''}<span class="muted">Op ${e.op_index + 1}</span></div>
        <div class="chain-ev det"><div><div class="chain-ev-label">Topics</div>${json(e.topics, 'inline')}</div><div><div class="chain-ev-label">Data</div>${json(e.data, 'inline')}</div></div></div></div>`)}</div>`
        : empty('No contract events', 'Token events are under Token transfers; other contract events appear here.');
    default:
      return html`<div class="chain-raw">${json(t)}</div>`;
  }
}
