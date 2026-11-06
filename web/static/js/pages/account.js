import { get, qs } from '../api.js';
import { html, setHTML, load, feed, pageHead, card, tile, kv, table, num, compact, amount, amt7, price, pct, when, ago, dt, addr, txLink,
  ledgerLink, asset, venue, tag, expert, copyBtn, snake, tabs, bindTabs, icon, hbars, seg, bindSeg, raw, empty, state, pairLink,
  VENUE_LABEL, venueColor, unverified } from '../ui.js';
import { line, donut, heatmap } from '../charts.js';
import { opSummary } from '../ops.js';
import { tagBar, watchBtn, bindWatch } from '../tags.js';
import { protoName } from '../protocols-meta.js';

const SLOTS = ['var(--s1)', 'var(--s2)', 'var(--s3)', 'var(--s4)', 'var(--s5)', 'var(--s6)', 'var(--s7)'];
const KIND = { native: 'Native', trustline: 'Trustline', token: 'Token, from transfers', pool_share: 'Pool shares', blend_supply: 'Blend supply', blend_borrow: 'Blend debt' };
const usdFmt = (v) => v === null || v === undefined ? '—' : '$' + (Math.abs(v) >= 1e4 ? compact(v) : num(v, 2));
const chg = (v) => v === null || v === undefined ? html`<span class="muted">—</span>` : html`<span class="${v > 0.005 ? 'up' : v < -0.005 ? 'down' : 'muted'}">${pct(v, 2, true)}</span>`;

export default async function account(view, [id], query, ctx) {
  setHTML(view, html`
    ${pageHead('Address portfolio', {
      crumbs: html`<a href="#/accounts">Accounts</a> / ${id.slice(0, 6)}…`,
      actions: html`${watchBtn(id)}<a class="btn" href="#/compare?a=${id}">Compare</a><a class="btn" href="#/txs?source=${id}">Transactions</a>${id[0] === 'C' ? html`<a class="btn" href="#/contract/${id}">Contract view</a>` : ''}${expert(id[0] === 'C' ? 'contract' : 'account', id)}`,
    })}
    <div class="acct-ident">
      <div class="acct-id"><span class="mono">${id}</span>${copyBtn(id)}</div>
      <div id="tags" class="acct-tags"></div>
    </div>
    <div id="pf"></div>
    <div class="section-title"><h2>Indicators</h2><span id="iseg"></span></div>
    <div id="ind"></div>
    <div class="section-title"><h2>Activity</h2></div>
    <section class="card acct-activity" aria-label="Activity"><div id="tabs" class="acct-tabs"></div><div class="card-body flush" id="tab"></div></section>`);
  bindWatch(view);
  tagBar(view.querySelector('#tags'), id);
  portfolio(view.querySelector('#pf'), id);
  let hours = query.hours || '168';
  setHTML(view.querySelector('#iseg'), seg('hours', [['24', '24h'], ['168', '7d'], ['720', '30d']], hours));
  bindSeg(view.querySelector('#iseg'), 'hours', (h) => { hours = h; ctx.setQuery({ hours }); indicators(view.querySelector('#ind'), id, hours); });
  indicators(view.querySelector('#ind'), id, hours);
  activity(view, id);
}

// ---- portfolio --------------------------------------------------------------

function portfolio(el, id) {
  return load(el, () => get(`/accounts/${id}/portfolio`), (p) => {
    // Fixed slot order by value rank (holdings arrive sorted); the rest folds into "Other".
    p.holdings.filter((h) => h.value_xlm > 0).slice(0, 6).forEach((h, i) => { h._color = SLOTS[i]; });
    const created = p.created_ledger && state.status?.cursor ? (state.status.cursor - p.created_ledger) * 5.5 : null;
    const usd = p.value_usd !== null && p.value_usd !== undefined;
    const notice = !p.known && !p.holdings.length
      ? html`<div class="notice">${icon('info')}<div>No ledger entry or token flows for this address since indexing began (ledger ${num(p.since_ledger)}), so there is nothing to value yet.</div></div>`
      : !p.known ? html`<div class="notice">${icon('info')}<div>${id[0] === 'C' ? 'Contract balances are' : 'This account’s ledger entry was not touched since indexing began, so balances are'} reconstructed from token transfers seen since ledger ${num(p.since_ledger)}, which makes them approximate.</div></div>` : '';
    return html`${notice}
      <section class="card acct-tally acct-tally-6" aria-label="Portfolio figures"><div class="tiles">
        ${tile('Net worth', usd ? usdFmt(p.value_usd) : html`${compact(p.value_xlm)} <span class="muted">XLM</span>`, { hero: true, foot: usd ? `${compact(p.value_xlm)} XLM, 1 XLM = $${num(p.usd_per_xlm, 4)}` : 'No USD market in the index', title: `${num(p.value_xlm, 4)} XLM` })}
        ${tile('24h change', chg(p.change_24h_pct), { foot: 'Balances × prices, reconstructed' })}
        ${tile('7d change', chg(p.change_7d_pct), { foot: p.history.length < 24 * 7 ? `Index covers ${Math.round(p.history.length)}h` : '' })}
        ${tile('Assets', num(p.holdings.length), { foot: p.unpriced ? `${p.unpriced} unpriced (illiquid)` : 'All priced' })}
        ${tile('Debt', p.debt_xlm ? html`<span class="down">${compact(p.debt_xlm)}</span>` : '0', { foot: 'Blend borrows, XLM' })}
        ${tile('Account age', created !== null ? (created > 86400 * 2 ? `${Math.round(created / 86400)} days` : `${Math.round(created / 3600)} h`) : '—', { foot: p.created_ledger ? `Created ~ledger ${num(p.created_ledger)}` : 'Unknown' })}
      </div></section>
      <div class="grid g-main acct-row">
        ${card('Value over time', html`<div id="pf-hist"></div>`, { hint: html`<span>Hourly, ${usd ? 'USD' : 'XLM'}</span><span class="acct-how" tabindex="0" title="Current balances walked back through token transfers, valued at hourly VWAPs. Fees, pool shares and Blend positions are held constant.">How?</span>` })}
        ${card('Allocation', html`<div id="pf-donut"></div><div id="pf-legend"></div>`)}
      </div>
      ${card('Holdings', table([
        { h: 'Asset', v: (h) => html`<span class="acct-asset">${h._color ? html`<i class="acct-sw" style="background:${raw(h._color)}"></i>` : html`<i class="acct-sw none"></i>`}${h.kind === 'pool_share' ? html`<span class="pair">${asset(h.parts?.[0]?.asset)}<span class="slash">/</span>${asset(h.parts?.[1]?.asset)}</span>` : asset(h.asset, { issuer: true })}</span><div class="kind acct-meta"><span>${KIND[h.kind] || h.kind}</span>${h.contract && h.kind !== 'pool_share' ? html`<span>${addr(h.contract)}</span>` : ''}${h.approx ? html`<span>Approx.</span>` : ''}</div>` },
        { h: 'Amount', cls: 'num', v: (h) => h.kind === 'pool_share' && h.parts ? html`${amount(h.amount)}<div class="kind">${amount(h.parts[0].amount)} + ${amount(h.parts[1].amount)}</div>` : amount(h.amount) },
        { h: 'Price (XLM)', cls: 'num', v: (h) => h.price_xlm ? price(h.price_xlm) : html`<span class="muted" title="Less than 250 XLM traded against XLM in 24h">illiquid</span>` },
        { h: 'Value (XLM)', cls: 'num', v: (h) => h.value_xlm !== null ? html`<span class="${h.value_xlm < 0 ? 'down' : ''}">${amount(h.value_xlm, 2)}</span>` : '—' },
        { h: 'Value (USD)', cls: 'num', v: (h) => usdFmt(h.value_usd) },
        { h: '24h', cls: 'num', v: (h) => chg(h.change_24h_pct) },
        { h: '7d', cls: 'num', v: (h) => chg(h.change_7d_pct) },
        { h: 'Share', v: (h) => h.share ? html`<div class="share"><div class="track"><span style="width:${raw((h.share * 100).toFixed(1))}%;background:${raw(h._color || 'var(--muted)')}"></span></div><span class="acct-share-pct">${pct(h.share * 100, 1)}</span></div>` : '' },
      ], p.holdings, { emptyTitle: 'No balances', emptyText: 'Nothing held according to the index.' }), { flush: true })}`;
  }, {
    after: (el, p) => {
      const priced = p.holdings.filter((h) => h.value_xlm > 0);
      const usd = p.value_usd !== null && p.value_usd !== undefined;
      const slices = priced.slice(0, 6).map((h) => ({ label: h.kind === 'pool_share' ? 'LP ' + (h.parts || []).map((x) => x.asset.split(':')[0].replace('native', 'XLM')).join('/') : h.asset === 'native' ? 'XLM' : h.asset.split(':')[0], value: h.value_xlm, color: h._color }));
      const rest = priced.slice(6).reduce((s, h) => s + h.value_xlm, 0);
      if (rest > 0) slices.push({ label: 'Other', value: rest, color: 'var(--muted)' });
      donut(el.querySelector('#pf-donut'), slices, { center: usd ? usdFmt(p.value_usd) : compact(p.value_xlm), sub: usd ? 'net worth' : 'XLM', fmt: (v) => `${compact(v)} XLM` });
      setHTML(el.querySelector('#pf-legend'), html`<div class="legend acct-legend">${slices.map((x) => html`<span><i class="sw" style="background:${raw(x.color)}"></i>${x.label} <span class="muted">${pct(x.value / p.assets_xlm * 100, 1)}</span></span>`)}</div>`);
      const hist = p.history.map((x) => ({ t: new Date(x.t).getTime(), v: usd ? x.value_usd : x.value_xlm }));
      const he = el.querySelector('#pf-hist');
      if (hist.length >= 2) line(he, hist, [{ key: 'v', label: usd ? 'Value (USD)' : 'Value (XLM)', color: 'var(--s1)', area: true }], { height: 240, fmt: usd ? usdFmt : compact, yMin: undefined });
      else setHTML(he, empty('Not enough history yet', 'The value chart needs at least two indexed hours.'));
    },
  });
}

// ---- indicators -------------------------------------------------------------

function indicators(el, id, hours) {
  return load(el, () => get(`/accounts/${id}/stats` + qs({ hours })), (s) => {
    const t = s.trading, tx = s.txs || {}, seen = s.seen || {}, u = s.usd_per_xlm;
    const xu = (v) => html`${compact(v)} <span class="muted">XLM</span>`;
    const usdFoot = (v) => u ? `≈ $${compact(v * u)}` : '';
    const protos = {};
    (s.protocols || []).forEach((p) => { protos[p.protocol] = (protos[p.protocol] || 0) + p.events; });
    return html`
      <section class="card acct-tally acct-tally-4" aria-label="Indicator figures"><div class="tiles">
        ${tile('Trading volume', xu(t.volume_xlm), { foot: [usdFoot(t.volume_xlm), `${num(t.trades)} fills${t.truncated ? ' (capped)' : ''}`].filter(Boolean).join(', ') })}
        ${tile('Realized PnL', html`<span class="${t.pnl.realized_xlm > 0 ? 'up' : t.pnl.realized_xlm < 0 ? 'down' : ''}">${t.pnl.realized_xlm > 0 ? '+' : ''}${compact(t.pnl.realized_xlm)}</span> <span class="muted">XLM</span>`, { foot: `Avg-cost, ${num(t.pnl.matched_sells)} matched sells`, title: 'Average-cost accounting in XLM over fills in the window; sells without a cost basis in the window are ignored.' })}
        ${tile('Unrealized', html`<span class="${t.pnl.unrealized_xlm > 0 ? 'up' : t.pnl.unrealized_xlm < 0 ? 'down' : ''}">${t.pnl.unrealized_xlm > 0 ? '+' : ''}${compact(t.pnl.unrealized_xlm)}</span>`, { foot: `${Object.keys(t.pnl.open_qty || {}).length} open positions at current prices` })}
        ${tile('Arbitrage cycles', num(t.cycles), { foot: t.cycles ? `Net ${t.cycle_profit_xlm >= 0 ? '+' : ''}${compact(t.cycle_profit_xlm)} XLM` : 'Same-tx round trips' })}
        ${tile('Transactions', num(tx.txs), { foot: tx.txs ? `${pct(tx.successful / tx.txs * 100, 0)} ok, ${num(tx.soroban)} Soroban` : 'As source' })}
        ${tile('Fees paid', html`${amount(tx.fees_xlm, 4)} <span class="muted">XLM</span>`, { foot: `${num(tx.contracts_called)} contracts called` })}
        ${tile('First seen', seen.first_seen ? ago(seen.first_seen) : '—', { foot: seen.first_seen ? dt(seen.first_seen) : 'In the indexed range' })}
        ${tile('Last seen', seen.last_seen ? ago(seen.last_seen) : '—', { foot: seen.last_seen ? dt(seen.last_seen) : '' })}
      </div></section>
      <div class="grid g-main acct-row">
        ${card('Activity heatmap', html`<div id="heat"></div>`, { hint: 'Distinct transactions per weekday × hour' })}
        <div class="stack">
        ${card('Venues & protocols', html`
          ${t.venues.length ? hbars(t.venues.map((v) => ({ label: VENUE_LABEL[v.venue] || v.venue, value: v.volume_xlm, color: venueColor(v.venue), title: `${num(v.trades)} fills, ${compact(v.volume_xlm)} XLM` })), { fmt: (v) => `${compact(v)} XLM` }) : html`<div class="muted">No fills in this window.</div>`}
          ${Object.keys(protos).length ? html`<h3 class="acct-sub">Protocol events</h3>${hbars(Object.entries(protos).map(([p, n]) => ({ label: protoName(p), value: n, color: venueColor(p), href: `#/protocol/${p}` })), { fmt: num })}` : ''}`)}
        ${card('Top pairs traded', table([
          { h: 'Pair', v: (p) => pairLink(p.base, p.counter) },
          { h: 'Fills', cls: 'num', v: (p) => num(p.trades) },
          { h: 'Volume (XLM)', cls: 'num', v: (p) => compact(p.volume_xlm) },
          { h: 'Realized', cls: 'num', v: (p) => { const r = (t.pnl.per_asset_realized_xlm || {})[p.base] ?? (t.pnl.per_asset_realized_xlm || {})[p.counter]; return r === undefined ? '—' : html`<span class="${r > 0 ? 'up' : r < 0 ? 'down' : ''}">${compact(r)}</span>`; } },
        ], t.pairs, { emptyTitle: 'No fills', rowHref: (p) => `#/pair/${encodeURIComponent(p.base)}/${encodeURIComponent(p.counter)}` }), { flush: true })}
        </div>
      </div>
      <div class="grid g2 acct-row">
        ${card('Largest transfers', table([
          { h: 'When', v: (r) => when(r.closed_at) },
          { h: 'Direction', v: (r) => r.direction === 'in' ? html`<span class="up">In</span>` : html`<span class="down">Out</span>` },
          { h: 'Amount', cls: 'num', v: (r) => html`${amount(r.amount)} ${asset(r.asset)}` },
          { h: 'Value', cls: 'num', v: (r) => html`<b>${compact(r.value_xlm)}</b> XLM` },
          { h: 'Tx', v: (r) => txLink(r.tx_hash) },
        ], s.largest_transfers, { emptyTitle: 'No priced transfers in this window' }), { flush: true })}
        ${card('Top counterparties', table([
          { h: 'Address', v: (c) => addr(c.address) },
          { h: 'Transfers', cls: 'num', v: (c) => num(c.transfers) },
          { h: 'In (XLM)', cls: 'num', v: (c) => html`<span class="up">${compact(c.in_xlm)}</span>` },
          { h: 'Out (XLM)', cls: 'num', v: (c) => html`<span class="down">${compact(c.out_xlm)}</span>` },
          { h: 'Last', v: (c) => when(c.last) },
        ], s.counterparties, { emptyTitle: 'No counterparties in this window', rowHref: (c) => `#/account/${c.address}` }), { flush: true })}
      </div>
      <div class="grid g2 acct-row">
        ${card('Net flows by asset', table([
          { h: 'Asset', v: (f) => asset(f.asset) },
          { h: 'In', cls: 'num', v: (f) => html`<span class="up">${compact(f.in_amount)}</span>` },
          { h: 'Out', cls: 'num', v: (f) => html`<span class="down">${compact(f.out_amount)}</span>` },
          { h: 'Net (XLM)', cls: 'num', v: (f) => f.in_xlm === null ? '—' : compact(f.in_xlm - f.out_xlm) },
        ], s.flows, { emptyTitle: 'No transfers in this window' }), { flush: true })}
      ${t.top_cycles?.length ? card('Most profitable round trips', table([
        { h: 'When', v: (c) => when(c.closed_at) },
        { h: 'Hops', cls: 'num', v: (c) => String(c.hops) },
        { h: 'Profit', cls: 'num', v: (c) => html`<span class="${c.profit > 0 ? 'up' : 'down'}">${amount(c.profit)}</span> ${asset(c.asset)}` },
        { h: 'In XLM', cls: 'num', v: (c) => compact(c.profit_xlm) },
        { h: 'Tx', v: (c) => txLink(c.tx_hash) },
      ], t.top_cycles, { rowHref: (c) => `#/tx/${c.tx_hash}` }), { flush: true, hint: 'First sold asset = last bought asset in one transaction' }) : ''}
      </div>`;
  }, { after: (el, s) => heatmap(el.querySelector('#heat'), s.heatmap) });
}

// ---- activity tabs ------------------------------------------------------------

function activity(view, id) {
  const T = [['ops', 'Operations'], ['transfers', 'Transfers'], ['trades', 'Trades'], ['protocol', 'DeFi activity'], ['offers', 'Offers']];
  setHTML(view.querySelector('#tabs'), tabs(T, 'ops'));
  const tab = view.querySelector('#tab');
  const show = (t) => {
    if (t === 'ops') {
      feed(tab, {
        fetchPage: (before) => get(`/accounts/${id}/operations` + qs({ before, limit: 50 })),
        cursor: (o) => o.id, rowHref: (o) => `#/tx/${o.tx_hash}`,
        cols: [
          { h: 'Time', v: (o) => when(o.closed_at) },
          { h: 'Type', v: (o) => html`<b>${snake(o.type)}</b>${o.successful ? '' : html` <span class="badge fail">Failed</span>`}` },
          { h: 'Details', v: (o) => opSummary(o.type, o.details) },
          { h: 'Tx', v: (o) => txLink(o.tx_hash) },
        ],
        emptyTitle: 'No operations sourced by this address',
      });
    } else if (t === 'transfers') {
      load(tab, () => get(`/accounts/${id}/transfers?limit=200`), (rows) => table([
        { h: 'Time', v: (r) => when(r.closed_at) },
        { h: 'Type', v: (r) => tag(r.type) },
        { h: 'Direction', v: (r) => r.to_addr === id ? html`<span class="up">In</span>` : html`<span class="down">Out</span>` },
        { h: 'Counterparty', v: (r) => addr(r.to_addr === id ? r.from_addr : r.to_addr) },
        { h: 'Amount', cls: 'num', v: (r) => html`<b>${amt7(r.amount)}</b> ${asset(r.asset)}` },
        { h: 'Tx', v: (r) => txLink(r.tx_hash) },
      ], rows, { emptyTitle: 'No token transfers' }));
    } else if (t === 'trades') {
      load(tab, () => get(`/accounts/${id}/trades?limit=200`), (rows) => table([
        { h: 'Time', v: (r) => when(r.closed_at) },
        { h: 'Venue', v: (r) => html`${venue(r.venue)}${unverified(r)}` },
        { h: 'Sold', cls: 'num', v: (r) => html`${amt7(r.sold_amount)} ${asset(r.sold_asset)}` },
        { h: 'Bought', cls: 'num', v: (r) => html`${amt7(r.bought_amount)} ${asset(r.bought_asset)}` },
        { h: 'Price', cls: 'num', v: (r) => price(r.price) },
        { h: 'Pool', v: (r) => addr(r.pool) },
        { h: 'Tx', v: (r) => txLink(r.tx_hash) },
      ], rows, { emptyTitle: 'No trades as taker' }));
    } else if (t === 'protocol') {
      load(tab, () => get(`/accounts/${id}/events?limit=200`), (rows) => table([
        { h: 'Time', v: (r) => when(r.closed_at) },
        { h: 'Protocol', v: (r) => html`<a href="#/protocol/${r.protocol}">${protoName(r.protocol)}</a>` },
        { h: 'Action', v: (r) => html`${tag(r.action)}${unverified(r)}` },
        { h: 'Asset', v: (r) => r.asset ? asset(r.asset) : '—' },
        { h: 'Amount', cls: 'num', v: (r) => r.amount0 ? amt7(r.amount0) : '—' },
        { h: 'Contract', v: (r) => addr(r.contract_id) },
        { h: 'Tx', v: (r) => txLink(r.tx_hash) },
      ], rows, { emptyTitle: 'No Soroswap / Aquarius / Blend activity' }));
    } else {
      load(tab, () => get(`/accounts/${id}`), (a) => html`${a.balance !== undefined ? html`<div class="acct-kv">${kv([
        ['Sequence', html`<span class="mono">${a.seq_num}</span>`], ['Subentries', num(a.num_subentries)],
        ['Home domain', a.home_domain || '—'], ['Signers', `${num(a.num_signers)} extra, master weight ${a.master_weight}`],
        ['Last modified', ledgerLink(a.last_modified)]])}</div>` : ''}${table([
        { h: 'Offer', v: (o) => tag(o.offer_id) },
        { h: 'Selling', cls: 'num', v: (o) => html`${amount(o.amount)} ${asset(o.selling)}` },
        { h: 'Buying', v: (o) => asset(o.buying) },
        { h: 'Price', cls: 'num', v: (o) => price(o.price) },
        { h: 'Updated', cls: 'num', v: (o) => ledgerLink(o.last_modified) },
      ], a.offers, { emptyTitle: 'No open offers indexed' })}`);
    }
  };
  bindTabs(view.querySelector('#tabs'), show);
  show('ops');
}
