// Browser smoke test for the explorer UI against a running indexer.
//
//   npm i --no-save playwright@1.56.1 && npx playwright install chromium
//   node web/e2e/smoke.mjs http://localhost:8080 [outdir]
//
// Opens every page (with real ids taken from the API), fails on JS errors,
// error boxes or horizontal overflow, and saves a screenshot per page.
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';

const base = (process.argv[2] || 'http://localhost:8080').replace(/\/$/, '');
const out = process.argv[3] || 'e2e-shots';
mkdirSync(out, { recursive: true });
const api = async (p) => (await fetch(base + p)).json();

const st = await api('/status');
const [tx] = await api('/transactions?limit=1');
const [pair] = await api('/pairs?hours=2160&limit=1');
const [acct] = await api('/accounts/top?limit=1');
const [contract] = await api('/contracts?hours=2160&limit=1');
const protos = (await api('/protocols?hours=2160')).protocols || [];

const lb = await api('/leaderboards?hours=2160&limit=1');

const routes = ['', 'ledgers', 'txs', 'ops', 'accounts', 'transfers', 'markets', 'trades', 'assets', 'asset/native', 'pools',
  'protocols', 'contracts', 'events', 'api', 'leaders', 'alerts', 'watchlist', 'tags', 'tags?auto=1', 'compare'];
const trader = lb.traders?.[0]?.address, arb = lb.arbitrageurs?.[0]?.address;
if (trader) routes.push(`account/${trader}`);
if (arb) routes.push(`account/${arb}`);
if (trader && acct) routes.push(`compare?a=${trader}&b=${acct.account_id}`);
if (st.cursor) routes.push(`ledger/${st.cursor}`);
if (tx) routes.push(`tx/${tx.hash}`);
if (pair) routes.push(`pair/${encodeURIComponent(pair.base)}/${encodeURIComponent(pair.counter)}`);
if (acct) routes.push(`account/${acct.account_id}`);
if (contract) routes.push(`contract/${contract.contract_id}`);
protos.forEach((p) => routes.push(`protocol/${p.protocol}`));

const browser = await chromium.launch();
let failures = 0;
for (const [label, viewport] of [['desktop', { width: 1440, height: 900 }], ['mobile', { width: 390, height: 844 }]]) {
  const page = await browser.newPage({ viewport });
  const errors = [];
  page.on('pageerror', (e) => errors.push(e.message));
  page.on('console', (m) => m.type() === 'error' && errors.push(m.text()));
  for (const r of routes) {
    errors.length = 0;
    await page.goto(`${base}/ui/#/${r}`, { waitUntil: 'networkidle' });
    await page.waitForTimeout(500);
    const boxes = await page.$$eval('.error-box', (els) => els.map((e) => e.innerText.replace(/\s+/g, ' ')));
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
    const bad = errors.length || boxes.length || overflow > 0;
    if (bad) failures++;
    console.log(`${bad ? 'FAIL' : 'ok  '} ${label} #/${r}${errors.length ? ` js:${errors[0]}` : ''}${boxes.length ? ` box:${boxes[0]}` : ''}${overflow > 0 ? ` overflow:${overflow}px` : ''}`);
    await page.screenshot({ path: `${out}/${label}_${(r || 'overview').replace(/[^a-z0-9]+/gi, '_').slice(0, 50)}.png` });
  }
  await page.close();
}
await browser.close();
console.log(failures ? `${failures} page(s) failed` : `all ${routes.length * 2} page views ok`);
process.exit(failures ? 1 : 0);
