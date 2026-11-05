// Dependency-free SVG charts: candlesticks (+volume pane), stacked columns
// and line/area. All are responsive (ResizeObserver) and ship a hover layer
// (crosshair + tooltip). Colors come from CSS custom properties.

import { esc, compact, price as fmtPrice } from './ui.js';

const observers = new Set();
// disposeCharts disconnects resize observers when the view changes.
export function disposeCharts() { observers.forEach((o) => o.disconnect()); observers.clear(); }

function mount(el, height, draw) {
  el.classList.add('chart');
  el.style.height = height + 'px';
  let w = 0;
  if (el._ro) { el._ro.disconnect(); observers.delete(el._ro); }
  const ro = el._ro = new ResizeObserver(() => {
    const nw = Math.floor(el.clientWidth);
    if (nw && nw !== w) { w = nw; draw(w); }
  });
  ro.observe(el);
  observers.add(ro);
}

export function niceTicks(min, max, count = 5) {
  if (!isFinite(min) || !isFinite(max)) return [];
  if (min === max) { const d = Math.abs(min) * 0.05 || 1; min -= d; max += d; }
  const span = max - min;
  const step0 = Math.pow(10, Math.floor(Math.log10(span / count)));
  const err = (span / count) / step0;
  const step = step0 * (err >= 7.5 ? 10 : err >= 3.5 ? 5 : err >= 1.5 ? 2 : 1);
  const out = [];
  for (let v = Math.ceil(min / step) * step; v <= max + step * 1e-9; v += step) out.push(+v.toPrecision(12));
  return out;
}

// coverTicks returns nice ticks whose last value is ≥ hi, so marks never
// overflow the plot.
function coverTicks(lo, hi, count) {
  const t = niceTicks(lo, hi, count);
  if (t.length > 1 && t[t.length - 1] < hi) t.push(+(t[t.length - 1] + (t[1] - t[0])).toPrecision(12));
  return t;
}

const MIN = 60e3, H = 3600e3, D = 86400e3;
const TIME_STEPS = [MIN, 5 * MIN, 15 * MIN, 30 * MIN, H, 3 * H, 6 * H, 12 * H, D, 2 * D, 7 * D];

function timeLabel(t, step) {
  const d = new Date(t);
  if (step >= D) return d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' });
  const hm = d.toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit' });
  return d.getHours() === 0 && d.getMinutes() === 0 ? d.toLocaleDateString('en-US', { month: 'short', day: 'numeric' }) : hm;
}

// timeTicks picks evenly spaced, round timestamps over [t0, t1].
function timeTicks(t0, t1, width) {
  const want = Math.max(2, Math.floor(width / 90));
  const step = TIME_STEPS.find((s) => (t1 - t0) / s <= want) || TIME_STEPS[TIME_STEPS.length - 1];
  const tz = new Date().getTimezoneOffset() * MIN;
  const out = [];
  for (let t = Math.ceil((t0 - tz) / step) * step + tz; t <= t1; t += step) out.push(t);
  return { ticks: out, step };
}

function tipHTML(title, rows) {
  return `<div class="t">${esc(title)}</div>` + rows.map(([k, v, color]) =>
    `<div class="r"><span class="k">${color ? `<span class="sw" style="background:${color}"></span>` : ''}${esc(k)}</span><b>${esc(v)}</b></div>`).join('');
}

function placeTip(tip, el, x, y) {
  const w = tip.offsetWidth, h = tip.offsetHeight, W = el.clientWidth;
  let left = x + 14;
  if (left + w > W) left = x - w - 14;
  tip.style.left = Math.max(0, left) + 'px';
  tip.style.top = Math.max(0, Math.min(y - h / 2, el.clientHeight - h)) + 'px';
}

const fmtTime = (t, withSec = false) => new Date(t).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', ...(withSec ? { second: '2-digit' } : {}), hour12: false });

function noData(el, msg) {
  if (el._ro) { el._ro.disconnect(); observers.delete(el._ro); el._ro = null; }
  el.style.height = '';
  el.innerHTML = `<div class="chart-empty" style="min-height:160px">${esc(msg)}</div>`;
}

const colPath = (x, y, w, h, r) => {
  r = Math.min(r, w / 2, h);
  if (h <= 0) return '';
  return `M${x},${y + h}V${y + r}Q${x},${y} ${x + r},${y}H${x + w - r}Q${x + w},${y} ${x + w},${y + r}V${y + h}Z`;
};

// ---- candlesticks -----------------------------------------------------------

// candles draws OHLC candles (ascending by t) with a volume pane below.
// opts: {height, mode: 'candles'|'line', volumeLabel, priceFmt}
export function candles(el, data, opts = {}) {
  const height = opts.height || 420;
  const pf = opts.priceFmt || fmtPrice;
  if (!data.length) return noData(el, opts.emptyText || 'No trades in this range');
  mount(el, height, (W) => {
    const padL = 8, padR = 72, padT = 12, axisH = 22, volH = Math.round((height - axisH) * 0.2), gap = 10;
    const priceH = height - axisH - volH - gap - padT;
    const n = data.length;
    const plotW = W - padL - padR;
    const band = plotW / n;
    const bw = Math.max(1, Math.min(14, band * 0.68));
    const lo = Math.min(...data.map((d) => d.low)), hi = Math.max(...data.map((d) => d.high));
    const pad = (hi - lo) * 0.08 || Math.abs(hi) * 0.02 || 1;
    const yMin = lo - pad, yMax = hi + pad;
    const y = (v) => padT + (yMax - v) / (yMax - yMin) * priceH;
    const vMax = Math.max(...data.map((d) => d.volume || 0), 1e-12);
    const volTop = padT + priceH + gap;
    const vy = (v) => volTop + volH - (v / vMax) * volH;
    const x = (i) => padL + band * i + band / 2;

    let s = `<svg viewBox="0 0 ${W} ${height}" height="${height}" role="img" aria-label="Price chart">`;
    const yt = niceTicks(yMin, yMax, Math.max(3, Math.round(priceH / 60)));
    const lastY = y(data[n - 1].close);
    for (const v of yt) {
      if (v < yMin || v > yMax) continue;
      s += `<line class="gridline" x1="${padL}" x2="${W - padR}" y1="${y(v)}" y2="${y(v)}"/>`;
      if (Math.abs(y(v) - lastY) > 13) s += `<text x="${W - padR + 8}" y="${y(v) + 4}">${esc(pf(v))}</text>`; // keep clear of the last-price tag
    }
    s += `<line class="baseline" x1="${padL}" x2="${W - padR}" y1="${volTop + volH}" y2="${volTop + volH}"/>`;
    s += `<text x="${W - padR + 8}" y="${volTop + 10}">${esc(compact(vMax))}</text>`;
    // x labels on candle indices, spaced ≥ 90px
    const every = Math.max(1, Math.ceil(90 / band));
    const spanMs = n > 1 ? data[n - 1].t - data[0].t : 0;
    for (let i = 0; i < n; i += every) {
      s += `<text x="${x(i)}" y="${height - 6}" text-anchor="middle">${esc(timeLabel(data[i].t, spanMs > 2 * D ? D : MIN))}</text>`;
    }
    if (opts.mode === 'line') {
      const pts = data.map((d, i) => `${x(i)},${y(d.close)}`).join(' ');
      s += `<polygon points="${x(0)},${padT + priceH} ${pts} ${x(n - 1)},${padT + priceH}" fill="var(--s1)" opacity="0.10"/>`;
      s += `<polyline points="${pts}" fill="none" stroke="var(--s1)" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>`;
    } else {
      for (let i = 0; i < n; i++) {
        const d = data[i];
        const up = d.close >= d.open;
        const c = d.close === d.open ? 'var(--ink-2)' : up ? 'var(--up)' : 'var(--down)';
        const top = y(Math.max(d.open, d.close)), bot = y(Math.min(d.open, d.close));
        s += `<line x1="${x(i)}" x2="${x(i)}" y1="${y(d.high)}" y2="${y(d.low)}" stroke="${c}" stroke-width="1"/>`;
        s += `<rect x="${x(i) - bw / 2}" y="${top}" width="${bw}" height="${Math.max(1, bot - top)}" fill="${c}" rx="${bw > 6 ? 1.5 : 0}"/>`;
      }
    }
    for (let i = 0; i < n; i++) {
      const d = data[i];
      const c = opts.mode === 'line' ? 'var(--s1)' : d.close >= d.open ? 'var(--up)' : 'var(--down)';
      const top = vy(d.volume || 0);
      s += `<path d="${colPath(x(i) - bw / 2, top, bw, volTop + volH - top, Math.min(2, bw / 2))}" fill="${c}" opacity="0.45"/>`;
    }
    const last = data[n - 1];
    const lastC = last.close >= last.open ? 'var(--up)' : 'var(--down)';
    s += `<line class="lastline" x1="${padL}" x2="${W - padR}" y1="${y(last.close)}" y2="${y(last.close)}" stroke="${lastC}"/>`;
    s += `<g class="pricetag"><rect x="${W - padR + 2}" y="${y(last.close) - 9}" width="${padR - 4}" height="18" fill="${lastC}"/><text x="${W - padR + 8}" y="${y(last.close) + 4}">${esc(pf(last.close))}</text></g>`;
    s += `<g class="hover" visibility="hidden"><line class="xhair vx" y1="${padT}" y2="${volTop + volH}"/><line class="xhair hy" x1="${padL}" x2="${W - padR}"/>
      <rect class="hb" x="${W - padR + 2}" width="${padR - 4}" height="18" rx="4" fill="var(--ink)"/><text class="ht" x="${W - padR + 8}" style="fill:var(--surface);font-weight:600"></text></g>`;
    s += `<rect class="hit" x="${padL}" y="0" width="${plotW}" height="${height}" fill="transparent"/></svg><div class="tip" hidden></div>`;
    el.innerHTML = s;

    const svg = el.querySelector('svg'), hov = svg.querySelector('.hover'), tip = el.querySelector('.tip');
    const vx = hov.querySelector('.vx'), hy = hov.querySelector('.hy'), hb = hov.querySelector('.hb'), ht = hov.querySelector('.ht');
    const move = (ev) => {
      const r = svg.getBoundingClientRect();
      const mx = ev.clientX - r.left, my = ev.clientY - r.top;
      const i = Math.max(0, Math.min(n - 1, Math.floor((mx - padL) / band)));
      const d = data[i];
      hov.setAttribute('visibility', 'visible');
      vx.setAttribute('x1', x(i)); vx.setAttribute('x2', x(i));
      if (my >= padT && my <= padT + priceH) {
        const pv = yMax - (my - padT) / priceH * (yMax - yMin);
        hy.setAttribute('y1', my); hy.setAttribute('y2', my); hy.style.display = '';
        hb.setAttribute('y', my - 9); hb.style.display = ''; ht.setAttribute('y', my + 4); ht.textContent = pf(pv); ht.style.display = '';
      } else { hy.style.display = hb.style.display = ht.style.display = 'none'; }
      const chg = d.open ? (d.close / d.open - 1) * 100 : 0;
      tip.hidden = false;
      tip.innerHTML = tipHTML(fmtTime(d.t), [
        ['Open', pf(d.open)], ['High', pf(d.high)], ['Low', pf(d.low)], ['Close', pf(d.close)],
        ['Change', (chg > 0 ? '+' : '') + chg.toFixed(2) + '%'],
        [opts.volumeLabel || 'Volume', compact(d.volume)], ['Trades', String(d.trades ?? '—')],
      ]);
      placeTip(tip, el, x(i), my);
    };
    const hit = svg.querySelector('.hit');
    hit.addEventListener('pointermove', move);
    hit.addEventListener('pointerleave', () => { hov.setAttribute('visibility', 'hidden'); tip.hidden = true; });
  });
}

// ---- stacked time columns ------------------------------------------------------

// columns draws stacked columns over time. data: [{t, ...}] ascending;
// series: [{key, label, color}]; opts: {height, fmt, bucketMs}
export function columns(el, data, series, opts = {}) {
  const height = opts.height || 220;
  const fmt = opts.fmt || compact;
  if (!data.length) return noData(el, opts.emptyText || 'No data in this window');
  mount(el, height, (W) => {
    const padL = 44, padR = 8, padT = 10, axisH = 22;
    const plotH = height - padT - axisH, plotW = W - padL - padR;
    const n = data.length, band = plotW / n;
    const bw = Math.max(1, Math.min(24, band - 2));
    const totals = data.map((d) => series.reduce((s, k) => s + (+d[k.key] || 0), 0));
    const yt = coverTicks(0, Math.max(...totals, 1), 4);
    const yMax = yt[yt.length - 1] || 1;
    const y = (v) => padT + plotH - (v / yMax) * plotH;
    let s = `<svg viewBox="0 0 ${W} ${height}" height="${height}" role="img">`;
    for (const v of yt) s += `<line class="${v === 0 ? 'baseline' : 'gridline'}" x1="${padL}" x2="${W - padR}" y1="${y(v)}" y2="${y(v)}"/><text x="${padL - 8}" y="${y(v) + 4}" text-anchor="end">${esc(compact(v))}</text>`;
    const t0 = data[0].t, t1 = data[n - 1].t;
    const { ticks, step } = timeTicks(t0, t1, plotW);
    const xOfT = (t) => padL + (n > 1 ? (t - t0) / (t1 - t0) * (plotW - band) : 0) + band / 2;
    for (const t of ticks) s += `<text x="${xOfT(t)}" y="${height - 6}" text-anchor="middle">${esc(timeLabel(t, step))}</text>`;
    s += `<rect class="hl" y="${padT}" height="${plotH}" width="${band}" fill="var(--ink)" opacity="0" />`;
    for (let i = 0; i < n; i++) {
      const cx = xOfT(data[i].t);
      let acc = 0;
      const segs = series.map((k) => ({ k, v: +data[i][k.key] || 0 })).filter((p) => p.v > 0);
      segs.forEach((p, j) => {
        const yTop = y(acc + p.v), yBot = y(acc);
        const isTop = j === segs.length - 1;
        const h = Math.max(0, yBot - yTop - (j > 0 ? 2 : 0)); // 2px surface gap between stacked segments
        s += isTop
          ? `<path d="${colPath(cx - bw / 2, yTop, bw, h, 4)}" fill="${p.k.color}"/>`
          : `<rect x="${cx - bw / 2}" y="${yTop}" width="${bw}" height="${h}" fill="${p.k.color}"/>`;
        acc += p.v;
      });
    }
    s += `<rect class="hit" x="${padL}" y="0" width="${plotW}" height="${height}" fill="transparent"/></svg><div class="tip" hidden></div>`;
    el.innerHTML = s;
    const svg = el.querySelector('svg'), tip = el.querySelector('.tip'), hl = svg.querySelector('.hl');
    const hit = svg.querySelector('.hit');
    hit.addEventListener('pointermove', (ev) => {
      const r = svg.getBoundingClientRect();
      const mx = ev.clientX - r.left;
      const i = Math.max(0, Math.min(n - 1, Math.round((mx - padL - band / 2) / ((plotW - band) / Math.max(1, n - 1)))));
      const d = data[i];
      hl.setAttribute('x', xOfT(d.t) - band / 2); hl.setAttribute('opacity', '0.05');
      tip.hidden = false;
      const rows = series.map((k) => [k.label, fmt(d[k.key] || 0), k.color]);
      if (series.length > 1) rows.push(['Total', fmt(totals[i])]);
      (opts.extra || []).forEach(([label, f]) => rows.push([label, f(d)]));
      tip.innerHTML = tipHTML(fmtTime(d.t), rows);
      placeTip(tip, el, xOfT(d.t), ev.clientY - r.top);
    });
    hit.addEventListener('pointerleave', () => { tip.hidden = true; hl.setAttribute('opacity', '0'); });
  });
}

// ---- line / area --------------------------------------------------------------

// line draws one or more series over time with a crosshair. data ascending.
// series: [{key, label, color, area}]; opts: {height, fmt, yMin}
export function line(el, data, series, opts = {}) {
  const height = opts.height || 200;
  const fmt = opts.fmt || compact;
  if (!data.length) return noData(el, opts.emptyText || 'No data in this window');
  mount(el, height, (W) => {
    const padL = 48, padR = 12, padT = 10, axisH = 22;
    const plotH = height - padT - axisH, plotW = W - padL - padR;
    const n = data.length;
    const vals = data.flatMap((d) => series.map((k) => +d[k.key]).filter((v) => isFinite(v)));
    const lo = opts.yMin ?? Math.min(0, ...vals), hi = Math.max(...vals, lo + 1e-9);
    const yt = coverTicks(lo, hi, 4);
    const yMin = Math.min(lo, yt[0]), yMax = Math.max(hi, yt[yt.length - 1]);
    const y = (v) => padT + plotH - (v - yMin) / (yMax - yMin || 1) * plotH;
    const t0 = data[0].t, t1 = data[n - 1].t;
    const x = (t) => padL + (n > 1 ? (t - t0) / (t1 - t0) : 0.5) * plotW;
    let s = `<svg viewBox="0 0 ${W} ${height}" height="${height}" role="img">`;
    for (const v of yt) s += `<line class="${v === 0 ? 'baseline' : 'gridline'}" x1="${padL}" x2="${W - padR}" y1="${y(v)}" y2="${y(v)}"/><text x="${padL - 8}" y="${y(v) + 4}" text-anchor="end">${esc(fmt(v))}</text>`;
    const { ticks, step } = timeTicks(t0, t1, plotW);
    for (const t of ticks) s += `<text x="${x(t)}" y="${height - 6}" text-anchor="middle">${esc(timeLabel(t, step))}</text>`;
    for (const k of series) {
      const pts = data.filter((d) => isFinite(+d[k.key])).map((d) => `${x(d.t)},${y(+d[k.key])}`).join(' ');
      if (k.area) s += `<polygon points="${x(t0)},${y(Math.max(0, yMin))} ${pts} ${x(t1)},${y(Math.max(0, yMin))}" fill="${k.color}" opacity="0.10"/>`;
      s += `<polyline points="${pts}" fill="none" stroke="${k.color}" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>`;
    }
    s += `<g class="hover" visibility="hidden"><line class="xhair vx" y1="${padT}" y2="${padT + plotH}"/>${series.map((k, i) => `<circle class="dot${i}" r="4" fill="${k.color}" stroke="var(--surface)" stroke-width="2"/>`).join('')}</g>`;
    s += `<rect class="hit" x="${padL}" y="0" width="${plotW}" height="${height}" fill="transparent"/></svg><div class="tip" hidden></div>`;
    el.innerHTML = s;
    const svg = el.querySelector('svg'), tip = el.querySelector('.tip'), hov = svg.querySelector('.hover');
    const hit = svg.querySelector('.hit');
    hit.addEventListener('pointermove', (ev) => {
      const r = svg.getBoundingClientRect();
      const mx = ev.clientX - r.left;
      const tm = t0 + (mx - padL) / plotW * (t1 - t0);
      let i = 0, best = Infinity;
      data.forEach((d, j) => { const dd = Math.abs(d.t - tm); if (dd < best) { best = dd; i = j; } });
      const d = data[i];
      hov.setAttribute('visibility', 'visible');
      const vx = hov.querySelector('.vx'); vx.setAttribute('x1', x(d.t)); vx.setAttribute('x2', x(d.t));
      series.forEach((k, j) => { const c = hov.querySelector('.dot' + j); c.setAttribute('cx', x(d.t)); c.setAttribute('cy', y(+d[k.key] || 0)); });
      tip.hidden = false;
      tip.innerHTML = tipHTML(fmtTime(d.t), series.map((k) => [k.label, fmt(d[k.key]), k.color]));
      placeTip(tip, el, x(d.t), ev.clientY - r.top);
    });
    hit.addEventListener('pointerleave', () => { tip.hidden = true; hov.setAttribute('visibility', 'hidden'); });
  });
}

// sparkline returns an inline SVG string (no hover; used inside stat tiles).
export function sparkline(values, color = 'var(--s1)', h = 28) {
  const v = values.filter((x) => isFinite(+x)).map(Number);
  if (v.length < 2) return '';
  const lo = Math.min(...v), hi = Math.max(...v), w = 120;
  const pts = v.map((x, i) => `${(i / (v.length - 1)) * w},${h - 2 - ((x - lo) / (hi - lo || 1)) * (h - 4)}`).join(' ');
  return `<svg viewBox="0 0 ${w} ${h}" preserveAspectRatio="none" width="100%" height="${h}" aria-hidden="true"><polyline points="${pts}" fill="none" stroke="${color}" stroke-width="1.5" vector-effect="non-scaling-stroke" stroke-linejoin="round"/></svg>`;
}

// ---- donut ----------------------------------------------------------------------

// donut draws part-to-whole slices [{label, value, color}] with a center total.
export function donut(el, slices, { size = 220, center = '', sub = '', fmt = compact } = {}) {
  const total = slices.reduce((s, x) => s + Math.max(0, x.value), 0);
  if (!total) return noData(el, 'Nothing to chart');
  el.classList.add('chart');
  const r = size / 2, ri = r * 0.62;
  let a0 = -Math.PI / 2;
  const pt = (a, rad) => `${r + rad * Math.cos(a)},${r + rad * Math.sin(a)}`;
  let s = `<svg viewBox="0 0 ${size} ${size}" width="${size}" height="${size}" style="max-width:100%;display:block;margin:0 auto" role="img" aria-label="Allocation">`;
  slices.forEach((x, i) => {
    const frac = Math.max(0, x.value) / total;
    if (frac <= 0) return;
    const a1 = a0 + frac * Math.PI * 2;
    const large = a1 - a0 > Math.PI ? 1 : 0;
    const d = frac >= 0.9999
      ? `M${pt(-Math.PI / 2, r)}A${r},${r} 0 1 1 ${pt(Math.PI * 1.5 - 0.0001, r)}L${pt(Math.PI * 1.5 - 0.0001, ri)}A${ri},${ri} 0 1 0 ${pt(-Math.PI / 2, ri)}Z`
      : `M${pt(a0, r)}A${r},${r} 0 ${large} 1 ${pt(a1, r)}L${pt(a1, ri)}A${ri},${ri} 0 ${large} 0 ${pt(a0, ri)}Z`;
    s += `<path class="slice" data-i="${i}" d="${d}" fill="${x.color}" stroke="var(--surface)" stroke-width="2"/>`;
    a0 = a1;
  });
  s += `<text class="c1" x="${r}" y="${r - 2}" text-anchor="middle" style="font-size:18px;font-weight:600;fill:var(--ink)">${esc(center)}</text>`;
  s += `<text class="c2" x="${r}" y="${r + 16}" text-anchor="middle">${esc(sub)}</text></svg>`;
  el.innerHTML = s;
  const c1 = el.querySelector('.c1'), c2 = el.querySelector('.c2');
  el.querySelectorAll('.slice').forEach((p) => {
    p.addEventListener('pointerenter', () => {
      const x = slices[+p.dataset.i];
      el.querySelectorAll('.slice').forEach((q) => q.style.opacity = q === p ? '1' : '0.45');
      c1.textContent = `${(x.value / total * 100).toFixed(1)}%`;
      c2.textContent = `${x.label} · ${fmt(x.value)}`;
    });
    p.addEventListener('pointerleave', () => {
      el.querySelectorAll('.slice').forEach((q) => q.style.opacity = '1');
      c1.textContent = center; c2.textContent = sub;
    });
  });
}

// ---- heatmap --------------------------------------------------------------------

const DOW = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
// Sequential blue ramp (light → dark); index 0 = empty.
const RAMP = ['var(--surface-2)', 'var(--h1)', 'var(--h2)', 'var(--h3)', 'var(--h4)', 'var(--h5)', 'var(--h6)'];

// heatmap draws a weekday × hour grid from cells [{dow 1-7, hour 0-23, txs}] (UTC).
export function heatmap(el, cells, { label = 'transactions' } = {}) {
  const grid = Array.from({ length: 7 }, () => Array(24).fill(0));
  cells.forEach((c) => { grid[c.dow - 1][c.hour] = +c.txs; });
  const max = Math.max(1, ...grid.flat());
  if (!cells.length) return noData(el, 'No activity in this window');
  const step = (v) => v === 0 ? 0 : 1 + Math.min(5, Math.floor(Math.log(v) / Math.log(max + 1) * 6));
  let h = `<div class="heat"><div></div>${Array.from({ length: 24 }, (_, i) => `<div class="hl">${i % 3 === 0 ? i : ''}</div>`).join('')}`;
  grid.forEach((row, d) => {
    h += `<div class="dl">${DOW[d]}</div>` + row.map((v, hr) => `<div class="hc" style="background:${RAMP[step(v)]}" data-tip="${DOW[d]} ${String(hr).padStart(2, '0')}:00 UTC · ${v} ${label}"></div>`).join('');
  });
  h += `</div><div class="legend" style="justify-content:flex-end">less ${RAMP.slice(1).map((c) => `<i class="sw" style="background:${c}"></i>`).join('')} more <span class="muted">· max ${max}/h · UTC</span></div>`;
  el.innerHTML = h;
}
