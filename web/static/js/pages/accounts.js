import { get } from '../api.js';
import { html, setHTML, load, pageHead, table, num, amount, addr, ledgerLink, icon } from '../ui.js';

export default async function accounts(view) {
  setHTML(view, html`
    ${pageHead('Accounts', { sub: 'Largest XLM balances among accounts touched since indexing began. The index keeps the latest state of every account, trustline and offer a ledger changed; it is not a full snapshot of the network.' })}
    <form class="toolbar" id="open"><input name="id" type="search" placeholder="Open account G… or contract C…" style="flex:1;max-width:520px" required pattern="[GCM][A-Z2-7]{55,68}"><button class="btn primary">${icon('search')}Open</button></form>
    <div class="card"><div class="card-head"><h2>Top accounts by XLM</h2></div><div class="card-body flush" id="list"></div></div>`);
  view.querySelector('#open').addEventListener('submit', (e) => {
    e.preventDefault();
    const id = new FormData(e.target).get('id').trim();
    location.hash = `/${id[0] === 'C' ? 'contract' : 'account'}/${id}`;
  });
  load(view.querySelector('#list'), () => get('/accounts/top?limit=100'), (rows) => table([
    { h: '#', cls: 'num', v: (a) => String(rows.indexOf(a) + 1) },
    { h: 'Account', v: (a) => addr(a.account_id, { full: true }) },
    { h: 'Home domain', v: (a) => a.home_domain || html`<span class="muted">—</span>` },
    { h: 'XLM', cls: 'num', v: (a) => amount(a.xlm, 2) },
    { h: 'Subentries', cls: 'num', v: (a) => num(a.num_subentries) },
    { h: 'Updated', cls: 'num', v: (a) => ledgerLink(a.last_modified) },
  ], rows, { rowHref: (a) => `#/account/${a.account_id}`, emptyTitle: 'No accounts indexed', emptyText: 'Account state is indexed when INDEX_STATE=true.' }));
}
