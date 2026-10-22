# 阅读套餐与积分支付

InfoGap 复用已有汇付签名适配器，使用独立订单和回调地址。新订单只支持支付宝，出售四种畅读套餐和三个永久积分包。付费段位已停止销售；贡献值保留，用于排行榜和套餐折扣，购买不会增加贡献值。

| 套餐 ID | 时长 | 有效天数 | 标价 | 内容开放延迟 |
|---|---:|---:|---:|---:|
| `reading_1` | 1个月 | 30 | ¥18 | 8小时 |
| `reading_3` | 3个月 | 90 | ¥38 | 4小时 |
| `reading_6` | 6个月 | 180 | ¥58 | 2小时 |
| `reading_12` | 12个月 | 365 | ¥88 | 实时 |

套餐有效期内，已到开放时间的详情可以直接阅读，不扣积分。开放时间从本站首次审核发布起算。畅读不会生成永久解锁记录；到期后仍可阅读此前用积分解锁的内容，其余内容恢复按条解锁。续费接在所有已购买时段之后，不提前消耗新时长；不同套餐的时效在对应周期开始时切换，不自动续费。

贡献值达到100、300、700、1400、2500，套餐分别按标价的95%、92%、90%、85%、80%计费。未达100按原价。服务端用整数分计算 `round(标价分 × 折扣百分比 / 100)`，只在最后一步四舍五入。订单保存下单时的标价、折扣与实付金额；之后贡献值变化不重定价待付款订单。

永久积分包为 `credits_100`（¥9.90 / 1000分）、`credits_350`（¥29 / 3500分）、`credits_1000`（¥79 / 10000分）。ID保留历史名称，展示数量以API的 `credits` 为准。积分包不参与贡献折扣。贡献奖励、购买积分以及历史永久段位赠分仍放在 `users.credits`，不会到期。

## 邀请赠分、历史订单与退款

新邀请活动只向名单内新注册用户发一次1000邀请赠分，从到账起30天有效，不授予付费段位或畅读。旧邀请赠分只迁移可核对的剩余部分：逐条核对永久流水与当前余额，优先把旧解锁、人工扣分、撤奖消耗归到邀请赠分，购买和贡献收入不补回邀请池。已使用的不追扣。迁移后总可用积分不减少，迁移部分从迁移日起30天有效。无法证明余量归属的账户保持原余额，记录原因供人工核对。

解锁扣减顺序为：每日赠分 → 邀请赠分 → 历史限时段位赠分 → 永久积分。内容证伪时，永久消耗退回永久余额；每日赠分仅当日退回；邀请和段位赠分只退到尚未过期的原批次，不延长有效期，也不变成永久积分。迁移同时拆分可确认的旧解锁消耗，保证以后退款仍回原来源。

旧 `rank_<tier>_<months>` 和升档套餐不再接受新订单，创建或升档报价返回410。已有订单仍可查询和核验，已付与未付的金额、天数、赠分快照都不改。旧付费段位按成功订单关联的 `rank_periods` 保留畅读，延迟按原档位并以8小时封顶。旧 `monthly/yearly` 成功订单通过 `billing_grants` 保留8小时畅读。旧订单迟到付款也会接在已有付费期之后。只有缓存的 `paid_tier/paid_until/membership_until` 或邀请奖励记录不能授予畅读。

历史段位订单未来各期仍按订单快照发放；政策2赠分从计划应到账日起30天有效，漏领的过期月份不会复活。历史已到账的政策1永久赠分不回收。旧升档订单仍按保存的差价快照处理，补差赠分单独从生效日起30天，原批次不续期。新畅读套餐不发段位赠分。

## 配置与启动

后端启动调用 `InitBilling(s)`，路由调用 `s.billingRoutes(mux)`。初始化只建本项目账单表并检查配置，不连接支付网关，不创建订单。只有 `PAY_PROVIDER=huifu` 且完整配置可读时才启用支付。`mock` 不会启用线上支付。

沿用配置键名：

- `PAY_PROVIDER`
- `PAY_HUIFU_BASE_URL`、`PAY_HUIFU_SYS_ID`、`PAY_HUIFU_PRODUCT_ID`、`PAY_HUIFU_ID`
- `PAY_HUIFU_PRIVATE_KEY_FILE`、`PAY_HUIFU_PLATFORM_PUBLIC_KEY_FILE`
- `PAY_HUIFU_NOTIFY_URL`
- 可选 `PAY_HUIFU_PROXY_TOKEN`、`PAY_HUIFU_CONNECT_TIMEOUT_MS`、`PAY_HUIFU_READ_TIMEOUT_MS`

部署可先加载 `/etc/prompt-osh/huifu.env`，再加载 InfoGap 独立配置，覆盖通知地址为 `https://infogap.osh.lol/api/billing/notify/huifu`。不要修改共享配置中的其他站点通知地址。通知地址必须与 `INFOGAP_PUBLIC_URL` 加上述路径完全一致；网关和通知地址须使用 HTTPS。进程需要读取密钥文件，但不要将文件内容放入代码、文档、日志或前端。

配置不完整时，套餐接口仍返回价格，但 `enabled=false` 并给出 `disabled_reason`，开单返回 503。配置完整只表示本地准备好，不能替代真实支付验收。

支付宝沿用汇付 PC 聚合正扫：预下单使用 `/v2/trade/payment/jspay` 与 `trade_type=A_NATIVE`，查单使用 `/v2/trade/payment/scanpay/query`。HTTP 开单接口和支付适配器都拒绝其他新渠道。历史微信订单仍可查询、核验回调并结算，数据库保留其渠道值，但不再输出微信付款链接或二维码。

## 前端接口

| 方法与路径 | 请求或返回 |
|---|---|
| `GET /api/billing/plans` | `{enabled,disabled_reason,plans:[{id,name,type,list_amount_cents,discount_percent,amount_cents,days,months,delay_hours,unlimited_reading,credits}],channels:["alipay"],auto_renew:false}` |
| `POST /api/billing/orders` | 请求 `{plan:套餐ID,channel:"alipay"}`；返回 `{order,message?}` |
| `GET /api/billing/orders` | 登录用户最近 50 笔订单 `{items:[Order]}` |
| `GET /api/billing/orders/{id}` | 核对本人订单，返回 `{order,user,message?}`；未终结订单最多每 5 秒向上游核查一次 |
| `POST /api/billing/notify/huifu` | 汇付签名表单通知；不使用登录 Cookie |

`Order` 字段为 `id,plan,name,type,list_amount_cents,discount_percent,amount_cents,days,months,delay_hours,unlimited_reading,credits,channel,status,pay_url,qr_payload,payment_kind,expires_at,paid_at,created_at,membership_until`。新套餐 `type` 为 `reading` 或 `credits`；旧 `rank/rank_upgrade/membership` 仍可读取，其历史字段保留。额度订单 `days=0`、`membership_until=""`。状态有 `pending/success/failed/expired`。ID 为 31 位，包含日期、InfoGap 标记与 80 位随机数。接口不会接受客户端价格、额度数量、时长或用户 ID。

查询订单返回最新 `user`：`reading_active/reading_until/reading_delay_hours` 表示当前畅读权益和连续有效期，`entitlements.unlimited_reading` 表示当前是否无限阅读。`credits` 是永久积分，`invite_credits` 是有效邀请赠分，`rank_credits` 是有效历史段位赠分；后两者各有 `*_expires_at` 最近到期时间（RFC3339，无余额则空）。`available_credits = daily_remaining + invite_credits + rank_credits + credits`。

`GET /api/billing/reading-status` 返回 `{user,periods:[{plan,name,delay_hours,starts_at,ends_at}],renewal_policy:"append"}`；`periods` 列出新畅读订单，`user` 同时合并有效的历史付费权益。`GET /api/billing/rank-status` 保留历史期数和批次查询：`{user,periods,next_gift_at,batches,gift_policy_version:2,gift_valid_days:30}`，不再提供新的升档报价。

活动流水的 `expires_at` 为空时表示历史或永久流水。邀请注册沿用 `invite_campaign`，新版记录附到期时间；消耗、退款、到期分别为 `invite_unlock/invite_refund/invite_expire`。旧邀请迁移使用一对 `invite_migrate_out/invite_migrate_in` 流水，表示余额转移而非再次赠送。历史段位批次使用 `rank_gift/rank_unlock/rank_refund/rank_expire`；`rank_monthly_gift` 仍表示过去的永久赠分。`purchase/contribution` 仍为永久积分。

新支付入口只接受已验签汇付响应中的 HTTPS 地址或 `alipays://` 载荷；拒绝 `weixin://`、明确的微信支付域名、带用户信息或换行的地址。汇付可能返回 HTTPS 中间支付页，因此不把合法入口限定为单一支付宝域名。HTTPS 入口写入 `pay_url`；`alipays://` 载荷通过 `qr_payload` 供本地生成二维码。前端按 `payment_kind` 显示支付链接或二维码，不把扫码载荷伪装成网页，也不把载荷发送给第三方图片服务。

同用户、同套餐、同渠道的有效待支付订单会复用，30 分钟后过期。开单超时不判定失败：返回 202、保留订单并提示稍后查询，避免重复收费。前端只有收到服务端的 `success` 才展示已付款。

## 结算检查

预下单与查单请求使用 RSA-SHA256 签名；同步响应验证平台签名。订单状态查询必须返回一致的商户、原订单和准确金额。成功必须含已验签的 `trans_stat=S` 和平台交易号，并核对商户、原订单与金额。原生支付可能不返回 `order_stat`；若返回，该字段必须为 `1`，与成功状态矛盾时拒绝结算。

回调读取最多 256 KB，只接受 `application/x-www-form-urlencoded` 且 `sign`、`resp_data` 各出现一次。真正的空请求可以作为网关探针；非空 JSON、其他正文、重复参数不会被误当成成功探针。正式通知按原始 `resp_data` 验签，再核对商户、订单、金额与终态。

签名回调路径精确豁免普通 API 的 Origin/JSON 限制；其他接口继续要求原有登录与同源检查。查单接口先验证订单属于当前用户，才允许访问上游。

成功结算在一个数据库事务内更新订单并按快照发放权益。畅读订单写入唯一 `reading_entitlements`；旧段位和升档按保存快照处理，历史会员订单延长有效期；积分包插入 `purchase` 流水和唯一额度授权，再增加永久余额。任一步失败会回滚整笔结算。平台交易号和订单授权都有唯一约束；重复或并发通知不会重复发放。失败通知不会撤回已成功的订单。已经在本地过期、随后收到完整有效到账通知的订单仍可正常核销。

## 数据与备份

主要账单表：

- `billing_orders`：用户、套餐、价格、天数和额度快照、支付渠道、状态、扫码载荷、平台交易号、到期和结算时间。
- `reading_entitlements`：新畅读订单的开始、结束、时效和政策版本，每单一条。
- `billing_grants`：历史会员订单的授权及有效起止时间。
- `billing_credit_grants`：每个额度订单最多一条授权，关联唯一 `ledger_id`，记录实际到账数量。
- `billing_events`：已处理事件的摘要键、订单、状态与时间，不保存完整支付通知或签名。
- `rank_periods`、`rank_gift_events`、`rank_upgrades`：计划期数、已发总额/政策版本、升档快照。
- `rank_credit_batches`：独立赠分批次的原额、余额、生效/到期时间和政策版本2。
- `rank_credit_spends`：每次解锁实际消耗的批次与金额，用于按原来源退款。

`invite_credit_batches/spends/migrations` 分别保存邀请批次、实际消耗归属和每个旧奖励的迁移对账凭证。迁移凭证记录 `reconciled/reason/transferred/balance_before/expected_balance/ledger_cutoff/assigned_spend/confirmed_refund`，跳过账户也留结果，不会重启后重复扣减。`unlocks.invite_cost` 表示邀请赠分消耗。

兼容迁移补字段/表，不改历史支付快照；旧赠分事件 `policy_version` 默认为1，新事件显式写2。`unlocks.rank_cost` 记录段位赠分消耗，原 `cost` 继续表示永久消耗，`daily_cost` 表示当日赠分消耗。

启动时检查 `billing_orders`，为旧数据库补上 `credits INTEGER NOT NULL DEFAULT 0`，旧会员订单保持原义。为保留旧表 `CHECK(days>0)` 而避免重建支付数据，额度订单在数据库中的 `days` 使用内部占位值 1；API 始终返回 0。结算以订单快照的 `credits>0` 区分额度产品，该占位值不会增加会员时间。

这些表与 `users.membership_until`、`users.credits`、`ledger` 使用同一 SQLite 数据库。备份应包含整个数据库，不能只导出订单表而遗漏授权与余额。不会修改旧站的订单、会员或支付配置。

## 验证范围

测试全部使用临时数据库、fake provider 与本机 TLS 测试网关，没有连接真实上游，也没有发真实订单。覆盖固定价格和订单归属、同渠道订单复用、超时保留、未配置时禁用、请求和响应验签、签名篡改、商户/订单/金额/状态缺失或不符、平台交易号重复、回调幂等、并发结算、续费顺延、过期会员、错误通知格式、旧订单迁移、额度订单快照、整笔回滚，以及积分、角色和查看延迟保持不变。

部署后仍需由真实付款者完成购买，并核对网关到账、回调、本站订单成功和对应的额度或会员权益。这一步未执行前，不能把“测试通过”写成“真实支付已验收”。

## 站长一分钱真实付款测试

功能自检页提供独立的真实付款入口。只有站长在管理员模式下可以创建 `payment_test` 订单，服务端金额固定为 1 分，只走支付宝。普通套餐目录包含3个永久积分包及4个畅读时长，不包含测试订单。

测试订单返回 `type=payment_test`、`days=0`、`credits=0`。为兼容旧表约束，数据库天数占位为 1；结算优先按 `plan=payment_test` 处理，只写订单和支付事件，不延长会员、不增加额度。重复通知保持幂等，其他用户不能查询该订单。

测试付款码与普通支付使用同一汇付签名和核验链路。HTTPS 扫码载荷也会在测试页面生成二维码，便于用支付宝扫描。生成入口不等于到账；必须查询到 `success` 才显示真实测试成功。

## 支付确认恢复（1.0.7）

已验签的原生支付终态以 `trans_stat` 判断。平台查询和通知均可能省略 `order_stat`，省略时不应拒绝正常成功交易；商户、原订单、金额、平台交易号和签名校验继续保留。

付款页每 5 秒自动查询，页面隐藏时暂停查询，返回后恢复。刷新付款页会恢复最近的订单。服务器另每 30 秒检查最近 72 小时的待支付或已过期订单，每轮最多 5 笔，使用已验签的主动查单结果幂等结算。用户关闭页面或回调失败时，仍可恢复已支付订单。

## 单独付费课程

课程使用同一支付宝订单接口，但属于独立产品。课程接口返回已发布课程的服务端价格；创建订单传 `{plan:"course_<课程ID>",channel:"alipay"}`。课程不会混入固定畅读与积分套餐目录，未发布、未定价的课程不能下单，已拥有的课程禁止重复购买。课程价格不参与贡献折扣。

课程订单返回 `type="course"`、`course_id`，`credits/days/months=0`、`unlimited_reading=false`。服务端保存课程编号、名称、标价和实付金额快照，客户端不能指定金额或借课程订单取得其他权益。课程后续改价不改变已生成订单的应付金额；迟到成功回调仍按订单快照核销。

成功结算只写独立 `course_access`，不会增加阅读积分、贡献值或会员时长。课程授权与订单成功、支付事件处于同一事务。重复通知不重复授权，授权失败整笔回滚。畅读套餐、贡献段位和阅读积分都不能代替课程购买；视频访问权限由课程模块逐次核验。
