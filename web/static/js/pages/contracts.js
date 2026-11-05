import { get, qs } from '../api.js';
import { html, setHTML, load, pageHead, seg, bindSeg, table, num, pct, when, addr, asset, tag, icon, card, hbars } from '../ui.js';
import { protoName } from '../protocols-meta.js';

export default async function contracts(view, _p, query, ctx) {
  let hours = query.hours || '24';
  setHTML(view, html`
    ${pageHead('Contracts', {
      sub: 'Most active Soroban contracts, combining direct invocations (transactions calling the contract) with the events it emitted. Token contracts are labelled with their asset.',
      actions: seg('hours', [['1', '1h'], ['24', '24h'], ['168', '7d']], hours),
    })}
    <form class="toolbar" id="open"><input name="id" type="search" placeholder="Open contract C…" style="flex:1;max-width:520px" required pattern="C[A-Z2-7]{55}"><button class="btn primary">${icon('search')}Open</button></form>
    <div class="grid g2" style="margin-bottom:16px">${card('Most called', html`<div id="b1"></div>`)}${card('Most events', html`<div id="b2"></div>`)}</div>
    <div class="card"><div class="card-body flush" id="list"></div></div>`);
  view.querySelector('#open').addEventListener('submit', (e) => { e.preventDefault(); location.hash = `/contract/${new FormData(e.target).get('id').trim()}`; });
  const label = (c) => c.token ? asset(c.token) : c.protocol ? tag(protoName(c.protocol)) : '';
  const run = () => load(view.querySelector('#list'), () => get('/contracts' + qs({ hours, limit: 200 })), (rows) => {
    const bars = (k) => hbars(rows.filter((c) => c[k]).sort((a, b) => b[k] - a[k]).slice(0, 8).map((c) => ({ label: html`<span class="mono">${c.contract_id.slice(0, 6)}…</span> ${c.token ? asset(c.token, { link: false }) : c.protocol ? protoName(c.protocol) : (c.functions || [])[0] || ''}`, plain: c.contract_id, value: c[k], href: `#/contract/${c.contract_id}` })));
    setHTML(view.querySelector('#b1'), bars('calls'));
    setHTML(view.querySelector('#b2'), bars('events'));
    return table([
      { h: 'Contract', v: (c) => addr(c.contract_id) },
      { h: 'Label', v: label },
      { h: 'Calls', cls: 'num', v: (c) => num(c.calls) },
      { h: 'Failed', cls: 'num', v: (c) => c.calls ? pct(c.failed_calls / c.calls * 100, 0) : '—' },
      { h: 'Callers', cls: 'num', v: (c) => num(c.callers) },
      { h: 'Events', cls: 'num', v: (c) => num(c.events) },
      { h: 'Functions / topics', v: (c) => html`${(c.functions || []).slice(0, 3).map((f) => html`<b>${f}</b> `)}${(c.topics || []).slice(0, 3).map((t) => html`${tag(t)} `)}` },
      { h: 'Last event', v: (c) => c.last_seen ? when(c.last_seen) : '—' },
    ], rows, { rowHref: (c) => `#/contract/${c.contract_id}`, emptyTitle: 'No contract activity in this window' });
  });
  run();
  bindSeg(view, 'hours', (h) => { hours = h; ctx.setQuery({ hours }); run(); });
}
