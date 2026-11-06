import { get, qs } from '../api.js';
import { html, setHTML, load, pageHead, seg, bindSeg, card, table, num, compact, pct, amount, addr, venue, pairLink, raw, VENUE_LABEL } from '../ui.js';
import { labelChips } from '../tags.js';
import { protoName } from '../protocols-meta.js';

const who = (r) => html`<span class="nowrap"><span class="rank">${r._rank}</span> ${addr(r.address)}</span> ${labelChips(r.labels)}`;

export default async function leaders(view, _p, query, ctx) {
  let hours = query.hours || '24';
  setHTML(view, html`
    ${pageHead('Leaderboards', {
      sub: 'Who moves the network: top traders by XLM-valued volume, arbitrage bots by round trips, the busiest and highest-fee accounts, the deepest pools, DeFi power users and whales. Your labels show next to each address.',
      actions: seg('hours', [['1', '1h'], ['24', '24h'], ['168', '7d']], hours),
    })}
    <div id="boards"></div>`);
  const run = () => load(view.querySelector('#boards'), () => get('/leaderboards' + qs({ hours, limit: 15 })), (d) => {
    for (const k of Object.keys(d)) if (Array.isArray(d[k])) d[k].forEach((r, i) => { r._rank = i + 1; });
    const href = (r) => `#/account/${r.address}`;
    return html`
      <div class="sor-boards">
      <div class="grid g2">
        ${card('Top traders', table([
          { h: 'Address', v: who },
          { h: 'Volume (XLM)', cls: 'num', v: (r) => html`<b>${compact(r.volume_xlm)}</b>` },
          { h: 'Fills', cls: 'num', v: (r) => num(r.trades) },
          { h: 'Venues', v: (r) => raw(r.venues.slice(0, 3).map((v) => venue(v).s).join(' ') + (r.venues.length > 3 ? ` <span class="muted">+${r.venues.length - 3}</span>` : '')) },
        ], d.traders, { rowHref: href, emptyTitle: 'No trades in this window' }), { flush: true, hint: 'Taker volume valued in XLM' })}
        ${card('Arbitrage bots', table([
          { h: 'Address', v: who },
          { h: 'Round trips', cls: 'num', v: (r) => html`<b>${num(r.cycles)}</b>` },
          { h: 'Avg hops', cls: 'num', v: (r) => num(r.avg_hops, 1) },
          { h: 'Profitable', cls: 'num', v: (r) => pct(r.profitable / r.cycles * 100, 0) },
          { h: 'Net (XLM)', cls: 'num', v: (r) => r.profit_xlm === null ? '—' : html`<span class="${r.profit_xlm > 0 ? 'up' : 'down'}">${compact(r.profit_xlm)}</span>` },
        ], d.arbitrageurs, { rowHref: href, emptyTitle: 'No round trips in this window' }), { flush: true, hint: 'Same-tx cycles' })}
      </div>
      <div class="grid g2">
        ${card('Most active', table([
          { h: 'Address', v: who },
          { h: 'Txs', cls: 'num', v: (r) => html`<b>${num(r.txs)}</b>` },
          { h: 'Failed', cls: 'num', v: (r) => pct(r.failed / r.txs * 100, 0) },
          { h: 'Ops', cls: 'num', v: (r) => compact(r.ops) },
          { h: 'Fees (XLM)', cls: 'num', v: (r) => amount(r.fees_xlm, 3) },
        ], d.active, { rowHref: href }), { flush: true, hint: 'Transactions as source' })}
        ${card('Top fee payers', table([
          { h: 'Address', v: who },
          { h: 'Fees (XLM)', cls: 'num', v: (r) => html`<b>${amount(r.fees_xlm, 3)}</b>` },
          { h: 'Txs', cls: 'num', v: (r) => num(r.txs) },
          { h: 'Max fee', cls: 'num', v: (r) => amount(r.max_fee_xlm, 4) },
        ], d.fee_payers, { rowHref: href }), { flush: true })}
      </div>
        ${card('Top pools', table([
          { h: 'Pool', v: (r) => html`<span class="rank">${r._rank}</span> ${r.venue === 'sdex' ? html`<span class="muted">Offer ${r.pool}</span>` : addr(r.pool)}` },
          { h: 'Venue', v: (r) => venue(r.venue) },
          { h: 'Pair', v: (r) => pairLink(r.base, r.counter) },
          { h: 'Volume (XLM)', cls: 'num', v: (r) => compact(r.volume_xlm) },
          { h: 'Fills', cls: 'num', v: (r) => num(r.trades) },
        ], d.pools, { rowHref: (r) => r.pool[0] === 'C' ? `#/contract/${r.pool}` : `#/pair/${encodeURIComponent(r.base)}/${encodeURIComponent(r.counter)}` }), { flush: true })}
      <div class="grid g2">
        ${card('DeFi power users', table([
          { h: 'Address', v: who },
          { h: 'Events', cls: 'num', v: (r) => html`<b>${num(r.events)}</b>` },
          { h: 'Txs', cls: 'num', v: (r) => num(r.txs) },
          { h: 'Protocols', v: (r) => r.protocols.map((p) => protoName(p)).join(', ') },
        ], d.protocol_users, { rowHref: href }), { flush: true })}
        ${card('Whales', table([
        { h: 'Address', v: who },
        { h: 'XLM', cls: 'num', v: (r) => html`<b>${amount(r.xlm, 0)}</b>` },
        { h: 'Home domain', v: (r) => r.home_domain || html`<span class="muted">—</span>` },
        { h: 'Subentries', cls: 'num', v: (r) => num(r.num_subentries) },
      ], d.whales, { rowHref: href }), { flush: true, hint: 'Largest indexed XLM balances' })}
      </div>
      </div>`;
  });
  run();
  bindSeg(view, 'hours', (h) => { hours = h; ctx.setQuery({ hours }); run(); });
}
