#!/usr/bin/env python3
"""Validate isolated browser candidates and queue them for review. Python 3.6+.

Only the fixed browser runner receives root privileges. SQLite connections,
locks and result files are opened as the existing database owner. No database,
session, service environment or credential is passed to the browser container.
定时采集不调用模型，模型token为0；仍有浏览器计算与网络流量开销。
后续AI整理只能作为新增合格候选的可选步骤，不属于此调度器。
"""
import argparse
import contextlib
import datetime as dt
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import signal
import sqlite3
import subprocess
import sys
import tempfile
import urllib.parse

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parent.parent
RUNNER = ROOT / "deploy" / "browser-run.sh"
spec = importlib.util.spec_from_file_location("infogap_feed_collector", str(ROOT / "scripts" / "collect.py"))
collect = importlib.util.module_from_spec(spec)
spec.loader.exec_module(collect)

MAX_CANDIDATES = 8
MAX_OUTPUT = 512 * 1024
PROCESS_TIMEOUT = 90
INTERVAL_SECONDS = 6 * 3600
HANDLE = re.compile(r"^[A-Za-z0-9_]{1,15}$")
DOMAIN = re.compile(r"^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$")
CATEGORIES = {'前沿科技', '人工智能', '生命科学', '物理', '化学', '地球与地理', '天文与空间', '材料科学', '开源技术', '军事与国防', '政治与公共政策', '农业与食品', '社会民生', '法律法规', '航空航天', '经济与产业', '能源与环境', '医疗健康', '教育与就业', '交通与基建', '公共安全'}
MARKETING = re.compile(r"抽奖|带货|优惠券|领券|返利|返现|推广合作|邀请码|转发.{0,8}(抽|送)|购买.{0,8}(优惠|折扣)|限时.{0,8}(折扣|优惠)|\b(?:giveaway|airdrop|sponsored|coupon|cashback|referral|discount)\b|\b(?:buy now|promo code|subscribe now)\b|follow.{0,25}retweet", re.I)
LINUX = {"id": "linuxdo-browser", "name": "Linux.do 浏览器线索", "url": "https://linux.do/c/news/34", "kind": "browser", "category": "开源技术", "enabled": True}


def bounded_text(value, label, minimum=0, maximum=1000):
    if not isinstance(value, str):
        raise ValueError(label + "必须是文本")
    value = value.strip()
    if len(value) < minimum or len(value) > maximum:
        raise ValueError(label + "长度不符合要求")
    if re.search(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]", value):
        raise ValueError(label + "含控制字符")
    return value


def https_parts(value):
    value = bounded_text(value, "链接", 1, 2048)
    if re.search(r"[\s\\]", value):
        raise ValueError("链接含无效字符")
    try:
        parsed = urllib.parse.urlsplit(value)
        host = (parsed.hostname or "").lower()
        if parsed.scheme != "https" or parsed.username or parsed.password or parsed.port not in (None, 443) or not DOMAIN.match(host):
            raise ValueError("链接必须是白名单公开HTTPS地址")
        path = urllib.parse.unquote(parsed.path)
        if "\\" in path or re.search(r"[\x00-\x20\x7f]", path) or any(part in (".", "..") for part in path.split("/")):
            raise ValueError("链接路径不合法")
        return parsed, host
    except (ValueError, TypeError) as exc:
        raise ValueError("链接格式不合法: " + str(exc)[:80])


def read_sources(path):
    path = Path(path)
    if path.stat().st_size > 256 * 1024:
        raise ValueError("账号配置过大")
    data = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(data, dict) or not isinstance(data.get("accounts"), list) or len(data["accounts"]) > 32:
        raise ValueError("账号配置必须提供accounts数组（最多32项）")
    sources, seen = [dict(LINUX)], {LINUX["id"]}
    for account in data["accounts"]:
        if not isinstance(account, dict):
            raise ValueError("账号配置格式不正确")
        handle = bounded_text(account.get("handle"), "账号名", 1, 15)
        if not HANDLE.match(handle):
            raise ValueError("账号名不合法")
        identifier = "x-" + handle.lower()
        if account.get("id") != identifier or identifier in seen:
            raise ValueError("账号ID必须唯一并与handle对应")
        parsed, host = https_parts(account.get("profile_url"))
        if host != "x.com" or parsed.path.rstrip("/").lower() != "/" + handle.lower() or parsed.query or parsed.fragment:
            raise ValueError("账号主页与白名单handle不符")
        domains = account.get("primary_domains")
        if not isinstance(domains, list) or not domains or len(domains) > 20:
            raise ValueError("账号必须配置原始证据域名")
        clean_domains = []
        for domain in domains:
            if not isinstance(domain, str) or domain != domain.lower() or not DOMAIN.match(domain) or domain in ("x.com", "twitter.com", "t.co", "localhost.localdomain"):
                raise ValueError("原始证据域名不合法")
            clean_domains.append(domain)
        if account.get("category") not in CATEGORIES or not isinstance(account.get("enabled"), bool):
            raise ValueError("账号领域或启用状态不正确")
        sources.append({"id": identifier, "handle": handle.lower(), "name": bounded_text(account.get("name"), "来源名称", 1, 80), "url": "https://x.com/" + handle, "kind": "browser", "category": account["category"], "primary_domains": clean_domains, "enabled": account["enabled"]})
        seen.add(identifier)
    return sources


@contextlib.contextmanager
def database_identity(path):
    """All database descriptors must close before restoring the privileged uid."""
    info = os.stat(str(path))
    original_uid, original_gid = os.geteuid(), os.getegid()
    switched = original_uid == 0 and (info.st_uid != 0 or info.st_gid != original_gid)
    groups = os.getgroups() if switched else None
    if original_uid != 0 and original_uid != info.st_uid:
        raise PermissionError("请以数据库属主或root运行采集父进程")
    try:
        if switched:
            os.setgroups([info.st_gid])
            os.setegid(info.st_gid)
            os.seteuid(info.st_uid)
        yield
    finally:
        if switched:
            os.seteuid(original_uid)
            os.setegid(original_gid)
            os.setgroups(groups)


@contextlib.contextmanager
def database(path):
    with database_identity(path):
        connection = sqlite3.connect(str(path), timeout=20)
        try:
            connection.execute("PRAGMA busy_timeout=20000")
            connection.execute("PRAGMA foreign_keys=ON")
            required = {"source_visibility", "released_at", "review_note"}
            if not required.issubset({row[1] for row in connection.execute("PRAGMA table_info(gaps)")}):
                raise RuntimeError("数据库结构尚未更新，浏览器候选未入库")
            yield connection
        finally:
            connection.close()


@contextlib.contextmanager
def collector_lock(path):
    handle = None
    try:
        with database_identity(path):
            handle = open(str(path) + ".collector.lock", "a")
            fcntl.flock(handle.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield
    finally:
        if handle is not None:
            handle.close()


def browser_command(source):
    identifier = source["id"]
    if identifier != LINUX["id"] and identifier != "x-" + source.get("handle", ""):
        raise ValueError("拒绝非白名单浏览器目标")
    if identifier != LINUX["id"] and not HANDLE.match(source.get("handle", "")):
        raise ValueError("拒绝非法账号参数")
    return [str(RUNNER), "--source", identifier]


def invoke_browser(source):
    # stdout/stderr go to bounded-on-read temporary files, never a shell or pipe
    # that can deadlock. No inherited API keys, proxy credentials or service env.
    environment = {"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C.UTF-8", "HOME": "/tmp"}
    with tempfile.TemporaryFile() as output, tempfile.TemporaryFile() as errors:
        process = subprocess.Popen(browser_command(source), cwd=str(ROOT), env=environment, stdout=output, stderr=errors, start_new_session=True)
        try:
            code = process.wait(timeout=PROCESS_TIMEOUT)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
            raise RuntimeError("浏览器子进程超过90秒，已终止；未导入候选")
        output.seek(0)
        raw = output.read(MAX_OUTPUT + 1)
        if len(raw) > MAX_OUTPUT:
            raise ValueError("浏览器结果超过512KiB，未导入候选")
        try:
            result = json.loads(raw.decode("utf-8"))
        except (ValueError, UnicodeError):
            raise ValueError("浏览器未返回单条有效JSON（退出码%d）" % code)
        if not isinstance(result, dict):
            raise ValueError("浏览器结果不是JSON对象")
        if code not in (0, 1, 3) or (code != 0 and result.get("status") == "ok"):
            raise ValueError("浏览器退出码与结果状态不一致")
        return result


def primary_url(value, source):
    parsed, host = https_parts(value)
    if host in ("t.co", "x.com", "twitter.com") or not any(host == domain or host.endswith("." + domain) for domain in source["primary_domains"]):
        raise ValueError("证据链接不在该账号的原始证据域名内")
    if parsed.path in ("", "/") or re.match(r"^/(?:login|signin|signup|oauth|account|intent|search)(?:/|$)", parsed.path, re.I):
        raise ValueError("证据链接必须指向具体公开内容")
    if any(re.search(r"token|password|secret|session|authorization", key, re.I) for key, _ in urllib.parse.parse_qsl(parsed.query)):
        raise ValueError("证据链接不应包含会话参数")
    return collect.canonical_url(value)


def validate_candidate(item, source):
    if not isinstance(item, dict):
        raise ValueError("候选必须是对象")
    title = bounded_text(item.get("title"), "标题", 8, 120)
    summary = bounded_text(item.get("summary"), "摘要", 15, 300)
    body = bounded_text(item.get("body"), "正文", 80, 15000)
    if MARKETING.search(title + "\n" + summary + "\n" + body):
        raise ValueError("候选含营销、带货或抽奖内容")
    parsed, host = https_parts(item.get("source_url"))
    primary = []
    if source["id"] == LINUX["id"]:
        match = re.match(r"^/t/(?:[A-Za-z0-9_-]+/)?([1-9][0-9]{0,17})(?:/[1-9][0-9]{0,8})?/?$", parsed.path)
        if host != "linux.do" or not match:
            raise ValueError("社区候选必须来自指定公开话题路径")
        link = "https://linux.do/t/topic/" + match.group(1)
        visibility = "internal"
    else:
        match = re.match(r"^/([A-Za-z0-9_]{1,15})/status/([1-9][0-9]{0,24})/?$", parsed.path)
        author = bounded_text(item.get("author_handle"), "发帖作者", 1, 16)
        if not re.match(r"^@?[A-Za-z0-9_]{1,15}$", author) or author.lstrip("@").lower() != source["handle"]:
            raise ValueError("帖子作者与账号白名单不符")
        if host != "x.com" or not match or match.group(1).lower() != source["handle"]:
            raise ValueError("帖子来源路径与账号白名单不符")
        link = "https://x.com/" + source["handle"] + "/status/" + match.group(2)
        values = item.get("primary_evidence_urls")
        if not isinstance(values, list) or not values or len(values) > 8:
            raise ValueError("X候选必须提供原始证据链接")
        primary = list(dict.fromkeys(primary_url(value, source) for value in values))
        visibility = "public"
        body += "\n\n## 原始证据\n\n" + "\n".join("- [原始证据 %d](%s)" % (i + 1, value) for i, value in enumerate(primary))
        if len(body) > 15000:
            raise ValueError("加入原始证据后正文超出长度上限")
    raw_date = item.get("published_at", "")
    if not isinstance(raw_date, str) or len(raw_date) > 64:
        raise ValueError("来源日期格式不正确")
    published = collect.stamp(raw_date)
    if raw_date and not published:
        raise ValueError("来源日期无法解析")
    if published and collect.parse_date(published) > collect.utcnow() + dt.timedelta(days=1):
        raise ValueError("来源日期在未来")
    tags = item.get("tags", [])
    if not isinstance(tags, list) or len(tags) > 8:
        raise ValueError("关键词数量不正确")
    tags = [bounded_text(value, "关键词", 1, 24) for value in tags]
    tags = list(dict.fromkeys(["浏览器采集", "待核验"] + tags))[:8]
    # Never import source-provided publication, attribution, tier or evidence flags.
    output = {"title": title, "summary": summary, "body": body, "category": source["category"], "source_name": source["name"], "source_url": link, "published_at": published, "source_visibility": visibility, "evidence": "unverified", "level": "ordinary", "status": "pending", "tags": tags, "review_note": "浏览器公开页面候选；来源与账号已校验，内容仍需人工核验，尚未发布。", "primary_evidence_urls": primary}
    if "raw_text" in item:
        raw_text = item["raw_text"]
        length, truncated = item.get("raw_text_length"), item.get("text_truncated")
        if (not isinstance(raw_text, str) or len(raw_text) > 20000 or type(length) is not int or
                type(truncated) is not bool or length < len(raw_text) or
                (not truncated and length != len(raw_text)) or
                item.get("raw_text_scope") != "main_post_visible_dom" or source["id"] != LINUX["id"]):
            raise ValueError("主帖原文范围或长度记录不正确")
        output.update(raw_text=raw_text, text_truncated=truncated)
    return output


def validate_result(raw, source):
    if not isinstance(raw, dict) or raw.get("source_id") != source["id"]:
        raise ValueError("浏览器结果的来源ID不匹配")
    status = raw.get("status")
    if status not in ("ok", "blocked", "login_required", "error"):
        raise ValueError("浏览器状态不合法")
    http_status = raw.get("http_status")
    if http_status is not None and (isinstance(http_status, bool) or not isinstance(http_status, int) or not 100 <= http_status <= 599):
        raise ValueError("HTTP状态格式不正确")
    result = {"checked_at": collect.now(), "source_id": source["id"], "status": status, "http_status": http_status, "error": str(raw.get("error") or "")[:300], "candidates": [], "diagnostics": {}}
    diagnostics = raw.get("diagnostics") or {}
    if not isinstance(diagnostics, dict):
        raise ValueError("浏览器诊断格式不正确")
    for key in ("visible_items", "filtered_count", "duration_ms", "blocked_resource_count", "mode", "page_title"):
        value = diagnostics.get(key)
        if isinstance(value, (int, float, bool)) or value is None:
            result["diagnostics"][key] = value
        elif isinstance(value, str):
            result["diagnostics"][key] = value[:160]
    if status != "ok":
        result["error"] = result["error"] or {"blocked": "公开页面访问受限", "login_required": "页面要求登录", "error": "浏览器采集失败"}[status]
        return result
    visible = diagnostics.get("visible_items")
    if isinstance(visible, bool) or not isinstance(visible, int) or visible <= 0 or http_status is None or not 200 <= http_status < 300:
        raise ValueError("页面没有可核对的公开帖子，不能记为采集成功")
    candidates = raw.get("candidates")
    if not isinstance(candidates, list):
        raise ValueError("候选列表格式不正确")
    rejected = []
    for candidate in candidates[:MAX_CANDIDATES]:
        try:
            result["candidates"].append(validate_candidate(candidate, source))
        except (ValueError, TypeError, KeyError) as exc:
            rejected.append(str(exc)[:160])
    result["diagnostics"].update(received=len(candidates), validated=len(result["candidates"]), rejected=len(rejected), ignored_over_limit=max(0, len(candidates) - MAX_CANDIDATES), rejection_reasons=rejected)
    if candidates and not result["candidates"]:
        result["status"], result["error"] = "error", "候选均未通过账号、证据或内容检查"
    return result


def save_result(path, result):
    with database_identity(path):
        folder = Path(path).parent / "browser-runs"
        folder.mkdir(mode=0o700, exist_ok=True)
        temporary = None
        try:
            with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=str(folder), prefix=".result-", delete=False) as stream:
                temporary = stream.name
                json.dump(result, stream, ensure_ascii=False)
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(temporary, str(folder / (result["source_id"] + ".json")))
        finally:
            if temporary and os.path.exists(temporary):
                os.unlink(temporary)


def content_fingerprint(body):
    # Different post URLs can quote the same material. Ignore whitespace and
    # the mechanically appended evidence list when comparing actual content.
    body = body.split("\n\n## 原始证据\n\n", 1)[0]
    return hashlib.sha256(re.sub(r"\s+", "", body).casefold().encode("utf-8")).hexdigest()


def record_result(path, source, result):
    with database(path) as db:
        with db:
            # An administrator can pause a source while the isolated browser runs.
            enabled = db.execute("SELECT enabled FROM sources WHERE id=?", (source["id"],)).fetchone()
            if not enabled or not enabled[0]:
                result["status"], result["error"], result["candidates"] = "error", "来源已被管理员暂停，本轮候选未导入", []
            inserted = 0
            if result["status"] == "ok":
                fingerprints = {content_fingerprint(row[0]) for row in db.execute("SELECT body FROM gaps")}
                duplicate_content = 0
                for candidate in result["candidates"]:
                    fingerprint = content_fingerprint(candidate["body"])
                    if fingerprint in fingerprints:
                        duplicate_content += 1
                        collect.queue_summary(db, candidate, candidate.get("raw_text", ""), candidate.get("text_truncated", False))
                        continue
                    inserted += collect.insert_gap(db, candidate, status="pending")
                    collect.queue_summary(db, candidate, candidate.get("raw_text", ""), candidate.get("text_truncated", False))
                    fingerprints.add(fingerprint)
                result["diagnostics"]["duplicate_content"] = duplicate_content
                db.execute("UPDATE sources SET last_success=?,last_error='',last_count=? WHERE id=?", (result["checked_at"], inserted, source["id"]))
            else:
                result["candidates"] = []
                db.execute("UPDATE sources SET last_error=?,last_count=0 WHERE id=?", ("[" + result["status"] + "] " + result["error"][:250], source["id"]))
            result["diagnostics"]["inserted"] = inserted
    try:
        save_result(path, result)
    except OSError:
        # The transaction has already committed. A diagnostic-file failure must
        # not falsely report a failed batch that was actually imported.
        result["diagnostics"]["result_record_error"] = "结果文件未写入；数据库记录已保存，请检查目录权限"
    return result


def process_source(path, source, invoke=invoke_browser):
    with database(path) as db:
        with db:
            db.execute("UPDATE sources SET last_attempt=? WHERE id=?", (collect.now(), source["id"]))
    try:
        result = validate_result(invoke(source), source)
    except Exception as exc:
        result = {"checked_at": collect.now(), "source_id": source["id"], "status": "error", "http_status": None, "error": str(exc)[:300], "candidates": [], "diagnostics": {}}
    try:
        return record_result(path, source, result)
    except Exception:
        # A failed insertion rolls back the whole batch; report failure in a fresh
        # owner connection without retrying or silently partially importing it.
        failure = {"checked_at": collect.now(), "source_id": source["id"], "status": "error", "http_status": result.get("http_status"), "error": "候选入库或结果记录失败，请检查数据库及目录权限", "candidates": [], "diagnostics": {}}
        with database(path) as db:
            with db:
                db.execute("UPDATE sources SET last_error=?,last_count=0 WHERE id=?", ("[error] " + failure["error"], source["id"]))
        try:
            save_result(path, failure)
        except OSError:
            pass
        return failure


def run(args, invoke=invoke_browser):
    path = Path(args.db).resolve()
    if not path.is_file():
        raise ValueError("数据库不存在；请先启动InfoGap服务")
    sources = read_sources(args.accounts)
    requested = set(args.source or [])
    if requested - {source["id"] for source in sources}:
        raise ValueError("请求的来源不在固定白名单中")
    with collector_lock(path):
        with database(path) as db:
            collect.sync_sources(db, sources)
            states = {row[0]: row[1:] for row in db.execute("SELECT id,enabled,last_attempt FROM sources")}
        results = []
        for source in sources:
            if requested and source["id"] not in requested:
                continue
            enabled, attempted = states[source["id"]]
            if not source["enabled"] or not enabled:
                continue
            previous = collect.parse_date(attempted)
            if not args.force and previous and (collect.utcnow() - previous).total_seconds() < INTERVAL_SECONDS:
                continue
            result = process_source(path, source, invoke)
            results.append(result)
            # Full private candidate records stay in the owner-only result file.
            print(json.dumps({key: result[key] for key in ("checked_at", "source_id", "status", "http_status", "error", "diagnostics")}, ensure_ascii=False), flush=True)
        failed = sum(result["status"] != "ok" for result in results)
        print(json.dumps({"checked": len(results), "successful": len(results) - failed, "failed": failed, "model_calls": 0, "model_tokens": 0}, ensure_ascii=False))
        return 1 if failed else 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--db", default="/var/lib/infogap-osh/infogap.sqlite")
    parser.add_argument("--accounts", default=str(ROOT / "data" / "x-accounts.json"))
    parser.add_argument("--source", action="append", help="Only a configured source ID; repeat to select more")
    parser.add_argument("--force", action="store_true", help="Ignore six-hour interval; never override an administrator pause")
    args = parser.parse_args()
    try:
        return run(args)
    except BlockingIOError:
        print('{"skipped":"another collector is running"}')
        return 0
    except Exception as exc:
        print(json.dumps({"error": str(exc)[:300]}, ensure_ascii=False), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
