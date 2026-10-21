# 科研来源、候选采集与首批内容

本页记录2026年10月21日的核验结果。来源状态会变，后台的最后成功时间与错误记录才是运行时状态。

## 13个来源的核验结果

| 来源 | 学科 | 方式 | 本机核验 | 订阅内条目数 | 默认间隔 |
|---|---|---|---|---:|---|
| [Nature](https://www.nature.com/nature.rss) | 前沿科技 | 官方RSS | 可读取 | 75 | 6小时 |
| [Nature Chemistry](https://www.nature.com/nchem.rss) | 化学 | 官方RSS | 可读取 | 8 | 6小时 |
| [Nature Geoscience](https://www.nature.com/ngeo.rss) | 地球与地理 | 官方RSS | 初次可读；采集器请求转登录地址，安全跳过 | 初次8 | 6小时 |
| [Science](https://www.science.org/action/showFeed?type=etoc&feed=rss&jc=science) | 前沿科技 | 官方RSS | 可读取 | 39 | 6小时 |
| [arXiv q-bio](https://rss.arxiv.org/rss/q-bio) | 生命科学 | 官方RSS | 可读取 | 34 | 12小时 |
| [arXiv physics](https://rss.arxiv.org/rss/physics) | 物理 | 官方RSS | 可读取 | 177 | 12小时 |
| [arXiv chemistry](https://rss.arxiv.org/rss/physics.chem-ph) | 化学 | 官方RSS | 可读取 | 11 | 12小时 |
| [arXiv geophysics](https://rss.arxiv.org/rss/physics.geo-ph) | 地球与地理 | 官方RSS | 可读取；曾出现暂时性TLS错误 | 7 | 12小时 |
| [NASA](https://www.nasa.gov/feed/) | 天文与空间 | 官方RSS | 可读取 | 10 | 6小时 |
| [ESA](https://www.esa.int/rssfeed/Our_Activities/Space_Science) | 天文与空间 | 官方RSS | 可读取 | 15 | 6小时 |
| [OpenMM](https://api.github.com/repos/openmm/openmm/releases?per_page=10) | 化学 | 官方GitHub API | 可读取 | 9个稳定版本 | 12小时 |
| [SciPy](https://api.github.com/repos/scipy/scipy/releases?per_page=10) | 开源技术 | 官方GitHub API | 可读取 | 6个稳定版本 | 12小时 |
| [LINUX DO 公开RSS](https://linux.do/latest.rss) | 前沿科技、人工智能、开源技术 | 官方公开RSS，内部线索 | 生产服务器robots与RSS均HTTP403，未采集 | 0 | 6小时 |

表中数量是订阅当时包含的记录，不是已发表到InfoGap的条数。脚本每个来源每次最多处理10条。Nature与Science的目录同时含研究、评论和新闻，进入候选库时统一标为待核验，不按期刊品牌自动标记为同行评审研究。

## LINUX DO 内部线索

2026年10月21日，从生产服务器以采集器身份访问 `https://linux.do/robots.txt` 和 `https://linux.do/latest.rss`，两者均返回 HTTP 403，响应类型为 HTML。官方首页可见[前沿快讯](https://linux.do/c/news/34)和[开发调优](https://linux.do/c/develop/4)分类，但这不代表服务器能自动读取RSS。本次没有拿到社区候选，更没有发布社区内容。

新增来源 ID 为 `linuxdo-tech`。定时任务会先检查 robots；403、登录或验证码页面会直接停止这个来源，把真实错误写入后台 `last_error`，不切换代理、Cookie、镜像或替代抓取渠道。仅当官方公开RSS正常允许读取时才进入筛选。

筛选只接收RSS中明确属于“前沿快讯/开发调优”的条目，并要求标题同时包含科技主题与发布、研究、性能、版本、漏洞修复等具体信号。羊毛、推广、充值、拼车、求助和闲聊等关键词会排除；缺分类也先跳过。规则偏保守，可能漏掉好帖，审核者可以后续调整配置。`filtered`记录本轮已检查后排除的数量，全部排除不算采集故障。

入库内容只包含标题、日期、最多200字的订阅摘录和内部原帖参考，强制 `source_visibility=internal`、`evidence=unverified`、`status=pending`。即使误传发布参数，也不能直接发布社区条目；旧数据库缺少隐私列时会停止入库。源码不会把社区发言改称官方消息。

原帖地址和社区名称保留给后台追溯。公开发布前应独立核验、重新编辑，并清除对外标题、摘要、正文、标签中的社区名称和原帖链接；普通用户接口不得输出内部出处。后台负责执行这一发布检查，不能只依赖前端隐藏。

部署后核验此来源：`python3 scripts/collect.py --db /path/to/infogap.db --source linuxdo-tech --force`。若仍为403，应保留失败状态，不能填写“来源已接通”。

没有把访问失败的NASA Science `/feed/`或已返回404的CERN旧订阅地址加入正式清单。NASA Science文章可以作为核验后的内容来源，但本次未声称其订阅已接通。知乎、B站目前不在这份自动采集器内；具体接入由后续验证结果决定。

## 采集行为

运行环境为Python 3.6及以上，全部使用标准库，不依赖收费接口。脚本只读官方RSS/Atom或GitHub Releases API，不下载论文全文，不登录出版社，不绕过验证码或付费限制。

每条自动内容保留原题名、最多500字符的短摘录、出处及订阅日期，以中文结构注明“候选线索，尚未形成研究解读”。英文订阅不会被伪装成已经人工阅读和翻译的论文。Science目录通常只有题名与卷期，这类条目会明确说明暂无研究摘要。

自动内容只进入`pending`，等级为`ordinary`。arXiv标为`preprint`；期刊混合目录标为`unverified`；NASA/ESA标为`official`；GitHub标为`repository`。采集器不授予稀有或独家等级，不增加任何用户贡献奖励。

脚本检查RSS来源的robots规则，按最长匹配处理明确Allow覆盖一般Disallow。Science的`/action/showFeed`有明确允许规则。GitHub使用公开API规则。每个域名请求至少间隔2秒，遵守更长的Crawl-delay；单次超时18秒，暂时性网络及429/5xx最多尝试3次。遇到401/403、登录重定向或较长Retry-After立即停止该源，保留错误并在后续调度重试。

请求地址限定在已核验的官方域名，并拒绝非公网解析、未核验重定向、大于3MB的响应和含实体声明的XML。条目中的来源链接只保存，不继续抓取。HTML脚本、样式等不会进入摘录。

## 调用方式

先由Go服务创建数据库表，再运行采集器。默认数据库为`data/infogap.db`，可以通过`INFOGAP_DB`或`--db`指定。

```sh
# 只检查来源，不写数据库
python3 scripts/collect.py --check --sources data/sources.json

# 定时任务每6小时调用；各来源仍按自己的6/12小时频率执行
python3 scripts/collect.py --db /path/to/infogap.db --sources data/sources.json

# 只核验某个来源
python3 scripts/collect.py --check --source science

# 手动重试，仍尊重后台停用与来源访问限制
python3 scripts/collect.py --db /path/to/infogap.db --source nature-geoscience --force
```

进程输出每源一行JSON，末尾输出本次成功/失败数。任何来源失败时最终退出码为1，其余来源仍会继续。生产调度可以据此触发失败告警。并发采集进程通过数据库旁的`.collector.lock`文件互斥。

后台停用的来源不会被配置同步重新打开。配置中的元信息可以更新，但采集器不会覆盖任何已有内容的正文、评级、状态或审核结果。同一来源URL唯一；Science的`/doi/abs/`、`af=R`以及arXiv版本后缀会归一化，避免同一论文重复奖励或重复入库。

`sources.last_attempt`表示本次尝试时间，`last_success`表示最后一次成功解析时间，`last_error`保留最近错误，`last_count`表示本次实际新插入的候选数。来源成功但没有新内容时，`last_count=0`，不代表失败。

## 初始10条中文内容

`data/seed.json`是顶层对象数组，字段与Gap API一致。包含5条精选、5条普通，没有把开放论文包装成独家消息。自动候选与这10条已核验的中文编辑内容分开导入。

| 内容 | 原始发布日期 | 证据 |
|---|---|---|
| Nature可编程金属蛋白酶 | 2026-10-14 | 公开摘要、出版记录、数据与代码声明 |
| Nature Chemistry近冰点析氧催化 | 2026-10-21 | 公开摘要、审稿信息、原始数据声明 |
| Nature Geoscience冰盖速率诱发退缩 | 2026-10-20 | 开放研究摘要、方法及代码数据声明 |
| Science植物壳聚糖受体 | 2026-10-15 | 出版社目录、Crossref登记摘要、PubMed及研究机构公告 |
| Science手性自旋模式 | 2026-10-15 | 正式出版记录、摘要及作者早期预印本 |
| arXiv氨与氢的行星内部相平衡 | 2026-10-19 | 作者预印本v1摘要，明确未审稿 |
| NASA厄尔尼诺多传感器观测 | 2026-10-15 | NASA官方说明 |
| ESA Euclid发现彗星 | 2026-10-19 | ESA官方公告，区分发现与确认日期 |
| OpenMM多状态采样 | 2026-08-19 | 官方8.6.0发布说明，并检查8.6.1修复 |
| SciPy批量线性代数及数组后端 | 2026-06-19 | 官方1.18.0发布说明，并检查1.18.1修复 |

日期仅明确到日的来源使用该日00:00Z存储，这不是精确上线时刻。GitHub和arXiv有明确时间的则使用原始时间。每条保留`review_note`，说明核验范围；订阅正文访问受限时不会声称读过全文。研究论文摘要仅做中文转述，数值、预印本状态与推断边界逐条核对。

```sh
# 默认导入待审核；不会调用外部网络
python3 scripts/collect.py --db /path/to/infogap.db --seed data/seed.json

# 仅首次部署经核验的这批内容，显式发布
python3 scripts/collect.py --db /path/to/infogap.db --seed data/seed.json --publish-seed
```

`--seed`与`--import-seed`等价。重复导入是无操作，已有条目不会被升为已发布或覆盖。如果曾以待审核状态导入，应从后台审核，不能靠重跑发布参数绕过现有状态。

正文使用四个 Markdown 二级标题，每条保留少量重点加粗和原始来源链接。显式使用 `--publish-seed` 首次导入历史种子时，`released_at` 取已核验的原始 `published_at`；普通采集和待审种子的 `released_at` 留空，由后台审核发布时设置。这个例外只适用于这批明确核验过的历史内容。

## 数据与验证

采集器只使用Go服务创建的`gaps`和`sources`表，不创建或迁移业务schema。`gaps.author_id=NULL`、`rewarded=0`；不会触碰账号、会话、额度流水、邀请或解锁记录。候选去重依赖`source_url`唯一约束，使用参数绑定、事务和20秒SQLite busy timeout。备份应使用SQLite在线备份接口，或停止服务后连同数据库WAL状态一起处理，不能只在运行中复制主文件就认定备份完整。

已用当前Go实际schema做临时库验证：10条种子可导入、再次导入新增0条、已发布正文和状态不被覆盖、后台停用来源保持停用、采集新增记录始终pending、不发贡献奖励、Science/arXiv URL归一化、Atom时区解析。脚本语法通过Python 3.6语法模式检查；生产解释器实际执行结果应在部署时再确认。
