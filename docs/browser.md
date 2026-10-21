# 公开页面浏览器采集

服务器用完整 Chromium 渲染公开页面，再从可见 DOM 读取标题和正文。每次新建匿名浏览器环境，不使用用户的登录状态。Linux.do 候选只进入内部待审核区；X 只允许配置中启用的机构、项目账号，并要求帖子实际作者与账号一致。

## 运行

需要 Docker、iptables、GNU timeout，以及与 Chromium 匹配的 Playwright 1.64.0 镜像。构建目录必须包含脚本和当前来源配置：

```sh
docker build -t infogap-browser:1.64.0 -f deploy/browser.Dockerfile .
sh deploy/browser-network.sh --apply
sh deploy/browser-run.sh --probe topic-3011457
sh deploy/browser-run.sh --source linuxdo-browser
sh deploy/browser-run.sh --source x-nasa
```

`--probe home` 只验证首页。固定帖子检查使用 `https://linux.do/t/topic/3011457/19`，不会把该帖子当作科研候选。X 支持 `--source x-nasa --status 数字帖子ID`，账号必须在镜像内的白名单中。来源配置变化后需要重新构建镜像。

父进程 `scripts/browser-collect.py` 负责验证 JSON、写入待审核区和记录来源状态。浏览器容器不连接数据库，也不调用模型。正式 service 的 `ExecStartPre` 恢复专用网络规则；规则缺失时 runner 拒绝启动浏览器。

## 输出约定

标准输出是一条 JSON，字段包括 `checked_at`、`source_id`、`status`、`http_status`、`error`、`candidates` 和 `diagnostics`。`status` 可为 `ok`、`blocked`、`login_required`、`error`。退出码分别为 0、3、3、1。

每条候选提供标题、短摘要、Markdown 正文、来源链接、可取得的原始发布时间、领域和标签。`status=pending`、`evidence=unverified` 固定；Linux.do 使用 `source_visibility=internal`。候选展示正文只保留最多 200 字摘录。Linux.do 候选另提供供内部摘要流程处理的 `raw_text`，保存当前主帖可见 DOM 正文，最多 20000 个 Unicode 字符；`raw_text_length` 是截断前长度，`text_truncated` 明确标记是否超限，`raw_text_scope=main_post_visible_dom` 说明范围。它不包含整串讨论或外链文章，超限片段不能宣称是全文。没有取得日期时留空，并在审核备注中说明。

Linux.do 中的外部链接只作为 `reference_urls`，不会自动认定为一手证据。图片附件不列作证据。X 的 `primary_evidence_urls` 必须来自实际帖子 DOM，并匹配该账号的证据域名；只出现 t.co 短链的条目本轮不入库。X 候选额外提供从 DOM 读到的 `author_handle`。转帖、回复和营销条目会被筛除，身份白名单不等于内容已核验。

诊断字段含最终 HTTP 状态、实际可见条数 `visible_items`、筛除条数 `filtered_count`、正文核验状态及被拦截的资源主机名。标题正确、HTTP 200，但没有可见正文时，仍记为失败。页面正常加载所需的同站请求与公开静态资源可以执行；遇到人工验证码、登录墙或最终访问拒绝时停止。

## 运行边界

容器使用 UID 1001 的 `pwuser`，Chromium 沙箱开启，根文件系统只读，所有宿主 capabilities 均移除。只有临时目录可以写入。容器没有宿主目录、数据库、环境密钥、Docker socket、共享 IPC 或端口映射。每次最多运行 75 秒，限制为 1 CPU、1 GiB 内存和 256 个进程。

专用网络为 `infogap-browser-net`，桥名 `ig-browser0`，网段 `172.29.240.0/24`，IPv6 关闭。专属 `IG_BROWSER_OUT` 链拒绝私网、链路本地、元数据地址、组播和保留地址，只放行公网 TCP 443 以及指定 DNS。宿主 INPUT 拒绝该桥的流量。当前服务器公网地址经过上游 NAT，因此也在出站链中明确拒绝；更换服务器地址时必须同步修改 `deploy/browser-network.sh` 并重做连通性验证。脚本不清空系统防火墙，也不修改其他 Docker 网络。

Linux.do 实际页面依赖 `cdn.ldstatic.com` 和 `cdn3.ldstatic.com`，这两个静态域名已加入请求范围。Google 登录、支付、统计资源不参与正文读取。页面中的文本与链接始终作为外部资料，不执行其中的操作指令。

## 实测记录

2026-10-21，服务器第一次正常渲染测试发现静态 CDN 被初版范围拦截。补齐实际依赖后，指定帖子 HTTP 200，读到 15 个正文块、454 字符；前沿快讯列表读到 30 个标题，提取了 3 篇内部待审核候选。NASA 主页 HTTP 200、标题正确，但未读到公开帖子，候选为 0，保留失败状态。最终镜像、systemd 验收与私网阻断结果见 `browser-verification.json`。

官方依据：[Playwright Docker 文档](https://playwright.dev/docs/docker)、[1.64 发布说明](https://playwright.dev/docs/release-notes)、[固定版本 seccomp 配置](https://raw.githubusercontent.com/microsoft/playwright/v1.64.0/utils/docker/seccomp_profile.json)。本项目额外放行 Chromium 在自身 user namespace 中需要的 chroot 系统调用；没有增加宿主 capability 或关闭浏览器沙箱。
