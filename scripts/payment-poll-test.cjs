const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8');
const elements = new Map(), timers = new Map();
let next = 0, calls = 0, qrCalls = 0;
function element(id) {
  if (!elements.has(id)) elements.set(id, { innerHTML: '', textContent: '', isConnected: true });
  return elements.get(id);
}
const context = {
  document: { querySelector: element, querySelectorAll: () => [], addEventListener() {}, visibilityState: 'visible' },
  location: { search: '', hash: '#membership' }, URL, URLSearchParams, Intl, Date,
  setTimeout: () => 0, clearTimeout: id => timers.delete(id),
  window: {
    setTimeout: (fn, delay) => { const id = ++next; timers.set(id, { fn, delay }); return id; },
    QRCode: { toCanvas: async () => { qrCalls++; } }
  }
};
vm.createContext(context);
vm.runInContext(source.slice(0, source.indexOf("$('#menu-toggle').addEventListener")), context);
const run = code => vm.runInContext(code, context);
const pending = { id: 'poll-test', status: 'pending', type: 'credits', channel: 'alipay', name: '100阅读额度', amount_cents: 990, credits: 100, payment_kind: 'qr', qr_payload: 'https://qr.alipay.com/test', expires_at: '2099-01-01T00:00:00Z' };
context.order = pending;
context.mockAPI = async () => { calls++; return context.result; };
run("state.routeID=1;state.user={credits:10};api=mockAPI;refreshBoot=async()=>{};loadBillingOrders=async()=>{};toast=()=>{};");
async function tick() {
  assert.equal(timers.size, 1);
  const [id, timer] = timers.entries().next().value;
  timers.delete(id);
  assert.equal(timer.delay, 5000);
  await timer.fn();
}
(async () => {
  context.result = { order: pending };
  await run('renderCheckout(order)');
  await tick();
  assert.equal(calls, 1);
  assert.equal(qrCalls, 1, 'pending polling must not rebuild the QR');
  assert.match(element('#payment-status-message').textContent, /自动更新/);
  context.result = { order: { ...pending, status: 'success' }, user: { credits: 110 } };
  await tick();
  assert.equal(timers.size, 0);
  assert.match(element('#membership-checkout').innerHTML, /积分已到账/);
  assert.equal(run('state.user.credits'), 110);
  await run('renderCheckout(order)');
  const before = calls;
  run('state.routeID++');
  await tick();
  assert.equal(calls, before, 'navigation must stop stale polling');
  assert.equal(timers.size, 0);
  context.document.visibilityState = 'hidden';
  await run('renderCheckout(order)');
  await tick();
  assert.equal(calls, before, 'hidden pages must pause gateway queries');
  context.document.visibilityState = 'visible';
  context.mockAPI = async () => { throw new Error('temporary network failure'); };
  run('api=mockAPI');
  await tick();
  assert.match(element('#payment-status-message').textContent, /请勿重复付款/);
  assert.equal(timers.size, 1, 'network failures must retry');
  run('stopPaymentPolling()');
  assert.equal(timers.size, 0);
  console.log('PASS: automatic payment confirmation, credit refresh, stable QR, route cancellation, hidden-page pause and network retry.');
})().catch(error => { console.error(error); process.exitCode = 1; });
