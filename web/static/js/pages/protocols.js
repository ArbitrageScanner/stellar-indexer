import { get, qs } from '../api.js';
import { html, setHTML, load, pageHead, seg, bindSeg, card, table, num, compact, venue, venueColor, stackbar, raw, empty, tile, logoMark } from '../ui.js';
import { PROTOCOLS, protoName } from '../protocols-meta.js';

export default async function protocols(view, _p, query, ctx) {
  let hours = query.hours || '24';
  setHTML(view, html`
    ${pageHead('Soroban protocols', {
      sub: 'DeFi activity decoded from contract events: DEX swaps and liquidity on Soroswap, Aquarius and CLMM pools, and lending on Blend. Event shapes were taken from live pubnet traffic.',
      actions: seg('hours', [['1', '1h'], ['24', '24h'], ['168', '7d']], hours),
    })}
    <div class="tiles" id="tiles"></div>
    <div class="sor-pgrid sor-gap" id="cards"></div>
    ${card('Trades by venue', html`<div id="venues"></div>`, { flush: true, hint: 'Classic venues included for comparison' })}`);
  const run = () => load(view.querySelector('#cards'), () => get('/protocols' + qs({ hours })), (d) => {
    const ps = d.protocols || [];
    setHTML(view.querySelector('#tiles'), html`
      ${tile('Protocol events', compact(ps.reduce((s, p) => s + p.events, 0)))}
      ${tile('Protocol txs', compact(ps.reduce((s, p) => s + p.txs, 0)))}
      ${tile('Distinct users', compact(ps.reduce((s, p) => s + p.users, 0)), { foot: 'Summed per protocol' })}
      ${tile('Contracts', num(ps.reduce((s, p) => s + p.contracts, 0)), { foot: 'Pools, pairs, routers, lending pools' })}`);
    setHTML(view.querySelector('#venues'), table([
      { h: 'Venue', v: (v) => venue(v.venue) },
      { h: 'Fills', cls: 'num', v: (v) => num(v.trades) },
      { h: 'Pools / makers', cls: 'num', v: (v) => num(v.pools) },
      { h: 'Takers', cls: 'num', v: (v) => num(v.takers) },
      { h: '', cls: 'num', v: (v) => html`<a href="#/trades?venue=${v.venue}">View trades</a>` },
    ], d.trade_venues || [], { emptyTitle: 'No trades in this window' }));
    if (!ps.length) return empty('No protocol events in this window', 'Protocol events are decoded from Soroban contract events. Try a longer window, or index pubnet where Aquarius, Soroswap and Blend are active.');
    return html`${ps.map((p) => {
      const actions = Object.entries(p.actions || {}).sort((a, b) => b[1] - a[1]);
      const top = actions.slice(0, 5), rest = actions.slice(5).reduce((s, [, n]) => s + n, 0);
      const slots = ['var(--s1)', 'var(--s2)', 'var(--s3)', 'var(--s4)', 'var(--s5)', 'var(--muted)'];
      const parts = top.map(([a, n], i) => ({ label: a, value: n, color: slots[i] }));
      if (rest) parts.push({ label: 'Other', value: rest, color: slots[5] });
      const m = PROTOCOLS[p.protocol];
      return html`<a class="card proto-card" href="#/protocol/${p.protocol}">
        <div class="hdr">${logoMark(p.protocol, 'logo logo-img') || html`<div class="logo" style="background:${raw(venueColor(p.protocol))}">${protoName(p.protocol)[0]}</div>`}
          <div><h2>${protoName(p.protocol)}</h2><div class="sor-meta"><span>${m?.kind || 'Protocol'}</span><span>${num(p.contracts)} contracts</span></div></div></div>
        ${m ? html`<p class="sor-about">${m.about}</p>` : ''}
        <div class="sor-nums"><div><b>${compact(p.events)}</b>Events</div><div><b>${compact(p.txs)}</b>Txs</div><div><b>${compact(p.users)}</b>Users</div></div>
        <div class="sor-mix">${stackbar(parts, { fmt: compact })}</div>
        ${p.unverified_events ? html`<div class="sor-note"><span class="badge warn">Unverified</span><span>${compact(p.unverified_events)} events from contracts whose code is not allowlisted (excluded above)</span></div>` : ''}
      </a>`;
    })}`;
  });
  run();
  bindSeg(view, 'hours', (h) => { hours = h; ctx.setQuery({ hours }); run(); });
}
