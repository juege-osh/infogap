const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8');
const css = fs.readFileSync(path.join(__dirname, '../web/style.css'), 'utf8');
const elements = new Map(), handlers = {};
let planInputs = [];
const attributes = html => Object.fromEntries([...html.matchAll(/([\w-]+)="([^"]*)"/g)].map(m => [m[1], m[2]]));
function readPlans(html, rankOnly) {
  const inputs = [...html.matchAll(/<input\b[^>]*name="plan"[^>]*>/g)].map(match => {
    const attrs = attributes(match[0]);
    return { value: attrs.value, name: 'plan', checked: /\schecked(?:\s|>)/.test(match[0]), disabled: /\sdisabled(?:\s|>)/.test(match[0]),
      dataset: { planName: attrs['data-plan-name'], planBenefit: attrs['data-plan-benefit'], planAmount: attrs['data-plan-amount'] },
      closest: selector => selector === '#membership-form' ? {} : null };
  });
  if (!rankOnly) planInputs = inputs;
  else {
    const kept = planInputs.filter(input => !input.value.startsWith('rank_'));
    if (inputs.some(input => input.checked)) kept.forEach(input => { input.checked = false; });
    planInputs = [...kept, ...inputs];
  }
}
function element(selector) {
  if (selector === 'input[name="plan"]:checked') return planInputs.find(input => input.checked) || null;
  if (!elements.has(selector)) {
    let html = '';
    const item = { textContent: '', hidden: false, isConnected: true, disabled: false,
      classList: { add() {}, remove() {}, toggle() {} }, setAttribute() {}, scrollIntoView() {} };
    Object.defineProperty(item, 'innerHTML', { get: () => html, set: value => {
      html = value;
      if (selector === '#membership-plans') readPlans(value, false);
      if (selector === '#rank-plan-grid') readPlans(value, true);
    } });
    elements.set(selector, item);
  }
  return elements.get(selector);
}
const context = {
  document: { querySelector: element, querySelectorAll: () => [], addEventListener: (kind, handler) => { handlers[kind] = handler; } },
  location: { hash: '#membership', search: '' }, URL, URLSearchParams, Date, Intl,
  setTimeout, clearTimeout, window: { scrollTo() {} }
};
vm.createContext(context);
vm.runInContext(source.slice(0, source.indexOf("$('#menu-toggle').addEventListener")), context);
const run = code => vm.runInContext(code, context);
const durations = [{ months: 1, days: 30, bonus: 0 }, { months: 3, days: 90, bonus: 5 }, { months: 6, days: 180, bonus: 10 }, { months: 12, days: 365, bonus: 20 }];
const plans = [{ id: 'credits_100', type: 'credits', name: '100积分', credits: 100, amount_cents: 990 }, ...Array.from({ length: 7 }, (_, n) => n + 1).flatMap(tier => durations.map(({ months, days }) => ({
  id: `rank_${tier}_${months}`, type: 'rank', rank_tier: tier, name: `段位 ${tier} · ${months}个月`, months, days,
  amount_cents: tier * 10000 + months * 137, monthly_credits: tier * 2000 + months * 37
})))];
context.catalog = { enabled: true, channels: ['alipay'], plans };
context.apiFixture = async (url, options) => {
  if (options) {
    context.lastOrderRequest = JSON.parse(options.body);
    const selected = plans.find(plan => plan.id === context.lastOrderRequest.plan);
    return { order: { ...selected, id: 'fixture-order', plan: selected.id, status: 'pending', channel: 'alipay', expires_at: '2099-01-01T00:00:00Z' } };
  }
  return url.endsWith('/plans') ? context.catalog : url.endsWith('/rank-status') ? { periods: [] } : { items: [] };
};
function choosePlan(id) {
  planInputs.forEach(input => { input.checked = input.value === id; });
  const selected = element('input[name="plan"]:checked');
  assert(selected, `missing plan ${id}`);
  handlers.change({ target: selected });
}
function changeDuration(value, checked = true, insideForm = true) {
  handlers.change({ target: { name: 'rank_duration', value: String(value), checked, closest: () => insideForm ? {} : null } });
}

(async () => {
  run("state.user={id:1,role:'member',tier:'黑铁',tier_index:0,credits:10};state.boot={};state.routeID=1;api=apiFixture;");
  await run('renderMembership(1)');
  const html = element('#membership-plans').innerHTML;
  assert(!html.includes('<select id="rank-duration"'));
  assert(html.includes('<fieldset class="rank-duration-options"><legend>购买时长</legend>'));
  const radios = [...html.matchAll(/<input type="radio" name="rank_duration" value="(\d+)"([^>]*)>/g)];
  assert.deepEqual(radios.map(m => Number(m[1])), [1, 3, 6, 12]);
  assert.equal(radios.filter(m => m[2].includes('checked')).length, 1);
  assert.equal(radios.find(m => m[2].includes('checked'))[1], '1');
  assert(!run('rankDurationOptions()').includes('tabindex="-1"'));
  assert.equal((run('rankDurationOptions()').match(/<label class="rank-duration-option">/g) || []).length, 4);
  for (const { days, bonus } of durations) {
    assert(html.includes(`<small>${days} 天</small>`));
    assert(html.includes(bonus ? `每期多赠 ${bonus}%` : '基础赠分'));
  }
  assert(html.includes('id="billing-selection" class="billing-selection" aria-live="polite"'));
  assert.equal(element('input[name="plan"]:checked').value, 'credits_100');
  changeDuration(6);
  assert.equal(element('input[name="plan"]:checked').value, 'credits_100', 'duration browsing must preserve a selected credit pack');
  assert(planInputs.filter(p => p.value.startsWith('rank_')).every(p => p.value.endsWith('_6')));

  for (let tier = 1; tier <= 7; tier++) {
    choosePlan(`rank_${tier}_${run('state.rankMonths')}`);
    for (const { months } of durations) {
      changeDuration(months);
      assert.equal(run('state.rankMonths'), months);
      assert.equal(planInputs.filter(input => input.checked).length, 1);
      const selected = element('input[name="plan"]:checked');
      assert.equal(selected.value, `rank_${tier}_${months}`);
      const plan = plans.find(p => p.id === selected.value);
      assert.equal(selected.dataset.planAmount, String(plan.amount_cents));
      const summary = element('#billing-selection').textContent;
      assert(summary.includes((plan.amount_cents / 100).toFixed(2)));
      assert(summary.includes(plan.monthly_credits.toLocaleString('zh-CN')));
      const form = { id: 'membership-form', elements: { plan: { value: selected.value } }, querySelector: () => element('#submit-button') };
      await handlers.submit({ target: form, preventDefault() {} });
      assert.equal(context.lastOrderRequest.plan, plan.id);
      assert.equal(context.lastOrderRequest.channel, 'alipay');
      assert.equal(element('#submit-button').disabled, false);
      assert(element('#membership-checkout').innerHTML.includes((plan.amount_cents / 100).toFixed(2)));
    }
  }
  for (const invalid of [0, 2, 4, 'bad']) { changeDuration(invalid); assert.equal(run('state.rankMonths'), 12); }
  changeDuration(1, false); assert.equal(run('state.rankMonths'), 12);
  changeDuration(1, true, false); assert.equal(run('state.rankMonths'), 12);
  assert.match(run('rankDurationOptions()'), /value="12" checked/);
  context.catalog.enabled = false;
  await run('renderMembership(1)');
  assert(planInputs.every(input => input.disabled));
  assert.equal((element('#membership-plans').innerHTML.match(/name="rank_duration"/g) || []).length, 4);
  assert.match(css, /\.rank-duration-grid\{[^}]*display:grid;[^}]*grid-template-columns:repeat\(4,minmax\(0,1fr\)\)/);
  assert.match(css, /@media\(max-width:760px\)\{\.rank-duration-grid\{grid-template-columns:repeat\(2,minmax\(0,1fr\)\)/);
  assert(css.includes('input:focus-visible+.rank-duration-card'));
  assert(css.includes('input:checked+.rank-duration-card'));
  assert(css.includes('@media(prefers-reduced-motion:reduce){.rank-duration-card{transition:none}}'));
  console.log('PASS: four labeled native-radio durations, days/bonuses/selected and focus styles, responsive grid, all 7 tiers × 4 durations retain tier and submit exact API plan/price/gift, credit-pack preservation, invalid change defense and disabled-payment state.');
})().catch(error => { console.error(error); process.exitCode = 1; });
