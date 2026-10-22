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
const plans = [{id:'credits_100',type:'credits',name:'100永久积分',credits:100,amount_cents:990},...[[1,30,8,1800],[3,90,4,3800],[6,180,2,5800],[12,365,0,8800]].map(([months,days,delay,amount])=>({id:`reading_${months}`,type:'reading',name:`${months}个月畅读`,months,days,delay_hours:delay,unlimited_reading:true,list_amount_cents:amount,discount_percent:92,amount_cents:Math.round(amount*.92)}))];
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

(async()=>{
 run("state.user={id:1,role:'member',credits:10,points:300};state.boot={};state.routeID=1;api=apiFixture;");
 await run('renderMembership(1)');
 const html=element('#membership-plans').innerHTML;
 assert.equal(planInputs.length,5);assert.equal(planInputs.filter(p=>p.checked).length,1);assert.equal(element('input[name="plan"]:checked').value,'reading_1');
 assert(!html.includes('段位'));assert(!html.includes('rank_duration'));assert(!html.includes('每期赠'));
 assert(html.includes('reading-plan-grid'));assert(html.includes('续费')||element('#main').innerHTML.includes('续费'));
 for(const plan of plans){choosePlan(plan.id);assert.equal(element('input[name="plan"]:checked').value,plan.id);assert(element('#billing-selection').textContent.includes((plan.amount_cents/100).toFixed(2)));const form={id:'membership-form',elements:{plan:{value:plan.id}},querySelector:()=>element('#submit-button')};await handlers.submit({target:form,preventDefault(){}});assert.equal(context.lastOrderRequest.plan,plan.id);assert.equal(context.lastOrderRequest.channel,'alipay');assert(element('#membership-checkout').innerHTML.includes((plan.amount_cents/100).toFixed(2)));}
 for(const [points,discount] of [[0,100],[99,100],[100,95],[299,95],[300,92],[700,90],[1400,85],[2500,80]]){context.points=points;assert.equal(run('discountPercent(points)'),discount);}
 assert(run('billingPlanCard(catalog.plans[1],true,true)').includes('¥16.56'));
 assert(run("billingOrderName({type:'rank',name:'old rank'})").includes('历史权益订单'));
 context.catalog.channels=['wechat'];await run('renderMembership(1)');assert(planInputs.every(p=>p.disabled));context.catalog.channels=['alipay'];
 context.catalog.enabled=false;await run('renderMembership(1)');assert(planInputs.every(p=>p.disabled));
 assert.match(css,/\.reading-plan-grid\{[^}]*grid-template-columns:repeat\(4,minmax\(0,1fr\)\)/);assert(css.includes('.membership-plan:has(input:focus-visible)'));
 console.log('PASS: reading 1/3/6/12 and credits submit exact API plans/discounted cents, all contribution boundaries, disabled payments, no new rank sale or gift claims; native radio focus styles retained.');
})().catch(error=>{console.error(error);process.exitCode=1});
