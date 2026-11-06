import { get, qs } from '../api.js';
import { html, setHTML, load, feed, pageHead, seg, bindSeg, card, tile, table, num, compact, amount, amt7, price, when, addr, txLink,
  asset, tag, hbars, pairLink, raw, venueColor, empty, unverified, logoMark } from '../ui.js';
import { columns } from '../charts.js';
import { PROTOCOLS, protoName } from '../protocols-meta.js';

const cap = (s) => s.charAt(0).toUpperCase() + s.slice(1);
const LENDING = ['supply', 'supply_collateral', 'withdraw', 'withdraw_collateral', 'borrow', 'repay', 'flash_loan'];

export default async function protocol(view, [name], query, ctx) {
  let hours = query.hours || '24';
  let action = query.action || '';
  let scope = query.verified === 'all' ? 'all' : '';
  const m = PROTOCOLS[name];
  setHTML(view, html`
    ${pageHead(html`<span class="sor-ptitle">${logoMark(name, 'sor-logo sor-logo-img') || html`<span class="sor-logo" style="background:${raw(venueColor(name))}" aria-hidden="true">${protoName(name)[0]}</span>`}${protoName(name)}</span>`, {
      crumbs: html`<a href="#/protocols">Protocols</a><span aria-hidden="true">/</span><span>${protoName(name)}</span>`,
      sub: m?.about || 'Decoded protocol events.',
      actions: html`${seg('scope', [['', 'Verified'], ['all', 'Include unverified']], scope)}${seg('hours', [['1', '1h'], ['24', '24h'], ['168', '7d']], hours)}`,
    })}
    <div id="summary"></div>
    <section class="card sor-section"><div class="card-head"><h2>Events</h2><span id="aseg"></span></div><div class="card-body flush" id="events"></div></section>`);

  const run = () => load(view.querySelector('#summary'), () => get(`/protocols/${name}/summary` + qs({ hours, verified: scope })), (s) => {
    const ev = s.actions.reduce((a, r) => a + r.events, 0);
    const lending = s.by_asset.filter((r) => LENDING.includes(r.action));
    const assets = [...new Set(lending.map((r) => r.asset))];
    const cell = (a, act) => lending.find((r) => r.asset === a && r.action === act);
    setHTML(view.querySelector('#aseg'), seg('action', [['', 'All'], ...s.actions.slice(0, 9).map((a) => [a.action, a.action])], action));
    bindSeg(view.querySelector('#aseg'), 'action', (a) => { action = a; ctx.setQuery({ hours, action, verified: scope }); events(); });
    return html`
      ${s.unverified_events && !scope ? html`<div class="notice">${raw('<i data-icon="alert"></i>')}<div><b>${num(s.unverified_events)}</b> events in this window come from contracts whose code is not allowlisted for ${protoName(name)} (possible spoofs or unknown versions). They are excluded from these figures, prices and analytics; switch to “Include unverified” to inspect them.</div></div>` : ''}
      <div class="tiles">
        ${tile('Events', compact(ev), { title: num(ev) })}
        ${tile('Transactions', compact(Math.max(0, ...s.actions.map((a) => a.txs))), { foot: 'Max over actions' })}
        ${tile('Active contracts', num(s.contracts.length))}
        ${tile('Users', num(Math.max(0, ...s.actions.map((a) => a.users))), { foot: 'Max over actions' })}
        ${s.pools.length ? tile('Fills', compact(s.pools.reduce((a, p) => a + p.trades, 0))) : ''}
      </div>
      <div class="grid g-main sor-gap">
        ${card('Activity per hour', html`<div id="tl"></div>`)}
        ${card('Actions', hbars(s.actions.map((a) => ({ label: a.action, value: a.events, title: `${a.action}: ${num(a.events)} events by ${num(a.users)} users in ${num(a.txs)} txs` }))))}
      </div>
      <div class="stack">
      ${assets.length ? card('Lending by asset', html`<div class="table-wrap">${table([
        { h: 'Asset', v: (a) => asset(a) },
        ...LENDING.filter((act) => lending.some((r) => r.action === act)).map((act) => ({ h: cap(act.replace('_collateral', ' coll.').replace('_', ' ')), cls: 'num', v: (a) => { const c = cell(a, act); return c ? html`${compact(c.amount0)}<div class="sor-sub">${num(c.events)} ev</div>` : html`<span class="muted">—</span>`; } })),
      ], assets)}</div>`, { flush: true, hint: 'Sum of underlying amounts (amount0) per action' }) : ''}
      ${s.pools.length ? card('Top pools', table([
        { h: 'Pool', v: (p) => addr(p.pool) },
        { h: 'Pair', v: (p) => pairLink(p.base, p.counter) },
        { h: 'Fills', cls: 'num', v: (p) => num(p.trades) },
        { h: 'Base volume', cls: 'num', v: (p) => compact(p.base_volume) },
        { h: 'Last price', cls: 'num', v: (p) => price(p.last_price) },
        { h: 'Takers', cls: 'num', v: (p) => num(p.takers) },
      ], s.pools.slice(0, 15), { rowHref: (p) => `#/pair/${encodeURIComponent(p.base)}/${encodeURIComponent(p.counter)}` }), { flush: true }) : ''}
      <div class="grid g2">
        ${card('Contracts', table([
          { h: 'Contract', v: (c) => addr(c.contract_id) },
          { h: 'Events', cls: 'num', v: (c) => num(c.events) },
          { h: 'Users', cls: 'num', v: (c) => num(c.users) },
          { h: 'Actions', v: (c) => html`<span class="sor-tags">${c.actions.slice(0, 3).map((a) => tag(a))}${c.actions.length > 3 ? html`<span class="muted">+${c.actions.length - 3}</span>` : ''}</span>` },
        ], s.contracts.slice(0, 15), { rowHref: (c) => `#/contract/${c.contract_id}` }), { flush: true })}
        ${card('Top users', table([
          { h: 'User', v: (u) => addr(u.user_addr) },
          { h: 'Events', cls: 'num', v: (u) => num(u.events) },
          { h: 'Txs', cls: 'num', v: (u) => num(u.txs) },
          { h: 'Actions', v: (u) => html`<span class="sor-tags">${u.actions.slice(0, 3).map((a) => tag(a))}</span>` },
        ], s.users, { rowHref: (u) => `#/${u.user_addr[0] === 'C' ? 'contract' : 'account'}/${u.user_addr}` }), { flush: true })}
      </div>
      </div>`;
  }, {
    after: (el, s) => {
      const t = s.timeline.map((r) => ({ ...r, t: new Date(r.t).getTime() }));
      if (t.length) columns(el.querySelector('#tl'), t, [{ key: 'events', label: 'Events', color: venueColor(name) }], { height: 200, extra: [['Users', (d) => num(d.users)]] });
      else setHTML(el.querySelector('#tl'), empty('No events in this window'));
    },
  });

  const events = () => feed(view.querySelector('#events'), {
    fetchPage: (before) => get(`/protocols/${name}/events` + qs({ action, before, limit: 50, verified: scope })),
    cursor: (e) => `${e.ledger}.${e.tx_index}.${e.op_index}.${e.event_index}`,
    rowHref: (e) => `#/tx/${e.tx_hash}`,
    cols: [
      { h: 'Time', v: (e) => when(e.closed_at) },
      { h: 'Action', v: (e) => html`<span class="sor-tags">${tag(e.action)}${unverified(e)}</span>` },
      { h: 'User', v: (e) => addr(e.user_addr) },
      { h: 'Asset', v: (e) => e.asset ? asset(e.asset) : '—' },
      { h: 'Amount 0', cls: 'num', v: (e) => e.amount0 ? amt7(e.amount0) : '—' },
      { h: 'Amount 1', cls: 'num', v: (e) => e.amount1 ? amt7(e.amount1) : '—' },
      { h: 'Contract', v: (e) => addr(e.contract_id) },
      { h: 'Tx', v: (e) => txLink(e.tx_hash) },
    ],
    emptyTitle: 'No events',
  });
  run(); events();
  bindSeg(view, 'hours', (h) => { hours = h; ctx.setQuery({ hours, action, verified: scope }); run(); });
  bindSeg(view, 'scope', (v) => { scope = v; ctx.setQuery({ hours, action, verified: scope }); run(); events(); });
}
