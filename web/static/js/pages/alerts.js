import { get, qs } from '../api.js';
import { html, setHTML, load, pageHead, seg, bindSeg, num, compact, amount, pct, when, addr, txLink, asset, pairLink, icon, raw, empty, card, VENUE_LABEL } from '../ui.js';

export const KINDS = {
  large_transfer: ['Large transfer', 'arrows', 'var(--s1)'],
  large_trade: ['Large swap', 'swap', 'var(--s2)'],
  flash_loan: ['Flash loan', 'bolt', 'var(--s7)'],
  arbitrage: ['Arbitrage', 'refresh', 'var(--s3)'],
  price_move: ['Price move', 'chart', 'var(--s4)'],
  new_asset: ['New asset', 'coin', 'var(--s5)'],
};

// alertRow renders one notable event as a sentence.
export function alertRow(a) {
  const [label, ic, color] = KINDS[a.kind] || [a.kind, 'info', 'var(--muted)'];
  let title, meta = [];
  switch (a.kind) {
    case 'large_transfer':
      title = html`${a.action === 'transfer' ? 'Moved' : a.action === 'mint' ? 'Minted' : a.action === 'burn' ? 'Burned' : a.action} <b>${amount(a.amount)}</b> ${asset(a.asset)}`;
      meta = [html`<span class="sor-flow">${addr(a.from_addr)}<span class="arrow sor-arrow" aria-hidden="true">→</span>${addr(a.to_addr)}</span>`]; break;
    case 'large_trade':
      title = html`Swapped <b>${amount(a.amount)}</b> ${asset(a.asset)} <span class="arrow sor-arrow" aria-hidden="true">→</span> <b>${amount(a.amount2)}</b> ${asset(a.asset2)}`;
      meta = [html`on ${VENUE_LABEL[a.action] || a.action}`, html`by ${addr(a.from_addr)}`]; break;
    case 'flash_loan':
      title = html`Flash loan of <b>${amount(a.amount)}</b> ${asset(a.asset)}`;
      meta = [html`by ${addr(a.from_addr)}`, html`pool ${addr(a.to_addr)}`]; break;
    case 'arbitrage':
      title = html`Round trip netted <b class="up">+${amount(a.amount)}</b> ${asset(a.asset)}`;
      meta = [a.action, html`by ${addr(a.from_addr)}`]; break;
    case 'price_move':
      title = html`${pairLink(a.asset, a.asset2)} moved <b class="${a.change_pct > 0 ? 'up' : 'down'}">${pct(a.change_pct, 1, true)}</b> in the last hour`;
      meta = [`${num(a.trades)} fills`]; break;
    case 'new_asset':
      title = html`New asset ${asset(a.asset, { issuer: true })}`;
      meta = [html`${num(a.trades)} fills since`, a.to_addr ? html`SAC ${addr(a.to_addr)}` : '']; break;
    default:
      title = a.kind;
  }
  return html`<div class="alert-row sor-arow"><div class="ic" style="background:${raw(color)}">${icon(ic)}</div>
    <div><div class="t">${title}</div><div class="m"><span class="badge sor-akind">${label}</span>${when(a.closed_at)}${meta.map((m) => html`<span>${m}</span>`)}${a.tx_hash ? html`<span>${txLink(a.tx_hash)}</span>` : ''}</div></div>
    <div class="v">${a.value_xlm ? html`<b>${compact(a.value_xlm)}</b> <span class="muted">XLM</span>` : ''}</div></div>`;
}

export default async function alerts(view, _p, query, ctx) {
  let hours = query.hours || '24', kind = query.kind || '', minXLM = query.min_xlm || '25000';
  setHTML(view, html`
    ${pageHead('Notable events', {
      sub: 'Alerts mined from the index: large transfers and swaps (valued in XLM), Blend flash loans, profitable same-transaction arbitrage, sharp hourly price moves on liquid XLM pairs, and assets first seen at least a day after indexing began. Refreshes every 30 s.',
      actions: seg('hours', [['1', '1h'], ['24', '24h'], ['168', '7d']], hours),
    })}
    <section class="card" aria-label="Notable events">
      <div class="sor-filters sor-afilters"><span id="kinds"></span>
        <label class="sor-min">Min value <input id="min" name="min_xlm" type="number" inputmode="numeric" min="1" step="1000" value="${minXLM}"> <span class="muted">XLM</span></label></div>
      <div class="card-body flush" id="feed"></div>
    </section>`);
  const feedEl = view.querySelector('#feed');
  const run = (quiet) => load(feedEl, () => get('/alerts' + qs({ hours, min_xlm: minXLM, limit: 300 })), (d) => {
    setHTML(view.querySelector('#kinds'), seg('kind', [['', html`All <span class="sor-count">${num(d.alerts.length)}</span>`], ...Object.entries(KINDS).map(([k, [l]]) => [k, html`${l} <span class="sor-count">${d.counts[k] ?? 0}</span>`])], kind));
    bindSeg(view.querySelector('#kinds'), 'kind', (k) => { kind = k; save(); run(false); });
    const list = d.alerts.filter((a) => !kind || a.kind === kind);
    return list.length ? html`${list.map(alertRow)}` : empty('Nothing notable', 'No events cross the thresholds in this window. Lower the minimum value or widen the window.');
  }, { quiet });
  const save = () => ctx.setQuery({ hours, kind, min_xlm: minXLM });
  run(false);
  bindSeg(view, 'hours', (h) => { hours = h; save(); run(false); });
  view.querySelector('#min').addEventListener('change', (e) => { minXLM = e.target.value; save(); run(false); });
  ctx.every(30000, () => run(true));
}
