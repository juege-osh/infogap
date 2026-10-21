# InfoGap v1 contract
Go 1.26 + modernc SQLite, static buildless web served by Go. Local listen 127.0.0.1:18869. PROD domain https://infogap.osh.lol. JSON all APIs. Times RFC3339 UTC. Errors {error:string}. Cookie session with HttpOnly SameSite Lax secure on production; origin check mutations. Frontend never receives locked body. No remote fonts/CDN/images. Science text source-first.

GET /api/bootstrap => {user:null|User,stats:{published,sources,members,pending},categories:[string],tiers:[{name,min_points,color}],rules:{welcome:10,weekly:3,contribution:100},mail_ready:bool}
User {id,email,username,role:'admin'|'reviewer'|'member',points,tier,tier_index,credits,weekly_remaining,contributions,featured_contributions,rare_contributions}
GET /api/gaps?q=&category=&level=&sort=latest|value&view=all|saved|unlocked|mine&page=1 => {items:[Gap],total,page}
Gap {id,title,summary,category,level:'ordinary'|'featured'|'rare'|'exclusive',evidence:'peer_reviewed'|'preprint'|'official'|'repository'|'unverified',source_name,source_url,published_at,created_at,updated_at,status:'pending'|'published'|'rejected'|'disputed'|'expired',tags:[string],author,unlocked:bool,saved:bool,body?:string,review_note?:string}
GET /api/gaps/{id} => Gap (no body unless unlocked or owner/admin; metadata published available to guest)
POST /api/gaps/{id}/unlock {} => {gap:Gap,user:User}; auth required; admin/reviewer free; ordinary uses weekly3 then credits; featured credits; rare silver index>=2 plus featured_contributions>=1; exclusive diamond index>=6 plus rare_contributions>=1 and last_quality_at within90days. All paid 1credit and permanent unlock. welcome10 credits. contribution approved =100credits, always. Quality points ordinary20 featured80 rare200 exclusive500; tier minima [0,100,300,700,1400,2500,4000,6500,10000,15000]. No caps reward. No self review reward bypass. Any rank may submit any proposed level.
POST /api/gaps/{id}/save {} => {saved:bool}
POST /api/gaps/{id}/report {reason} => {ok:true}
POST /api/auth/code {email,invite?} => {ok:true}; existing email or verified admin allow no invite. Invite required new other. No code in responses/logs; SMTP config env SMTP_HOST/PORT/USER/PASSWORD/FROM/SECURITY.
POST /api/auth/verify {email,code,username?,invite?} => {user:User}; atomic invite consume, admin only exact verified 1402272093@qq.com
POST /api/auth/logout {} => {ok:true}
POST /api/submissions {title,summary,body,category,source_url,source_name,level,evidence,tags:[string]} => {id,status:'pending'}
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
