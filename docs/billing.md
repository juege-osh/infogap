# 阅读积分与段位支付

InfoGap 复用已有汇付签名适配器，保留独立订单和回调地址。新订单只支持支付宝，可购买永久阅读积分或青铜至大师的段位时长。宗师、王者不出售。套餐价格、段位、时长和每期赠分由服务端生成，购买不增加贡献积分；有效段位取贡献段位与当前付费段位的较高者。

永久积分包为 `credits_100`（¥9.90 / 1000分）、`credits_350`（¥29 / 3500分）、`credits_1000`（¥79 / 10000分）。ID 保留历史名称，展示数量以 API 的 `credits` 为准。贡献奖励、购买积分以及已到账的历史永久赠分仍放在 `users.credits`，不会到期。

段位套餐 ID 为 `rank_<tier>_<months>`，tier 为 1–7，months 为 1/3/6/12，对应 30/90/180/365 天。年付共发 12 期，其余套餐按月数发放，每 30 天一期。年付每期最终赠分如下，**不能再加20%**；其他时长按 `四舍五入(年付每期值 × (100+加成百分比) / 120)` 计算，加成为 0/5/10/20%，只对最终结果取整。

| 段位 | 1个月每期 | 3个月每期 | 6个月每期 | 12个月每期 |
|---|---:|---:|---:|---:|
| 青铜 | 1667 | 1750 | 1833 | 2000 |
| 白银 | 3333 | 3500 | 3667 | 4000 |
| 黄金 | 5000 | 5250 | 5500 | 6000 |
| 铂金 | 6667 | 7000 | 7333 | 8000 |
| 翡翠 | 8333 | 8750 | 9167 | 10000 |
| 钻石 | 10000 | 10500 | 11000 | 12000 |
| 大师 | 11667 | 12250 | 12833 | 14000 |

以上是每期赠分，不是一次性到账总额。首购从支付确认起生效，续购或降档接在已有付费时段之后；升档按剩余时间计算差价与赠分补差。均不自动续费。当前月价为 ¥10/13/16/19/22/25/28，年价为 ¥68/88/108/128/148/168/188；3/6个月价格为月价乘月数。

## 赠分有效期与历史订单

新段位赠分使用独立批次，政策版本为 `2`，不再写入永久余额。每期从计划应到账时间起有效30天；期限到达即不可用，后台任务或用户访问会记录到期流水。补发漏掉的月份时，已经过期的批次仅记录处理结果，不会从当前时间重新获得30天。升档补差单独建批次，从确认生效起30天，原批次到期时间不延长。

解锁顺序为：每日赠分 → 最快到期的段位赠分 → 永久积分。并发解锁、重复支付回调都只结算一次。内容证伪时，永久消耗退回永久余额；每日赠分仅当日退回；段位赠分只退回尚未过期的原批次，既不延长有效期，也不转换成永久积分。

迁移不会回收或重分类已经到账的历史永久赠分。旧 `rank_gift_events` 保留政策版本 `1`，原 `users.credits` 不变；已支付和未支付订单的金额、时长、`monthly_credits` 快照均不重写。旧订单未来尚未发放的期数按原快照数量发放，但使用版本2的30天批次。待付款旧订单被复用时，也返回原快照。已下架的 `monthly/yearly` 会员订单仍按历史权益结算。

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
| `GET /api/billing/plans` | `{enabled,disabled_reason,plans:[{id,name,type,amount_cents,days,credits,rank_tier,months,monthly_credits}],channels:["alipay"],auto_renew:false}` |
| `POST /api/billing/orders` | 请求 `{plan:套餐ID,channel:"alipay"}`；返回 `{order,message?}` |
| `GET /api/billing/orders` | 登录用户最近 50 笔订单 `{items:[Order]}` |
| `GET /api/billing/orders/{id}` | 核对本人订单，返回 `{order,user,message?}`；未终结订单最多每 5 秒向上游核查一次 |
| `POST /api/billing/notify/huifu` | 汇付签名表单通知；不使用登录 Cookie |

`Order` 字段为 `id,plan,name,type,amount_cents,days,credits,rank_tier,months,monthly_credits,channel,status,pay_url,qr_payload,payment_kind,expires_at,paid_at,created_at,membership_until`。新套餐 `type` 为 `rank`、`rank_upgrade` 或 `credits`；历史 `membership` 仍可读取。额度订单 `days=0`、`membership_until=""`。状态有 `pending/success/failed/expired`。ID 为 31 位，包含日期、InfoGap 标记与 80 位随机数。接口不会接受客户端价格、额度数量、时长或用户 ID。

查询订单返回最新 `user`：`credits` 为永久积分，`rank_credits` 为当前有效段位赠分，`rank_credits_expires_at` 为最近有余额批次的到期时间（无则空），`available_credits` 为每日可用赠分、段位赠分和永久积分之和。购买积分成功读取 `credits`；段位支付成功读取有效段位和批次，不把整单未来赠分加进当前余额。

`GET /api/billing/rank-status` 返回 `{user,periods,next_gift_at,batches,gift_policy_version:2,gift_valid_days:30}`。`periods` 保留原计划字段；`batches` 只包含有效未用完批次，每项为 `{id,amount,remaining,granted_at,expires_at,policy_version}`，按到期先后排列。`granted_at` 为该批次生效时间；常规期数采用计划应到账时间。`GET /api/billing/rank-upgrade?tier=N` 返回升档计划，实际下单仍由服务端重新计算并保存快照。

活动流水新增 `expires_at` 字段，无到期时间的历史/永久流水为空。新段位流水类型：`rank_gift`（发放）、`rank_unlock`（解锁扣减）、`rank_refund`（退回原批次）、`rank_expire`（余额到期）。旧 `rank_monthly_gift` 表示历史永久赠分；`purchase` 与 `contribution` 仍表示永久积分。

新支付入口只接受已验签汇付响应中的 HTTPS 地址或 `alipays://` 载荷；拒绝 `weixin://`、明确的微信支付域名、带用户信息或换行的地址。汇付可能返回 HTTPS 中间支付页，因此不把合法入口限定为单一支付宝域名。HTTPS 入口写入 `pay_url`；`alipays://` 载荷通过 `qr_payload` 供本地生成二维码。前端按 `payment_kind` 显示支付链接或二维码，不把扫码载荷伪装成网页，也不把载荷发送给第三方图片服务。

同用户、同套餐、同渠道的有效待支付订单会复用，30 分钟后过期。开单超时不判定失败：返回 202、保留订单并提示稍后查询，避免重复收费。前端只有收到服务端的 `success` 才展示已付款。

## 结算检查

预下单与查单请求使用 RSA-SHA256 签名；同步响应验证平台签名。订单状态查询必须返回一致的商户、原订单和准确金额。成功必须含已验签的 `trans_stat=S` 和平台交易号，并核对商户、原订单与金额。原生支付可能不返回 `order_stat`；若返回，该字段必须为 `1`，与成功状态矛盾时拒绝结算。

回调读取最多 256 KB，只接受 `application/x-www-form-urlencoded` 且 `sign`、`resp_data` 各出现一次。真正的空请求可以作为网关探针；非空 JSON、其他正文、重复参数不会被误当成成功探针。正式通知按原始 `resp_data` 验签，再核对商户、订单、金额与终态。

签名回调路径精确豁免普通 API 的 Origin/JSON 限制；其他接口继续要求原有登录与同源检查。查单接口先验证订单属于当前用户，才允许访问上游。

成功结算在一个数据库事务内更新订单并按快照发放权益。段位订单建立计划期数并发首期到期批次；升档按保存的补差快照处理；历史会员订单延长原有效期；积分包插入 `purchase` 流水和唯一额度授权，再增加永久余额。任一步失败会回滚整笔结算。平台交易号和订单授权都有唯一约束；重复或并发通知不会重复发放。失败通知不会撤回已成功的订单。已经在本地过期、随后收到完整有效到账通知的订单仍可正常核销。

## 数据与备份

主要账单表：

- `billing_orders`：用户、套餐、价格、天数和额度快照、支付渠道、状态、扫码载荷、平台交易号、到期和结算时间。
- `billing_grants`：每个订单最多一条会员授权及有效起止时间。
- `billing_credit_grants`：每个额度订单最多一条授权，关联唯一 `ledger_id`，记录实际到账数量。
- `billing_events`：已处理事件的摘要键、订单、状态与时间，不保存完整支付通知或签名。
- `rank_periods`、`rank_gift_events`、`rank_upgrades`：计划期数、已发总额/政策版本、升档快照。
- `rank_credit_batches`：独立赠分批次的原额、余额、生效/到期时间和政策版本2。
- `rank_credit_spends`：每次解锁实际消耗的批次与金额，用于按原来源退款。

兼容迁移只补字段/表，不改历史支付快照；旧赠分事件 `policy_version` 默认为1，新事件显式写2。`unlocks.rank_cost` 记录段位赠分消耗，原 `cost` 继续表示永久消耗，`daily_cost` 表示当日赠分消耗。

启动时检查 `billing_orders`，为旧数据库补上 `credits INTEGER NOT NULL DEFAULT 0`，旧会员订单保持原义。为保留旧表 `CHECK(days>0)` 而避免重建支付数据，额度订单在数据库中的 `days` 使用内部占位值 1；API 始终返回 0。结算以订单快照的 `credits>0` 区分额度产品，该占位值不会增加会员时间。

这些表与 `users.membership_until`、`users.credits`、`ledger` 使用同一 SQLite 数据库。备份应包含整个数据库，不能只导出订单表而遗漏授权与余额。不会修改旧站的订单、会员或支付配置。

## 验证范围

测试全部使用临时数据库、fake provider 与本机 TLS 测试网关，没有连接真实上游，也没有发真实订单。覆盖固定价格和订单归属、同渠道订单复用、超时保留、未配置时禁用、请求和响应验签、签名篡改、商户/订单/金额/状态缺失或不符、平台交易号重复、回调幂等、并发结算、续费顺延、过期会员、错误通知格式、旧订单迁移、额度订单快照、整笔回滚，以及积分、角色和查看延迟保持不变。

部署后仍需由真实付款者完成购买，并核对网关到账、回调、本站订单成功和对应的额度或会员权益。这一步未执行前，不能把“测试通过”写成“真实支付已验收”。

## 站长一分钱真实付款测试

功能自检页提供独立的真实付款入口。只有站长在管理员模式下可以创建 `payment_test` 订单，服务端金额固定为 1 分，只走支付宝。普通套餐目录包含3个永久积分包及28个段位/时长组合，不包含测试订单。

测试订单返回 `type=payment_test`、`days=0`、`credits=0`。为兼容旧表约束，数据库天数占位为 1；结算优先按 `plan=payment_test` 处理，只写订单和支付事件，不延长会员、不增加额度。重复通知保持幂等，其他用户不能查询该订单。

测试付款码与普通支付使用同一汇付签名和核验链路。HTTPS 扫码载荷也会在测试页面生成二维码，便于用支付宝扫描。生成入口不等于到账；必须查询到 `success` 才显示真实测试成功。

## 支付确认恢复（1.0.7）

已验签的原生支付终态以 `trans_stat` 判断。平台查询和通知均可能省略 `order_stat`，省略时不应拒绝正常成功交易；商户、原订单、金额、平台交易号和签名校验继续保留。

付款页每 5 秒自动查询，页面隐藏时暂停查询，返回后恢复。刷新付款页会恢复最近的订单。服务器另每 30 秒检查最近 72 小时的待支付或已过期订单，每轮最多 5 笔，使用已验签的主动查单结果幂等结算。用户关闭页面或回调失败时，仍可恢复已支付订单。
