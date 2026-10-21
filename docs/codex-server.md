# 服务器 Codex CLI

2026-10-21 已安装官方 Codex CLI 0.163.0。程序位于 `/opt/infogap-tools/codex-0.163.0`，`/usr/local/bin/codex` 指向该版本。使用官方 Linux musl 完整包，以兼容当前服务器系统。

- 官方版本：https://github.com/openai/codex/releases/tag/rust-v0.163.0
- 包名：`codex-package-x86_64-unknown-linux-musl.tar.gz`
- SHA-256：`582646c6bf0561eb0de540f7fc85b9121b0091a826bc6e96830b7f1722122182`
- 已核验下载摘要与官方资产摘要一致，`codex --version` 返回 `codex-cli 0.163.0`。

日常运行使用 `infogap-codex` 包装命令，切换到独立的 `infogap-agent` 账号和工作目录。它不属于站点数据库及支付密钥所属的用户组，也没有 Docker 权限。默认只读沙箱，关闭本地会话历史。配置与凭据仅保存在该账号的私有目录，文件权限为 0600，未进入 Git。

服务器保留原有网关配置。早期 `gpt-5.5` 测试收到上游 HTTP 404；随后按用户明确要求，以命令行固定 `gpt-6.1-sol` 和 `model_reasoning_effort=low`，独立账号的连通测试已成功返回 `INFOGAP_READY`。未改动默认模型或认证文件，也没有在指定模型失败时换用其他模型。

长文整理采用 `scripts/summarize.py` 和 `deploy/summarize-run.sh`，关闭工具与网页搜索，输出只作为待审核文本。CLI 0.163.0 的 `exec --help` 与对应官方源码都明确：同时提供 prompt 参数及管道 stdin 时，stdin 会追加为 `<stdin>` 上下文。完整原文通过该管道输入，不放在进程命令行，也不由模型自行访问来源网址。实际包装命令已将 4684 字官方原文整理为 109 字摘要和 758 字正文，独特事实与原文对应；同一原文再次运行命中缓存，没有再次调用模型。验证记录见 `docs/summarizer-verification.json`。

当前 CLI 不再识别旧的 `disable_response_storage` 配置，已移除该无效字段；本地历史设为不保存，整理任务另用 `--ephemeral`。上游数据保留行为仍需由所用网关确认。

网页读取由独立的浏览器采集组件执行，正常采集不依赖模型推理。Codex 用于维护及独立的受限长文整理队列；网页内容不能当作服务器操作指令。
