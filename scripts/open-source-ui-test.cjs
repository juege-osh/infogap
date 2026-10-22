const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8');
const elements = new Map(), handlers = {};
function element(id) {
  if (!elements.has(id)) elements.set(id, {
    innerHTML: '', textContent: '', hidden: false, isConnected: true, attrs: {},
    classList: { add() {}, remove() {}, toggle() {} },
    setAttribute(key, value) { this.attrs[key] = value; }, scrollIntoView() {}
  });
  return elements.get(id);
}
const context = {
  document: { querySelector: element, querySelectorAll: () => [], addEventListener: (kind, fn) => { handlers[kind] = fn; } },
  location: { hash: '#feed', search: '' }, URL, URLSearchParams, Intl, Date,
  setTimeout, clearTimeout, window: { scrollTo() {} }
};
vm.createContext(context);
vm.runInContext(source.slice(0, source.indexOf("$('#menu-toggle').addEventListener")), context);
const run = code => vm.runInContext(code, context);
function groups(html) {
  return [...html.matchAll(/<details class="category-group"([^>]*)><summary><span>([^<]+)<\/span><small>([\d,]+)<\/small><\/summary>([\s\S]*?)<\/details>/g)].map(m => ({
    open: m[1].includes('open'), name: m[2], count: Number(m[3].replaceAll(',', '')), html: m[4],
    categories: [...m[4].matchAll(/data-category="([^"]+)"/g)].map(c => c[1])
  }));
}

(async () => {
  run("state.user={role:'admin',username:'站长',tier_index:0,entitlements:{delay_hours:0,subscriptions:true,subscription_limit:40}};state.routeID=1;");
  const categories = Array.from(run('fallbackCategories'));
  assert.equal(categories.length, 28);
  assert(categories.includes('开源技术'));
  assert(!categories.includes('开源工具'));

  for (const bootstrap of ['{}', "{categories:fallbackCategories,category_counts:{'开源技术':37,'人工智能':5,'省钱优惠':2}}"] ) {
    run(`state.boot=${bootstrap};state.category='';renderShell()`);
    const nav = groups(element('#sidebar').innerHTML);
    assert.deepEqual(nav.map(g => g.name), ['开源工具', '生活与机会', '科学与技术', '产业与民生', '公共事务']);
    assert.deepEqual(nav[0].categories, ['开源技术']);
    assert(nav[0].html.includes('全部开源工具'));
    assert.equal(nav[0].count, bootstrap === '{}' ? 0 : 37);
    assert.equal((nav[0].html.match(/data-category="开源技术"/g) || []).length, 1);
    assert(!nav[2].categories.includes('开源技术'));
    assert.deepEqual(nav.flatMap(g => g.categories).sort(), [...categories].sort());
    assert.deepEqual(nav.filter(g => g.open).map(g => g.name), ['开源工具', '生活与机会']);
    assert.deepEqual(nav[1].categories.slice(0, 2), ['省钱优惠', '招聘求职']);
    assert(nav[0].html.includes(`class="count">${bootstrap === '{}' ? 0 : 37}</span>`));
  }

  for (const category of categories) {
    context.category = category;
    run('state.category=category;renderShell()');
    const nav = groups(element('#sidebar').innerHTML), opened = nav.filter(g => g.open);
    assert.equal(opened.length, 1);
    assert(opened[0].categories.includes(category));
    assert(opened[0].html.includes('nav-link active'));
  }
  run("state.category='';state.boot={categories:fallbackCategories.filter(c=>c!=='开源技术')};renderShell()");
  assert(!groups(element('#sidebar').innerHTML).some(g => g.name === '开源工具'));

  run("state.boot={categories:fallbackCategories,topic_codes:{'开源技术':'KY'}};state.category='出行旅行';state.country='日本';state.region='东亚';state.travelTopic='优惠渠道';state.q='';state.level='';state.page=4;state.view='saved';api=async request=>{globalThis.lastRequest=request;return {items:[],total:0}};");
  await handlers.click({ target: { closest: selector => selector === '[data-category]' ? { dataset: { category: '开源技术' } } : null } });
  await Promise.resolve();
  const query = new URL(context.lastRequest, 'https://infogap.osh.lol').searchParams;
  assert.equal(query.get('category'), '开源技术');
  assert.equal(query.get('view'), 'all');
  assert.equal(query.get('page'), '1');
  for (const key of ['country', 'region', 'travel_topic']) assert.equal(query.has(key), false);
  assert.equal(run('state.country'), '');
  assert.match(element('#main').innerHTML, /<h2>开源工具 /);
  assert(element('#main').innerHTML.includes('查看项目用途、上手方法与许可证'));
  assert.equal(element('#result-count').textContent, '0 条');
  assert.equal(element('#sidebar-scrim').hidden, true);
  assert.equal(element('#menu-toggle').attrs['aria-expanded'], 'false');

  context.gap = { id: 205, code: 'PT-KY-205', title: '已有开源项目', summary: '公开摘要', body: '项目详情', category: '开源技术', level: 'ordinary', status: 'published', evidence: 'repository', tags: [] };
  run('api=async()=>({items:[gap],total:1})');
  await run('renderFeed(state.routeID)');
  assert(element('#feed-results').innerHTML.includes('>PT-KY-205</span>'));
  assert(element('#feed-results').innerHTML.includes('href="#gap/205"'));
  assert.equal(element('#result-count').textContent, '1 条');
  run("state.q='终端';api=async request=>{globalThis.lastRequest=request;return {items:[],total:0}};");
  await run('renderFeed(state.routeID)');
  assert.match(element('#main').innerHTML, /搜索“终端”/);
  assert.equal(new URL(context.lastRequest, 'https://infogap.osh.lol').searchParams.get('category'), '开源技术');

  run('renderSubmission()');
  assert(element('#main').innerHTML.includes('<option>开源技术</option>'));
  assert(!element('#main').innerHTML.includes('<option>开源工具</option>'));
  run('api=async()=>gap');
  await run("renderEdit('205',state.routeID)");
  assert(element('#main').innerHTML.includes('<option selected>开源技术</option>'));
  run('api=async()=>({items:[],enabled:true,limit:40})');
  await run('renderSubscriptions(state.routeID)');
  assert(element('#subscription-content').innerHTML.includes('<option>开源技术</option>'));
  assert(!element('#subscription-content').innerHTML.includes('<option>开源工具</option>'));
  console.log('PASS: independent open-source group, all 28 categories exactly once, live counts, active/default expansion, original category request and KY code, search, mobile drawer close, and unchanged submission/edit/subscription values.');
})().catch(error => { console.error(error); process.exitCode = 1; });
