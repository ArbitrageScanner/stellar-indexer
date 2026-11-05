import { get, qs } from '../api.js';
import { html, setHTML, load, card, tile, pageHead, seg, bindSeg, table, num, compact, price, pairLink, venue, raw, parseAsset,
  stackbar, VENUE_LABEL, venueColor, empty } from '../ui.js';

export default async function markets(view, _p, query, ctx) {
  let hours = query.hours || '24';
  let filter = query.q || '';
  let venueF = query.venue || '';
  setHTML(view, html`
    ${pageHead('Markets', {
      sub: 'Every trading pair seen in the index. Prices are normalised to one canonical pair across SDEX, classic AMM pools and Soroban DEXs, so XLM/USDC from Aquarius and from the order book land on the same chart.',
      actions: seg('hours', [['1', '1h'], ['24', '24h'], ['168', '7d']], hours),
    })}
    <div class="tiles" id="tiles"></div>
    <div class="grid g-main" style="margin-bottom:16px">
      ${card('Open any pair', html`
        <form id="open" class="toolbar" style="margin:0">
          <input name="base" list="assets-dl" placeholder="Base: native, USDC:G…, or C… contract" style="flex:1;min-width:200px" required>
          <input name="counter" list="assets-dl" placeholder="Counter" style="flex:1;min-width:200px" required>
          <button class="btn primary">Open chart</button>
          <datalist id="assets-dl"></datalist>
        </form>
        <div class="muted" style="font-size:12.5px;margin-top:8px">Examples: <a href="#/pair/native/USDC:GA5ZSEJYB37JRC5AVCIA5MOP4RHTM335X2KGX3IHOJAPP5RE34K4KZVN">XLM / USDC</a> (pubnet) · <a href="#/pair/native/AQUA:GBNZILSTVQZ4R7IKQDGHYGY2QXL5QOFJYQMXPKWRRM5PAV7Y4M67AQUA">XLM / AQUA</a> (pubnet) · or pick from the table below.</div>`)}
      ${card('Trades by venue', html`<div id="venues"></div>`)}
    </div>
    <div class="card">
      <div class="card-head"><h2>Pairs</h2>
        <div class="toolbar" style="margin:0"><input type="search" id="filter" placeholder="Filter by asset code" value="${filter}"><span id="vseg"></span></div></div>
      <div class="card-body flush" id="pairs"></div>
    </div>`);

  get('/assets?hours=720&limit=200').then((as) => {
    setHTML(view.querySelector('#assets-dl'), html`${as.map((a) => html`<option value="${a.asset_key}">${a.code || a.asset_key}</option>`)}`);
  }).catch(() => {});
  view.querySelector('#open').addEventListener('submit', (e) => {
    e.preventDefault();
    const f = new FormData(e.target);
    location.hash = `/pair/${encodeURIComponent(f.get('base').trim())}/${encodeURIComponent(f.get('counter').trim())}`;
  });

  let rows = [];
  const draw = () => {
    const q = filter.trim().toUpperCase();
    const list = rows.filter((r) => (!q || parseAsset(r.base).code.toUpperCase().includes(q) || parseAsset(r.counter).code.toUpperCase().includes(q) || r.base.includes(filter) || r.counter.includes(filter))
      && (!venueF || r.venues.includes(venueF)));
    setHTML(view.querySelector('#pairs'), table([
      { h: 'Pair', v: (r) => pairLink(r.base, r.counter) },
      { h: 'Last price', cls: 'num', v: (r) => price(r.last_price) },
      { h: 'VWAP', cls: 'num', v: (r) => price(r.vwap) },
      { h: 'Trades', cls: 'num', v: (r) => num(r.trades) },
      { h: 'Base volume', cls: 'num', v: (r) => html`${compact(r.base_volume)} <span class="muted">${parseAsset(r.base).code}</span>` },
      { h: 'Counter volume', cls: 'num', v: (r) => html`${compact(r.counter_volume)} <span class="muted">${parseAsset(r.counter).code}</span>` },
      { h: 'Venues', v: (r) => raw(r.venues.map((v) => venue(v).s).join(' ')) },
    ], list.slice(0, 300), { rowHref: (r) => `#/pair/${encodeURIComponent(r.base)}/${encodeURIComponent(r.counter)}`, emptyTitle: rows.length ? 'No pair matches the filter' : 'No trades in this window', emptyText: rows.length ? '' : 'Try a longer window, or wait for the indexer to ingest DEX activity.' }));
  };

  const loadAll = () => {
    load(view.querySelector('#pairs'), () => get('/pairs' + qs({ hours, limit: 500 })), (r) => {
      rows = r;
      const trades = r.reduce((s, x) => s + x.trades, 0);
      const venues = new Set(r.flatMap((x) => x.venues));
      const assets = new Set(r.flatMap((x) => [x.base, x.counter]));
      setHTML(view.querySelector('#tiles'), html`
        ${tile('Active pairs', num(r.length), { foot: r.length === 500 ? 'top 500 by trades' : `in the last ${hours}h` })}
        ${tile('Fills', compact(trades), { foot: 'all venues', title: num(trades) })}
        ${tile('Assets traded', num(assets.size))}
        ${tile('Venues', num(venues.size), { foot: [...venues].map((v) => VENUE_LABEL[v] || v).join(', ') })}`);
      setHTML(view.querySelector('#vseg'), seg('venue', [['', 'All'], ...[...venues].map((v) => [v, VENUE_LABEL[v] || v])], venueF));
      bindSeg(view.querySelector('#vseg'), 'venue', (v) => { venueF = v; ctx.setQuery({ hours, q: filter, venue: venueF }); draw(); });
      return '';
    }, { after: draw });
    load(view.querySelector('#venues'), () => get('/protocols' + qs({ hours })), (d) => (d.trade_venues || []).length
      ? html`${stackbar(d.trade_venues.map((v) => ({ label: VENUE_LABEL[v.venue] || v.venue, value: v.trades, color: venueColor(v.venue) })))}
        <div class="table-wrap" style="margin-top:12px">${table([
          { h: 'Venue', v: (v) => venue(v.venue) }, { h: 'Fills', cls: 'num', v: (v) => num(v.trades) },
          { h: 'Pools', cls: 'num', v: (v) => num(v.pools) }, { h: 'Takers', cls: 'num', v: (v) => num(v.takers) },
        ], d.trade_venues, { rowHref: (v) => `#/trades?venue=${v.venue}` })}</div>`
      : empty('No trades in this window'));
  };
  loadAll();
  bindSeg(view, 'hours', (h) => { hours = h; ctx.setQuery({ hours, q: filter, venue: venueF }); loadAll(); });
  view.querySelector('#filter').addEventListener('input', (e) => { filter = e.target.value; ctx.setQuery({ hours, q: filter, venue: venueF }); draw(); });
}
