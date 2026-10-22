const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../web/app.js'), 'utf8');
const css = fs.readFileSync(path.join(__dirname, '../web/style.css'), 'utf8');
const elements = new Map(), handlers = {}, requests = [], copied = [];
function element(selector) {
  if (!elements.has(selector)) {
    let html = '', text = '';
    const item = { value: '', hidden: false, disabled: false, isConnected: true, attributes: {},
      classList: { add() {}, remove() {}, toggle() {} }, scrollIntoView() {},
      setAttribute(name, value) { this.attributes[name] = value; } };
    Object.defineProperties(item, {
      innerHTML: { get: () => html, set: value => { html = value; text = value; } },
      textContent: { get: () => text, set: value => { text = value; html = value; } }
    });
    elements.set(selector, item);
  }
  return elements.get(selector);
}
const context = {
  document: { querySelector: element, querySelectorAll: () => [], addEventListener: (kind, handler) => { handlers[kind] = handler; } },
  location: { hash: '#admin', search: '', origin: 'https://example.test' }, URL, URLSearchParams, Date, Intl,
  setTimeout, clearTimeout, window: { scrollTo() {} }, navigator: { clipboard: { writeText: async value => copied.push(value) } }
};
vm.createContext(context);
vm.runInContext(source.slice(0, source.indexOf("$('#menu-toggle').addEventListener")), context);
const run = code => vm.runInContext(code, context);
const plain = value => JSON.parse(JSON.stringify(value));
const campaign = { id: 9, reward_rank: 'bronze', reward_days: 30, reward_credits: 1000, email_count: 2, awarded_count: 1,
  emails: [{ email: 'one@example.test', awarded: true, awarded_at: '2026-10-22T01:00:00Z', rank_until: '2026-11-21T01:00:00Z' },
    { email: 'two@example.test', awarded: false, awarded_at: '', rank_until: '' }] };
context.campaign = campaign;
context.apiFixture = async (url, options) => {
  requests.push({ url, options });
  if (options) return { id: 12, code: 'ONCE-ONLY-CODE', expires_at: '2026-10-29T01:00:00Z', campaign };
  return campaign;
};
function makeForm(enabled, emails, permanent = false) {
  const button = element('#submit-button'); button.disabled = false;
  return { id: 'invite-form', elements: { max_uses: { value: '25' }, days: { value: '7' }, permanent: { checked: permanent }, campaign: { checked: enabled }, emails: { value: emails } },
    querySelector: () => button };
}
function clickTarget(selector, item) { return { closest: target => target === selector ? item : null }; }

(async () => {
  run("state.user={id:1,role:'admin'};state.boot={};state.admin={invites:[]};state.adminTab='invites';state.routeID=1;api=apiFixture;toast=()=>{};");
  context.value = '  One@Example.test\r\n\nTWO@example.test\none@example.test\n';
  assert.deepEqual(plain(run('parseInviteEmails(value)')), { emails: ['one@example.test', 'two@example.test'], duplicates: 1, invalidLines: [] });
  context.value = 'bad\n<a@example.test>\na@example.test,b@example.test\na@example.test';
  assert.deepEqual(plain(run('parseInviteEmails(value).invalidLines')), [1, 2, 3]);
  element('#invite-emails').value = ' A@example.test\na@example.test\nnot-an-email';
  handlers.input({ target: { id: 'invite-emails' } });
  assert.match(element('#invite-email-count').textContent, /1 个邮箱.*已去重 1 行.*1 行格式待检查/);

  const toggle = { id: 'invite-campaign-enable', checked: true, setAttribute(name, value) { this[name] = value; } };
  handlers.change({ target: toggle });
  assert.equal(element('#invite-campaign-fields').hidden, false);
  assert.equal(element('#invite-campaign-fields').disabled, false);
  assert.equal(toggle['aria-expanded'], 'true');
  toggle.checked = false;handlers.change({ target: toggle });
  assert.equal(element('#invite-campaign-fields').hidden, true);
  assert.equal(element('#invite-campaign-fields').disabled, true);
  handlers.change({ target: { id: 'invite-permanent', checked: true } });
  assert.equal(element('#invite-uses').disabled, true);
  assert.equal(element('#invite-days').disabled, true);
  assert(element('#invite-limit-note').textContent.includes('名单外成员也可持续注册'));
  assert(element('#invite-limit-note').textContent.includes('奖励仍只发给名单内首次注册'));
  handlers.change({ target: { id: 'invite-permanent', checked: false } });
  assert.equal(element('#invite-uses').disabled, false);
  assert.equal(element('#invite-days').disabled, false);
  assert(element('#invite-limit-note').textContent.includes('最多 100 次'));

  context.form = makeForm(false, 'hidden@example.test');
  assert.deepEqual(plain(run('inviteRequest(form)')), { max_uses: 25, days: 7 }, 'ordinary invite must omit reward fields and hidden emails');
  context.form = makeForm(true, 'ONE@example.test\ntwo@example.test\none@example.test');
  assert.deepEqual(plain(run('inviteRequest(form)')), { max_uses: 25, days: 7, emails: ['one@example.test', 'two@example.test'], reward_rank: 'bronze', reward_days: 30, reward_credits: 1000 });
  context.form = makeForm(false, 'hidden@example.test', true);
  context.form.elements.max_uses.value = '0';context.form.elements.days.value = '';
  assert.deepEqual(plain(run('inviteRequest(form)')), { permanent: true }, 'fixed invites must omit limits and inactive rewards');
  context.form = makeForm(true, 'ONE@example.test\none@example.test', true);
  assert.deepEqual(plain(run('inviteRequest(form)')), { permanent: true, emails: ['one@example.test'], reward_rank: 'bronze', reward_days: 30, reward_credits: 1000 });
  for (const bad of ['', 'bad', '<x@example.test>', 'a@example.test,b@example.test']) {
    context.form = makeForm(true, bad);assert.throws(() => run('inviteRequest(form)'));
  }
  context.form = makeForm(true, Array.from({ length: 1000 }, (_, n) => `u${n}@example.test`).join('\n'));
  assert.equal(run('inviteRequest(form).emails.length'), 1000);
  context.form.elements.emails.value += '\nu1000@example.test';
  assert.throws(() => run('inviteRequest(form)'), /1,000/);
  for (const max of ['0', '101', '1.5']) { context.form = makeForm(false, '');context.form.elements.max_uses.value = max;assert.throws(() => run('inviteRequest(form)')); }

  context.data = { invites: [{ id: 8, code: 'OLD-PLAIN-CODE', campaign: false, active: true, max_uses: 5, uses: 1 },
    { id: 9, code: 'ACCIDENTAL-CAMPAIGN-CODE', campaign: true, campaign_email_count: 2, active: true, max_uses: 25, uses: 1, emails: campaign.emails }] };
  run('state.admin=data;renderAdminContent()');
  const html = element('#admin-content').innerHTML;
  assert(html.includes('青铜 30 天'));
  assert(html.includes('1,000 永久积分'));
  assert(html.includes('已有账号不自动补发'));
  assert(html.includes('名单内外共用，最多 100 次'));
  assert(html.includes('name="campaign" type="checkbox"'));
  assert(html.includes('固定邀请码（长期有效）'));
  const permanentInput = html.match(/<input\b[^>]*name="permanent"[^>]*>/)[0];
  assert(!/\schecked(?:\s|>)/.test(permanentInput), 'permanent must default off');
  assert(html.includes('class="invite-campaign-fields" disabled hidden'));
  assert(html.includes('id="invite-email-count" class="hint" aria-live="polite"'));
  for (const secret of ['OLD-PLAIN-CODE', 'ACCIDENTAL-CAMPAIGN-CODE', 'one@example.test', 'two@example.test']) assert(!html.includes(secret), `history leaked ${secret}`);
  assert.equal(requests.length, 0, 'do not preload the private allowlist');
  context.fixed = { id: 10, permanent: true, max_uses: 99999999, expires_at: '9999-12-31', uses: 101, active: true };
  const fixedCard = run('adminInviteCard(fixed)');
  assert(fixedCard.includes('长期有效 · 不限次数 · 已用 101 次'));
  assert(!fixedCard.includes('99999999') && !fixedCard.includes('9999.12.31'));
  assert(fixedCard.includes('data-revoke="10"'));
  context.fixed.active = false;
  assert(run('adminInviteCard(fixed)').includes('已停用'));
  assert(!run('adminInviteCard(fixed)').includes('data-revoke'));
  assert(run('inviteCampaignDetails({...campaign,permanent:true})').includes('名单外也可注册，奖励仍仅限指定邮箱'));

  const campaignButton = element('#campaign-button');campaignButton.dataset = { inviteCampaign: '9' };
  const panel = element('#invite-campaign-9');panel.hidden = true;
  await handlers.click({ target: clickTarget('[data-invite-campaign]', campaignButton) });
  assert.equal(requests.at(-1).url, '/api/admin/invites/9/campaign');
  assert(panel.innerHTML.includes('one@example.test'));
  assert(panel.innerHTML.includes('已领取'));
  assert(panel.innerHTML.includes('未领取'));
  assert(panel.innerHTML.includes('30 天 + 1,000 永久积分'));
  assert.equal(campaignButton.attributes['aria-expanded'], 'true');
  await handlers.click({ target: clickTarget('[data-invite-campaign]', campaignButton) });
  assert.equal(panel.hidden, true);
  assert.equal(panel.innerHTML, '', 'closing must remove the private allowlist from the panel');
  assert.equal(campaignButton.attributes['aria-expanded'], 'false');

  context.malicious = { ...campaign, emails: [{ email: '<img src=x onerror=alert(1)>', awarded: false }] };
  assert(!run('inviteCampaignDetails(malicious)').includes('<img'));
  assert(run('inviteCampaignDetails(malicious)').includes('&lt;img'));
  run("api=async()=>{throw new Error('请求失败 RQ-private-test')}");
  await run('toggleInviteCampaign(document.querySelector("#campaign-button"))');
  assert(panel.innerHTML.includes('请求失败 RQ-private-test'));
  assert(!panel.innerHTML.includes('one@example.test'));
  await run('toggleInviteCampaign(document.querySelector("#campaign-button"))');

  let resolve;
  context.pending = () => new Promise(done => { resolve = done; });run('api=pending');
  const pending = run('toggleInviteCampaign(document.querySelector("#campaign-button"))');
  run("state.routeID++;state.user={role:'member'}");resolve(campaign);await pending;
  assert(!panel.innerHTML.includes('one@example.test'), 'late privileged response must not paint after a role/route change');
  const before = requests.length;run('api=apiFixture');
  await run('toggleInviteCampaign(document.querySelector("#campaign-button"))');
  assert.equal(requests.length, before);
  run("state.adminTab='invites';renderAdminContent()");
  assert.equal(run('state.adminTab'), 'queue', 'ordinary members cannot select invite management');
  assert(!element('#main').innerHTML.includes('邀请管理'));

  run("state.user={id:1,role:'admin'};state.adminTab='invites';state.routeID=5;api=apiFixture;state.admin={invites:[]}");
  context.form = makeForm(true, 'One@Example.test\none@example.test\ntwo@example.test');
  await handlers.submit({ target: context.form, preventDefault() {} });
  const posted = requests.at(-1);
  assert.equal(posted.url, '/api/admin/invites');
  assert.equal(posted.options.method, 'POST');
  assert.deepEqual(JSON.parse(posted.options.body).emails, ['one@example.test', 'two@example.test']);
  assert(element('#new-invite').innerHTML.includes('ONCE-ONLY-CODE'));
  assert(!element('#new-invite').innerHTML.includes('one@example.test'));
  assert(!JSON.stringify(run('state.admin')).includes('ONCE-ONLY-CODE'), 'single-use code is not retained in admin state');
  assert(!element('#admin-content').innerHTML.includes('one@example.test'));
  await handlers.click({ target: clickTarget('[data-copy-invite-code]', { dataset: { copyInviteCode: 'ONCE-ONLY-CODE' } }) });
  await handlers.click({ target: clickTarget('[data-copy-invite]', { dataset: { copyInvite: 'ONCE-ONLY-CODE' } }) });
  assert.deepEqual(copied, ['ONCE-ONLY-CODE', 'https://example.test/?invite=ONCE-ONLY-CODE']);
  assert(requests.every(request => !request.url.includes('@')), 'private allowlist never appears in a URL');

  context.form = makeForm(true, 'keep@example.test');
  run("api=async()=>{throw new Error('创建失败 RQ-create-test')}");
  await handlers.submit({ target: context.form, preventDefault() {} });
  assert.equal(context.form.elements.emails.value, 'keep@example.test', 'request failure must retain the input');
  assert.equal(element('#invite-error').textContent, '创建失败 RQ-create-test');
  assert.equal(element('#submit-button').disabled, false);
  const seen = requests.length;run("state.user={role:'reviewer'};api=apiFixture");
  await handlers.submit({ target: context.form, preventDefault() {} });
  assert.equal(requests.length, seen, 'reviewers cannot create campaigns');
  run("state.user={id:1,role:'admin'};state.adminTab='invites';state.routeID=7");
  context.apiFixture = async (url, options) => {
    requests.push({ url, options });
    return { id: 13, code: 'ORDINARY-CODE', expires_at: '2026-10-29T01:00:00Z' };
  };
  run('api=apiFixture');context.form = makeForm(false, 'hidden@example.test');
  await handlers.submit({ target: context.form, preventDefault() {} });
  assert.deepEqual(JSON.parse(requests.at(-1).options.body), { max_uses: 25, days: 7 });
  assert(element('#new-invite').innerHTML.includes('ORDINARY-CODE'));
  assert(!element('#new-invite').innerHTML.includes('奖励名单'));
  assert.equal(run('state.admin.invites[0].campaign'), false);
  context.form = makeForm(true, 'fixed@example.test', true);
  await handlers.submit({ target: context.form, preventDefault() {} });
  assert.deepEqual(JSON.parse(requests.at(-1).options.body), { permanent: true, emails: ['fixed@example.test'], reward_rank: 'bronze', reward_days: 30, reward_credits: 1000 });
  assert.equal(run('state.admin.invites[0].permanent'), true, 'creation fallback must retain fixed mode before overview reload');
  assert(element('#new-invite').innerHTML.includes('长期有效 · 不限次数，直到站长停用'));
  assert(!element('#admin-content').innerHTML.includes('时间待核对 到期'), 'fixed history must not render an empty expiry as an unknown date');
  run('renderRules()');
  assert(element('#main').innerHTML.includes('永久积分初始为 0，每日赠送 1 额度'));
  assert(!element('#main').innerHTML.includes('10 长期额度'));
  assert(!source.includes('<div class="welcome-number">10'));
  assert.equal(run("ledgerName('invite_campaign')"), '邀请活动奖励 · 永久积分');
  assert.equal(run("ledgerName('welcome')"), '历史新人长期额度');
  assert(css.includes('.invite-campaign-fields[hidden],.invite-campaign-panel[hidden]{display:none}'));
  assert(css.includes('.invite-campaign-table td{overflow-wrap:anywhere'));
  assert(css.includes('.invite-settings input,.invite-campaign-fields textarea{font-size:16px}'));
  console.log('PASS: admin-only campaigns, deduplicated 1–1000 emails, fixed/limited invitation modes and payloads, unlimited display without sentinel dates/counts, fixed rewards, one-time code receipt, private lazy statistics and guards, escaped data, safe copy, retained failures and mobile form styles.');
})().catch(error => { console.error(error); process.exitCode = 1; });
