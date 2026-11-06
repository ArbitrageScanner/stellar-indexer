import { get, qs } from '../api.js';
import { html, setHTML, feed, pageHead, seg, bindSeg, num, amount, when, addr, txLink, ledgerLink, okBadge, tag } from '../ui.js';

export default async function txs(view, _p, query, ctx) {
  const f = { successful: query.successful || '', soroban: query.soroban || '', source: query.source || '', contract: query.contract || '', function: query.function || '' };
  setHTML(view, html`
    ${pageHead('Transactions', { sub: 'Filter by outcome, Soroban vs classic, source account or invoked contract and function. Click a row for operations, token transfers, trades and decoded events.' })}
    <section class="card chain-list chain-txs" aria-label="Transactions">
      <form class="toolbar chain-filters" id="filters" role="search">
        <div class="chain-segs">
          ${seg('successful', [['', 'Any status'], ['true', 'Successful'], ['false', 'Failed']], f.successful)}
          ${seg('soroban', [['', 'All'], ['true', 'Soroban'], ['false', 'Classic']], f.soroban)}
        </div>
        <div class="chain-fields">
          <input name="source" type="search" placeholder="Source G…" value="${f.source}" aria-label="Source account" autocomplete="off" spellcheck="false" class="chain-f-addr">
          <input name="contract" type="search" placeholder="Contract C…" value="${f.contract}" aria-label="Contract" autocomplete="off" spellcheck="false" class="chain-f-addr">
          <input name="function" type="search" placeholder="Function" value="${f.function}" aria-label="Function" autocomplete="off" spellcheck="false" class="chain-f-fn">
          <button type="submit" class="btn primary">Apply</button>
        </div>
      </form>
      <div class="card-body flush" id="list"></div>
    </section>`);
  const run = () => {
    ctx.setQuery(f);
    feed(view.querySelector('#list'), {
      fetchPage: (before) => get('/transactions' + qs({ ...f, before, limit: 50 })),
      cursor: (t) => t.id,
      rowHref: (t) => `#/tx/${t.hash}`,
      cols: [
        { h: 'Hash', v: (t) => txLink(t.hash) },
        { h: 'Status', v: (t) => okBadge(t.successful) },
        { h: 'Time', v: (t) => when(t.closed_at) },
        { h: 'Ledger', v: (t) => ledgerLink(t.ledger) },
        { h: 'Source', v: (t) => addr(t.source) },
        { h: 'Ops', cls: 'num', v: (t) => num(t.op_count) },
        { h: 'Call / memo', v: (t) => t.contract_id ? html`<span class="chain-call">${addr(t.contract_id)}<span class="muted">.</span><b>${t.function || '?'}</b></span>` : t.memo ? html`<span class="chain-call"><span class="muted">Memo</span>${tag(t.memo.slice(0, 28))}</span>` : html`<span class="muted">Classic</span>` },
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
