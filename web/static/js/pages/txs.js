import { get, qs } from '../api.js';
import { html, setHTML, feed, pageHead, seg, bindSeg, num, amount, when, addr, txLink, ledgerLink, okBadge, tag } from '../ui.js';

export default async function txs(view, _p, query, ctx) {
  const f = { successful: query.successful || '', soroban: query.soroban || '', source: query.source || '', contract: query.contract || '', function: query.function || '' };
  setHTML(view, html`
    ${pageHead('Transactions', { sub: 'Filter by outcome, Soroban vs classic, source account or invoked contract and function. Click a row for operations, token transfers, trades and decoded events.' })}
    <form class="toolbar" id="filters">
      ${seg('successful', [['', 'Any status'], ['true', 'Successful'], ['false', 'Failed']], f.successful)}
      ${seg('soroban', [['', 'All'], ['true', 'Soroban'], ['false', 'Classic']], f.soroban)}
      <input name="source" type="search" placeholder="Source G…" value="${f.source}" style="width:200px">
      <input name="contract" type="search" placeholder="Contract C…" value="${f.contract}" style="width:200px">
      <input name="function" type="search" placeholder="Function" value="${f.function}" style="width:130px">
      <button class="btn primary">Apply</button>
    </form>
    <div class="card"><div class="card-body flush" id="list"></div></div>`);
  const run = () => {
    ctx.setQuery(f);
    feed(view.querySelector('#list'), {
      fetchPage: (before) => get('/transactions' + qs({ ...f, before, limit: 50 })),
      cursor: (t) => t.id,
      rowHref: (t) => `#/tx/${t.hash}`,
      cols: [
        { h: 'Hash', v: (t) => txLink(t.hash) },
        { h: 'Time', v: (t) => when(t.closed_at) },
        { h: 'Ledger', v: (t) => ledgerLink(t.ledger) },
        { h: 'Status', v: (t) => okBadge(t.successful) },
        { h: 'Source', v: (t) => addr(t.source) },
        { h: 'Ops', cls: 'num', v: (t) => num(t.op_count) },
        { h: 'Call / memo', v: (t) => t.contract_id ? html`${addr(t.contract_id)}.<b>${t.function || '?'}</b>` : t.memo ? html`<span class="muted">memo</span> ${tag(t.memo.slice(0, 28))}` : html`<span class="muted">classic</span>` },
        { h: 'Fee (XLM)', cls: 'num', v: (t) => amount(t.fee_charged / 1e7, 5) },
      ],
      emptyTitle: 'No transactions match', emptyText: f.successful === 'false' ? 'Failed transactions are not stored when the indexer runs with INDEX_FAILED_TXS=false.' : 'Try relaxing the filters.',
    });
  };
  run();
  for (const k of ['successful', 'soroban']) bindSeg(view, k, (v) => { f[k] = v; run(); });
  view.querySelector('#filters').addEventListener('submit', (e) => {
    e.preventDefault();
    const d = new FormData(e.target);
    for (const k of ['source', 'contract', 'function']) f[k] = (d.get(k) || '').trim();
    run();
  });
}
