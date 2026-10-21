# InfoGap v1 contract
Go 1.26 + modernc SQLite, static buildless web served by Go. Local listen 127.0.0.1:18869. PROD domain https://infogap.osh.lol. JSON all APIs. Times RFC3339 UTC. Errors {error:string}. Cookie session with HttpOnly SameSite Lax secure on production; origin check mutations. Frontend never receives locked body. No remote fonts/CDN/images. Science text source-first.

GET /api/bootstrap => {user:null|User,stats:{published,sources,members,pending},categories:[string],topic_codes:{主题名:代码},tiers:[{name,min_points,color}],rules:{welcome:10,contribution:100,unlock_costs:{ordinary:1,featured:2,rare:4,exclusive:8},daily_reset_timezone:"Asia/Shanghai"},mail_ready:bool}
User {id,email,username,role:'admin'|'reviewer'|'member',points,tier,tier_index,credits,daily_allowance,daily_remaining,contributions,featured_contributions,rare_contributions}
GET /api/gaps?q=&category=&level=&sort=latest|value&view=all|saved|unlocked|mine&page=1 => {items:[Gap],total,page}
Gap {id,code,title,summary,category,level:'ordinary'|'featured'|'rare'|'exclusive',evidence:'peer_reviewed'|'preprint'|'official'|'repository'|'unverified',source_name,source_url,published_at,created_at,updated_at,status:'pending'|'published'|'rejected'|'disputed'|'expired',tags:[string],author,unlocked:bool,saved:bool,body?:string,review_note?:string}
GET /api/gaps/{id} => Gap (no body unless unlocked or owner/admin; metadata published available to guest)
POST /api/gaps/{id}/unlock {} => {gap:Gap,user:User}; auth required; admin/reviewer free; all content uses daily allowance first, persistent credits second; rare silver index>=2 plus featured_contributions>=1; exclusive diamond index>=6 plus rare_contributions>=1 and last_quality_at within90days. Costs ordinary1/featured2/rare4/exclusive8 and permanent unlock. welcome10 credits. contribution approved =100credits, always. Quality points ordinary20 featured80 rare200 exclusive500; tier minima [0,100,300,700,1400,2500,4000,6500,10000,15000]. No caps reward. No self review reward bypass. Any rank may submit any proposed level.
POST /api/gaps/{id}/save {} => {saved:bool}
POST /api/gaps/{id}/report {reason} => {ok:true}
POST /api/auth/code {email,invite?} => {ok:true}; existing email or verified admin allow no invite. Invite required new other. No code in responses/logs; SMTP config env SMTP_HOST/PORT/USER/PASSWORD/FROM/SECURITY.
POST /api/auth/verify {email,code,username?,invite?} => {user:User}; atomic invite consume, admin only exact verified 1402272093@qq.com
POST /api/auth/logout {} => {ok:true}
POST /api/submissions {title,summary,body,category,source_url,source_name,level,evidence,tags:[string]} => {id,code,status:'pending'}
GET /api/me/activity => {ledger:[{kind,amount,created_at,note}],submissions:[Gap]}
GET /api/admin/overview => {stats,queue:[Gap],users:[User],invites:[{id,code,max_uses,uses,expires_at,active}],sources:[Source],reports:[...]}
POST /api/admin/invites {max_uses:1,days:7} => {code,expires_at}
POST /api/admin/invites/{id}/revoke {} => {ok:true}
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

数字直接使用 `gaps.id` 的全站 `AUTOINCREMENT` 序号，不创建按等级或主题分组的计数器。序号可跳号，不等于公开内容数量。重新审核、修改等级或主题时，只更新显示前缀，ID不变。列表、详情、后台、投稿响应和成员投稿记录返回同一规则生成的当前编号；详情、解锁等既有路径仍接收数字 `{id}`。

`GET /api/gaps?q=JX-AI-116` 识别完整新编号，兼容旧格式 `PT01`、`JX116`、`XY08`、`DJ205`，忽略字母大小写及首尾空白。新编号的等级和主题代码必须属于已定义映射，数字至少两位。`PT-01` 等两段格式和纯数字 `116` 不作为编号识别，继续按普通关键词搜索。

编号搜索按全站数字ID精确定位。为保留旧引用，合法的旧等级、旧主题前缀仍可定位同一ID，响应显示该内容当前的 `code`；前缀不替代 `level`、`category` 筛选。当前状态、视图、旅行条件、订阅、段位延迟等筛选照常生效。编号不会授予阅读权：未解锁正文不返回，内部出处继续隐藏，未到可见时间的内容也不会因命中编号而暴露。
