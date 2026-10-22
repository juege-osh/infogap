const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8');
const elements = new Map();
function element(id) {
  if (!elements.has(id)) elements.set(id, {
    innerHTML: '', textContent: '', hidden: false, isConnected: true,
    classList: { add() {}, remove() {}, toggle() {} }, setAttribute() {}, scrollIntoView() {},
    insertAdjacentHTML(position, html) { this.innerHTML += html; }
  });
  return elements.get(id);
}
const context = {
  document: { querySelector: element, querySelectorAll: () => [], addEventListener() {} },
  location: { hash: '#feed', search: '' }, URL, URLSearchParams, Date, Intl,
  setTimeout, clearTimeout, window: { scrollTo() {} }
};
vm.createContext(context);
vm.runInContext(source.slice(0, source.indexOf("$('#menu-toggle').addEventListener")), context);
const run = code => vm.runInContext(code, context);
const secretName = 'HiddenOriginalLibrary', secretURL = 'https://private-source.example/article/undisclosed', secretHost = 'private-source.example';
const user = { id: 7, role: 'member', username: '成员', tier_index: 6, credits: 10, daily_remaining: 1,
  entitlements: { subscriptions: true, subscription_limit: 20 } };
const gap = { id: 51, code: 'PT-WL-51', title: '研究结果与实验范围', summary: '公开摘要说明发现。阅读详情后核对适用范围。',
  body: `## 完整解读\n\n[原始证据](${secretURL})`, category: '物理', level: 'ordinary', evidence: 'preprint',
  source_name: secretName, source_url: secretURL, source_visibility: 'public',
  sources: [{ name: secretName, url: secretURL }], source_urls: [secretURL],
  review_note: `核验见 ${secretURL}`, status: 'published', tags: ['实验'], author: '编辑部',
  published_at: '2026-10-21', created_at: '2026-10-21', unlocked: false };
context.gap = gap;context.reader = user;
function noSources(html) {
  for (const text of [secretName, secretURL, secretHost]) assert(!html.includes(text), `source leaked: ${text}`);
  assert(!html.includes('<aside class="source-box">'));
  assert(!html.includes('回到原始来源'));
}

(async () => {
  run('state.boot={};state.routeID=1');
  for (const reader of [null, user]) {
    context.reader = reader;run('state.user=reader');
    for (const unlocked of [false, undefined, 'true']) {
      gap.unlocked = unlocked;
      const card = run('card(gap)');
      noSources(card);
      assert(card.includes('预印本'));
      assert(card.includes(gap.summary));
      assert(card.includes('来源随详情解锁'));
      run('api=async()=>gap');await run("renderArticle('51',1)");
      const article = element('#main').innerHTML;
      noSources(article);
      assert(article.includes('预印本研究'));
      assert(article.includes(gap.summary));
      assert(!article.includes('id="article-body"'), 'accidental body must not bypass locked-source access');
    }
  }
  gap.unlocked = false;context.reader = user;run('state.user=reader');
  for (const status of ['disputed', 'expired']) {
    gap.status = status;run('api=async()=>gap');await run("renderArticle('51',1)");
    noSources(element('#main').innerHTML);
    assert(!element('#main').innerHTML.includes(gap.review_note));
  }
  gap.status = 'published';
  for (const view of ['all', 'saved', 'subscribed']) {
    context.view = view;run("state.view=view;state.q='实验';api=async()=>({items:[gap],total:1})");
    await run('renderFeed(1)');noSources(element('#feed-results').innerHTML);
    assert(element('#feed-results').innerHTML.includes('预印本'));
  }

  gap.unlocked = true;
  run('state.user=reader;api=async()=>gap');
  assert(run('card(gap)').includes(secretName));
  await run("renderArticle('51',1)");
  assert(element('#main').innerHTML.includes('回到原始来源'));
  assert(element('#main').innerHTML.includes(`href="${secretURL}"`));
  assert(element('#main').innerHTML.includes('id="article-body"'));
  run('state.user=null');
  noSources(run('card(gap)'));
  await run("renderArticle('51',1)");noSources(element('#main').innerHTML);

  gap.source_visibility = 'internal';gap.body = '## 社区线索\n\n已整理的公开内容，不包含私人社区出处。';
  run('state.user=reader;api=async()=>gap');
  noSources(run('card(gap)'));
  assert(run('card(gap)').includes('社区线索'));
  await run("renderArticle('51',1)");
  noSources(element('#main').innerHTML);
  assert(element('#main').innerHTML.includes('社区线索'));
  assert(element('#main').innerHTML.includes('id="article-body"'));

  for (const role of ['admin', 'reviewer']) {
    context.role = role;gap.unlocked = false;gap.status = 'pending';
    run('state.user={...reader,role};api=async()=>gap');
    assert(run('card(gap)').includes(secretName));
    await run("renderArticle('51',1)");
    assert(element('#main').innerHTML.includes('原始来源 · 仅后台可见'));
    assert(element('#main').innerHTML.includes(`href="${secretURL}"`));
    await run("renderEdit('51',1)");
    assert(element('#main').innerHTML.includes(`href="${secretURL}"`));
  }
  gap.unlocked = true;gap.source_visibility = 'public';gap.status = 'pending';
  run('state.user=reader;api=async()=>gap');await run("renderArticle('51',1)");
  assert(element('#main').innerHTML.includes(`href="${secretURL}"`), 'server-authorized owner reads remain compatible');
  gap.unlocked = false;gap.summary = '公开摘要中的固定内容保留：HiddenOriginalLibrary。';
  await run("renderArticle('51',1)");
  assert(element('#main').innerHTML.includes(gap.summary), 'do not rewrite public summaries');
  assert(element('#main').innerHTML.includes('<span class="source-name">来源随详情解锁</span>'));
  assert(!element('#main').innerHTML.includes('<aside class="source-box">'));
  console.log('PASS: locked guest/member card, search, saved, subscribed and detail omit concrete sources even with accidental complete API fields; preprint evidence retained; unlocked/owner/staff access works; internal sources remain staff-only; body/review-note bypass blocked; summary unchanged.');
})().catch(error => { console.error(error); process.exitCode = 1; });
