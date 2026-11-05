import { get, qs } from '../api.js';
import { html, setHTML, load, card, tile, pageHead, seg, bindSeg, hbars, stackbar, table, num, compact, pct, amount, ago, duration,
  bytes, ledgerLink, pairLink, venue, price, raw, state, snake, VENUE_LABEL, venueColor, empty } from '../ui.js';
import { columns, line, sparkline } from '../charts.js';
import { alertRow } from './alerts.js';

const WINDOWS = { '1h': [1, 'minute'], '24h': [24, 'hour'], '7d': [168, 'day'] };

export default async function overview(view, _p, query, ctx) {
  // Default to the last hour while the index holds less than ~3h of ledgers.
  let win = WINDOWS[query.window] ? query.window : (state.status?.ledgers_indexed ?? 0) < 2000 ? '1h' : '24h';
  setHTML(view, html`
    ${pageHead('Network overview', {
      sub: html`Live analytics from the indexed Stellar <b>${state.network || '…'}</b> ledgers: throughput, fees, the operation mix, markets and Soroban protocol activity.`,
      actions: seg('window', [['1h', 'Last hour'], ['24h', '24 hours'], ['7d', '7 days']], win),
    })}
    <div class="tiles" id="tiles"></div>
    <div class="grid g2" style="margin-bottom:16px">
      ${card('Transactions', html`<div id="c-tx"></div><div class="legend"><span><i class="sw" style="background:var(--s1)"></i>Successful</span><span><i class="sw" style="background:var(--s2)"></i>Failed</span></div>`, { hint: html`<span id="c-tx-hint"></span>` })}
      ${card('Operations & Soroban share', html`<div id="c-ops"></div><div id="c-sor" style="margin-top:8px"></div><div class="legend"><span><i class="ln" style="background:var(--s1)"></i>Operations</span><span><i class="ln" style="background:var(--s7)"></i>Soroban share of txs</span></div>`)}
    </div>
    <div class="grid g-main" style="margin-bottom:16px">
      ${card('Latest ledgers', html`<div id="feed"></div>`, { flush: true, hint: html`auto-refreshing · <a href="#/ledgers">all ledgers</a>` })}
      ${card('Operation mix', html`<div id="opmix"></div>`, { hint: html`<a href="#/ops">explore</a>` })}
    </div>
    <div class="grid g2" style="margin-bottom:16px">
      ${card('Top markets', html`<div id="pairs"></div>`, { flush: true, hint: html`by trades · <a href="#/markets">all markets</a>` })}
      ${card('Soroban protocols', html`<div id="protos"></div>`, { hint: html`<a href="#/protocols">details</a>` })}
    </div>
    <div style="margin-bottom:16px">${card('Notable events', html`<div id="alerts"></div>`, { flush: true, hint: html`large transfers, swaps, flash loans, arbitrage · <a href="#/alerts">all events</a>` })}</div>
    ${card('Index storage', html`<div id="tables"></div>`, { flush: true, hint: 'Postgres table sizes (planner row estimates)' })}
  `);

  const statsEl = view.querySelector('#tiles');
  let lastStats = [];

  const drawTiles = () => {
    const s = state.status || {};
    const b = lastStats;
    const tot = (k) => b.reduce((a, r) => a + (+r[k] || 0), 0);
    const txs = tot('txs'), ok = tot('txs_ok'), ops = tot('ops'), sor = tot('soroban_txs'), fees = tot('fees_xlm'), ledgers = tot('ledgers');
    const asc = [...b].reverse();
    const lag = s.lag_seconds;
    setHTML(statsEl, html`
      ${tile('Latest indexed ledger', s.cursor ? html`<a href="#/ledger/${s.cursor}" style="color:inherit">${num(s.cursor)}</a>` : '—', { foot: s.last_closed_at ? `closed ${ago(s.last_closed_at)} · protocol ${b[0]?.protocol ?? '—'}` : 'waiting for first ledger' })}
      ${tile('Ingestion lag', lag === undefined || lag === null ? '—' : lag <= 60 ? html`<span class="up">${lag}s</span>` : duration(lag),
        { foot: s.rpc_tip ? `${num(s.lag_ledgers)} ledgers behind RPC tip ${num(s.rpc_tip)}` : 'API-only mode: tip unknown' })}
      ${tile('Transactions', compact(txs), { foot: `${pct(txs ? ok / txs * 100 : 0, 1)} successful · ${num(ledgers ? txs / ledgers : 0, 1)}/ledger`, title: num(txs) })}
      ${tile('Operations', compact(ops), { foot: `${num(ledgers ? ops / ledgers : 0, 1)} per ledger`, title: num(ops) })}
      ${tile('Soroban share', pct(txs ? sor / txs * 100 : 0, 1), { foot: `${compact(sor)} smart-contract txs` })}
      ${tile('Fees burned', html`${amount(fees, 2)} <span class="muted" style="font-size:14px">XLM</span>`, { foot: `over ${ledgers} ledgers in window` })}
    `);
    statsEl.querySelectorAll('.tile').forEach((t, i) => {
      const key = [null, null, 'txs', 'ops', 'soroban_txs', 'fees_xlm'][i];
      if (key && asc.length > 2) t.insertAdjacentHTML('beforeend', `<div class="spark">${sparkline(asc.map((r) => r[key]), 'var(--s1)')}</div>`);
    });
  };

  const loadWindow = async () => {
    const [hours, bucket] = WINDOWS[win];
    const rows = await get('/stats/network' + qs({ hours, bucket }));
    if (!ctx.alive()) return;
    lastStats = rows;
    drawTiles();
    const asc = rows.map((r) => ({ ...r, t: new Date(r.t).getTime(), share: r.txs ? r.soroban_txs / r.txs * 100 : 0 })).reverse();
    const txEl = view.querySelector('#c-tx');
    if (!asc.length) {
      setHTML(txEl, empty('No ledgers in this window', 'The indexer has not ingested ledgers that closed in this time range yet.'));
      view.querySelector('#c-ops').innerHTML = view.querySelector('#c-sor').innerHTML = '';
    } else {
      columns(txEl, asc, [{ key: 'txs_ok', label: 'Successful', color: 'var(--s1)' }, { key: 'txs_failed', label: 'Failed', color: 'var(--s2)' }],
        { height: 240, fmt: (v) => num(v), extra: [['Ledgers', (d) => num(d.ledgers)], ['Avg txs/ledger', (d) => num(d.avg_txs_per_ledger, 1)]] });
      line(view.querySelector('#c-ops'), asc, [{ key: 'ops', label: 'Operations', color: 'var(--s1)', area: true }], { height: 140, fmt: compact });
      line(view.querySelector('#c-sor'), asc, [{ key: 'share', label: 'Soroban share', color: 'var(--s7)' }], { height: 92, fmt: (v) => pct(v, 0), yMin: 0 });
    }
    view.querySelector('#c-tx-hint').textContent = `per ${bucket}`;
  };

  const opmix = () => load(view.querySelector('#opmix'), () => get('/stats/operations' + qs({ hours: WINDOWS[win][0] })), (rows) => {
    const top = rows.slice(0, 10);
    const rest = rows.slice(10).reduce((s, r) => s + r.ops, 0);
    if (rest) top.push({ type: `other (${rows.length - 10} types)`, ops: rest });
    return hbars(top.map((r) => ({ label: snake(r.type), value: r.ops, href: r.type.startsWith('other') ? '' : `#/ops?type=${r.type}`, title: `${r.type}: ${num(r.ops)} ops, ${num(r.sources)} source accounts` })), { fmt: compact });
  });

  const protos = () => load(view.querySelector('#protos'), () => get('/protocols' + qs({ hours: WINDOWS[win][0] })), (d) => {
    const venues = d.trade_venues || [];
    return html`
      ${hbars((d.protocols || []).map((p) => ({ label: VENUE_LABEL[p.protocol] || p.protocol, value: p.events, color: venueColor(p.protocol), href: `#/protocol/${p.protocol}`, title: `${p.protocol}: ${num(p.events)} events, ${num(p.users)} users, ${num(p.txs)} txs` })), { fmt: compact })}
      <h3 style="margin:18px 0 8px">Trades by venue</h3>
      ${venues.length ? stackbar(venues.map((v) => ({ label: VENUE_LABEL[v.venue] || v.venue, value: v.trades, color: venueColor(v.venue) }))) : empty('No trades in this window')}`;
  });

  const pairs = () => load(view.querySelector('#pairs'), () => get('/pairs' + qs({ hours: WINDOWS[win][0], limit: 8 })), (rows) => table([
    { h: 'Pair', v: (r) => pairLink(r.base, r.counter) },
    { h: 'Last price', cls: 'num', v: (r) => price(r.last_price) },
    { h: 'Trades', cls: 'num', v: (r) => num(r.trades) },
    { h: 'Venues', v: (r) => raw(r.venues.slice(0, 3).map((v) => venue(v).s).join(' ') + (r.venues.length > 3 ? ` <span class="muted">+${r.venues.length - 3}</span>` : '')) },
  ], rows, { rowHref: (r) => `#/pair/${encodeURIComponent(r.base)}/${encodeURIComponent(r.counter)}`, emptyTitle: 'No trades yet', emptyText: 'Markets appear once the indexer has seen SDEX, AMM or Soroban DEX fills.' }));

  // Live ledger feed: new rows flash in.
  const feedEl = view.querySelector('#feed');
  let seen = new Set();
  const feed = (quiet) => load(feedEl, () => get("/ledgers?limit=9"), (rows) => {
    const html_ = html`${rows.map((l) => {
      const okW = l.tx_count ? l.tx_success / l.tx_count * 100 : 0;
      return html`<div class="feed-row ${seen.size && !seen.has(l.sequence) ? 'new' : ''}">
        <div><div class="seq">${ledgerLink(l.sequence)}</div><div class="muted" style="font-size:12px">${ago(l.closed_at)}</div></div>
        <div><div style="font-size:12.5px"><b>${num(l.tx_count)}</b> txs · ${num(l.op_count)} ops · ${num(l.soroban_tx_count)} soroban</div>
          <div class="minibar" title="${num(l.tx_success)} successful / ${num(l.tx_failed)} failed"><span style="width:${raw(okW.toFixed(1))}%;background:var(--s1)"></span><span style="width:${raw((100 - okW).toFixed(1))}%;background:var(--s2)"></span></div></div>
        <div class="num muted" style="font-size:12px">${amount(l.fees_charged / 1e7, 3)} XLM fees</div></div>`;
    })}`;
    seen = new Set(rows.map((l) => l.sequence));
    return rows.length ? html_ : empty('No ledgers indexed yet', 'Start the indexer (indexer run) and ledgers will stream in here.');
  }, { quiet, skel: undefined });

  const tables = () => load(view.querySelector('#tables'), () => get('/stats/tables'), (rows) => table([
    { h: 'Table', v: (r) => html`<code>${r.table}</code>` },
    { h: 'Rows (est.)', cls: 'num', v: (r) => num(r.rows_estimate) },
    { h: 'Size', cls: 'num', v: (r) => bytes(r.bytes) },
    { h: '', v: (r) => html`<div class="minibar" style="height:6px;border-radius:3px;background:var(--surface-3);min-width:80px"><span style="display:block;height:100%;border-radius:3px;width:${raw((r.bytes / Math.max(...rows.map((x) => x.bytes), 1) * 100).toFixed(1))}%;background:var(--s1)"></span></div>` },
  ], rows));

  const alertsEl = view.querySelector('#alerts');
  const alerts = (quiet) => load(alertsEl, () => get('/alerts' + qs({ hours: WINDOWS[win][0], limit: 8, kinds: 'large_transfer,large_trade,flash_loan,arbitrage,price_move' })),
    (d) => d.alerts.length ? html`${d.alerts.map(alertRow)}` : empty('Nothing notable in this window'), { quiet });

  const all = () => { loadWindow().catch((e) => setHTML(view.querySelector('#c-tx'), html`${e.message}`)); opmix(); protos(); pairs(); alerts(false); };
  all(); feed(false); tables();
  bindSeg(view, 'window', (v) => { win = v; ctx.setQuery({ window: v }); all(); });
  const onStatus = () => drawTiles();
  window.addEventListener('status', onStatus);
  ctx.onCleanup(() => window.removeEventListener('status', onStatus));
  ctx.every(5000, () => feed(true));
  ctx.every(30000, () => { loadWindow().catch(() => {}); alerts(true); });
}
