// Human-readable one-line summaries of operation details (raw stroop amounts).
import { html, asset, addr, amt7, price, tag, snake } from './ui.js';

const A = (v, a) => html`<b>${amt7(v)}</b> ${asset(a)}`;

export function opSummary(type, d = {}) {
  switch (type) {
    case 'payment':
      return html`${A(d.amount, d.asset)} <span class="arrow" aria-hidden="true">→</span> ${addr(d.to)}`;
    case 'path_payment_strict_send':
      return html`send ${A(d.send_amount, d.send_asset)} → ${addr(d.to)} for ≥ ${A(d.dest_min, d.dest_asset)}${path(d.path)}`;
    case 'path_payment_strict_receive':
      return html`send ≤ ${A(d.send_max, d.send_asset)} → ${addr(d.to)} to deliver ${A(d.dest_amount, d.dest_asset)}${path(d.path)}`;
    case 'manage_sell_offer':
    case 'create_passive_sell_offer':
      return +d.amount === 0 ? html`cancel offer ${tag(d.offer_id)}` : html`sell ${A(d.amount, d.selling)} for ${asset(d.buying)} @ ${price(d.price)}${d.offer_id ? html`, offer ${tag(d.offer_id)}` : ''}`;
    case 'manage_buy_offer':
      return +d.amount === 0 ? html`cancel offer ${tag(d.offer_id)}` : html`buy ${A(d.amount, d.buying)} with ${asset(d.selling)} @ ${price(d.price)}${d.offer_id ? html`, offer ${tag(d.offer_id)}` : ''}`;
    case 'invoke_host_function':
      if (d.contract) return html`${addr(d.contract)}.<b>${d.function || '?'}</b>(${(d.args || []).slice(0, 4).map((a, i) => html`${i ? ', ' : ''}<span class="mono">${argText(a)}</span>`)}${(d.args || []).length > 4 ? ', …' : ''})`;
      return html`${tag(d.host_function || 'host function')}`;
    case 'change_trust':
      return d.limit === 0 || d.limit === '0' ? html`remove trustline ${asset(d.asset)}` : html`trust ${asset(d.asset)}`;
    case 'create_account':
      return html`create ${addr(d.destination)} with ${A(d.starting_balance, 'native')}`;
    case 'account_merge':
      return html`merge into ${addr(d.destination || d.to)}`;
    default: {
      const keys = Object.keys(d).slice(0, 3);
      return keys.length ? html`<span class="muted">${keys.map((k, i) => html`${i ? ', ' : ''}${snake(k)}: ${argText(d[k])}`)}</span>` : '';
    }
  }
}

function path(p) {
  return p?.length ? html` <span class="muted">via</span> ${p.map((a) => asset(a))}` : '';
}

function argText(v) {
  if (v === null || v === undefined) return 'null';
  if (typeof v === 'string') return /^[GC][A-Z2-7]{55}$/.test(v) ? `${v.slice(0, 4)}…${v.slice(-4)}` : v.length > 24 ? v.slice(0, 22) + '…' : v;
  const s = JSON.stringify(v);
  return s.length > 28 ? s.slice(0, 26) + '…' : s;
}
