# X 来源白名单

核对日期：2026-10-21。配置文件：`data/x-accounts.json`。

本次整理了 11 个机构或项目账号，其中 10 个允许进入采集候选流程；Science 保留为待复核条目，暂未启用。身份依据来自机构官网的账号链接。X 账号是否有蓝标、粉丝多少，都不作为研究结论可信的依据。

## 账号与身份凭证

| 账号 | 关注范围 | 官网身份凭证 | 状态 |
| --- | --- | --- | --- |
| [@nature](https://x.com/nature) | 综合科学、论文与研究分析 | [Nature 官网](https://www.nature.com/)的 Follow us on X 链接指向 `twitter.com/nature` | 允许候选 |
| [@ScienceMagazine](https://x.com/ScienceMagazine) | 综合科学、论文与更正 | [AAAS 作者手册](https://promo.aaas.org/images/Publishing/Journals/2019/Authors%20Guide%20Brochure_Final.pdf)标注该账号；这是 2019 年材料 | 暂停，待当前官网链接复核 |
| [@NASA](https://x.com/NASA) | 空间科学、任务研究结果、开放数据 | [NASA 官方社交账号目录](https://www.nasa.gov/social-media/) | 允许候选 |
| [@NASAEarth](https://x.com/NASAEarth) | 地球观测、气候、遥感 | [NASA 官方社交账号目录](https://www.nasa.gov/social-media/)中的 Earth 账号 | 允许候选 |
| [@esa](https://x.com/esa) | 空间科学、航天技术、地球观测 | [ESA Connect with us](https://www.esa.int/ESA/Connect_with_us/Connect_with_us) | 允许候选 |
| [@CERN](https://x.com/CERN) | 粒子物理、加速器、开放科学 | [CERN 官网](https://home.cern/)页脚链接[官方社交目录](https://linktr.ee/CERN_official)，目录包含 `x.com/CERN` | 允许候选 |
| [@EMBL](https://x.com/EMBL) | 分子生物学、结构生物学、基因组学 | [EMBL 社交账号目录](https://www.embl.org/social-media/) | 允许候选 |
| [@emblebi](https://x.com/emblebi) | 生物信息学、蛋白质、开放数据 | [EMBL 社交账号目录](https://www.embl.org/social-media/)中的 EMBL-EBI X 链接 | 允许候选 |
| [@PyTorch](https://x.com/PyTorch) | 深度学习框架、版本更新、安全修复 | [PyTorch 官网](https://pytorch.org/)的 Twitter 链接 | 允许候选 |
| [@github](https://x.com/github) | 开源基础设施、软件安全、技术更新 | [GitHub 官网](https://github.com/)公开 HTML 中的 `x.com/github` 链接 | 允许候选 |
| [@huggingface](https://x.com/huggingface) | 开放模型、数据集、开源机器学习工具 | [Hugging Face 官网](https://huggingface.co/)页脚 Twitter 链接 | 允许候选 |

Science 官网和其 X 主页本次均返回 403。AAAS 历史手册可直接读取，但它不能单独证明当前账号归属未变，因此配置中 `enabled=false`。其余账号核对的是官网出站链接，并未据此宣称 X 时间线可以免登录读取。所有账号的 `collection_status` 均为 `not_tested`，没有写入采集成功时间。

## 帖子筛选

账号在名单中，只代表可以查看它的候选帖子。每条候选还要满足以下条件：

1. 帖子来自名单中的原作者，有可核对的帖子地址和发布时间；默认跳过回复、转帖和推广帖。
2. 有一手材料链接，例如论文 DOI、机构研究报告、数据集说明、项目 release、模型卡或技术文档。展开短链接后，核对最终域名、作者与项目归属。只有截图、二手转述或口号的帖子不入库。
3. 原文确实支持拟写入的结论。预印本、机构公告、同行评审论文要分清，保留样本、实验条件、版本和限制。机构账号发布的消息也可能有误，不能自动标成“已验证”。
4. 排除广告、带货、优惠码、返佣链接、付费套餐促销、招聘、会议售票、拉群及涨粉诱导。GitHub、Hugging Face 等商业机构账号也执行同样规则。发布新工具可以收录，但必须有实际文档、代码或可复核结果。
5. 同一 DOI、论文、数据集版本或 release 多平台出现时合并去重。候选只生成中文摘要和核验线索，交给后台审核，不自动发布或奖励贡献。

`primary_domains` 是每个账号常见的一手资料域名提示。匹配须检查完整域名或明确的子域边界，不能用字符串包含判断；`nature.com.evil.example` 不属于 `nature.com`。域名命中也不能代替内容核验。

## 接入方式与失败状态

浏览器采集组件读取顶层 `accounts` 数组，只处理 `enabled=true` 的条目。`handle` 不带 `@`，比较时忽略大小写；`profile_url` 是规范主页地址，`id` 可作为来源表的稳定标识。`kind` 为 `browser`，公开机构账号的 `source_visibility` 为 `public`。`identity_evidence` 保存官网凭证和核对方法，便于以后复查账号改名或转移。

实际流程是：打开允许访问的公开主页或指定帖子 → 确认原作者 → 读取帖子和证据链接 → 核对一手材料 → 输出待审核候选。帖子正文与作者名必须来自本次真实页面，不能把搜索摘要拼成抓取结果。

遇到登录墙记 `login_required`，验证码或访问挑战记 `blocked`，网络失败记具体错误。失败不得更新 `last_success`，空结果也不能伪装为采集成功。不索取或复制用户账号、Cookie 和凭证，不接付费 API，不绕过访问限制。

X 暂不可读时，可以回到该机构的官网、公开论文、RSS 或项目 release 获取相同进展。此时出处写实际读取的网站；没有读取过 X 帖子，就不能把它记成 X 采集成功。Nature 的官网明确提供 RSS 入口，GitHub 的项目 release、NASA 和 ESA 的官网新闻也是可核对的一手入口。

当前文件仅提供账号配置和筛选规则，不包含浏览器执行结果。运行组件是否成功、筛掉多少条、写入多少候选，须以实际执行回执为准。
