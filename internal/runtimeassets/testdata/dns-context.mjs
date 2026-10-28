import { test } from 'node:test';
import assert from 'node:assert/strict';
import { AsyncResource } from 'node:async_hooks';
import { readFile } from 'node:fs/promises';
// Exercise the production context and DNS functions; other runtime services
// are not needed for this context-selection contract.
let source = await readFile(new URL('../assets/dynamic-env.js', import.meta.url), 'utf8');
source = source.slice(0, source.indexOf('async function scopedFetch'));
source = `const errorReporter=()=>()=>{}; const observeResponseErrors=x=>x;\n` + source;
const { withInvocation, invocationDNS } = await import('data:text/javascript,' + encodeURIComponent(source));
const seen = [];
const context = lease => ({ props: { __NANOFLARE_BROKER: { dns: async () => { seen.push(lease); return lease; } } }, waitUntil() {} });
test('cached pool method selects the calling invocation, not its creation context', async () => {
 let pool;
 await withInvocation(context('A'), async () => { pool = { reconnect: () => invocationDNS().fetch('http://dns/lookup') }; });
 await withInvocation(context('B'), async () => assert.equal(await pool.reconnect(), 'B'));
 assert.deepEqual(seen, ['B']);
});
test('async resource retained from A selects A even when triggered by B', async () => {
 let reconnect;
 await withInvocation(context('A'), async () => { reconnect = AsyncResource.bind(() => invocationDNS().fetch('http://dns/lookup')); });
 await withInvocation(context('B'), async () => assert.equal(await reconnect(), 'A'));
});
test('lookup outside an invocation fails explicitly', () => {
 assert.throws(() => invocationDNS(), /live invocation/);
});
test('pool background loop awakened by B retains its creation invocation A', async () => {
 let wake, complete, stopped = false, expired = false, observed;
 const pending = new Promise(resolve => { complete = resolve; });
 const a = context('A');
 a.props.__NANOFLARE_BROKER.dns = async () => {
  observed = 'A';
  if (expired) throw Object.assign(new Error('expired invocation'), { status: 410 });
 };
 let loop;
 // This is the promise/wakeup pattern in node-oracledb thin pool.bgThreadFunc:
 // create() starts the loop; acquire() wakes it when a new connection is needed.
 await withInvocation(a, async () => {
  loop = (async () => {
   while (!stopped) {
    await new Promise(resolve => { wake = resolve; });
    if (stopped) break;
    try { await invocationDNS().fetch('http://dns/lookup'); complete(null); }
    catch (error) { complete(error); }
   }
  })();
 });
 expired = true;
 await withInvocation(context('B'), async () => {
  wake();
  const error = await pending;
  assert.equal(observed, 'A');
  assert.equal(error.status, 410);
 });
 stopped = true;
 wake();
 await loop;
});
