import { get, qs } from '../api.js';
import { html, setHTML, feed, pageHead, seg, bindSeg, when, venue, unverified, asset, amt7, price, addr, txLink, pairLink, VENUE_LABEL } from '../ui.js';

export default async function trades(view, _p, query, ctx) {
  let venueF = query.venue || '';
  setHTML(view, html`
    ${pageHead('Trades', { sub: 'Every fill across venues, newest first, from the taker’s point of view: what the taker sold and what they received. Pages back through history with keyset pagination.' })}
    <div class="toolbar">${seg('venue', [['', 'All venues'], ...Object.entries(VENUE_LABEL).filter(([k]) => k !== 'blend')], venueF)}
      <span class="grow"></span><input id="taker" type="search" placeholder="Filter by taker G… / C…" value="${query.taker || ''}" style="width:320px"></div>
    <div class="card"><div class="card-body flush" id="list"></div></div>`);
  const list = view.querySelector('#list');
  const run = () => {
    const taker = view.querySelector('#taker').value.trim();
    ctx.setQuery({ venue: venueF, taker });
    feed(list, {
      fetchPage: (before) => get('/trades' + qs({ venue: venueF, taker, before, limit: 50 })),
      cursor: (t) => `${t.ledger}.${t.tx_index}.${t.op_index}.${t.trade_index}`,
      cols: [
        { h: 'Time', v: (t) => when(t.closed_at) },
        { h: 'Venue', v: (t) => html`${venue(t.venue)}${unverified(t)}` },
        { h: 'Pair', v: (t) => pairLink(t.base, t.counter) },
        { h: 'Sold', cls: 'num', v: (t) => html`${amt7(t.sold_amount)} ${asset(t.sold_asset)}` },
        { h: 'Bought', cls: 'num', v: (t) => html`${amt7(t.bought_amount)} ${asset(t.bought_asset)}` },
        { h: 'Price', cls: 'num', v: (t) => price(t.price) },
        { h: 'Taker', v: (t) => addr(t.taker) },
        { h: 'Pool / maker', v: (t) => addr(t.pool || t.maker) },
        { h: 'Tx', v: (t) => txLink(t.tx_hash) },
      ],
      emptyTitle: 'No trades', emptyText: 'Nothing matches these filters in the indexed range.',
    });
  };
  run();
  bindSeg(view, 'venue', (v) => { venueF = v; run(); });
  view.querySelector('#taker').addEventListener('change', run);
}
