# InfoGap v1 contract
Go 1.26 + modernc SQLite, static buildless web served by Go. Local listen 127.0.0.1:18869. PROD domain https://infogap.osh.lol. JSON all APIs. Times RFC3339 UTC. Errors {error:string}. Cookie session with HttpOnly SameSite Lax secure on production; origin check mutations. Frontend never receives locked body or original source identity. No remote fonts/CDN/images. Science text source-first.

GET /api/bootstrap => {user:null|User,stats:{published,sources,members,pending},categories:[string],topic_codes:{主题名:代码},tiers:[{name,min_points,color}],rules:{welcome:0,contribution:100,contribution_credits:{ordinary:100,featured:200,rare:400,exclusive:800},contribution_points:{ordinary:20,featured:80,rare:200,exclusive:500},rank_gift_valid_days:30,unlock_costs:{ordinary:1,featured:2,rare:4,exclusive:8},daily_reset_timezone:"Asia/Shanghai"},mail_ready:bool}
User {id,email,username,role:'admin'|'reviewer'|'member',points,tier,tier_index,credits,rank_credits,rank_credits_expires_at,available_credits,daily_allowance,daily_remaining,contributions,featured_contributions,rare_contributions}
GET /api/gaps?q=&category=&level=&sort=latest|value&view=all|saved|unlocked|mine&page=1 => {items:[Gap],total,page}
Gap {id,code,title,summary,category,level:'ordinary'|'featured'|'rare'|'exclusive',evidence:'peer_reviewed'|'preprint'|'official'|'repository'|'unverified',source_name,source_url,published_at,created_at,updated_at,status:'pending'|'published'|'rejected'|'disputed'|'expired',tags:[string],author,unlocked:bool,saved:bool,body?:string,review_note?:string}
GET /api/gaps/{id} => Gap (body and original public source require unlocked/owner/staff; visible preview metadata available to guest; internal source remains staff-only)
POST /api/gaps/{id}/unlock {} => {gap:Gap,user:User}; auth required; admin/reviewer free; all content uses daily allowance, earliest-expiring rank gifts, then permanent credits; rare requires silver index>=2; exclusive requires diamond index>=6. Costs ordinary1/featured2/rare4/exclusive8 and permanent unlock. New ordinary registrations start with 0 permanent credits; eligible invite campaigns grant their explicit reward once. contribution approved credits ordinary100/featured200/rare400/exclusive800, once. Quality points ordinary20 featured80 rare200 exclusive500; tier minima [0,100,300,700,1400,2500,4000,6500,10000,15000]. No caps reward. No self review reward bypass. Any rank may submit any proposed level.
POST /api/gaps/{id}/save {} => {saved:bool}
POST /api/gaps/{id}/report {reason} => {ok:true}
POST /api/auth/code {email,invite?} => {ok:true}; existing email or verified admin allow no invite. Invite required new other. No code in responses/logs; SMTP config env SMTP_HOST/PORT/USER/PASSWORD/FROM/SECURITY.
POST /api/auth/verify {email,code,username?,invite?} => {user:User}; atomic invite consume, admin only exact verified 1402272093@qq.com
POST /api/auth/logout {} => {ok:true}
POST /api/submissions {title,summary,body,category,source_url,source_name,level,evidence,tags:[string]} => {id,code,status:'pending'}
GET /api/me/activity => {ledger:[{kind,amount,created_at,note,expires_at}],submissions:[Gap]}
GET /api/admin/overview => {stats,queue:[Gap],users:[User],invites:[{id,code,max_uses,uses,expires_at,active,permanent,campaign,campaign_email_count}],sources:[Source],reports:[...]}
POST /api/admin/invites {max_uses:1,days:7} 或 {permanent:true} => {id,code,expires_at,permanent,campaign?}
GET /api/admin/invites/{id}/campaign => {id,permanent,reward_rank,reward_days,reward_credits,email_count,awarded_count,emails:[{email,awarded,awarded_at,rank_until}]}（仅站长）
POST /api/admin/invites/{id}/revoke {} => {ok:true}

`permanent:true` 的固定邀请码不限次数、长期有效，直到撤销；`max_uses` 和 `days` 不参与限制。普通邀请码仍限 1–100 次、1–90 天。活动配置通过创建请求的 `emails[]/reward_rank/reward_days/reward_credits` 提交，实际名单只保存在数据库并限站长读取。固定码及活动码仅创建时返回，后续列表与详情不返回该码；完整规则见 `docs/invite-campaigns.md`。
POST /api/admin/review/{id} {action:'approve'|'reject'|'dispute'|'expire',level, note} => {ok:true} approve only pending/rejected; once reward; dispute refunds unlock credits once; marks cannot consume, preserve audit.
POST /api/admin/users/{id} {role?,disabled?} => {ok:true}; only admin grant reviewer (never admin). protect founder.
POST /api/admin/sources/{id} {enabled:bool} => {ok:true}
GET /healthz => {ok:true,version,service:'infogap',...}

SQLite content tables shared with Python collector (backend creates schema):
gaps(id INTEGER PK AUTOINCREMENT,title TEXT,summary TEXT,body TEXT,category TEXT,level TEXT DEFAULT 'ordinary',evidence TEXT,source_name TEXT,source_url TEXT UNIQUE,published_at TEXT,created_at TEXT,updated_at TEXT,status TEXT DEFAULT 'pending',tags TEXT DEFAULT '[]',author_id INTEGER NULL,review_note TEXT DEFAULT '',rewarded INTEGER DEFAULT 0)
sources(id TEXT PK,name TEXT,url TEXT,kind TEXT,category TEXT,enabled INTEGER DEFAULT 1,last_attempt TEXT,last_success TEXT,last_error TEXT DEFAULT '',last_count INTEGER DEFAULT 0)
Collector only inserts candidate pending content. Never automatically reward/claim human approval. Curated initial seed vetted by agent may be explicitly published during deployment import. Collector standard Python3 only, HTTP RSS/Atom/GitHub JSON; update sources stats no secrets. Backend module/schema owner agent. Frontend owner web/* agent. Collector owner scripts/collect.py,data/sources.json,data/seed.json plus source docs agent. Main integration/deploy/docs. Any contract amendments message teammates.

## 补充接口与权限

段位时效、订阅配置、会员有效期和站长全状态查询的最终字段与规则见 `docs/backend.md`；会员计划、订单、查询与验签回调见 `docs/billing.md`。`healthz.commit` 为实际运行的Git提交。

支付仅支持支付宝：`GET /api/billing/plans` 返回 `channels:["alipay"]`，`POST /api/billing/orders` 的 `channel` 必须为 `"alipay"`。其他渠道的新订单会被拒绝。历史微信订单保留本人查询、回调核验与幂等结算，但不再提供微信付款入口。

GET /api/admin/overview additionally returns summary_jobs:{pending,completed,failed,too_long,edited,processing}; staff only. No source text, credentials or model output logs are exposed.


## 1.0.3 旅行筛选

`GET /api/bootstrap` 增加 `travel_topics`（7个旅行标签）和 `travel_regions`（12个地区）。分类总数为28，出行旅行使用同一套额度与段位规则。

`GET /api/travel/facets` 返回 `{countries:[]}`。国家仅从当前用户可见、已发布的旅行内容中读取；不会因筛选选项泄露尚未到可见时间的条目。

`GET /api/gaps?country=日本&region=东亚&travel_topic=优惠渠道` 按三项精确AND匹配，并限定“出行旅行”。各项均可省略，国家可输入未收录名称并返回空结果，不做模糊匹配。

投稿与后台编辑继续使用 `tags`；旅行国家、地区、旅行类型分别保存为 `国家:日本`、`地区:东亚`、`旅行:优惠渠道`。页面提供普通中文表单，其他标签与旅行信息合计最多10个标签。

每条内容的 `category` 是唯一主题，必须是28个已定义主题中的单个字符串；数组和多个主题拼接值均拒绝。投稿和编辑 `tags` 最多10项，超过10项返回错误，不静默截断。

## 1.0.4 内容编号

内容响应的 `code` 格式为 `等级代码-主题代码-全站入库ID`。例如 `JX-AI-116`、`PT-LX-205`、`XY-WL-08`。等级映射：`ordinary→PT`、`featured→JX`、`rare→XY`、`exclusive→DJ`。数字部分为正整数，至少两位，不足两位补零；超过99不截断。

`GET /api/bootstrap` 的 `topic_codes` 返回下表同一映射，键为主题中文名，值为代码。每条内容仍只有一个 `category`，标签不参与生成编号。

| 主题 | 代码 | 主题 | 代码 |
|---|---|---|---|
| 前沿科技 | KJ | 人工智能 | AI |
| 生命科学 | SM | 物理 | WL |
| 化学 | HX | 地球与地理 | DL |
| 天文与空间 | TW | 材料科学 | CL |
| 开源技术 | KY | 军事与国防 | JS |
| 政治与公共政策 | ZZ | 农业与食品 | NY |
| 社会民生 | MS | 法律法规 | FL |
| 航空航天 | HT | 经济与产业 | JJ |
| 能源与环境 | NH | 医疗健康 | YL |
| 教育与就业 | JY | 交通与基建 | JT |
| 公共安全 | AQ | 生活实用 | SH |
| 省钱优惠 | SQ | 招聘求职 | ZP |
| 职业成长 | ZY | 住房居住 | ZF |
| 出行旅行 | LX | 商业创业 | CY |

数字直接使用 `gaps.id` 的全站 `AUTOINCREMENT` 序号，不创建按等级或主题分组的计数器。序号可跳号，不等于公开内容数量。待审稿首次定级按审核结果生成编号。自1.0.14起，已发布稿通过修订更新时固定修订前的编号，改变等级或主题也不会改变已有编号；实际阅读门槛、成本和分类仍按当前字段执行。列表、详情、后台、投稿响应和成员投稿记录返回一致编号；详情、解锁等既有路径仍接收数字 `{id}`。

`GET /api/gaps?q=JX-AI-116` 识别完整新编号，兼容旧格式 `PT01`、`JX116`、`XY08`、`DJ205`，忽略字母大小写及首尾空白。新编号的等级和主题代码必须属于已定义映射，数字至少两位。`PT-01` 等两段格式和纯数字 `116` 不作为编号识别，继续按普通关键词搜索。

编号搜索按全站数字ID精确定位。为保留旧引用，合法的旧等级、旧主题前缀仍可定位同一ID，响应显示该内容当前的 `code`；前缀不替代 `level`、`category` 筛选。当前状态、视图、旅行条件、订阅、段位延迟等筛选照常生效。编号不会授予阅读权：未解锁正文不返回，内部出处继续隐藏，未到可见时间的内容也不会因命中编号而暴露。

## 站长测试模式与功能自检（1.0.6）

`User` 新增 `test_mode`、`can_test_mode`。仅原始登录会话属于已验证站长时，允许切换测试模式或执行自检；前端传入用户 ID、角色等额外字段不会被接受。

- `POST /api/admin/test-mode`：`{tier:0..9,credits:0..100000,member:bool,quality:bool,reset:bool}`。创建或复用独立测试账号，并仅对当前登录会话启用。每次应用重置今日额度和测试长期额度；`reset=true` 另清空该测试账号的解锁、收藏、订阅和额度流水。返回 `{user}`。
- `DELETE /api/admin/test-mode`：当前会话返回原站长身份，返回 `{user}`。
- `POST /api/admin/diagnostics`：`{group:"all"|"service"|"auth"|"reading"|"subscriptions"|"payment"}`。返回 `{id,version,commit,at,group,duration_ms,passed,checks:[{name,status,detail}],notice}`。`status` 为 `passed/failed/ready/unavailable`；配置未就绪会使 `passed=false`。业务检查使用独立内存数据库，服务检查只读生产配置和数据库。

测试账号没有独立邮箱登录入口，不计入普通成员数量，也不出现在成员管理列表。测试模式按真实用户逻辑限制阅读、时效、额度和订阅；不能发起真实付款、正式投稿或反馈。站长另开的登录会话仍保持管理员身份。

`POST /api/billing/orders` 额外支持站长在管理员模式传入 `{plan:"payment_test",channel:"alipay"}`。金额由服务端固定为 1 分，订单 `type="payment_test"`、`days=0`、`credits=0`；核验成功只更新订单和支付事件，不发放会员或额度。普通成员、审核员和测试用户均无法创建。测试套餐不出现在普通套餐目录。

API 错误包含 `request_id`，并通过 `X-Request-ID` 返回。服务器错误日志记录相同编号、接口路由、状态和耗时，不记录邮箱、验证码或正文。


## 畅读套餐、积分与历史支付兼容

`GET /api/billing/plans` 仅出售 `reading_1/3/6/12` 四个畅读套餐和三个永久积分包。畅读套餐 `type="reading"`，1/3/6/12个月对应30/90/180/365天，标价1800/3800/5800/8800分，`delay_hours=8/4/2/0`，`unlimited_reading=true`。返回 `list_amount_cents`（标价）、`discount_percent`（整数百分比）和 `amount_cents`（实付）。贡献值100/300/700/1400/2500对应95/92/90/85/80%，只对畅读套餐有效，最终金额四舍五入到分。订单保存实际快照，待付款期间贡献变化不重定价。

`User.reading_active/reading_until/reading_delay_hours` 表示当前畅读及连续有效期；`entitlements.unlimited_reading` 表示无限阅读权限。新套餐付款后接续既有付费时段，速度在对应周期开始时改变。`GET /api/billing/reading-status` 返回 `{user,periods:[{plan,name,delay_hours,starts_at,ends_at}],renewal_policy:"append"}`。`periods` 列出新套餐；`user` 合并成功历史段位与会员订单的有效权益，不根据旧缓存或邀请活动判断付费状态。

付费段位停售，新建 `rank_*` 或调用升档报价返回410。历史订单的金额、时长和赠分快照不变，仍可查询及幂等结算。旧段位成功订单按原档位且最多8小时延迟保留畅读；旧 `monthly/yearly` 成功订单按8小时保留。旧订单迟到付款也接续已有付费时段。旧段位未来赠分继续按原快照发放，政策2从每期计划应到账日起30天有效，过期月份不会因补发复活；历史政策1永久赠分不回收。

`User.credits` 为永久积分；`invite_credits` 为有效邀请赠分；`rank_credits` 为有效历史段位赠分。后两者的 `*_expires_at` 返回最近有余额批次到期时间（RFC3339，无则空）。`available_credits = daily_remaining + invite_credits + rank_credits + credits`。按每日赠分、邀请赠分、历史限时段位赠分、永久积分依次扣减。

新邀请名单奖励一次1000积分，30天有效，不授予畅读或段位。旧邀请剩余部分仅在永久账本和余额可核对时迁到30天批次，已用部分不追扣，不改购买或贡献积分；无法核对时原余额不动并留迁移凭证。退款只回原未过期批次，不延长时间，不变成永久积分。

`GET /api/me/activity` 的流水包含 `expires_at`。新邀请仍使用 `invite_campaign` 并附到期时间，旧记录为空；邀请其他类型为 `invite_unlock/invite_refund/invite_expire/invite_migrate_out/invite_migrate_in`。历史段位为 `rank_gift/rank_unlock/rank_refund/rank_expire`，旧 `rank_monthly_gift` 表示历史永久赠分。`GET /api/billing/rank-status` 保留历史期数、下期赠分及有效批次查询。详细结算与迁移规则见 `docs/billing.md`。


## 1.0.11 原始来源随详情解锁

未解锁的列表、详情、收藏和搜索响应不提供原始来源：`source_name/source_url` 为空，正文不返回，预览中的原来源名称、链接和域名同步清理。证据类型仍可显示，例如预印本、已发表论文。搜索、订阅标签及旅行筛选使用相同清理规则，不能通过条件推断被锁定来源。

已解锁者及投稿者本人可看公开来源，管理员和审核员可直接核查。`source_visibility=internal` 的私人社区来源仍仅审核人员可见，普通成员即使已解锁也不会获得其原始出处。来源字段保留在数据库，响应清理不改原始记录。


## 1.0.14 我发布的、修订审核与人工扣分

`GET /api/me/submissions?status=all|pending|published|rejected|disputed|expired&q=&category=&page=1` 返回 `{items:[SubmissionItem],total,page}`，每页20条，只包含本人投稿。支持标题/摘要关键词、完整编号和主题筛选。本人读取原稿全文与公开出处不扣积分，也不创建解锁记录；私人社区出处继续隐藏。其他用户即使已解锁，也不能通过作者编辑接口查看原稿或待审修订。

`GET /api/submissions/{id}/edit` 返回 `SubmissionItem`，限作者本人或站长。其他用户返回404。结构如下：

```text
SubmissionItem {gap:Gap,edit_token,revision:null|Revision,editable:bool}
Revision {id,gap_id,status:'pending'|'rejected'|'approved'|'superseded',review_note,created_at,updated_at,edit_token,draft:Gap,original:Gap}
```

`gap` 始终是当前原稿；存在修订时，编辑表单使用 `revision.draft`。`revision` 只包含当前待审或退回稿。证伪和失效记录只读，`editable=false`。内部来源在当前原稿或历史修订任一版本被标为私密后，成员响应均隐藏对应出处；站长与审核员保留核查能力。

`POST /api/submissions/{id}/edit` 接收 `{title,summary,body,category,level,evidence,tags,edit_token}`，返回更新后的 `SubmissionItem`。提交 `item.edit_token`，不要提交 `revision.edit_token`。标题8–360字、摘要15–300字、正文存稿80–15000字、单个主题、标签最多10个，每个标签最多24字；`level` 只是作者建议。原始链接、来源名、来源可见性固定，传入这些额外字段会被拒绝。测试模式不能修改正式稿件。

- 待审或退回稿：原稿更新为待审，清除上一轮退回原因，并更新审核快照。
- 已发布稿：另存待审修订，公开原稿、首次发布时间、编号和已解锁权限全部保留。再次提交会将之前待审或退回修订标为 `superseded`。
- 并发编辑或页面过期：返回409，需重新读取后再提交。编辑与审计写入同一事务，保存失败不留下半份稿件。

旧的 `POST /api/admin/gaps/{id}` 继续编辑未发布稿。对已发布稿返回409，站长应改走上述修订接口。审核员可以核验修订，但不能通过作者接口编辑他人的已发布稿件。

`GET /api/admin/revisions?status=pending|rejected|approved|superseded` 返回 `{items:[Revision]}`，默认待审，最多200条。`GET /api/admin/overview` 另返回 `revisions` 待审列表。两者仅审核员或站长可读。

`POST /api/admin/revisions/{id}/review` 接收 `{action:'approve'|'reject',level,note,edit_token}`，使用该修订的 `revision.edit_token`，返回 `{ok:true,gap_id,status}`。作者不能审核自己的修订。退回原因至少3字，原因最多2000字；批准需指定最终等级，稀有/独家需至少10字核验依据。发布修订正文必须为300–1000字，中文内容完整，不能残留内部社区标识。若原稿状态或内容已经变化，返回409，不覆盖新版本。

修订批准在同一条内容上替换文字、主题、标签和最终等级；原来源、首次发布时间、解锁记录与首次贡献奖励事实不变。首次批准修订时将旧编号写入 `gaps.stable_code`，以后继续沿用；`edit_version` 作为单调增加的内部编辑版本参与快照，防止同一秒重投相同文字误用旧审核结果。修订不二次发奖，作者自评也不能决定最终贡献奖励。待审修订不会出现在公开列表、搜索、收藏或普通详情中。

`POST /api/admin/users/{id}/credits/deduct` 仅站长可用，接收 `{amount,reason,idempotency_key}`：

- `amount` 为正整数，只扣永久积分，余额不足返回409，余额不会变负数；每日赠分、段位赠分、成长点和贡献资格不变。
- `reason` 去首尾空白后3–500字，原字符串也不得超过500字；保存原文作为账本与审计依据。
- `idempotency_key` 为8–100字，不含控制字符或首尾空白。重试同一请求必须沿用同一key和完全相同的用户、金额、原因；参数不同返回409。
- 站长、测试账号不能作为扣分目标，站长测试模式也不能操作。普通成员和审核员（包括已停用账户）可纠正永久余额。停用账号仍使用原 `POST /api/admin/users/{id}` 的 `disabled` 字段；退稿不会自动扣积分。

成功与一致重试都返回200：`{receipt:{idempotency_key,actor_id,target_user_id,amount,reason,before_credits,after_credits,ledger_id,created_at},user:User,replayed:bool}`。`receipt` 保留当次快照，`user` 返回最新余额。扣减、`ledger.kind=admin_deduct` 负数流水、幂等回执和审计同一事务提交。网络结果不确定时用原key重试，不重复扣分。


`POST /api/admin/invites/{id}/campaign/emails` 接收 `{emails:[...]}`，向既有活动追加奖励邮箱，返回 `{added,existing,total}`，不返回名单或邀请码。仅真实站长可用，测试模式禁止操作。活动必须仍有效；撤销、过期或次数用尽返回409，不存在或普通非活动邀请码返回404。固定邀请码继续沿用原码，不改变有效期、使用次数规则或奖励。

邮箱沿用创建活动时的格式校验，去首尾空白、转小写并去重。单次输入最多1000条，活动累计最多1000个不同邮箱，超限整批拒绝。`existing` 按本批规范化后已在活动中的不同邮箱数计算，重复提交返回 `added:0`。只追加，不删除原成员、不更改已发奖励；新加邮箱仍须在今后的首次注册时满足活动条件，已有账号不会补发。追加名单和计数审计同事务提交，失败完整回滚。

## 单独购买课程

`POST /api/billing/orders` 支持 `{plan:"course_<正整数课程ID>",channel:"alipay"}`。只允许已发布且已定价课程，课程价格由服务端读取，不适用贡献折扣。响应订单为 `type:"course"`，新增 `course_id`，并带 `name/list_amount_cents/amount_cents/discount_percent:100`。`days/credits/months` 为0，`unlimited_reading` 为false。未发布或不存在返回404，未定价或已拥有返回409。

课程不出现在 `/api/billing/plans` 的固定套餐列表。沿用本人订单查询和支付宝核验；成功只授予对应课程访问权限，不增加积分或会员权益。已生成订单使用价格快照，修改课程价格不影响该订单。客户端不能提交价格、课程授权、积分数量或用户编号。课程正文及媒体权限由课程接口单独检查，畅读与阅读积分不能解锁课程。
