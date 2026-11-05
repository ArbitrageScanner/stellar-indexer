import { get, qs } from '../api.js';
import { html, setHTML, load, feed, pageHead, card, tile, kv, table, num, pct, dt, when, addr, txLink, asset, tag, okBadge, expert,
  copyBtn, json, hbars, tabs, bindTabs, amount, seg, bindSeg } from '../ui.js';
import { protoName } from '../protocols-meta.js';

export default async function contract(view, [id]) {
  setHTML(view, html`
    ${pageHead('Contract', { crumbs: html`<a href="#/contracts">Contracts</a> / ${id.slice(0, 6)}…`, actions: html`<a class="btn" href="#/account/${id}">As account (balances, transfers)</a>${expert('contract', id)}` })}
    <div class="title-id" style="margin:-12px 0 16px"><span class="mono" style="font-size:13px;color:var(--ink-2)">${id}</span>${copyBtn(id)}</div>
    <div id="body"></div>`);
  await load(view.querySelector('#body'), () => get(`/contracts/${id}`), (c) => html`
    <div class="tiles">
      ${tile('Invocations', num(c.calls), { foot: `${num(c.callers)} distinct callers` })}
      ${tile('Events emitted', num(c.events))}
      ${tile('Label', c.token ? asset(c.token) : c.protocol ? protoName(c.protocol) : html`<span class="muted">unknown</span>`, { foot: c.token ? 'Stellar Asset Contract / token' : c.protocol ? 'decoded protocol' : 'no decoder matched' })}
      ${tile('Last event', c.last_seen ? when(c.last_seen) : '—', { foot: c.first_seen ? `first ${dt(c.first_seen)}` : '' })}
    </div>
    <div class="grid g3" style="margin-bottom:16px">
      ${card('Functions called', table([
        { h: 'Function', v: (f) => html`<a href="#/txs?contract=${id}&function=${encodeURIComponent(f.function || '')}"><b>${f.function || '—'}</b></a>` },
        { h: 'Calls', cls: 'num', v: (f) => num(f.calls) },
        { h: 'Failed', cls: 'num', v: (f) => pct(f.failed / f.calls * 100, 0) },
      ], c.functions, { emptyTitle: 'Never invoked directly', emptyText: 'It may still be called by other contracts.' }), { flush: true })}
      ${card('Event topics', hbars(c.topics.map((t) => ({ label: t.topic0 || '(none)', value: t.events }))), {})}
      ${card('Protocol actions', c.protocol_actions.length ? hbars(c.protocol_actions.map((a) => ({ label: a.action, value: a.events, href: `#/protocol/${c.protocol}` }))) : html`<div class="muted">No decoded protocol events.</div>`)}
    </div>
    <div id="tabs"></div><div class="card"><div class="card-head" id="tf" style="padding-bottom:12px"></div><div class="card-body flush" id="tab"></div></div>`, {
    after: (el, c) => {
      setHTML(el.querySelector('#tabs'), tabs([['events', 'Events', num(c.events)], ['calls', 'Invocations', num(c.calls)]], 'events'));
      const tab = el.querySelector('#tab'), tf = el.querySelector('#tf');
      let topic = '';
      const show = (t) => {
        if (t === 'events') {
          setHTML(tf, html`${seg('topic', [['', 'All topics'], ...c.topics.slice(0, 8).map((x) => [x.topic0, x.topic0])], topic)}`);
          bindSeg(tf, 'topic', (v) => { topic = v; show('events'); });
          feed(tab, {
            fetchPage: (before) => get(`/contracts/${id}/events` + qs({ topic, before, limit: 30 })),
            cursor: (e) => `${e.ledger}.${e.tx_index}.${e.op_index}.${e.event_index}`,
            cols: [
              { h: 'Time', v: (e) => when(e.closed_at) },
              { h: 'Topic', v: (e) => html`${tag(e.topic0)}${e.successful ? '' : html` <span class="badge fail">failed tx</span>`}` },
              { h: 'Topics', v: (e) => json(e.topics.slice(1), 'inline') },
              { h: 'Data', v: (e) => json(e.data, 'inline') },
              { h: 'Tx', v: (e) => txLink(e.tx_hash) },
            ],
            pageSize: 30, emptyTitle: 'No events from this contract',
          });
        } else {
          setHTML(tf, '');
          feed(tab, {
            fetchPage: (before) => get('/transactions' + qs({ contract: id, before, limit: 50 })),
            cursor: (t) => t.id, rowHref: (t) => `#/tx/${t.hash}`,
            cols: [
              { h: 'Time', v: (t) => when(t.closed_at) },
              { h: 'Function', v: (t) => html`<b>${t.function || '—'}</b>` },
              { h: 'Status', v: (t) => okBadge(t.successful) },
              { h: 'Caller', v: (t) => addr(t.source) },
              { h: 'Fee (XLM)', cls: 'num', v: (t) => amount(t.fee_charged / 1e7, 5) },
              { h: 'Tx', v: (t) => txLink(t.hash) },
            ],
            emptyTitle: 'No direct invocations indexed',
          });
        }
      };
      bindTabs(el.querySelector('#tabs'), show);
      show('events');
    },
  });
}
