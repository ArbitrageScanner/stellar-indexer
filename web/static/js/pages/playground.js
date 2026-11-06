import { get, raw as rawFetch, apiURL, blocked } from '../api.js';
import { html, setHTML, pageHead, jsonHTML, bytes, num, icon, raw, tabs, bindTabs, toast, errorBox } from '../ui.js';

// Endpoint catalog. {x} placeholders are filled from live samples.
const CATALOG = [
  ['Status', [
    ['/status', 'Cursor, ingestion lag, pipeline component heartbeats and ledger gaps.'],
    ['/health', 'Liveness: database ping; 503 when Postgres is unreachable.'],
    ['/ready', 'Readiness: 503 when Postgres is down or the newest ledger is older than READY_MAX_LAG.'],
    ['/openapi.yaml', 'OpenAPI 3.1 specification of every endpoint.'],
    ['/search?q={ledger}', 'Classify a ledger number, tx hash, G…/C… address or asset code.'],
    ['/stats/tables', 'Table sizes and row estimates.'],
  ]],
  ['Network', [
    ['/stats/network?hours=24&bucket=hour', 'Transactions, operations, failures, Soroban share and fees per bucket (minute | hour | day).'],
    ['/stats/operations?hours=24', 'Operation mix: count, successful and distinct sources per type.'],
    ['/ledgers?limit=5', 'Latest ledgers. Page with ?before=<sequence>.'],
    ['/ledgers/{ledger}', 'One ledger header with its transaction list.'],
  ]],
  ['Transactions', [
    ['/transactions?limit=5&soroban=true', 'Filter by successful, soroban, source, contract, function, ledger. Page with ?before=<id>.'],
    ['/transactions/{tx}', 'Full transaction: operations, transfers, trades, contract and protocol events.'],
    ['/operations?type=invoke_host_function&limit=5', 'Typed operations with decoded details.'],
    ['/transfers?limit=5&type=mint', 'CAP-67 token flows; filter asset and type. Page with ?before=ledger.tx.index.'],
  ]],
  ['Markets', [
    ['/pairs?hours=24&limit=10', 'Top pairs: trades, volumes, VWAP, last price, venues.'],
    ['/price?base={base}&counter={counter}', 'Last price and 24h open/high/low/volume/change. Swap base/counter to invert.'],
    ['/candles?base={base}&counter={counter}&interval=15m', 'OHLCV candles (1m, 5m, 15m, 30m, 1h, 4h, 1d); from/to as RFC 3339.'],
    ['/trades?base={base}&counter={counter}&limit=10', 'Latest fills for a pair; filter venue, taker, pool.'],
    ['/trades?venue=aquarius&limit=5', 'Latest fills on one venue.'],
    ['/pools?limit=5', 'Classic AMM pools: reserves and spot price.'],
  ]],
  ['Assets & accounts', [
    ['/assets?hours=24&limit=10', 'Most active assets by trades and transfers.'],
    ['/assets/{asset}', 'Asset details, 24h transfer/mint/burn totals and top pairs.'],
    ['/assets/{asset}/holders?limit=10', 'Largest indexed holders.'],
    ['/accounts/top?limit=10', 'Largest XLM balances among indexed accounts.'],
    ['/accounts/{account}', 'Balances, trustlines, offers and 24h activity.'],
    ['/accounts/{account}/operations?limit=5', 'Operations sourced by the account.'],
    ['/accounts/{account}/transfers?limit=5', 'Transfers in and out.'],
    ['/accounts/{account}/trades?limit=5', 'Trades as taker.'],
    ['/accounts/{account}/events?limit=5', 'Protocol events where the account is the user.'],
  ]],
  ['Soroban', [
    ['/protocols?hours=24', 'Per-protocol events, users, contracts and action breakdown; trades per venue.'],
    ['/protocols/{protocol}/summary?hours=24', 'Actions, per-asset totals, contracts, users, pools and hourly timeline.'],
    ['/protocols/blend/events?action=flash_loan&limit=5', 'Decoded protocol events; filter action, user, contract, asset.'],
    ['/contracts?hours=24&limit=10', 'Most active contracts: calls, callers, events, functions, topics.'],
    ['/contracts/{contract}', 'Contract summary: functions, topics, protocol actions.'],
    ['/contracts/{contract}/events?limit=5', 'Raw decoded events of one contract.'],
    ['/events?protocol=none&limit=5', 'All contract events; protocol=none finds contracts without a decoder.'],
  ]],
];

async function samples() {
  const s = { ledger: '', tx: '', base: 'native', counter: '', asset: 'native', account: '', contract: '', protocol: 'aquarius' };
  const safe = (p) => get(p).catch(() => null);
  const [st, pairs, txs, contracts, protos, accts] = await Promise.all([
    safe('/status'), safe('/pairs?hours=168&limit=1'), safe('/transactions?limit=1&soroban=true'), safe('/contracts?hours=168&limit=1'),
    safe('/protocols?hours=168'), safe('/accounts/top?limit=1')]);
  if (st?.cursor) s.ledger = String(st.cursor);
  if (pairs?.[0]) { s.base = pairs[0].base; s.counter = pairs[0].counter; s.asset = pairs[0].base; }
  if (txs?.[0]) { s.tx = txs[0].hash; s.account = txs[0].source; }
  if (accts?.[0]) s.account = accts[0].account_id;
  if (contracts?.[0]) s.contract = contracts[0].contract_id;
  if (protos?.protocols?.[0]) s.protocol = protos.protocols[0].protocol;
  return s;
}

function fill(path, s) {
  return path.replace(/\{(\w+)\}/g, (_, k) => encodeURIComponent(s[k] || k)).replace(/%3A/g, ':');
}

export default async function playground(view, _p, query, ctx) {
  setHTML(view, html`
    ${pageHead('API playground', { sub: html`Every endpoint of the indexer’s JSON API, with examples filled from live data. Edit the path, run it, and copy the request as curl, JavaScript or Python. All endpoints are <code>GET</code>, return JSON and send <code>Access-Control-Allow-Origin: *</code>.` })}
    <div class="pg">
      <nav class="card pg-list" id="list"><div class="skel-rows"><div class="skel"></div><div class="skel"></div><div class="skel"></div></div></nav>
      <div class="stack">
        <div class="card"><div class="card-body">
          <form class="pg-url" id="form"><input id="path" spellcheck="false" aria-label="Request path"><button class="btn primary">${icon('play')}Run</button></form>
          <div class="muted" id="desc" style="margin-top:8px;font-size:13px"></div>
          <div style="margin-top:12px" id="snip-tabs"></div>
          <div class="curl" id="snip"></div>
        </div></div>
        <div class="card"><div class="card-head"><h2>Response</h2><div class="actions" style="display:flex;gap:6px"><button class="btn sm" id="copy-resp">${icon('copy')}Copy</button><a class="btn sm" id="open-new" target="_blank" rel="noopener">${icon('ext')}Open</a></div></div>
          <div class="card-body"><div class="pg-meta" id="meta"></div><div id="resp"></div></div></div>
      </div>
    </div>`);

  const s = await samples();
  if (!ctx.alive()) return;
  const items = CATALOG.flatMap(([g, list]) => list.filter(([p]) => !blocked(p)).map(([p, d]) => ({ g, path: fill(p, s), tpl: p, d })));
  const listEl = view.querySelector('#list');
  setHTML(listEl, html`${CATALOG.map(([g]) => html`<div class="grp">${g}</div>${items.filter((i) => i.g === g).map((i) => html`<a role="button" tabindex="0" data-path="${i.path}"><span class="m">GET</span><span class="p">${i.tpl.split('?')[0]}</span>${i.tpl.includes('?') ? html`<span class="muted">?${i.tpl.split('?')[1]}</span>` : ''}</a>`)}`)}`);

  const pathEl = view.querySelector('#path'), descEl = view.querySelector('#desc');
  let lang = 'curl', last = null;
  const snippet = () => {
    const url = apiURL(pathEl.value.trim());
    const code = {
      curl: `curl -s '${url}' | jq .`,
      js: `const res = await fetch('${url}');\nif (!res.ok) throw new Error((await res.json()).error);\nconst data = await res.json();\nconsole.log(data);`,
      python: `import requests\n\nr = requests.get('${url}', timeout=30)\nr.raise_for_status()\nprint(r.json())`,
    }[lang];
    setHTML(view.querySelector('#snip'), html`<pre>${code}</pre><button class="copy" data-copy="${code}" title="Copy">${icon('copy')}</button>`);
    view.querySelector('#open-new').href = url;
  };
  setHTML(view.querySelector('#snip-tabs'), tabs([['curl', 'curl'], ['js', 'JavaScript'], ['python', 'Python']], lang));
  bindTabs(view.querySelector('#snip-tabs'), (t) => { lang = t; snippet(); });

  const run = async () => {
    const p = pathEl.value.trim();
    if (!p.startsWith('/')) { pathEl.value = '/' + p; }
    ctx.setQuery({ path: pathEl.value });
    const it = items.find((i) => i.path === pathEl.value || i.path.split('?')[0] === pathEl.value.split('?')[0]);
    descEl.textContent = it?.d || '';
    listEl.querySelectorAll('a').forEach((a) => a.classList.toggle('on', a.dataset.path === pathEl.value));
    snippet();
    const meta = view.querySelector('#meta'), resp = view.querySelector('#resp');
    setHTML(meta, html`<span>Running…</span>`);
    try {
      const r = await rawFetch(pathEl.value);
      if (!ctx.alive()) return;
      last = r;
      const count = Array.isArray(r.body) ? `${num(r.body.length)} items` : r.body && typeof r.body === 'object' ? `${Object.keys(r.body).length} keys` : '';
      setHTML(meta, html`<span>Status <b style="color:${raw(r.ok ? 'var(--good-ink)' : 'var(--critical)')}">${r.status}</b></span><span>Time <b>${num(r.ms)} ms</b></span><span>Size <b>${bytes(r.bytes)}</b></span>${count ? html`<span><b>${count}</b></span>` : ''}<span class="mono">${r.type}</span>`);
      setHTML(resp, r.body !== null ? html`<pre class="json" style="max-height:640px">${jsonHTML(r.body)}</pre>` : html`<pre class="json">${r.text.slice(0, 20000)}</pre>`);
    } catch (e) {
      setHTML(meta, '');
      setHTML(resp, errorBox(e, false));
    }
  };

  listEl.addEventListener('click', (e) => {
    const a = e.target.closest('a[data-path]');
    if (!a) return;
    e.preventDefault();
    pathEl.value = a.dataset.path;
    run();
  });
  view.querySelector('#form').addEventListener('submit', (e) => { e.preventDefault(); run(); });
  pathEl.addEventListener('input', snippet);
  view.querySelector('#copy-resp').addEventListener('click', () => {
    if (last) navigator.clipboard?.writeText(last.text).then(() => toast('Response copied'));
  });
  pathEl.value = query.path || items[0].path;
  run();
}
