import { get, qs } from '../api.js';
import { html, setHTML, load, feed, pageHead, card, hbars, num, compact, when, addr, txLink, snake, pct } from '../ui.js';
import { opSummary } from '../ops.js';

export default async function ops(view, _p, query, ctx) {
  let type = query.type || '';
  setHTML(view, html`
    ${pageHead('Operations', { sub: 'Operation mix over the last 24 hours and a typed feed of individual operations with decoded details.' })}
    <div class="grid g-main" style="margin-bottom:16px">
      <div class="card"><div class="card-head"><h2 id="feed-title">Latest operations</h2><select id="type"><option value="">All types</option></select></div><div class="card-body flush" id="list"></div></div>
      ${card('Mix (24h)', html`<div id="mix"></div>`, { hint: 'click to filter' })}
    </div>`);
  const sel = view.querySelector('#type');
  load(view.querySelector('#mix'), () => get('/stats/operations?hours=24'), (rows) => {
    setHTML(sel, html`<option value="">All types</option>${rows.map((r) => html`<option value="${r.type}" ${r.type === type ? 'selected' : ''}>${snake(r.type)} (${compact(r.ops)})</option>`)}`);
    const total = rows.reduce((s, r) => s + r.ops, 0);
    return hbars(rows.map((r) => ({ label: snake(r.type), value: r.ops, href: `#/ops?type=${r.type}`, title: `${num(r.ops)} ops (${pct(r.ops / total * 100, 1)}), ${num(r.sources)} source accounts, ${pct(r.successful / r.ops * 100, 1)} successful` })));
  });
  const run = () => {
    ctx.setQuery({ type });
    view.querySelector('#feed-title').textContent = type ? snake(type) : 'Latest operations';
    feed(view.querySelector('#list'), {
      fetchPage: (before) => get('/operations' + qs({ type, before, limit: 50 })),
      cursor: (o) => o.id,
      rowHref: (o) => `#/tx/${o.tx_hash}`,
      cols: [
        { h: 'Time', v: (o) => when(o.closed_at) },
        { h: 'Type', v: (o) => html`<b>${snake(o.type)}</b>${o.successful ? '' : html` <span class="badge fail">failed</span>`}` },
        { h: 'Source', v: (o) => addr(o.source) },
        { h: 'Details', v: (o) => opSummary(o.type, o.details) },
        { h: 'Tx', v: (o) => txLink(o.tx_hash) },
      ],
      emptyTitle: 'No operations', emptyText: 'Operations are indexed when INDEX_OPERATIONS=true.',
    });
  };
  run();
  sel.addEventListener('change', () => { type = sel.value; run(); });
}
