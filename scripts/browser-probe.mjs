#!/usr/bin/env node
// Public rendered DOM only. No login, CAPTCHA interaction, saved profile or LLM.
import { chromium } from 'playwright';
import { readFileSync } from 'node:fs';
import { lookup } from 'node:dns/promises';
import { isIP } from 'node:net';
import { createHash } from 'node:crypto';

const [mode,id,extraMode,statusID] = process.argv.slice(2);
const probes = {home:'https://linux.do/','topic-3011457':'https://linux.do/t/topic/3011457/19'};
const result = {checked_at:new Date().toISOString(),source_id:id||'',status:'error',http_status:null,error:'',candidates:[],
  diagnostics:{playwright_version:'1.64.0',uid:process.getuid(),sandbox_requested:true,mode:'headed-chromium-xvfb',
    visible_items:0,filtered_count:0,page_content_verified:false,blocked_resource_count:0,blocked_resource_hosts:{},navigation_statuses:[]}};
const started=Date.now();
let browser,context,account,targetURL,communityPolicy;
const dnsCache=new Map();
const linuxStaticHosts=new Set(['cdn.ldstatic.com','cdn3.ldstatic.com']);
const cfHosts=new Set(['challenges.cloudflare.com','static.cloudflareinsights.com']);
const xHosts=new Set(['x.com','www.x.com','api.x.com','twitter.com','api.twitter.com','abs.twimg.com','pbs.twimg.com','video.twimg.com','t.co']);
const sleep=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const clip=(value,n)=>String(value||'').replace(/\s+/g,' ').trim().slice(0,n);
const fail=(status,message)=>{const e=new Error(message);e.probeStatus=status;throw e;};
function publicIP(address){
  if(isIP(address)===4){const[a,b]=address.split('.').map(Number);return !(a===0||a===10||a===127||a>=224||(a===100&&b>=64&&b<=127)||(a===169&&b===254)||(a===172&&b>=16&&b<=31)||(a===192&&[0,168].includes(b))||(a===198&&[18,19].includes(b)));}
  return isIP(address)===6&&/^[23][0-9a-f]{3}:/i.test(address);
}
async function allowedRequest(raw){
  let u;try{u=new URL(raw);}catch{return false;}
  if(u.protocol!=='https:'||u.username||u.password||(u.port&&u.port!=='443'))return false;
  const h=u.hostname;
  if(!(cfHosts.has(h)||(!account&&(h==='linux.do'||h.endsWith('.linux.do')||linuxStaticHosts.has(h)))||(account&&(xHosts.has(h)||h.endsWith('.twimg.com')))))return false;
  if(!dnsCache.has(h))dnsCache.set(h,lookup(h,{all:true}).then(a=>a.length>0&&a.every(x=>publicIP(x.address))).catch(()=>false));
  return dnsCache.get(h);
}
function isoDate(value){if(!value)return '';const t=new Date(value);return Number.isNaN(t.valueOf())?'':t.toISOString().replace('.000Z','Z');}
function evidenceLinks(values,domains){
  const out=[];
  for(const raw of values||[]){
    let u;try{u=new URL(raw);}catch{continue;}
    if(u.protocol!=='https:'||u.username||u.password||u.port)continue;
    if(/\.(?:png|jpe?g|gif|webp|svg|mp4)(?:$|\?)/i.test(u.pathname)||u.hostname==='ldstatic.com'||u.hostname.endsWith('.ldstatic.com'))continue;
    if(['linux.do','x.com','twitter.com','t.co'].some(h=>u.hostname===h||u.hostname.endsWith('.'+h)))continue;
    if(domains&&!domains.some(h=>u.hostname===h||u.hostname.endsWith('.'+h)))continue;
    if(isIP(u.hostname)||u.hostname==='localhost'||u.hostname.endsWith('.local'))continue;
    u.hash='';for(const key of [...u.searchParams.keys()])if(/^utm_|^(fbclid|gclid)$/i.test(key))u.searchParams.delete(key);
    if(!out.includes(u.href))out.push(u.href);if(out.length>=5)break;
  }return out;
}
function draft(title,text,url,published,urls,extra={}){
  const internal=!account;
  return {title:clip(title,220),summary:`待核验线索｜${clip(title,110)}；原始证据与适用范围待审核。`,
    body:`## 核心进展\n\n**待核验候选**。以下是浏览器实际渲染正文中的短摘录：\n\n${clip(text,200)}\n\n## 为什么值得关注\n\n线索符合所选主题；具体发现、实际价值与适用条件仍需编辑核对。\n\n## 证据与边界\n\n只读取公开可见页面，未登录、未绕过人工验证，未自动认定为官方结论或同行评审论文。${internal?'社区名称和原帖链接仅供内部追溯；公开发布前须独立核验并清除正文中的内部出处。':''}\n\n## 编辑核验参考\n\n[原始页面](${url})${urls.map((u,i)=>`\n\n[待核验参考链接 ${i+1}](${u})`).join('')}`,
    category:account?.category||(/人工智能|模型|OpenAI|Anthropic|GPT|Claude|Gemini|DeepSeek|AI|LLM/i.test(title)?'人工智能':'前沿科技'),
    level:'ordinary',evidence:'unverified',status:'pending',source_visibility:internal?'internal':'public',
    source_name:account?`X · ${account.name}`:'LINUX DO 公开浏览器线索',source_url:url,published_at:isoDate(published),
    tags:['浏览器采集','待核验'],primary_evidence_urls:urls,
    review_note:`公开页面DOM核验；只存最多200字摘录，不搬运全文。${published?'':'未取得原始发布时间，需人工补核。'}${internal?'内部来源，公开发布前须编辑核验。':'白名单身份不代表本条已核验。'}`,...extra};
}
async function openRendered(page,url,selector){
  let navigationError='';
  try{await page.goto(url,{waitUntil:'domcontentloaded',timeout:25000});}catch(e){navigationError=clip(e.message.split('\n')[0],180);}
  // Allow ordinary JS and automatic intermediate responses to settle for 15 seconds.
  // Never click a challenge or import clearance cookies; no stealth or UA spoofing.
  const deadline=Date.now()+15000;
  while(Date.now()<deadline){
    if(await page.locator(selector).count()){
      const visible=await page.locator(selector).evaluateAll(es=>es.some(e=>e.getClientRects().length>0&&(e.innerText||e.textContent||'').trim().length>0));
      if(visible){result.diagnostics.page_content_verified=true;return;}
    }await sleep(400);
  }
  const title=await page.title().catch(()=>'');
  const text=await page.locator('body').innerText({timeout:1000}).catch(()=>'');
  result.diagnostics.page_title=clip(title,160);result.diagnostics.final_url=page.url();
  if(/just a moment|verify.*human|attention required|access denied|captcha|验证您|安全验证|访问被拒绝/i.test(title+' '+text.slice(0,1200))||[401,403,429].includes(result.http_status))fail('blocked',`浏览器正常加载后仍被限制（HTTP ${result.http_status||'unknown'}）`);
  if(/\/i\/flow\/login|\/login(?:\?|$)/.test(page.url())||/sign in to x|log in to x|登录.*查看|登录.*继续/i.test(text.slice(0,1500)))fail('login_required','公开正文未显示，页面要求登录');
  fail('error',navigationError||'页面已加载，但未找到可核验的公开正文或帖子列表');
}
function watchPage(page){
  page.setDefaultTimeout(1500);page.on('dialog',d=>d.dismiss().catch(()=>{}));
  page.on('response',r=>{if(r.request().isNavigationRequest()&&r.frame()===page.mainFrame()){result.http_status=r.status();if(result.diagnostics.navigation_statuses.length<12)result.diagnostics.navigation_statuses.push(r.status());}});
}
async function linuxList(page){
  await openRendered(page,targetURL,'a.title,a.raw-topic-link,a.topic-title');
  const links=await page.locator('a.title,a.raw-topic-link,a.topic-title').evaluateAll(es=>es.filter(e=>e.getClientRects().length>0).map(e=>({title:e.textContent.trim(),url:e.href,tags:Array.from(e.closest('tr,.topic-list-item')?.querySelectorAll('.discourse-tag')||[]).map(t=>t.textContent.trim())})));
  const seen=new Set();result.diagnostics.visible_items=links.length;
  for(const item of links){
    let u;try{u=new URL(item.url);}catch{continue;}
    const match=u.pathname.match(/^\/t\/(?:[^/]+\/)?(\d+)(?:\/\d+)?$/);
    if(u.hostname!=='linux.do'||!match||seen.has(match[1]))continue;seen.add(match[1]);
    if(!new RegExp(communityPolicy.topic_pattern,'i').test(item.title)||!new RegExp(communityPolicy.signal_pattern,'i').test(item.title)||new RegExp(communityPolicy.exclude_pattern,'i').test(item.title+' '+item.tags.join(' '))){result.diagnostics.filtered_count++;continue;}
    const postURL=`https://linux.do/t/topic/${match[1]}`;const post=await context.newPage();watchPage(post);
    try{
      await openRendered(post,postURL,'#post_1 .cooked,.topic-post:first-of-type .cooked');
      const data=await post.locator('#post_1,.topic-post').first().evaluate(el=>({text:el.querySelector('.cooked')?.innerText||'',date:el.querySelector('time[datetime]')?.getAttribute('datetime')||(el.querySelector('.post-date .relative-date[data-time]')?.getAttribute('data-time')?new Date(Number(el.querySelector('.post-date .relative-date[data-time]').getAttribute('data-time'))).toISOString():''),links:Array.from(el.querySelectorAll('.cooked a[href]')).map(a=>a.href)}));
      if(!data.text.trim()){result.diagnostics.filtered_count++;continue;}
      const references=evidenceLinks(data.links);
      const rawCharacters=Array.from(data.text);
      result.candidates.push(draft(item.title,data.text,postURL,data.date,references,{primary_evidence_urls:[],reference_urls:references,raw_text:rawCharacters.slice(0,20000).join(''),raw_text_length:rawCharacters.length,text_truncated:rawCharacters.length>20000,raw_text_scope:'main_post_visible_dom'}));
    }finally{await post.close();}
    if(result.candidates.length>=3)break;
  }
}
async function xProfile(page){
  await openRendered(page,targetURL,'article[data-testid="tweet"] [data-testid="tweetText"]');
  const posts=await page.locator('article[data-testid="tweet"]').evaluateAll(es=>es.filter(e=>e.getClientRects().length>0).map(e=>({text:e.querySelector('[data-testid="tweetText"]')?.innerText||'',url:e.querySelector('time')?.closest('a')?.href||'',date:e.querySelector('time')?.getAttribute('datetime')||'',author_url:e.querySelector('[data-testid="User-Name"] a[href]')?.href||'',context:e.querySelector('[data-testid="socialContext"]')?.innerText||'',article_text:(e.innerText||'').slice(0,2000),links:Array.from(e.querySelectorAll('a[href]')).map(a=>a.href)})));
  result.diagnostics.visible_items=posts.length;
  for(const post of posts){
    let u,author;try{u=new URL(post.url);author=new URL(post.author_url);}catch{result.diagnostics.filtered_count++;continue;}
    const match=u.pathname.match(/^\/([A-Za-z0-9_]+)\/status\/(\d+)$/),handle=author.pathname.replace(/^\/|\/$/g,'');
    const primary=evidenceLinks(post.links,account.primary_domains||[]);
    const promotional=/\b(?:subscribe|discount|coupon|giveaway|hiring|sponsored)\b|promo code|paid partnership|register now|优惠|折扣|抽奖|返佣|招聘|活动报名|订阅促销/i.test(post.text);
    const replyOrRepost=/reposted|retweeted|转帖|转发/i.test(post.context)||/(?:^|\n)(?:Replying to|回复\s?@|Promoted|Sponsored|广告|推广)(?:\s|$)/im.test(post.article_text);
    if(!match||match[1].toLowerCase()!==account.handle.toLowerCase()||handle.toLowerCase()!==account.handle.toLowerCase()||!post.text.trim()||!primary.length||promotional||replyOrRepost){result.diagnostics.filtered_count++;continue;}
    if(statusID&&match[2]!==statusID){result.diagnostics.filtered_count++;continue;}
    result.candidates.push(draft(post.text.split('\n')[0],post.text,`https://x.com/${account.handle}/status/${match[2]}`,post.date,primary,{author_handle:handle}));
    if(result.candidates.length>=5)break;
  }
}
try{
  if(process.getuid()===0)fail('error','拒绝以root运行浏览器');
  if(mode==='--probe'&&probes[id])targetURL=probes[id];
  else if(mode==='--source'&&id==='linuxdo-browser'){
    targetURL='https://linux.do/c/news/34';communityPolicy=JSON.parse(readFileSync('/opt/infogap-browser/data/sources.json','utf8')).find(s=>s.id==='linuxdo-tech');
    if(!communityPolicy)fail('error','未找到社区筛选规则');
  }else if(mode==='--source'&&/^x-[A-Za-z0-9_-]+$/.test(id||'')){
    const list=JSON.parse(readFileSync('/opt/infogap-browser/data/x-accounts.json','utf8'));account=list.accounts.find(a=>a.id===id&&a.enabled===true);
    if(!account||!Array.isArray(account.primary_domains)||!account.primary_domains.length)fail('error','X账号不在有效证据白名单中');
    if(!/^[A-Za-z0-9_]{1,15}$/.test(account.handle))fail('error','X账号格式不正确');targetURL=`https://x.com/${account.handle}`;
    if(extraMode){if(extraMode!=='--status'||!/^\d{1,25}$/.test(statusID||''))fail('error','X帖子参数不正确');targetURL+=`/status/${statusID}`;}
  }else fail('error','只接受已配置来源或固定诊断目标');
  result.diagnostics.requested_url=targetURL;
  browser=await chromium.launch({headless:false,chromiumSandbox:true,timeout:25000});result.diagnostics.chromium_version=browser.version();
  context=await browser.newContext({viewport:{width:1280,height:900},acceptDownloads:false});
  // Normal anonymous same-site XHR/guest activation is allowed. No action is clicked.
  await context.route('**/*',async route=>{if(!(await allowedRequest(route.request().url()))){result.diagnostics.blocked_resource_count++;try{const h=new URL(route.request().url()).hostname;result.diagnostics.blocked_resource_hosts[h]=(result.diagnostics.blocked_resource_hosts[h]||0)+1;}catch{}return route.abort('blockedbyclient');}return route.continue();});
  const page=await context.newPage();watchPage(page);
  if(mode==='--probe'){
    const selector=id==='home'?'a.title,a.raw-topic-link,a.topic-title':'#post_19 .cooked,.topic-body .cooked,.topic-post .cooked';
    await openRendered(page,targetURL,selector);result.diagnostics.page_title=clip(await page.title(),160);result.diagnostics.final_url=page.url();
    const texts=await page.locator(selector).evaluateAll(es=>es.filter(e=>e.getClientRects().length>0).map(e=>e.innerText||e.textContent||''));result.diagnostics.visible_items=texts.length;
    const text=texts.join('\n');result.diagnostics.rendered_text_chars=text.length;result.diagnostics.rendered_text_sha256=createHash('sha256').update(text).digest('hex');result.diagnostics.body_excerpt=clip(text,40);
    // The user-supplied example is a reachability check, never a research candidate.
  }else if(account)await xProfile(page);else await linuxList(page);
  if(!result.http_status||result.http_status<200||result.http_status>=300)fail('blocked',`最终页面HTTP状态为${result.http_status||'unknown'}`);
  result.status='ok';
}catch(e){result.status=e.probeStatus||'error';result.error=clip(String(e.message).split('\n')[0],220);result.candidates=[];}
finally{if(context)await context.close().catch(()=>{});if(browser)await browser.close().catch(()=>{});}
result.diagnostics.duration_ms=Date.now()-started;
console.log(JSON.stringify(result));
process.exitCode=result.status==='ok'?0:['blocked','login_required'].includes(result.status)?3:1;
