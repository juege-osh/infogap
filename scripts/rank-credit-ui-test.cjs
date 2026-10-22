const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8');
const elements = new Map();
function element(id) {
  if (id === 'input[name="plan"]:checked') return null;
  if (!elements.has(id)) elements.set(id, {
    innerHTML: '', textContent: '', hidden: false, isConnected: true,
    classList: { add() {}, remove() {}, toggle() {} }, setAttribute() {}, scrollIntoView() {}
  });
  return elements.get(id);
}
const context = {
  document: { querySelector: element, querySelectorAll: () => [], addEventListener() {} },
  location: { hash: '#profile', search: '' }, URL, URLSearchParams, Date, Intl,
  setTimeout, clearTimeout, window: { scrollTo() {} }
};
vm.createContext(context);
vm.runInContext(source.slice(0, source.indexOf("$('#menu-toggle').addEventListener")), context);
const run = code => vm.runInContext(code, context);
const expiry = '2030-11-21T10:30:00Z';
const user = { id: 1, role: 'member', username: '成员', email: 'reader@example.com', tier: '钻石', tier_index: 6,
  daily_remaining: 1, daily_allowance: 15, rank_credits: 3, rank_credits_expires_at: expiry, credits: 10, contributions: 2,
  available_credits: 14, entitlements: { subscriptions: true, subscription_limit: 20, delay_hours: 1 } };
context.reader = user;

(async () => {
  run('state.user=reader;state.boot={};state.routeID=1');
  const budget = JSON.parse(run("JSON.stringify(unlockBudget({level:'exclusive',unlock_cost:8}))"));
  assert.deepEqual(budget, { cost: 8, daily: 1, rank: 3, credits: 10, total: 14, fromDaily: 1, fromRank: 3, fromCredits: 4, enough: true });
  for (const [daily, rank, permanent, cost, expected] of [
    [10, 8, 0, 8, [8, 0, 0, true]], [0, 8, 0, 8, [0, 8, 0, true]],
    [1, 6, 1, 8, [1, 6, 1, true]], [1, 2, 0, 4, [1, 2, 1, false]],
    [0, 0, 8, 8, [0, 0, 8, true]], [-1, -2, -3, 1, [0, 0, 1, false]]
  ]) {
    context.balance = { daily_remaining: daily, rank_credits: rank, credits: permanent };
    context.cost = cost;run('state.user={...reader,...balance}');
    const actual = JSON.parse(run('JSON.stringify(unlockBudget({unlock_cost:cost}))'));
    assert.deepEqual([actual.fromDaily, actual.fromRank, actual.fromCredits, actual.enough], expected);
  }
  run('state.user={...reader};delete state.user.rank_credits;delete state.user.rank_credits_expires_at');
  assert.equal(run('rankCredits()'), 0);
  assert.equal(run('rankCreditExpiry()'), '暂无可用段位赠分');
  run('state.user={...reader,rank_credits:0}');
  assert.equal(run('rankCreditExpiry()'), '暂无可用段位赠分');
  run('state.user=reader;renderShell()');
  const formattedExpiry = run('dateText(reader.rank_credits_expires_at,true)');
  for (const target of ['#top-actions', '#mobile-timeliness', '#rail']) {
    assert(element(target).innerHTML.includes('段位赠分'));
    assert(element(target).innerHTML.includes(formattedExpiry));
  }
  const hint = run("accessHint({level:'exclusive',unlock_cost:8})");
  assert(hint.includes('今日赠分 1 + 段位赠分 3 + 永久积分 4'));
  assert(hint.includes('钻石及以上有效段位'));
  assert(!hint.includes('90 天'));
  assert(!hint.includes('贡献记录'));
  run('state.user={...reader,daily_remaining:0,rank_credits:8,credits:0}');
  let locked = run("locked({id:5,level:'exclusive',unlock_cost:8})");
  assert(locked.includes('data-unlock="5"'));
  assert(!locked.includes('贡献发现，获得额度'));
  assert(locked.includes('临近到期的段位赠分'));
  run('state.user={...reader,daily_remaining:1,rank_credits:2,credits:0}');
  locked = run("locked({id:5,level:'exclusive',unlock_cost:8})");
  assert(locked.includes('目前可用 3，还差 5'));
  assert(locked.includes('贡献发现，获得额度'));
  assert(!locked.includes('data-unlock="5"'));
  run("state.user={...reader,role:'admin',rank_credits:0};renderShell()");
  assert(element('#top-actions').innerHTML.includes('全文不限额'));
  assert(!element('#top-actions').innerHTML.includes('rank-credit-chip'));

  run("state.user=reader;api=async()=>({submissions:[],ledger:[{kind:'rank_gift',amount:2000,created_at:'2030-10-22T10:30:00Z',expires_at:reader.rank_credits_expires_at},{kind:'rank_monthly_gift',amount:300,created_at:'2026-10-22T10:30:00Z'},{kind:'rank_unlock',amount:-3,expires_at:reader.rank_credits_expires_at},{kind:'rank_refund',amount:2,expires_at:reader.rank_credits_expires_at},{kind:'rank_expire',amount:-4,expires_at:reader.rank_credits_expires_at}]})");
  await run('renderProfile(1)');
  assert(element('#main').innerHTML.includes('metrics credit-metrics'));
  assert(element('#main').innerHTML.includes(formattedExpiry));
  assert(element('#main').innerHTML.includes('永久积分 · 不过期'));
  assert(element('#main').innerHTML.includes('计划发放日起30天有效'));
  assert(element('#main').innerHTML.includes('历史永久余额不过期'));
  const ledger = element('#my-ledger').innerHTML;
  for (const label of ['段位赠分 · 按批次到期', '历史段位月赠永久积分', '使用段位赠分', '段位赠分返还', '段位赠分到期']) assert(ledger.includes(label));
  assert.equal((ledger.match(/批次到期：/g) || []).length, 4);
  assert(!ledger.includes('段位月赠永久积分</strong><small>2030'));
  const rankPlan = { id: 'rank_1_12', type: 'rank', rank_tier: 1, months: 12, days: 365, monthly_credits: 2000, amount_cents: 10800, name: '青铜年付' };
  context.plan = rankPlan;
  assert.equal(run('billingBenefit(plan)'), '12个月（365天）· 每期赠 2,000 积分');
  assert(!run('billingBenefit(plan)').includes('永久'));
  assert(!run('billingBenefit(plan)').includes('30天'), 'historical and current mixed-policy orders must not claim the same expiry');
  assert(run("billingBenefit({type:'credits',credits:100})").includes('永久阅读积分'));
  run("api=async path=>path.endsWith('/plans')?{enabled:true,plans:[plan],channels:['alipay']}:path.endsWith('/rank-status')?{periods:[],batches:[]}:{items:[]}");
  await run('renderMembership(1)');
  assert(element('#main').innerHTML.includes('段位赠分余额'));
  assert(element('#main').innerHTML.includes(formattedExpiry));
  assert(element('#membership-plans').innerHTML.includes('每期赠分按计划发放日起30天有效'));
  assert(element('#membership-plans').innerHTML.includes('历史已到账永久积分保持不变'));
  run('renderRules()');
  assert(element('#main').innerHTML.includes('临近到期的段位赠分'));
  assert(element('#main').innerHTML.includes('升档补差生效后30天有效'));
  assert(!source.includes('每期赠分到账后永久有效'));
  assert(!source.includes('贡献、购买和段位月赠到账后均不过期'));
  console.log('PASS: daily → expiring rank gifts → permanent split, gifted-balance-only unlock, shortfall, desktop/mobile/profile/shop expiry visibility, staff exemption, new expiring vs legacy permanent ledger, batch expiry and API-only gift amounts.');
})().catch(error => { console.error(error); process.exitCode = 1; });
