import { get } from '../api.js';
import { html, setHTML, pageHead, table, compact, pct, ago, addr, icon, toast, empty, raw } from '../ui.js';
import { watchlist, addWatch, toggleWatch, chip } from '../tags.js';

const usd = (v) => v === null || v === undefined ? '—' : '$' + compact(v);

export default async function watch(view) {
  setHTML(view, html`
    ${pageHead('Watchlist', { sub: 'Addresses you follow, with live net worth, tags and last activity. The list is stored in this browser; labels are shared through the API.' })}
    <form class="toolbar" id="add"><input name="a" placeholder="Add G… or C… address" style="flex:1;max-width:480px" pattern="[GCM][A-Z2-7]{55,68}" required>
      <button class="btn primary">${icon('check')}Add</button><span class="grow"></span>
      <input name="tag" placeholder="import by #tag" style="width:160px"><button class="btn" name="imp" value="1" formnovalidate>Import</button></form>
    <div class="card"><div class="card-body flush" id="list"></div></div>`);
  const listEl = view.querySelector('#list');
  const draw = async () => {
    const list = watchlist();
    if (!list.length) { setHTML(listEl, empty('Your watchlist is empty', 'Add an address above, use ☆ Watch on any address page, or import everyone carrying one of your #tags.', 'user')); return; }
    setHTML(listEl, html`<div class="skel-rows">${list.map(() => html`<div class="skel"></div>`)}</div>`);
    const rows = await Promise.all(list.map(async (a) => {
      const [pf, tags, st] = await Promise.all([
        get(`/accounts/${a}/portfolio?history=false`).catch(() => null),
        get(`/accounts/${a}/tags`).catch(() => []),
        get(`/accounts/${a}/stats?hours=24`).catch(() => null),
      ]);
      return { a, pf, tags, st };
    }));
    setHTML(listEl, table([
      { h: 'Address', v: (r) => addr(r.a) },
      { h: 'Tags', v: (r) => html`<div class="chips">${r.tags.slice(0, 4).map((t) => chip(t))}${r.tags.length > 4 ? html`<span class="muted">+${r.tags.length - 4}</span>` : ''}</div>` },
      { h: 'Net worth', cls: 'num', v: (r) => r.pf ? (r.pf.value_usd !== null ? usd(r.pf.value_usd) : `${compact(r.pf.value_xlm)} XLM`) : '—' },
      { h: '24h', cls: 'num', v: (r) => r.pf?.change_24h_pct !== null && r.pf?.change_24h_pct !== undefined ? html`<span class="${r.pf.change_24h_pct > 0 ? 'up' : r.pf.change_24h_pct < 0 ? 'down' : 'muted'}">${pct(r.pf.change_24h_pct, 2, true)}</span>` : '—' },
      { h: 'Volume 24h', cls: 'num', v: (r) => r.st ? `${compact(r.st.trading.volume_xlm)} XLM` : '—' },
      { h: 'Last seen', v: (r) => r.st?.seen?.last_seen ? ago(r.st.seen.last_seen) : '—' },
      { h: '', v: (r) => html`<a class="btn sm" href="#/compare?a=${r.a}">compare</a> <button class="btn sm ghost" data-rm="${r.a}" title="Remove">✕</button>` },
    ], rows, { rowHref: (r) => `#/account/${r.a}` }));
    listEl.querySelectorAll('[data-rm]').forEach((b) => b.addEventListener('click', () => { toggleWatch(b.dataset.rm); draw(); }));
  };
  view.querySelector('#add').addEventListener('submit', async (e) => {
    e.preventDefault();
    const d = new FormData(e.target);
    if (e.submitter?.name === 'imp') {
      const t = (d.get('tag') || '').replace(/^#/, '').trim().toLowerCase();
      if (!t) return;
      try {
        const ls = await get(`/labels?tag=${encodeURIComponent(t)}`);
        addWatch(ls.map((l) => l.address));
        toast(`Imported ${ls.length} address(es) tagged #${t}`);
      } catch (err) { toast(err.message); }
    } else {
      addWatch([(d.get('a') || '').trim()]);
      e.target.reset();
    }
    draw();
  });
  draw();
}
