import { get, qs } from '../api.js';
import { html, setHTML, load, card, pageHead, seg, bindSeg, table, num, compact, price, amount, stroops, pct, ago, when, asset,
  venue, addr, txLink, raw, parseAsset, expert, stackbar, VENUE_LABEL, venueColor, icon, empty } from '../ui.js';
import { candles } from '../charts.js';

const RANGES = { '6h': [6, '5m'], '24h': [24, '15m'], '3d': [72, '1h'], '7d': [168, '4h'], '30d': [720, '1d'] };
const INTERVALS = ['1m', '5m', '15m', '30m', '1h', '4h', '1d'];

export default async function pair(view, [base, counter], query, ctx) {
  let range = RANGES[query.range] ? query.range : '24h';
  let interval = INTERVALS.includes(query.interval) ? query.interval : RANGES[range][1];
  let mode = query.mode === 'line' ? 'line' : 'candles';
  let venueFilter = query.venue || '';
  const B = parseAsset(base), C = parseAsset(counter);
  const swapHref = `#/pair/${encodeURIComponent(counter)}/${encodeURIComponent(base)}`;
  const save = () => ctx.setQuery({ range, interval, mode: mode === 'line' ? 'line' : '', venue: venueFilter });

  setHTML(view, html`
    ${pageHead(html`<span class="pair" style="font-size:20px">${asset(base, { issuer: true })}<span class="slash">/</span>${asset(counter, { issuer: true })}</span>`, {
      crumbs: html`<a href="#/markets">Markets</a> / pair`,
      sub: html`Price of 1 ${B.code} in ${C.code}, combining every venue the indexer decodes: SDEX order book, classic AMM pools and Soroban DEXs (Soroswap, Aquarius, CLMM).`,
      actions: html`<a class="btn" href="${swapHref}" title="Flip base and counter">${icon('flip')}Invert</a>${expert('asset', base, `${B.code} on stellar.expert`)}`,
    })}
    <div class="card" style="margin-bottom:16px"><div class="card-body">
      <div id="ticker" class="price-head"><div class="skel" style="height:40px;width:60%"></div></div>
      <div class="toolbar" style="margin:16px 0 8px">
        ${seg('range', Object.keys(RANGES).map((r) => [r, r]), range)}
        ${seg('interval', INTERVALS.map((i) => [i, i]), interval)}
        <span class="grow"></span>
        ${seg('mode', [['candles', 'Candles'], ['line', 'Line']], mode)}
      </div>
      <div id="chart"></div>
    </div></div>
    <div class="grid g-main">
      ${card('Recent trades', html`<div class="toolbar" style="padding:0 16px" id="vf"></div><div id="trades"></div>`, { flush: true, hint: 'taker side · auto-refreshing' })}
      <div class="stack">
        ${card('Venue split', html`<div id="split"></div>`, { hint: 'last 200 fills' })}
        ${card('Top takers', html`<div id="takers"></div>`, { flush: true, hint: 'last 200 fills' })}
      </div>
    </div>`);

  const ticker = () => load(view.querySelector('#ticker'), () => get('/price' + qs({ base, counter })), (p) => {
    const chg = p.change_24h_pct;
    return html`
      <div><div class="big">${price(p.last)}</div><div class="muted" style="font-size:12.5px">${C.code} per ${B.code}${p.last_trade_at ? html` · last trade ${ago(p.last_trade_at)}` : ''}</div></div>
      <div class="chg ${chg > 0 ? 'up' : chg < 0 ? 'down' : ''}">${chg === undefined || chg === null ? '—' : html`${chg > 0 ? '▲' : chg < 0 ? '▼' : ''} ${pct(chg, 2, true)}`} <span class="muted" style="font-size:12px;font-weight:400">24h</span></div>
      <div class="stat">24h high <b>${price(p.high_24h)}</b></div>
      <div class="stat">24h low <b>${price(p.low_24h)}</b></div>
      <div class="stat">24h volume <b>${compact(p.base_volume_24h)}</b> ${B.code} · <b>${compact(p.counter_volume_24h)}</b> ${C.code}</div>
      <div class="stat">24h trades <b>${num(p.trades_24h)}</b></div>`;
  }, { quiet: !!view.querySelector('#ticker .big') });

  const chartEl = view.querySelector('#chart');
  const chart = async (quiet) => {
    if (!quiet) setHTML(chartEl, html`<div class="skel" style="height:420px"></div>`);
    try {
      const to = new Date();
      const from = new Date(to - RANGES[range][0] * 3600e3);
      const d = await get('/candles' + qs({ base, counter, interval, from: from.toISOString(), to: to.toISOString(), limit: 2000 }));
      if (!ctx.alive()) return;
      const rows = d.candles.map((c) => ({ ...c, t: new Date(c.t).getTime(), volume: c.base_volume })).reverse();
      candles(chartEl, rows, { height: 420, mode, volumeLabel: `Volume (${B.code})`, emptyText: `No ${B.code}/${C.code} trades in the last ${range}. Try a longer range.` });
    } catch (e) {
      if (!quiet) setHTML(chartEl, html`<div class="error-box">${icon('alert')}<div class="msg">${e.message}</div></div>`);
    }
  };

  // Trades come back in canonical orientation; project them onto base/counter.
  const view_ = (t) => {
    const same = t.base === base;
    const baseAmt = same ? t.base_volume : t.counter_volume;
    const ctrAmt = same ? t.counter_volume : t.base_volume;
    return { ...t, p: same ? t.price : 1 / t.price, baseAmt, ctrAmt, sell: t.sold_asset === base };
  };

  let lastTrades = [];
  const tradesEl = view.querySelector('#trades');
  const trades = (quiet) => load(tradesEl, () => get('/trades' + qs({ base, counter, venue: venueFilter, limit: 200 })), (rows) => {
    lastTrades = rows.map(view_);
    drawSide();
    return table([
      { h: 'Time', v: (t) => when(t.closed_at) },
      { h: 'Venue', v: (t) => venue(t.venue) },
      { h: 'Side', v: (t) => html`<span class="${t.sell ? 'down' : 'up'}">${t.sell ? 'Sell' : 'Buy'}</span>` },
      { h: `Price (${C.code})`, cls: 'num', v: (t) => price(t.p) },
      { h: `Amount (${B.code})`, cls: 'num', v: (t) => amount(t.baseAmt) },
      { h: `Total (${C.code})`, cls: 'num', v: (t) => amount(t.ctrAmt) },
      { h: 'Taker', v: (t) => addr(t.taker) },
      { h: 'Tx', v: (t) => txLink(t.tx_hash) },
    ], lastTrades.slice(0, 30), { emptyTitle: 'No trades', emptyText: venueFilter ? `No ${VENUE_LABEL[venueFilter] || venueFilter} fills for this pair in the index.` : 'No fills for this pair in the index yet.' });
  }, { quiet });

  const drawSide = () => {
    const by = {};
    for (const t of lastTrades) by[t.venue] = (by[t.venue] || 0) + 1;
    const parts = Object.entries(by).sort((a, b) => b[1] - a[1]).map(([v, n]) => ({ label: VENUE_LABEL[v] || v, value: n, color: venueColor(v) }));
    setHTML(view.querySelector('#split'), parts.length ? stackbar(parts, { fmt: num }) : empty('No fills'));
    const tk = {};
    for (const t of lastTrades) { const o = tk[t.taker] ||= { taker: t.taker, n: 0, vol: 0 }; o.n++; o.vol += t.baseAmt; }
    const top = Object.values(tk).sort((a, b) => b.vol - a.vol).slice(0, 8);
    setHTML(view.querySelector('#takers'), table([
      { h: 'Taker', v: (r) => addr(r.taker) },
      { h: 'Fills', cls: 'num', v: (r) => num(r.n) },
      { h: `Volume (${B.code})`, cls: 'num', v: (r) => compact(r.vol) },
    ], top));
  };

  // Venue filter chips come from the pair's venues in /pairs.
  const vf = view.querySelector('#vf');
  get('/pairs' + qs({ hours: 720, limit: 500 })).then((ps) => {
    const p = ps.find((x) => (x.base === base && x.counter === counter) || (x.base === counter && x.counter === base));
    const vs = p?.venues || [];
    if (!ctx.alive()) return;
    setHTML(vf, html`${seg('venue', [['', 'All venues'], ...vs.map((v) => [v, VENUE_LABEL[v] || v])], venueFilter)}`);
    bindSeg(vf, 'venue', (v) => { venueFilter = v; save(); trades(false); });
  }).catch(() => {});

  bindSeg(view, 'range', (r) => {
    range = r; interval = RANGES[r][1];
    view.querySelectorAll('[data-seg="interval"] button').forEach((b) => b.classList.toggle('on', b.dataset.v === interval));
    save(); chart(false);
  });
  bindSeg(view, 'interval', (i) => { interval = i; save(); chart(false); });
  bindSeg(view, 'mode', (m) => { mode = m; save(); chart(false); });

  ticker(); chart(false); trades(false);
  ctx.every(15000, () => { ticker(); chart(true); trades(true); });
}
