import { get, qs } from '../api.js';
import { html, setHTML, feed, pageHead, seg, bindSeg, when, addr, txLink, tag, json, unverified } from '../ui.js';
import { PROTOCOLS } from '../protocols-meta.js';

export default async function events(view, _p, query, ctx) {
  const f = { protocol: query.protocol || '', topic: query.topic || '', contract: query.contract || '' };
  setHTML(view, html`
    ${pageHead('Contract events', { sub: 'Every non-token Soroban contract event, with topics and data decoded from ScVal to JSON. Filter by decoded protocol (or “unattributed” to find contracts without a decoder), by the first topic, or by contract.' })}
    <form class="toolbar" id="filters">
      ${seg('protocol', [['', 'All'], ['none', 'Unattributed'], ...Object.entries(PROTOCOLS).map(([k, v]) => [k, v.name])], f.protocol)}
      <input name="topic" type="search" placeholder="topic0, e.g. swap" value="${f.topic}" style="width:160px">
      <input name="contract" type="search" placeholder="Contract C…" value="${f.contract}" style="width:240px">
      <button class="btn primary">Apply</button>
    </form>
    <div class="card"><div class="card-body flush" id="list"></div></div>`);
  const run = () => {
    ctx.setQuery(f);
    feed(view.querySelector('#list'), {
      fetchPage: (before) => get('/events' + qs({ ...f, before, limit: 30 })),
      cursor: (e) => `${e.ledger}.${e.tx_index}.${e.op_index}.${e.event_index}`,
      pageSize: 30,
      cols: [
        { h: 'Time', v: (e) => when(e.closed_at) },
        { h: 'Contract', v: (e) => html`${addr(e.contract_id)}${e.protocol ? html`<div style="margin-top:2px">${tag(e.protocol)}${unverified(e)}</div>` : ''}` },
        { h: 'Topic', v: (e) => html`<a href="#/events?topic=${encodeURIComponent(e.topic0)}">${tag(e.topic0)}</a>` },
        { h: 'Topics', v: (e) => json(e.topics.slice(1), 'inline') },
        { h: 'Data', v: (e) => json(e.data, 'inline') },
        { h: 'Tx', v: (e) => txLink(e.tx_hash) },
      ],
      emptyTitle: 'No events match',
    });
  };
  run();
  bindSeg(view, 'protocol', (v) => { f.protocol = v; run(); });
  view.querySelector('#filters').addEventListener('submit', (e) => {
    e.preventDefault();
    const d = new FormData(e.target);
    f.topic = d.get('topic').trim(); f.contract = d.get('contract').trim();
    run();
  });
}
