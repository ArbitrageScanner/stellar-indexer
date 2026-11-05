import { get, send } from '../api.js';
import { html, setHTML, load, pageHead, card, table, amount, when, addr, icon, toast, empty, raw } from '../ui.js';
import { AUTO_TAGS, chip, addWatch } from '../tags.js';

export default async function labels(view, _p, query, ctx) {
  let current = (query.tag || '').toLowerCase();
  setHTML(view, html`
    ${pageHead('Tags', { sub: 'Your shared address labels, stored in the index database (labels table) and visible to everyone using this API, plus the automatic tags the indexer derives from on-chain behaviour.' })}
    <div class="grid g-main">
      <div class="stack">
        ${card('Your labels', html`<div id="cloud"></div>`, { hint: 'click a tag to list its addresses' })}
        <div class="card"><div class="card-head"><h2 id="sel-title">All labelled addresses</h2><span id="sel-actions"></span></div><div class="card-body flush" id="sel"></div></div>
      </div>
      <div class="stack">
        ${card('Add a label', html`<form id="add" class="stack" style="gap:8px">
          <input name="address" placeholder="Address G… / C…" required pattern="[GCM][A-Z2-7]{55,68}">
          <input name="tag" placeholder="tag, e.g. exchange, team, scam" required pattern="#?[a-zA-Z0-9][a-zA-Z0-9_.\\-]{0,31}">
          <input name="note" placeholder="note (optional)" maxlength="280">
          <button class="btn primary">${icon('check')}Save label</button>
          <div class="muted" style="font-size:12px">If the server sets LABELS_TOKEN, enter it under settings (⚙) first.</div></form>`)}
        <div id="auto">${card('Automatic tags', html`<div class="stack" style="gap:10px">${AUTO_TAGS.map(([id, label, group, why]) => html`<div>${chip({ id, label, group, kind: 'auto', reason: why })}<div class="muted" style="font-size:12.5px;margin-top:4px">${why}</div></div>`)}</div>`, { hint: 'heuristics, recomputed on every view' })}</div>
      </div>
    </div>`);
  const cloud = view.querySelector('#cloud');
  const drawCloud = () => load(cloud, () => get('/labels/tags'), (ts) => ts.length
    ? html`<div class="chips">${ts.map((t) => html`<a class="chip user ${t.tag === current ? 'on' : ''}" href="#/tags?tag=${encodeURIComponent(t.tag)}" style="${raw(t.tag === current ? 'border-color:var(--accent);background:var(--accent-wash)' : '')}"><span class="sw" style="background:var(--accent)"></span>#${t.tag} <span class="muted">${t.addresses}</span></a>`)}</div>`
    : empty('No labels yet', 'Add one here or from any address page.'));
  const drawSel = () => {
    setHTML(view.querySelector('#sel-title'), current ? html`#${current}` : html`All labelled addresses`);
    setHTML(view.querySelector('#sel-actions'), current ? html`<button class="btn sm" id="watch-all">☆ Watch all</button> <a class="btn sm ghost" href="#/tags">clear</a>` : '');
    load(view.querySelector('#sel'), () => get('/labels' + (current ? `?tag=${encodeURIComponent(current)}` : '')), (rows) => table([
      { h: 'Address', v: (r) => addr(r.address) },
      { h: 'Tag', v: (r) => chip({ id: r.tag, label: r.tag, kind: 'user', group: 'user', reason: r.note }) },
      { h: 'Note', v: (r) => r.note || html`<span class="muted">—</span>` },
      { h: 'XLM', cls: 'num', v: (r) => r.xlm !== null ? amount(r.xlm, 0) : '—' },
      { h: 'Added', v: (r) => when(r.created_at) },
      { h: '', v: (r) => html`<button class="btn sm ghost" data-del="${r.address}|${r.tag}" title="Delete">✕</button>` },
    ], rows, { rowHref: (r) => `#/account/${r.address}`, emptyTitle: current ? `Nobody is tagged #${current}` : 'No labels yet' }), {
      after: (el, rows) => {
        el.querySelectorAll('[data-del]').forEach((b) => b.addEventListener('click', async () => {
          const [a, t] = b.dataset.del.split('|');
          try { await send('DELETE', `/labels/${a}/${encodeURIComponent(t)}`); toast('Deleted'); drawCloud(); drawSel(); } catch (e) { toast(e.message); }
        }));
        view.querySelector('#watch-all')?.addEventListener('click', () => { addWatch(rows.map((r) => r.address)); toast(`Watching ${rows.length} address(es)`); });
      },
    });
  };
  view.querySelector('#add').addEventListener('submit', async (e) => {
    e.preventDefault();
    const d = Object.fromEntries(new FormData(e.target));
    try {
      const r = await send('POST', '/labels', { address: d.address.trim(), tag: d.tag.trim(), note: d.note.trim() });
      toast(`Labelled #${r.tag}`);
      e.target.reset();
      drawCloud(); drawSel();
    } catch (err) { toast(err.message); }
  });
  drawCloud(); drawSel();
  if (query.auto) view.querySelector('#auto').scrollIntoView();
}
