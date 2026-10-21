#!/usr/bin/env python3
"""Official index parser fixtures: no network or production database."""
import argparse
import contextlib
import datetime as dt
import importlib.util
import io
import json
from pathlib import Path
import re
import sqlite3
import tempfile
import unittest
from unittest import mock
from xml.sax.saxutils import escape
from urllib.parse import urlsplit

spec = importlib.util.spec_from_file_location("collect", str(Path(__file__).with_name("collect.py")))
worker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)

SOURCE = {"id": "fixture", "kind": "html", "url": "https://www.moa.gov.cn/xw/zwdt/", "name": "农业农村部", "category": "农业与食品", "evidence": "official", "article_pattern": r"^/xw/zwdt/\d{6}/t\d{8}_\d+\.htm$", "jurisdiction": "中国"}


class OfficialListTests(unittest.TestCase):
    def parse(self, html, source=None):
        return worker.parse_items(html.encode("utf-8"), source or SOURCE)

    def test_practical_official_candidates_are_routed_with_conditions_unverified(self):
        examples = {
            "招聘求职": "事业单位公开招聘工作人员公告",
            "住房居住": "住房公积金租房提取办理说明",
            "职业成长": "职业技能培训报名服务正式上线",
            "省钱优惠": "本市新一批家电以旧换新补贴申领指南",
            "出行旅行": "铁路旅客客票退票规则公布",
            "商业创业": "支持初创企业发展的服务措施",
            "生活实用": "居民身份证异地办理便民服务更新",
        }
        for expected, title in examples.items():
            with self.subTest(category=expected):
                gap = worker.candidate({"title": title, "url": "https://www.moa.gov.cn/xw/zwdt/202610/t20261020_123.htm", "date": "2026-10-20"}, SOURCE)
                self.assertEqual(gap["category"], expected)
                self.assertEqual(gap["level"], "ordinary")
                self.assertIn("参与资格、费用、办理入口及截止时间待核验", gap["body"])
                self.assertNotIn("status", gap)

    def test_practical_routing_preserves_unmatched_and_nonofficial_sources(self):
        self.assertEqual(worker.official_category("全国农作物新品种研究进展", SOURCE), "农业与食品")
        for source in (dict(SOURCE, evidence="unverified"), dict(SOURCE, url="https://www.nature.com/nature.rss", category="前沿科技")):
            self.assertEqual(worker.official_category("公开招聘研究人员", source), source["category"])
        # An employment announcement stays in jobs even if it also mentions a perk.
        self.assertEqual(worker.official_category("校园招聘及租房补贴说明", SOURCE), "招聘求职")

    def test_actual_html_config_matches_verified_article_urls(self):
        # Load production JSON rather than duplicating its regex as a Python raw
        # string: JSON escaping is part of the configuration contract.
        config = json.loads((Path(__file__).resolve().parents[1] / "data" / "sources.json").read_text(encoding="utf-8"))
        sources = {source["id"]: source for source in config if source["kind"] == "html"}
        examples = {
            "cn-moa-news": ["https://www.moa.gov.cn/xw/zwdt/202610/t20261015_6488642.htm"],
            "cn-gov-policy": ["https://www.gov.cn/zhengce/202610/content_7082820.htm", "https://www.gov.cn/zhengce/content/202610/content_7082737.htm"],
            "cn-mca-announcements": ["https://www.mca.gov.cn/n152/n165/c1662004999980013632/content.html"],
            "cn-mot-news": ["https://www.mot.gov.cn/xinwen/jiaotongyaowen/202610/t20261021_4226351.html"],
            "cn-ndrc-news": ["https://www.ndrc.gov.cn/xwdt/xwfb/202610/t20261020_1408278.html"],
            "cn-mee-news": ["https://www.mee.gov.cn/ywdt/hjywnews/202610/t20261020_1183416.shtml"],
            "cn-cnsa-news": ["https://www.cnsa.gov.cn/n6758823/n6758838/c10780180/content.html"],
        }
        self.assertEqual(set(sources), set(examples))
        for source_id, urls in examples.items():
            source = sources[source_id]
            pattern = re.compile(source["article_pattern"])
            for url in urls:
                with self.subTest(source=source_id, url=url):
                    self.assertIsNotNone(pattern.fullmatch(urlsplit(url).path))
                    self.assertIsNone(pattern.fullmatch(urlsplit(source["url"]).path))
                    self.assertIsNone(pattern.fullmatch(urlsplit(url).path + ".invalid"))
                    title = source["name"] + "公开资料与政策更新"
                    items = self.parse('<li><a href="' + url + '">' + title + '</a></li>', source)
                    self.assertEqual(len(items), 1)
                    self.assertEqual(items[0]["url"], url)
                    self.assertEqual(items[0]["title"], title)

    def test_only_expected_official_articles_and_unique_urls(self):
        rows = self.parse('''<!doctype html><nav><a href="/">首页导航不是文章</a></nav>
        <script>var fake = '<a href="/xw/zwdt/202610/t20261020_99.htm">脚本内的链接不采集</a>';</script>
        <li><a href="./202610/t20261020_123.htm "><strong>全国农产品质量安全监测结果发布</strong></a><span>2026-10-21</span></li>
        <a href="./202610/t20261020_123.htm">全国农产品质量安全监测结果发布</a>
        <a href="https://evil.example/xw/zwdt/202610/t20261020_555.htm">外部假冒链接不能被采集</a>''')
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["date"], "2026-10-21")
        self.assertEqual(rows[0]["title"], "全国农产品质量安全监测结果发布")
        self.assertTrue(rows[0]["url"].endswith("/202610/t20261020_123.htm"))

    def test_url_date_and_unknown_date_are_distinct(self):
        item = self.parse('<a href="./202610/t20261020_123.htm">农业农村部公开发布政策文件</a>')[0]
        self.assertEqual(item["date"], "2026-10-20")
        source = dict(SOURCE, article_pattern=r"^/xw/zwdt/article/\d+\.html$")
        item = self.parse('<li><a href="./article/1.html">农业农村部公开发布政策文件</a></li>', source)[0]
        self.assertEqual(item["date"], "")

    def test_no_matching_articles_is_failure_not_success(self):
        for page in ('<title>请完成验证</title>', '<a href="/about.html">关于本网站的信息</a>'):
            with self.assertRaises(ValueError):
                self.parse(page)

    def test_policy_template_does_not_invent_effective_date(self):
        item = self.parse('<li><a href="./202610/t20261020_123.htm">农业农村部公开发布政策文件</a></li>')[0]
        gap = worker.candidate(item, SOURCE)
        self.assertIn("生效日期：待核验", gap["body"])
        self.assertIn("尚未读取和核实文章正文", gap["body"])
        self.assertEqual(gap["evidence"], "official")
        self.assertNotIn("status", gap)

    def test_title_effective_date_does_not_replace_publication(self):
        item = self.parse('<li><time>2026-10-21</time><a href="./202610/t20261020_123.htm">新规自2026年11月1日起施行</a></li>')[0]
        self.assertEqual(item["date"], "2026-10-21")
        item = self.parse('<li><time>2026-10-21</time><a href="./202610/t20261020_123.htm">农业农村部政策文件公开</a><span>生效2026-11-01</span></li>')[0]
        self.assertEqual(item["date"], "2026-10-20")

    def test_bad_dates_remain_unknown(self):
        self.assertEqual(worker.list_date("2026-02-30"), "")
        self.assertEqual(worker.list_date("2026年工作安排"), "")

    def test_card_uses_title_attribute_and_explicit_date_node(self):
        source = dict(SOURCE, date_class="article-date")
        item = self.parse('<li><a href="./202610/t20261020_123.htm" title="官方新型运载火箭发射成功"><div>标题重复<span class="article-date">2026-10-21</span></div><p>正文摘要含2026年11月1日的其他日期</p></a></li>', source)[0]
        self.assertEqual(item["title"], "官方新型运载火箭发射成功")
        self.assertEqual(item["date"], "2026-10-21")

    def test_mca_table_row_uses_sibling_date_cell_without_leaking_between_rows(self):
        config = json.loads((Path(__file__).resolve().parents[1] / "data" / "sources.json").read_text(encoding="utf-8"))
        source = next(value for value in config if value["id"] == "cn-mca-announcements")
        # Structure observed on the official index: the date is in a sibling td,
        # outside the anchor. Separator rows must not transfer a previous date.
        items = self.parse('''<ul class="alist_ul"><span id="comp_1284">
        <table class="article"><tbody><tr>
        <td class="ardot"><img src="../../template/26/1202.gif"></td>
        <td class="arlisttd" style="width:89%"><a href="
          ../../n152/n165/c1662004999980013632/content.html
        " title="民政部、人力资源社会保障部、农业农村部联合印发《关于加强农村助老员队伍建设的通知》">通知标题</a></td>
        <td class="timedefault">2026-10-13</td></tr>
        <tr><td colspan="10" class="listline"></td></tr>
        <tr><td class="arlisttd"><a href="../../n152/n165/c1662004999980014017/content.html"
        title="民政部2026年第三季度政府网站和政务新媒体检查情况">检查情况</a></td>
        <td class="timedefault">2026-10-08</td></tr>
        <tr><td class="arlisttd"><a href="../../n152/n165/c1662004999980013252/content.html"
        title="关于防范假冒民政部工作人员身份进行诈骗的风险提示">风险提示</a></td>
        <td class="timedefault"></td></tr></tbody></table></span></ul>''', source)
        self.assertEqual(len(items), 3)
        self.assertEqual([item["date"] for item in items], ["2026-10-13", "2026-10-08", ""])
        self.assertTrue(items[0]["url"].endswith("/c1662004999980013632/content.html"))


class FreshPublicationTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.path = self.root / "fixture.sqlite"
        root = Path(__file__).resolve().parents[1]
        sources = json.loads((root / "data" / "sources.json").read_text(encoding="utf-8"))
        self.who = next(source for source in sources if source["id"] == "who-news")
        self.assertEqual(self.who["date_from_url"], "dd-mm-yyyy")
        self.source_file = self.root / "sources.json"
        self.source_file.write_text(json.dumps([self.who]), encoding="utf-8")
        schema = re.search(r"const schema = `(.*?)`", (root / "schema.go").read_text(encoding="utf-8"), re.S).group(1)
        with contextlib.closing(sqlite3.connect(str(self.path))) as db:
            db.executescript(schema)
        self.current = dt.datetime(2026, 10, 21, 12, 0, 0)
        self.clock = mock.patch.object(worker, "utcnow", return_value=self.current)
        self.clock.start()

    def tearDown(self):
        self.clock.stop()
        self.temp.cleanup()

    def rss_item(self, day, title=None, pub_date="Wed, 21 Oct 2026 11:00:00 GMT", description="短摘要"):
        # WHO's RSS pubDate can be a later update; the URL retains original date.
        path = day.strftime("%d-%m-%Y") if day else "undated"
        return {"title": title or "Official health publication " + path,
                "url": "https://www.who.int/news/item/" + path + "-health-update",
                "date": pub_date, "description": description}

    def feed(self, items):
        return ("<rss version='2.0'><channel><title>WHO news</title>" + "".join(
            "<item><title>" + escape(item["title"]) + "</title><link>" + escape(item["url"]) +
            "</link><pubDate>" + escape(item.get("date", "")) + "</pubDate><description>" +
            escape(item.get("description", "")) + "</description></item>" for item in items) +
            "</channel></rss>").encode("utf-8")

    def execute(self, items=None, source=None, seed=None):
        if source is not None:
            self.source_file.write_text(json.dumps([source]), encoding="utf-8")
        args = argparse.Namespace(db=str(self.path), sources=str(self.source_file), check=False,
                                  import_seed=str(seed) if seed else None, publish_seed=bool(seed),
                                  source=None, force=True, max_items=10)
        output = io.StringIO()
        with mock.patch.object(worker, "fetch", return_value=self.feed(items or [])) as fetch, mock.patch.object(worker, "robot_rules", return_value=0), contextlib.redirect_stdout(output):
            code = worker.run(args)
        return code, [json.loads(line) for line in output.getvalue().splitlines()], fetch

    def rows(self, query):
        with contextlib.closing(sqlite3.connect(str(self.path))) as db:
            return db.execute(query).fetchall()

    def test_common_insert_taxonomy_matches_go_api(self):
        root = Path(__file__).resolve().parents[1]
        main = (root / "main.go").read_text(encoding="utf-8")
        content = (root / "content.go").read_text(encoding="utf-8")
        values = re.search(r'var categories = \[\]string\{([^}]+)\}', main).group(1)
        self.assertEqual(worker.CATEGORIES, set(json.loads("[" + values + "]")))
        self.assertEqual(worker.MAX_TAGS, int(re.search(r'const maxTags = (\d+)', content).group(1)))

    def test_common_insert_preserves_ten_tags_and_unicode_length(self):
        item = worker.candidate(self.rss_item(dt.datetime(2026, 10, 20)), self.who)
        # Go counts Unicode code points and preserves whitespace/duplicates.
        tags = ["国家:日本", "地区:东亚", "旅行:优惠渠道", "😀" * 24,
                " 原样 ", "重复", "重复", "", "甲", "乙"]
        item.update(category="出行旅行", tags=tags, review_note="已核验的测试种子。")
        seed = self.root / "ten-tags.json"
        seed.write_text(json.dumps([item], ensure_ascii=False), encoding="utf-8")
        code, report, fetch = self.execute(seed=seed)
        self.assertEqual(code, 0)
        fetch.assert_not_called()
        self.assertEqual(report[0]["inserted"], 1)
        category, stored, status = self.rows("SELECT category,tags,status FROM gaps")[0]
        self.assertEqual((category, json.loads(stored), status), ("出行旅行", tags, "published"))
        self.assertEqual(item["tags"], tags)

    def test_common_insert_rejects_invalid_tag_shape_count_and_length(self):
        item = worker.candidate(self.rss_item(dt.datetime(2026, 10, 20)), self.who)
        invalid = [None, "标签", {"标签": True}, ("标签",), [1], [False], [None],
                   [["标签"]], ["字" * 25], ["😀" * 25],
                   ["标签%d" % i for i in range(11)], ["重复"] * 11]
        with contextlib.closing(sqlite3.connect(str(self.path))) as db:
            for tags in invalid:
                with self.subTest(tags=tags), self.assertRaises(ValueError):
                    worker.insert_gap(db, dict(item, tags=tags))
                self.assertEqual(db.execute("SELECT COUNT(*) FROM gaps").fetchone()[0], 0)

    def test_common_insert_rejects_multiple_or_unknown_categories(self):
        item = worker.candidate(self.rss_item(dt.datetime(2026, 10, 20)), self.who)
        invalid = [None, "", "未知主题", " 物理 ", "物理,化学", "物理、化学",
                   ["物理"], ["物理", "化学"], {"category": "物理"}]
        with contextlib.closing(sqlite3.connect(str(self.path))) as db:
            for category in invalid:
                with self.subTest(category=category), self.assertRaises(ValueError):
                    worker.insert_gap(db, dict(item, category=category))
                self.assertEqual(db.execute("SELECT COUNT(*) FROM gaps").fetchone()[0], 0)

    def test_common_insert_validates_before_duplicate_url_shortcut(self):
        item = worker.candidate(self.rss_item(dt.datetime(2026, 10, 20)), self.who)
        with contextlib.closing(sqlite3.connect(str(self.path))) as db:
            with db:
                self.assertEqual(worker.insert_gap(db, item), 1)
            self.assertEqual(worker.insert_gap(db, item), 0)
            for changed in (dict(item, tags=["重复"] * 11), dict(item, category="物理,化学")):
                with self.assertRaises(ValueError):
                    worker.insert_gap(db, changed)
            stored = db.execute("SELECT category,tags FROM gaps").fetchall()
        self.assertEqual(stored, [(item["category"], json.dumps(item["tags"], ensure_ascii=False))])

    def test_seed_batch_rolls_back_when_later_taxonomy_is_invalid(self):
        existing = worker.candidate(self.rss_item(dt.datetime(2026, 10, 18)), self.who)
        with contextlib.closing(sqlite3.connect(str(self.path))) as db:
            with db:
                worker.insert_gap(db, existing)
        first = worker.candidate(self.rss_item(dt.datetime(2026, 10, 19)), self.who)
        second = worker.candidate(self.rss_item(dt.datetime(2026, 10, 20)), self.who)
        seed = self.root / "invalid-batch.json"
        for invalid in (dict(second, tags=["标签%d" % i for i in range(11)]),
                        dict(second, category="物理,化学"), dict(second, tags=[7])):
            seed.write_text(json.dumps([first, invalid], ensure_ascii=False), encoding="utf-8")
            with self.assertRaises(ValueError):
                self.execute(seed=seed)
            self.assertEqual(self.rows("SELECT source_url,status FROM gaps"), [(existing["source_url"], "pending")])

    def test_who_original_url_date_overrides_recent_feed_modified_date(self):
        item = self.rss_item(dt.datetime(2020, 4, 6))
        parsed = worker.parse_items(self.feed([item]), self.who)[0]
        self.assertEqual(worker.published_date(parsed, self.who), "2020-04-06T00:00:00Z")
        self.assertEqual(worker.candidate(parsed, self.who)["published_at"], "2020-04-06T00:00:00Z")
        eligible, reasons, age = worker.recent_items([parsed], self.who)
        self.assertEqual(eligible, [])
        self.assertEqual(reasons["old"], 1)
        self.assertEqual(age, 180)

    def test_who_full_feed_is_sorted_before_ten_item_limit(self):
        old = [self.rss_item(dt.datetime(2020, 4, day)) for day in range(1, 11)]
        recent = [self.rss_item(dt.datetime(2026, 10, day)) for day in range(1, 16)]
        code, report, unused = self.execute(old + recent)
        self.assertEqual(code, 0)
        rows = self.rows("SELECT published_at,status FROM gaps ORDER BY id")
        self.assertEqual([row[0] for row in rows], ["2026-10-%02dT00:00:00Z" % day for day in range(15, 5, -1)])
        self.assertTrue(all(row[1] == "pending" for row in rows))
        self.assertEqual(report[0]["received"], 25)
        self.assertEqual(report[0]["filtered"], 10)
        self.assertEqual(report[0]["filtered_reasons"]["old"], 10)
        self.assertEqual(report[0]["limited"], 5)
        self.assertEqual(report[0]["inserted"], 10)

    def test_old_and_unknown_long_articles_never_enter_database_or_summary_queue(self):
        long_text = "这份原始资料提供了研究条件和需要核验的具体内容。" * 130
        items = [self.rss_item(dt.datetime(2020, 4, 6), description=long_text),
                 self.rss_item(dt.datetime(2025, 10, 1), description=long_text),
                 self.rss_item(None, description=long_text),
                 self.rss_item(dt.datetime(2026, 10, 20), description=long_text)]
        code, report, unused = self.execute(items)
        self.assertEqual(code, 0)
        self.assertEqual(self.rows("SELECT COUNT(*) FROM gaps")[0][0], 1)
        self.assertEqual(self.rows("SELECT COUNT(*) FROM summary_jobs")[0][0], 1)
        self.assertEqual(self.rows("SELECT published_at FROM gaps")[0][0], "2026-10-20T00:00:00Z")
        self.assertEqual(report[0]["filtered_reasons"]["old"], 2)
        self.assertEqual(report[0]["filtered_reasons"]["undated"], 1)
        self.assertEqual(report[0]["unknown_date_policy"], "skip")

    def test_unknown_dates_are_transparently_filtered_including_who_invalid_path_date(self):
        invalid = self.rss_item(None)
        invalid["url"] = "https://www.who.int/news/item/31-02-2026-invalid-calendar-date"
        code, report, unused = self.execute([self.rss_item(None), invalid])
        self.assertEqual(code, 0)
        self.assertEqual(report[0]["filtered"], 2)
        self.assertEqual(report[0]["filtered_reasons"]["undated"], 2)
        self.assertEqual(report[0]["checked"], 0)
        self.assertEqual(self.rows("SELECT COUNT(*) FROM summary_jobs")[0][0], 0)

    def test_atom_updated_is_not_used_as_publication(self):
        atom = b"""<feed xmlns='http://www.w3.org/2005/Atom'><entry><title>Updated historical article</title>
        <link href='https://www.nature.com/articles/old-paper'/><updated>2026-10-21T10:00:00Z</updated>
        </entry><entry><title>Newly published article</title><link href='https://www.nature.com/articles/new-paper'/>
        <published>2026-10-20T10:00:00Z</published><updated>2026-10-21T11:00:00Z</updated></entry></feed>"""
        source = dict(self.who, date_from_url="", name="Nature")
        rows = worker.parse_items(atom, source)
        self.assertEqual(rows[0]["date"], "")
        self.assertEqual(rows[1]["date"], "2026-10-20T10:00:00Z")
        eligible, reasons, unused = worker.recent_items(rows, source)
        self.assertEqual(len(eligible), 1)
        self.assertEqual(reasons["undated"], 1)

    def test_actual_published_dates_are_sorted_after_timezone_normalization(self):
        source = dict(self.who, date_from_url="")
        items = [{"date": "2026-10-21T10:00:00+08:00", "title": "later-looking local time"},
                 {"date": "2026-10-21T03:00:00Z", "title": "actually newer"}]
        rows, reasons, unused = worker.recent_items(items, source)
        self.assertEqual([row["title"] for row in rows], ["actually newer", "later-looking local time"])
        self.assertEqual(sum(reasons.values()), 0)

    def test_age_boundary_and_configured_maximum_are_respected(self):
        source = dict(self.who, date_from_url="")
        boundary = self.current - dt.timedelta(days=180)
        items = [{"date": boundary.strftime("%Y-%m-%dT%H:%M:%SZ")},
                 {"date": (boundary - dt.timedelta(seconds=1)).strftime("%Y-%m-%dT%H:%M:%SZ")}]
        rows, reasons, age = worker.recent_items(items, source)
        self.assertEqual(len(rows), 1)
        self.assertEqual(reasons["old"], 1)
        self.assertEqual(age, 180)
        rows, reasons, age = worker.recent_items(items, dict(source, max_age_days=365))
        self.assertEqual(len(rows), 2)
        self.assertEqual(age, 365)
        for invalid in (0, -1, True, "180"):
            with self.assertRaises(ValueError):
                worker.recent_items(items, dict(source, max_age_days=invalid))

    def test_future_dates_are_filtered_before_database_and_model(self):
        item = self.rss_item(dt.datetime(2026, 11, 1), description="长原文" * 900)
        code, report, unused = self.execute([item])
        self.assertEqual(code, 0)
        self.assertEqual(report[0]["filtered_reasons"]["future"], 1)
        self.assertEqual(self.rows("SELECT COUNT(*) FROM gaps")[0][0], 0)
        self.assertEqual(self.rows("SELECT COUNT(*) FROM summary_jobs")[0][0], 0)

    def test_curated_historical_seed_import_bypasses_automatic_age_filter(self):
        item = worker.candidate(self.rss_item(dt.datetime(2020, 4, 6)), self.who)
        item["review_note"] = "历史资料，原始发布日期与出处已人工核验。"
        seed = self.root / "seed.json"
        seed.write_text(json.dumps([item]), encoding="utf-8")
        code, report, fetch = self.execute(seed=seed)
        self.assertEqual(code, 0)
        fetch.assert_not_called()
        self.assertEqual(self.rows("SELECT published_at,released_at,status FROM gaps"), [("2020-04-06T00:00:00Z", "2020-04-06T00:00:00Z", "published")])
        self.assertEqual(self.rows("SELECT COUNT(*) FROM summary_jobs")[0][0], 0)

    def test_verified_evergreen_seed_keeps_unknown_source_date_and_starts_delay_now(self):
        item = worker.candidate(self.rss_item(None), self.who)
        item.update(review_note="官方常设指南，条件已逐项核验。", verified_at="2026-10-21T11:00:00Z")
        seed = self.root / "seed.json"
        seed.write_text(json.dumps([item]), encoding="utf-8")
        code, report, fetch = self.execute(seed=seed)
        self.assertEqual(code, 0)
        fetch.assert_not_called()
        self.assertEqual(self.rows("SELECT published_at,released_at,status FROM gaps"), [("", "2026-10-21T12:00:00Z", "published")])
        self.assertIn("原站未标注发布日期", self.rows("SELECT review_note FROM gaps")[0][0])
        self.assertEqual(self.rows("SELECT COUNT(*) FROM summary_jobs")[0][0], 0)

    def test_undated_seed_requires_explicit_nonfuture_verification(self):
        item = worker.candidate(self.rss_item(None), self.who)
        item["review_note"] = "只有说明不足以证明核验时间。"
        seed = self.root / "seed.json"
        for verified in ("", "invalid", "2026-10-21T12:00:01Z", "2026-11-01T00:00:00Z"):
            item["verified_at"] = verified
            seed.write_text(json.dumps([item]), encoding="utf-8")
            with self.assertRaises(ValueError):
                self.execute(seed=seed)
        self.assertEqual(self.rows("SELECT COUNT(*) FROM gaps")[0][0], 0)


if __name__ == "__main__":
    unittest.main()
