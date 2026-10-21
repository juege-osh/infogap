# 阅读额度与订阅会员支付

InfoGap 复用现有 Go 项目的汇付签名适配器，保留独立订单和回调地址。会员订单只延长 `users.membership_until`；额度订单只增加持久阅读余额 `users.credits`。两种购买都不增加贡献积分、提升段位或缩短内容查看延迟。额度包也不授予订阅会员。

套餐价格由服务端固定。均为一次性购买，不自动续费。会员续购从当前有效期结束时顺延；已经过期则从本次确认到账时开始计算。

| 套餐 ID | 类型 | 价格 | 内容 |
|---|---|---:|---|
| `monthly` | `membership` | 990 分 | 30 天会员 |
| `yearly` | `membership` | 9900 分 | 365 天会员 |
| `credits_100` | `credits` | 990 分 | 100 阅读额度 |
| `credits_350` | `credits` | 2900 分 | 350 阅读额度 |
| `credits_1000` | `credits` | 7900 分 | 1000 阅读额度 |

额度属于长期余额，不随每日赠额刷新。每条信息的消耗由内容等级规则决定，额度包中的数字不等同于可阅读篇数。每日赠额和贡献奖励由阅读系统管理，支付结算不修改它们。

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

## 前端接口

| 方法与路径 | 请求或返回 |
|---|---|
| `GET /api/billing/plans` | `{enabled,disabled_reason,plans:[{id,name,type,amount_cents,days,credits}],channels:["alipay","wechat"],auto_renew:false}` |
| `POST /api/billing/orders` | 请求 `{plan:套餐ID,channel:"alipay"或"wechat"}`；返回 `{order,message?}` |
| `GET /api/billing/orders` | 登录用户最近 50 笔订单 `{items:[Order]}` |
| `GET /api/billing/orders/{id}` | 核对本人订单，返回 `{order,user,message?}`；未终结订单最多每 5 秒向上游核查一次 |
| `POST /api/billing/notify/huifu` | 汇付签名表单通知；不使用登录 Cookie |

`Order` 字段为 `id,plan,name,type,amount_cents,days,credits,channel,status,pay_url,qr_payload,payment_kind,expires_at,paid_at,created_at,membership_until`。`type` 为 `membership` 或 `credits`；会员订单 `credits=0`，额度订单 `days=0`、`membership_until=""`。状态有 `pending/success/failed/expired`。ID 为 31 位，包含日期、InfoGap 标记与 80 位随机数。接口不会接受客户端价格、额度数量、时长或用户 ID。

查询订单返回的 `user` 是最新账户。额度购买成功后，前端读取 `user.credits` 刷新余额，并展示额度到账提示；会员订单则展示新的会员有效期。账户流水中额度购买类型为 `purchase`，金额为实际增加的额度。

`pay_url` 仅在上游返回 HTTP(S) 地址时提供；`qr_payload` 也可能是 `weixin://` 或 `alipays://`。前端按 `payment_kind` 显示支付链接或本地生成的二维码，不把扫码载荷伪装成网页，也不把载荷发送给第三方图片服务。

同用户、同套餐、同渠道的有效待支付订单会复用，30 分钟后过期。开单超时不判定失败：返回 202、保留订单并提示稍后查询，避免重复收费。前端只有收到服务端的 `success` 才展示已付款。

## 结算检查

预下单与查单请求使用 RSA-SHA256 签名；同步响应验证平台签名。订单状态查询必须返回一致的商户、原订单和准确金额。成功还必须同时含 `trans_stat=S`、`order_stat=1` 及平台交易号，缺字段不会通过兜底获得会员。

回调读取最多 256 KB，只接受 `application/x-www-form-urlencoded` 且 `sign`、`resp_data` 各出现一次。真正的空请求可以作为网关探针；非空 JSON、其他正文、重复参数不会被误当成成功探针。正式通知按原始 `resp_data` 验签，再核对商户、订单、金额与终态。

签名回调路径精确豁免普通 API 的 Origin/JSON 限制；其他接口继续要求原有登录与同源检查。查单接口先验证订单属于当前用户，才允许访问上游。

成功结算在一个数据库事务内更新订单并发放订单快照中的权益。会员订单插入唯一会员授权，再延长有效期；额度订单插入 `purchase` 流水和唯一额度授权，再增加余额。任一步失败会回滚整笔结算。平台交易号和订单授权都有唯一约束；重复或并发通知不会重复发放。失败通知不会撤回已成功的订单。已经在本地过期、随后收到完整有效到账通知的订单仍可正常核销。

## 数据与备份

账单使用四张表：

- `billing_orders`：用户、套餐、价格、天数和额度快照、支付渠道、状态、扫码载荷、平台交易号、到期和结算时间。
- `billing_grants`：每个订单最多一条会员授权及有效起止时间。
- `billing_credit_grants`：每个额度订单最多一条授权，关联唯一 `ledger_id`，记录实际到账数量。
- `billing_events`：已处理事件的摘要键、订单、状态与时间，不保存完整支付通知或签名。

启动时检查 `billing_orders`，为旧数据库补上 `credits INTEGER NOT NULL DEFAULT 0`，旧会员订单保持原义。为保留旧表 `CHECK(days>0)` 而避免重建支付数据，额度订单在数据库中的 `days` 使用内部占位值 1；API 始终返回 0。结算以订单快照的 `credits>0` 区分额度产品，该占位值不会增加会员时间。

这些表与 `users.membership_until`、`users.credits`、`ledger` 使用同一 SQLite 数据库。备份应包含整个数据库，不能只导出订单表而遗漏授权与余额。不会修改旧站的订单、会员或支付配置。

## 验证范围

测试全部使用临时数据库、fake provider 与本机 TLS 测试网关，没有连接真实上游，也没有发真实订单。覆盖固定价格和订单归属、同渠道订单复用、超时保留、未配置时禁用、请求和响应验签、签名篡改、商户/订单/金额/状态缺失或不符、平台交易号重复、回调幂等、并发结算、续费顺延、过期会员、错误通知格式、旧订单迁移、额度订单快照、整笔回滚，以及积分、角色和查看延迟保持不变。

部署后仍需由真实付款者完成购买，并核对网关到账、回调、本站订单成功和对应的额度或会员权益。这一步未执行前，不能把“测试通过”写成“真实支付已验收”。
