// Run with Playwright available on NODE_PATH. Uses an isolated headless Chrome;
// every request is served from local files or fixtures, never production.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require('playwright');
const root = path.resolve(__dirname, '..');
const origin = 'https://infogap-layout.test';
const widths = [1728, 1280, 1024, 760, 390, 375];
const user = { id: 1, username: '布局检查', role: 'member', tier: '黑铁', tier_index: 0, earned_tier: '黑铁',
  email: 'layout@example.test', points: 0, contributions: 0, credits: 0, daily_remaining: 1, daily_allowance: 1,
  rank_credits: 0, entitlements: { delay_hours: 72, subscriptions: false, subscription_limit: 0 } };
const plans = [...[[1,30,8,1800],[3,90,4,3800],[6,180,2,5800],[12,365,0,8800]].map(([months,days,delay,amount])=>({id:`reading_${months}`,name:`${months}个月畅读`,type:'reading',months,days,delay_hours:delay,amount_cents:amount,list_amount_cents:amount,discount_percent:100,unlimited_reading:true})),{id:'credits_100',type:'credits',name:'100永久积分',credits:100,amount_cents:990}];
const json = data => ({ status: 200, contentType: 'application/json', body: JSON.stringify(data) });
const output = [];
(async () => {
  const browser = await chromium.launch({ headless: true, channel: process.env.INFOGAP_BROWSER_CHANNEL || 'chrome' });
  try {
    const context = await browser.newContext({ viewport: { width: widths[0], height: 1000 }, reducedMotion: 'reduce' });
    await context.route('**/*', async route => {
      const url = new URL(route.request().url());
      if (url.origin !== origin) return route.abort();
      if (url.pathname === '/api/bootstrap') return route.fulfill(json({ user, stats: {}, category_counts: {}, rules: { welcome: 0 } }));
      if (url.pathname === '/api/billing/plans') return route.fulfill(json({ enabled: true, channels: ['alipay'], plans }));
      if (url.pathname === '/api/billing/orders') return route.fulfill(json({ items: [] }));
      if (url.pathname === '/api/billing/rank-status') return route.fulfill(json({ user, periods: [], batches: [] }));
      const relative = url.pathname === '/' ? 'index.html' : url.pathname.slice(1);
      if (!['index.html', 'app.js', 'style.css', 'music.js', 'music.css', 'feedback.js', 'feedback.css', 'vendor/qrcode.min.js'].includes(relative)) return route.fulfill({ status: 404, body: '' });
      const file = relative === 'style.css' && process.env.INFOGAP_LAYOUT_CSS ? process.env.INFOGAP_LAYOUT_CSS : path.join(root, 'web', relative);
      const type = relative.endsWith('.css') ? 'text/css' : relative.endsWith('.js') ? 'text/javascript' : 'text/html';
      return route.fulfill({ status: 200, contentType: type, body: fs.readFileSync(file) });
    });
    const page = await context.newPage();
    const errors = [];page.on('pageerror', error => errors.push(error.message));
    await page.goto(origin + '/#membership');
    await page.locator('.reading-plan').last().waitFor();
    await page.locator('.reading-plan-grid input[name=plan]').first().waitFor();
    for (const width of widths) {
      await page.setViewportSize({ width, height: 1000 });
      for (const theme of ['light', 'dark']) {
        await page.evaluate(value => { document.documentElement.dataset.theme = value; }, theme);
        const boxes = await page.evaluate(() => {
          const box = element => { const r = element.getBoundingClientRect();return { x: r.x, y: r.y, w: r.width, h: r.height, right: r.right, bottom: r.bottom }; };
          const grid = document.querySelector('.reading-plan-grid'), fieldset = document.querySelector('.billing-group');
          return { grid: box(grid), fieldset: box(fieldset), legendFloat: getComputedStyle(fieldset.querySelector('legend')).float,
            cards: [...grid.querySelectorAll('.reading-plan')].map(box),
            gridWidth: grid.clientWidth, gridScroll: grid.scrollWidth, pageWidth: document.documentElement.clientWidth,
            pageScroll: document.documentElement.scrollWidth };
        });
        const label = `${width}px ${theme}`;
        assert.equal(boxes.cards.length, 4, label);
        assert.equal(boxes.legendFloat, 'none', `${label}: nested duration legend must not inherit the billing legend float`);
        assert(boxes.grid.w >= boxes.fieldset.w - 2, `${label}: grid collapsed beside legend: ${JSON.stringify(boxes)}`);
        assert(boxes.gridScroll <= boxes.gridWidth + 1, `${label}: duration contents overflow`);
        assert(boxes.pageScroll <= boxes.pageWidth + 1, `${label}: page overflows horizontally`);
        for (const card of boxes.cards) {
          assert(card.w >= 100, `${label}: card too narrow (${card.w}px)`);
          assert(card.x >= boxes.grid.x - 1 && card.right <= boxes.grid.right + 1, `${label}: card outside grid`);
        }
        for (let i = 0; i < boxes.cards.length; i++) for (let j = i + 1; j < boxes.cards.length; j++) {
          const a = boxes.cards[i], b = boxes.cards[j];
          assert(a.right <= b.x + 1 || b.right <= a.x + 1 || a.bottom <= b.y + 1 || b.bottom <= a.y + 1, `${label}: cards overlap`);
        }
        const rows = [...new Set(boxes.cards.map(card => Math.round(card.y)))];
        assert.equal(rows.length, width <= 760 ? 2 : 1, `${label}: wrong row count`);
        output.push({ width, theme, grid: Math.round(boxes.grid.w), card: Number(boxes.cards[0].w.toFixed(2)), rows: rows.length, pageScroll: boxes.pageScroll });
      }
      for(const month of [1,3,6,12]){await page.locator('label.membership-plan').filter({has:page.locator(`input[value="reading_${month}"]`)}).click();assert.equal(await page.locator('input[name=plan]:checked').inputValue(),`reading_${month}`);}
    }
    await page.setViewportSize({width:390,height:844});await page.locator('input[value="reading_12"]').focus();await page.keyboard.press('ArrowLeft');assert.equal(await page.locator('input[name=plan]:checked').inputValue(),'reading_6');
    if (process.env.INFOGAP_LAYOUT_EVIDENCE) {
      fs.mkdirSync(process.env.INFOGAP_LAYOUT_EVIDENCE, { recursive: true });
      for (const width of [1728, 390]) {
        await page.setViewportSize({ width, height: width === 390 ? 844 : 1000 });
        await page.evaluate(() => { document.documentElement.dataset.theme = 'light'; });
        await page.locator('.billing-group').first().scrollIntoViewIfNeeded();
        await page.screenshot({ path: path.join(process.env.INFOGAP_LAYOUT_EVIDENCE, `reading-${width}.png`) });
      }
    }
    assert.deepEqual(errors, [], 'unexpected browser errors');
    console.log(JSON.stringify({ result: 'PASS', browser: await browser.version(), layouts: output, interaction: 'real clicks and keyboard choose exact reading package' }, null, 2));
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
