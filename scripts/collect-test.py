#!/usr/bin/env python3
"""Official index parser fixtures: no network or production database."""
import importlib.util
import json
from pathlib import Path
import re
import unittest
from urllib.parse import urlsplit

spec = importlib.util.spec_from_file_location("collect", str(Path(__file__).with_name("collect.py")))
worker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)

SOURCE = {"id": "fixture", "kind": "html", "url": "https://www.moa.gov.cn/xw/zwdt/", "name": "农业农村部", "category": "农业与食品", "evidence": "official", "article_pattern": r"^/xw/zwdt/\d{6}/t\d{8}_\d+\.htm$", "jurisdiction": "中国"}


class OfficialListTests(unittest.TestCase):
    def parse(self, html, source=None):
        return worker.parse_items(html.encode("utf-8"), source or SOURCE)

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


if __name__ == "__main__":
    unittest.main()
