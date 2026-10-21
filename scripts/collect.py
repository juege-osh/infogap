#!/usr/bin/env python3
"""Official-source candidate collector. Python 3.6+, standard library only.

The Go application owns the schema. This script never publishes crawled items.
--check makes no database writes; --import-seed imports separately reviewed data.
"""
import argparse
import datetime as dt
import email.utils
import fcntl
import html
import hashlib
from html.parser import HTMLParser
import ipaddress
import json
import os
from pathlib import Path
import re
import socket
import sqlite3
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import xml.etree.ElementTree as ET

ROOT = Path(__file__).resolve().parent.parent
UA = "InfoGap/1.0 (+https://infogap.osh.lol; official-feed-monitor)"
MAX_BYTES = 3 * 1024 * 1024
TIMEOUT = 18
MAX_TAGS = 10
# Keep the shared database entry point aligned with the Go content API.
CATEGORIES = frozenset({"前沿科技", "人工智能", "生命科学", "物理", "化学", "地球与地理", "天文与空间", "材料科学", "开源技术", "军事与国防", "政治与公共政策", "农业与食品", "社会民生", "法律法规", "航空航天", "经济与产业", "能源与环境", "医疗健康", "教育与就业", "交通与基建", "公共安全", "生活实用", "省钱优惠", "招聘求职", "职业成长", "住房居住", "出行旅行", "商业创业"})
ALLOWED_HOSTS = {"www.nature.com", "www.science.org", "rss.arxiv.org", "www.nasa.gov", "www.esa.int", "api.github.com", "linux.do", "www.gov.cn", "www.mca.gov.cn", "www.mot.gov.cn", "www.cnsa.gov.cn", "www.gov.uk", "www.federalregister.gov", "www.who.int", "www.moa.gov.cn", "www.ndrc.gov.cn", "www.mee.gov.cn"}
ROBOT_CACHE = {}
HOST_LAST = {}


def now():
    return utcnow().strftime("%Y-%m-%dT%H:%M:%SZ")


def utcnow():
    return dt.datetime.now(dt.timezone.utc).replace(tzinfo=None)


def parse_date(value):
    value = (value or "").strip()
    if not value:
        return None
    try:
        d = email.utils.parsedate_to_datetime(value)
        if d.tzinfo is not None:
            d = d.astimezone(dt.timezone.utc).replace(tzinfo=None)
        return d
    except (ValueError, TypeError, IndexError):
        pass
    # RSS/Atom dates are not all UTC. Preserve a numeric offset when present.
    match = re.match(r"^(\d{4}-\d{2}-\d{2})(?:T(\d{2}:\d{2}:\d{2})(?:\.\d+)?(Z|[+-]\d{2}:?\d{2})?)?$", value)
    if not match:
        return None
    d = dt.datetime.strptime(match.group(1) + "T" + (match.group(2) or "00:00:00"), "%Y-%m-%dT%H:%M:%S")
    offset = match.group(3)
    if offset and offset != "Z":
        digits = offset[1:].replace(":", "")
        minutes = int(digits[:2]) * 60 + int(digits[2:])
        d -= dt.timedelta(minutes=minutes * (1 if offset[0] == "+" else -1))
    return d


def stamp(value):
    d = parse_date(value)
    return d.strftime("%Y-%m-%dT%H:%M:%SZ") if d else ""


class TextOnly(HTMLParser):
    def __init__(self):
        HTMLParser.__init__(self)
        self.parts = []
        self.skip = 0

    def handle_starttag(self, tag, attrs):
        if tag in ("script", "style"):
            self.skip += 1

    def handle_endtag(self, tag):
        if tag in ("script", "style"):
            self.skip = max(0, self.skip - 1)

    def handle_data(self, value):
        if not self.skip:
            self.parts.append(value)


def text_only(value):
    p = TextOnly()
    p.feed(value or "")
    return re.sub(r"\s+", " ", html.unescape(" ".join(p.parts))).strip()


def canonical_url(value):
    p = urllib.parse.urlsplit((value or "").strip())
    if p.scheme not in ("http", "https") or not p.hostname or p.username or p.password:
        raise ValueError("来源链接不是公开网页地址")
    host = p.hostname.lower()
    if host in ("localhost", "localhost.localdomain") or host.endswith(".local"):
        raise ValueError("拒绝本机来源地址")
    try:
        if not ipaddress.ip_address(host).is_global:
            raise ValueError("拒绝非公网来源地址")
    except ValueError as exc:
        if "拒绝" in str(exc):
            raise
    path = p.path or "/"
    if host in ("arxiv.org", "www.arxiv.org"):
        host = "arxiv.org"
        path = re.sub(r"v\d+$", "", path)
    if host == "www.science.org":
        path = re.sub(r"^/doi/(abs|full)/", "/doi/", path)
    query = urllib.parse.urlencode([(k, v) for k, v in urllib.parse.parse_qsl(p.query, keep_blank_values=True)
                                   if not k.lower().startswith("utm_") and k.lower() not in ("af", "fbclid", "gclid")])
    return urllib.parse.urlunsplit(("https", host + ((":" + str(p.port)) if p.port and p.port != 443 else ""), path.rstrip("/") or "/", query, ""))


def guard_fetch(url):
    p = urllib.parse.urlsplit(url)
    if p.scheme != "https" or p.hostname not in ALLOWED_HOSTS or p.username or p.password or p.port not in (None, 443):
        raise ValueError("请求地址不在已核验的官方来源白名单内")
    for result in socket.getaddrinfo(p.hostname, 443, type=socket.SOCK_STREAM):
        if not ipaddress.ip_address(result[4][0]).is_global:
            raise ValueError("官方来源解析到了非公网地址，已停止")


class SafeRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        guard_fetch(newurl)
        return urllib.request.HTTPRedirectHandler.redirect_request(self, req, fp, code, msg, headers, newurl)


OPENER = urllib.request.build_opener(SafeRedirect())


def fetch(url, delay=2):
    guard_fetch(url)
    host = urllib.parse.urlsplit(url).hostname
    for attempt in range(3):
        elapsed = time.monotonic() - HOST_LAST.get(host, 0)
        if elapsed < delay:
            time.sleep(delay - elapsed)
        HOST_LAST[host] = time.monotonic()
        req = urllib.request.Request(url, headers={"User-Agent": UA, "Accept": "application/atom+xml, application/rss+xml, application/json, application/xml, text/html;q=0.9, text/xml;q=0.9"})
        try:
            with OPENER.open(req, timeout=TIMEOUT) as r:
                data = r.read(MAX_BYTES + 1)
                if len(data) > MAX_BYTES:
                    raise ValueError("来源响应超过3MB，已停止")
                return data
        except urllib.error.HTTPError as exc:
            if exc.code not in (429, 500, 502, 503, 504) or attempt == 2:
                raise
            wait = exc.headers.get("Retry-After", "")
            if wait.isdigit() and int(wait) > 30:
                raise RuntimeError("来源要求较长等待，本次跳过，下一轮再试")
            time.sleep(max(2 ** (attempt + 1), int(wait) if wait.isdigit() else 0))
        except (urllib.error.URLError, socket.timeout, ConnectionError):
            if attempt == 2:
                raise
            time.sleep(2 ** (attempt + 1))


def robot_rules(url):
    """Longest matching rule, including specific Allow overriding /action Disallow."""
    origin = "https://" + urllib.parse.urlsplit(url).netloc
    if origin not in ROBOT_CACHE:
        try:
            raw = fetch(origin + "/robots.txt").decode("utf-8", "replace")
        except urllib.error.HTTPError as exc:
            if exc.code not in (404, 410):
                raise RuntimeError("robots.txt不可读取，暂缓采集（HTTP %s）" % exc.code)
            raw = "User-agent: *\nAllow: /"
        if raw.lstrip().startswith("<"):
            raise RuntimeError("robots.txt返回网页或验证页面，已停止")
        groups, agents, rules, crawl = [], [], [], 2.0
        for line in (raw + "\nUser-agent: __end__").splitlines():
            line = line.split("#", 1)[0].strip()
            if ":" not in line:
                continue
            key, value = [part.strip() for part in line.split(":", 1)]
            key = key.lower()
            if key == "user-agent":
                if rules:
                    groups.append((agents, rules, crawl))
                    agents, rules, crawl = [], [], 2.0
                agents.append(value.lower())
            elif key in ("allow", "disallow") and agents and value:
                rules.append((key == "allow", value))
            elif key == "crawl-delay" and agents:
                try:
                    crawl = max(2.0, float(value))
                except ValueError:
                    pass
        matches = []
        for names, entries, pause in groups:
            strength = max([len(a) if a != "*" else 0 for a in names if a == "*" or a in UA.lower()] or [-1])
            if strength >= 0:
                matches.append((strength, entries, pause))
        highest = max([m[0] for m in matches] or [-1])
        selected = [m for m in matches if m[0] == highest]
        ROBOT_CACHE[origin] = ([rule for _, entries, _ in selected for rule in entries], max([m[2] for m in selected] or [2]))
    rules, delay = ROBOT_CACHE[origin]
    p = urllib.parse.urlsplit(url)
    target = p.path + ("?" + p.query if p.query else "")
    hits = []
    for allow, pattern in rules:
        anchor = pattern.endswith("$")
        expr = re.escape(pattern[:-1] if anchor else pattern).replace(r"\*", ".*")
        if re.match("^" + expr + ("$" if anchor else ""), target):
            hits.append((len(pattern.replace("*", "").rstrip("$")), allow))
    if hits and not max(hits)[1]:
        raise RuntimeError("该来源robots.txt不允许当前地址，已跳过")
    if delay > 60:
        raise RuntimeError("来源请求间隔超过60秒，留待人工配置")
    return delay


def child_text(item, names):
    for name in names:
        for node in item:
            if node.tag.split("}")[-1] == name:
                if name == "link" and node.attrib.get("href") and node.attrib.get("rel", "alternate") == "alternate":
                    return node.attrib["href"]
                value = "".join(node.itertext()).strip()
                if value:
                    return value
    return ""


def list_date(value):
    """Only explicit calendar dates; an undated announcement stays undated."""
    matches = list(re.finditer(r"(?<!\d)(20\d{2})[-年/.](\d{1,2})[-月/.](\d{1,2})(?:日|\b)", value))
    match = matches[-1] if matches else None
    if not match:
        match = re.search(r"(?:^|[/_t-])(20\d{2})(\d{2})(\d{2})(?=[_/.-]|$)", value)
    if not match:
        return ""
    try:
        return dt.date(*[int(v) for v in match.groups()]).isoformat()
    except ValueError:
        return ""


class OfficialList(HTMLParser):
    """Read configured article links from a public official index, not its navigation."""
    def __init__(self, source):
        HTMLParser.__init__(self)
        self.source = source
        self.host = urllib.parse.urlsplit(source["url"]).hostname
        self.pattern = re.compile(source["article_pattern"])
        self.items, self.seen = [], set()
        self.anchor, self.row = None, None
        self.date_tag, self.date_text = None, []
        self.skip = 0

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag in ("script", "style"):
            self.skip += 1
        if tag in ("li", "tr"):
            self.row = {"text": [], "items": []}
        if tag == "a" and not self.skip:
            url = urllib.parse.urljoin(self.source["url"], attrs.get("href", "").strip())
            parsed = urllib.parse.urlsplit(url)
            if (parsed.scheme in ("http", "https") and parsed.hostname == self.host and
                    not parsed.username and not parsed.password and parsed.port in (None, 80, 443) and
                    self.pattern.search(parsed.path)):
                self.anchor = {"url": url, "text": [], "fallback": attrs.get("title", "")}
                self.date_text = []
        if self.anchor is not None and self.source.get("date_class") in attrs.get("class", "").split():
            self.date_tag = tag

    def handle_data(self, value):
        if self.skip:
            return
        if self.anchor is not None:
            self.anchor["text"].append(value)
            if self.date_tag:
                self.date_text.append(value)
        # Dates inside an article title may be its effective date, not publication.
        if self.row is not None and self.anchor is None:
            self.row["text"].append(value)

    def handle_endtag(self, tag):
        if tag in ("script", "style"):
            self.skip = max(0, self.skip - 1)
        if tag == self.date_tag:
            self.date_tag = None
        if tag == "a" and self.anchor is not None:
            title = text_only(self.anchor["fallback"]) or text_only(" ".join(self.anchor["text"]))
            url = canonical_url(self.anchor["url"])
            if len(title) >= 8 and url not in self.seen:
                item = {"title": title, "url": url, "description": "", "date": list_date(" ".join(self.date_text)) or list_date(urllib.parse.urlsplit(url).path)}
                self.items.append(item)
                self.seen.add(url)
                if self.row is not None:
                    self.row["items"].append(item)
            self.anchor = None
        if tag in ("li", "tr") and self.row is not None:
            row_text = " ".join(self.row["text"])
            matches = re.findall(r"(?<!\d)20\d{2}[-年/.]\d{1,2}[-月/.]\d{1,2}(?:日|\b)", row_text)
            dates = set(list_date(value) for value in matches)
            dates.discard("")
            date = next(iter(dates)) if len(dates) == 1 else ""
            if date:
                for item in self.row["items"]:
                    item["date"] = date
            self.row = None


def parse_items(raw, source):
    if source["kind"] == "html":
        parser = OfficialList(source)
        parser.feed(raw.decode(source.get("encoding", "utf-8"), "replace"))
        if not parser.items:
            raise ValueError("官方栏目没有匹配到文章，可能是页面结构变化或访问验证；本轮不视为成功")
        return parser.items
    if source["kind"] == "github":
        records = json.loads(raw.decode("utf-8"))
        if not isinstance(records, list):
            raise ValueError("GitHub未返回发布列表")
        return [{"title": r.get("name") or r.get("tag_name", ""), "url": r.get("html_url", ""),
                 "description": r.get("body", ""), "date": r.get("published_at", "")}
                for r in records if not r.get("draft") and not r.get("prerelease")]
    if b"<!ENTITY" in raw.upper() or b"<!DOCTYPE" in raw.upper():
        raise ValueError("XML包含不受支持的实体声明")
    root = ET.fromstring(raw)
    records = [n for n in root.iter() if n.tag.split("}")[-1] in ("item", "entry")]
    if not records:
        # A valid empty feed is allowed; HTML/challenge pages are not.
        if root.tag.split("}")[-1].lower() not in ("rss", "rdf", "feed"):
            raise ValueError("来源返回了网页而非RSS/Atom订阅")
    return [{"title": child_text(n, ["title"]), "url": child_text(n, ["link"]),
             "description": child_text(n, ["description", "summary", "encoded", "content"]),
             "categories": [text_only(c.attrib.get("term", "") or "".join(c.itertext()))
                            for c in n if c.tag.split("}")[-1] == "category"],
             # A modification timestamp is not proof of initial publication.
             "date": child_text(n, ["published", "pubDate", "date"])} for n in records]


def published_date(item, source):
    """One publication-date policy for sorting, freshness and stored metadata."""
    try:
        if source.get("date_from_url") == "dd-mm-yyyy":
            # WHO may republish an old item at the front with a fresh RSS pubDate.
            # Its dated article path takes precedence; missing paths stay unknown.
            path = urllib.parse.urlsplit(item.get("url", "")).path
            match = re.search(r"/(\d{2})-(\d{2})-(20\d{2})-", path)
            return stamp("%s-%s-%s" % (match.group(3), match.group(2), match.group(1))) if match else ""
        return stamp(item.get("date"))
    except (ValueError, TypeError, OverflowError):
        return ""


def recent_items(items, source):
    """Exclude old/undated automatic candidates before limiting or model queuing.

    Historical seed imports intentionally do not pass through this function.
    """
    max_age_days = source.get("max_age_days", 180)
    if type(max_age_days) is not int or not 1 <= max_age_days <= 36500:
        raise ValueError("max_age_days必须是1至36500之间的整数")
    current = utcnow()
    cutoff = current - dt.timedelta(days=max_age_days)
    reasons = {"old": 0, "undated": 0, "future": 0, "editorial": 0}
    eligible = []
    for item in items:
        if not source_filter(item, source):
            reasons["editorial"] += 1
            continue
        published = published_date(item, source)
        date = parse_date(published) if published else None
        if date is None:
            reasons["undated"] += 1
        elif date > current + dt.timedelta(days=1):
            reasons["future"] += 1
        elif date < cutoff:
            reasons["old"] += 1
        else:
            normalized = dict(item)
            normalized["date"] = published
            eligible.append(normalized)
    eligible.sort(key=lambda item: item["date"], reverse=True)
    return eligible, reasons, max_age_days


def community_source(source):
    return (source.get("source_visibility") == "internal" or
            urllib.parse.urlsplit(source["url"]).hostname == "linux.do")


def source_filter(item, source):
    """Conservative editorial gate for public community metadata, not a quality verdict."""
    if source.get("editorial_filter") != "linuxdo_tech":
        return True
    if urllib.parse.urlsplit(item.get("url", "")).hostname != "linux.do":
        return False
    categories = [text_only(value).casefold() for value in item.get("categories", [])]
    allowed = [value.casefold() for value in source.get("allowed_categories", [])]
    # Missing category metadata is not enough to admit a mixed latest-feed post.
    if not any(value in allowed for value in categories):
        return False
    title = text_only(item.get("title", ""))
    labels = " ".join(categories)
    if re.search(source["exclude_pattern"], title + " " + labels, re.I):
        return False
    return bool(re.search(source["topic_pattern"], title, re.I) and
                re.search(source["signal_pattern"], title, re.I))


def official_category(title, source):
    """Route practical official updates without an extra model call.

    This is a review-queue suggestion, never a claim of eligibility or value.
    Scientific and community sources retain their existing classification.
    """
    host = urllib.parse.urlsplit(source.get("url", "")).hostname
    if source.get("evidence") != "official" or host not in {
            "www.gov.cn", "www.mca.gov.cn", "www.mot.gov.cn",
            "www.moa.gov.cn", "www.ndrc.gov.cn", "www.mee.gov.cn"}:
        return source["category"]
    rules = [
        ("招聘求职", r"招聘|求职|招录|招考|见习岗位|岗位征集"),
        ("住房居住", r"公积金|租房|住房租赁|保障性住房|保障房|公租房|购房"),
        ("职业成长", r"职业培训|职业技能|技能培训|继续教育|资格考试|职称|职业资格"),
        ("省钱优惠", r"消费券|优惠券|以旧换新|购置补贴|消费补贴|家电补贴|购车补贴|免费开放|免费乘车|费用减免"),
        ("出行旅行", r"旅游|出游|景区|景点|免签|签证|出入境|旅客|客票|退票|车票|机票|列车"),
        ("商业创业", r"创业|初创企业|个体工商户|小微企业"),
        ("生活实用", r"便民|生活服务|身份证|居住证|社保卡|医保结算|证件办理|水电气|异地就医"),
    ]
    for category, pattern in rules:
        if re.search(pattern, title):
            return category
    return source["category"]


def candidate(item, source):
    title = text_only(item["title"])[:220]
    if not title:
        raise ValueError("条目没有标题")
    url = canonical_url(item["url"])
    # Feed text is an excerpt, never claimed to be an original Chinese analysis.
    internal = community_source(source)
    excerpt = text_only(item.get("description", ""))[:200 if internal else 500]
    date = published_date(item, source)
    if date and parse_date(date) > utcnow() + dt.timedelta(days=1):
        raise ValueError("来源日期在未来，需人工核对")
    if internal:
        category = source["category"]
        if re.search(r"人工智能|大模型|机器学习|深度学习|神经网络|模型|智能体|OpenAI|Anthropic|PyTorch|TensorFlow|\b(?:AI|LLM|GPT|Claude|Gemini|DeepSeek|Qwen|Llama|Codex|Agent)\b", title, re.I):
            category = "人工智能"
        elif re.search(r"开源|编程|开发|数据库|编译|软件|漏洞|补丁|\b(?:Linux|Python|Rust|PHP|Java|GitHub|Docker|Kubernetes|PostgreSQL|Redis|API|SDK)\b", title, re.I):
            category = "开源技术"
        body = ("## 核心进展\n\n**待核验线索**。以下仅为公开订阅的短摘录，不代表已确认原帖结论：\n\n" +
                (excerpt or "订阅没有可用摘要；需编辑核对原始证据。") +
                "\n\n## 为什么值得关注\n\n标题符合科技或开发主题，具体进展、适用条件和实际价值仍需审核者确认。" +
                "\n\n## 证据与边界\n\n社区用户帖不等于项目官方公告或同行评审论文。未访问登录内容，也未读取或复制原帖全文。" +
                "发布前须独立核验，重写正文，并去除社区名称和原帖链接；内部出处只供管理员追溯，不能改称官方来源。" +
                "\n\n## 内部核验参考\n\n原帖标题：" + title + "\n\n[" + source["name"] + "原帖](" + url + ")")
        return {"title": title, "summary": "待核验候选｜社区公开线索，需补充原始证据和编辑解读。",
                "body": body, "category": category, "level": "ordinary", "evidence": "unverified",
                "source_name": source["name"], "source_url": url, "source_visibility": "internal", "published_at": date,
                "tags": ["自动采集", "待核验", "内部线索"],
                "review_note": "内部社区线索：只采集公开RSS元信息与最多200字摘录；发布前须去除正文中的社区名称与链接，并独立核验。"}
    if source["kind"] == "html":
        limitation = "目前只核对了官方栏目中的标题、文章地址和可识别的发布日期，尚未读取和核实文章正文。具体变化、适用对象和执行条件需编辑补充。"
    else:
        limitation = "目前仅采集官方订阅或发布说明，未读取论文全文。英文标题和摘录保留原文，中文解读及价值评级待审核。"
    if source["evidence"] == "preprint":
        limitation += " arXiv收录不等于通过同行评审；订阅日期也可能是公告或修订日期。"
    elif source["evidence"] == "unverified":
        limitation += " 期刊目录也可能包含新闻、社论或评论，必须核对文章类型。"
    body = "## 核心进展\n\n" + (excerpt or "来源列表只提供了标题和链接，具体内容待核验。") + "\n\n## 为什么值得关注\n\n**待核验候选**，需审核者补充具体变化、适用对象和价值。\n\n## 证据与边界\n\n" + limitation
    if source.get("jurisdiction"):
        body += ("\n\n## 适用范围与时间\n\n地区：" + source["jurisdiction"] +
                 "\n\n来源发布日期：" + (date[:10] if date else "待核验") +
                 "\n\n生效日期：待核验。文件属于草案、正式公布、现行有效或已废止，需以正文及后续官方文件为准；发布日期不等于生效日期。")
    category = official_category(title, source)
    if category in {"招聘求职", "省钱优惠", "职业成长", "住房居住", "商业创业", "出行旅行", "生活实用"}:
        body += "\n\n## 参与条件与时限\n\n适用地区、参与资格、费用、办理入口及截止时间待核验；正式发布前须补齐。长期服务也需确认目前仍开放。"
    body += "\n\n## 原始来源\n\n[" + source["name"] + "](" + url + ")"
    return {"title": title, "summary": "待核验候选｜" + source["name"] + "新条目，具体内容和价值待编辑核实。",
            "body": body, "category": category, "level": "ordinary", "evidence": source["evidence"],
            "source_name": source["name"], "source_url": url, "published_at": date,
            "tags": ["自动采集", "待核验"], "review_note": "只采集元信息和短摘录；禁止直接把此条视为全文解读。"}


def insert_gap(db, item, status="pending", historical_seed=False):
    data = dict(item)
    if not isinstance(data.get("category"), str) or data["category"] not in CATEGORIES:
        raise ValueError("每条信息必须选择一个有效主题")
    tags = data.get("tags", [])
    if not isinstance(tags, list) or any(not isinstance(tag, str) for tag in tags):
        raise ValueError("标签必须是文本数组")
    if len(tags) > MAX_TAGS:
        raise ValueError("每条信息最多10个标签")
    if any(len(tag) > 24 for tag in tags):
        raise ValueError("每个标签最多24字")
    # Like Go's []string API, preserve order, whitespace and duplicates; count
    # the supplied tags before any normalization instead of silently truncating.
    data["tags"] = tags
    data["source_url"] = canonical_url(data["source_url"])
    if db.execute("SELECT 1 FROM gaps WHERE source_url=?", (data["source_url"],)).fetchone():
        return 0
    fields = ["title", "summary", "body", "category", "level", "evidence", "source_name", "source_url", "published_at", "review_note"]
    if data.get("source_visibility") == "internal" or urllib.parse.urlsplit(data["source_url"]).hostname == "linux.do":
        if "source_visibility" not in [row[1] for row in db.execute("PRAGMA table_info(gaps)")]:
            raise RuntimeError("数据库缺少source_visibility列；内部来源停止入库，请先更新Go服务")
        data["source_visibility"], data["evidence"] = "internal", "unverified"
        status = "pending"
        fields.append("source_visibility")
    # Only explicitly published, curated historical seeds inherit their source date.
    # Pending feed candidates keep the backend default and receive a release date on approval.
    if historical_seed and status == "published":
        if "released_at" not in [row[1] for row in db.execute("PRAGMA table_info(gaps)")]:
            raise RuntimeError("数据库缺少released_at列；请先更新Go服务")
        fields.append("released_at")
        # A manually verified evergreen guide may have no source publication
        # date. Publish it now; never present verification as the source date.
        data["released_at"] = stamp(data.get("published_at")) or now()
    values = [data.get(key, "") for key in fields]
    ts = now()
    db.execute("INSERT OR IGNORE INTO gaps (" + ",".join(fields) + ",created_at,updated_at,status,tags,author_id,rewarded) VALUES (" + ",".join(["?"] * (len(fields) + 4)) + ",NULL,0)", values + [ts, ts, status, json.dumps(data.get("tags", []), ensure_ascii=False)])
    return db.execute("SELECT changes()").fetchone()[0]


def queue_summary(db, item, source_text, truncated=False):
    """Private source material for a separate limited model worker; no LLM here."""
    if not isinstance(source_text, str) or (len(source_text) <= 2000 and not truncated):
        return 0
    row = db.execute("SELECT id,title,body,updated_at,status,author_id FROM gaps WHERE source_url=?", (canonical_url(item["source_url"]),)).fetchone()
    if not row or row[4] != "pending" or row[5] is not None:
        return 0
    state = "too_long" if truncated or len(source_text) > 20000 else "pending"
    error = "原文超过20000字或未完整提取，需要人工分段核验；未调用模型。" if state == "too_long" else ""
    digest = hashlib.sha256(source_text.encode("utf-8")).hexdigest()
    db.execute("INSERT OR IGNORE INTO summary_jobs(gap_id,content_hash,title,source_text,original_body_hash,original_updated_at,state,error,updated_at) VALUES(?,?,?,?,?,?,?,?,?)",
               (row[0], digest, row[1], source_text[:20000], hashlib.sha256(row[2].encode("utf-8")).hexdigest(), row[3], state, error, now()))
    inserted = db.execute("SELECT changes()").fetchone()[0]
    if inserted:
        note = "待长文精炼（gpt-6.1-sol，low）；模型稿也须审核。" if state == "pending" else error
        db.execute("UPDATE gaps SET review_note=review_note || ? WHERE id=?", (" " + note, row[0]))
    return inserted


def sync_sources(db, sources):
    for s in sources:
        db.execute("INSERT OR IGNORE INTO sources(id,name,url,kind,category,enabled) VALUES(?,?,?,?,?,?)", (s["id"], s["name"], s["url"], s["kind"], s["category"], int(s.get("enabled", True))))
        # Never turn back on a source disabled in the administrator UI.
        db.execute("UPDATE sources SET name=?,url=?,kind=?,category=? WHERE id=?", (s["name"], s["url"], s["kind"], s["category"], s["id"]))
    db.commit()


def run(args):
    sources = json.loads(Path(args.sources).read_text(encoding="utf-8"))
    db = None
    if not args.check:
        if not Path(args.db).is_file():
            raise RuntimeError("数据库不存在；请先运行Go服务创建schema，或使用--check只核验来源")
        db = sqlite3.connect(args.db, timeout=20)
        db.execute("PRAGMA busy_timeout=20000")
        for table in ("gaps", "sources"):
            if not db.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name=?", (table,)).fetchone():
                raise RuntimeError("数据库缺少%s表；请先启动Go服务" % table)
        sync_sources(db, sources)
    if args.import_seed:
        if args.check:
            raise ValueError("--check与--import-seed不能同时使用")
        try:
            seeds = json.loads(Path(args.import_seed).read_text(encoding="utf-8"))
            count = 0
            with db:
                for item in seeds:
                    source_date = stamp(item.get("published_at"))
                    verified = stamp(item.get("verified_at"))
                    if (not item.get("review_note") or (not source_date and not verified) or
                            (item.get("published_at") and not source_date) or
                            (verified and parse_date(verified) > utcnow())):
                        raise ValueError("种子内容必须有核验说明及原始日期；无原日期的常设指南需明确verified_at核验时间")
                    if not source_date:
                        item = dict(item, published_at="", review_note=item["review_note"] + " 原站未标注发布日期；本站核验时间：" + verified + "，公开发布后按本站发布时间计时。")
                    count += insert_gap(db, item, "published" if args.publish_seed else "pending", historical_seed=True)
            print(json.dumps({"seed_count": len(seeds), "inserted": count, "status": "published" if args.publish_seed else "pending"}, ensure_ascii=False))
        finally:
            db.close()
        return 0
    results, failures = [], 0
    for source in sources:
        if args.source and source["id"] not in args.source:
            continue
        if not source.get("enabled", True):
            continue
        if db:
            enabled, last_attempt = db.execute("SELECT enabled,last_attempt FROM sources WHERE id=?", (source["id"],)).fetchone()
            if not enabled:
                continue
            previous = parse_date(last_attempt)
            if not args.force and previous and (utcnow() - previous).total_seconds() < source.get("interval_hours", 6) * 3600:
                continue
            db.execute("UPDATE sources SET last_attempt=? WHERE id=?", (now(), source["id"]))
            db.commit()
        result = {"id": source["id"], "name": source["name"], "checked_at": now(), "url": source["url"]}
        try:
            # Official GitHub API access follows API rate limits, not HTML robots.
            delay = robot_rules(source["url"]) if source["kind"] != "github" else 2
            items = parse_items(fetch(source["url"], delay), source)
            eligible, filtered_reasons, max_age_days = recent_items(items, source)
            inserted, invalid, valid = 0, 0, 0
            for item in eligible[:args.max_items]:
                try:
                    gap = candidate(item, source)
                    valid += 1
                    if db:
                        with db:
                            inserted += insert_gap(db, gap)
                            queue_summary(db, gap, text_only(item.get("description", "")))
                except (ValueError, KeyError, TypeError):
                    invalid += 1
            if items and not valid and invalid:
                raise ValueError("订阅有条目但没有有效来源链接，需检查解析")
            if db:
                db.execute("UPDATE sources SET last_success=?,last_error='',last_count=? WHERE id=?", (now(), inserted, source["id"]))
                db.commit()
            result.update(ok=True, received=len(items), checked=valid, inserted=inserted, invalid=invalid,
                          filtered=sum(filtered_reasons.values()), filtered_reasons=filtered_reasons,
                          max_age_days=max_age_days, unknown_date_policy="skip", limited=max(0, len(eligible)-args.max_items))
        except Exception as exc:
            failures += 1
            # Do not include headers, credentials or response bodies in logs.
            error = "HTTP %s" % exc.code if isinstance(exc, urllib.error.HTTPError) else str(exc)[:250]
            if db:
                db.execute("UPDATE sources SET last_error=?,last_count=0 WHERE id=?", (error, source["id"]))
                db.commit()
            result.update(ok=False, error=error)
        results.append(result)
        print(json.dumps(result, ensure_ascii=False), flush=True)
    if db:
        db.close()
    print(json.dumps({"checked": len(results), "successful": len(results) - failures, "failed": failures, "dry_run": args.check}, ensure_ascii=False))
    return 1 if failures else 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--db", default=os.environ.get("INFOGAP_DB", str(ROOT / "data" / "infogap.db")))
    parser.add_argument("--sources", default=str(ROOT / "data" / "sources.json"))
    parser.add_argument("--source", action="append", help="Only this source ID; may be repeated")
    parser.add_argument("--max-items", type=int, default=10)
    parser.add_argument("--check", action="store_true", help="Read official feeds, report health, do not write DB")
    parser.add_argument("--force", action="store_true", help="Ignore interval, but respect disabled sources and access restrictions")
    parser.add_argument("--import-seed", "--seed", dest="import_seed", metavar="JSON", help="Import curated seed JSON and exit without fetching")
    parser.add_argument("--publish-seed", action="store_true", help="Explicitly publish only the reviewed --import-seed records")
    args = parser.parse_args()
    if args.max_items < 1 or args.max_items > 10:
        parser.error("--max-items must be 1..10")
    if args.publish_seed and not args.import_seed:
        parser.error("--publish-seed requires --import-seed")
    try:
        if args.check:
            return run(args)
        # A lock alongside the DB prevents scheduled workers from overlapping.
        with open(args.db + ".collector.lock", "a") as lock:
            try:
                fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                print('{"skipped":"another collector is running"}')
                return 0
            return run(args)
    except Exception as exc:
        print(json.dumps({"error": str(exc)[:300]}, ensure_ascii=False), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
