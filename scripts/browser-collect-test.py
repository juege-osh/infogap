#!/usr/bin/env python3
"""Local fixtures only: no Docker, network, live account or production database."""
import argparse
import contextlib
import copy
import datetime as dt
import importlib.util
import io
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("browser_collect", str(HERE / "browser-collect.py"))
worker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)

ACCOUNT = {"id": "x-nature", "handle": "nature", "name": "Nature", "profile_url": "https://x.com/nature", "category": "前沿科技", "primary_domains": ["nature.com", "doi.org"], "enabled": True, "collection_status": "not_tested"}
FIXTURE_NOW = dt.datetime(2026, 10, 21, 12, 0, 0)


def candidate(linux=False, identifier="123456789"):
    return {"title": "研究团队报告了一项需要核验的新方法", "summary": "公开帖子介绍实验方法与适用范围，研究结论需要回到原始证据核验。", "body": "## 研究线索\n\n" + "这项工作描述了一种实验方法，需要核对样本范围、实验条件和结果解释。" * 4, "source_name": "不能信任的来源名称", "source_url": ("https://linux.do/t/topic/" if linux else "https://x.com/nature/status/") + identifier, "published_at": "2026-10-20T00:00:00Z", "source_visibility": "public", "evidence": "official", "level": "exclusive", "status": "published", "author_handle": "nature", "tags": ["研究"], "review_note": "不应信任此审核说明", "primary_evidence_urls": ["https://www.nature.com/articles/example-research"]}


def result(source, items=None, status="ok"):
    return {"checked_at": "2026-01-01T00:00:00Z", "source_id": source["id"], "status": status, "http_status": 200 if status == "ok" else 403, "error": "访问被限制" if status != "ok" else "", "candidates": items if items is not None else [candidate()], "diagnostics": {"visible_items": 3, "filtered_count": 0, "mode": "headed-chromium-xvfb"}}


class BrowserCollectorTests(unittest.TestCase):
    def setUp(self):
        clock = mock.patch.object(worker.collect, "utcnow", return_value=FIXTURE_NOW)
        clock.start()
        self.addCleanup(clock.stop)
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.path = self.root / "fixture.sqlite"
        self.accounts = self.root / "accounts.json"
        self.accounts.write_text(json.dumps({"accounts": [ACCOUNT]}), encoding="utf-8")
        self.sources = worker.read_sources(self.accounts)
        self.linux, self.x = self.sources
        db = sqlite3.connect(str(self.path))
        db.executescript("""
        CREATE TABLE gaps(id INTEGER PRIMARY KEY,title TEXT,summary TEXT,body TEXT,category TEXT,level TEXT,evidence TEXT,source_name TEXT,source_url TEXT UNIQUE,source_visibility TEXT DEFAULT 'public',published_at TEXT,released_at TEXT DEFAULT '',created_at TEXT,updated_at TEXT,status TEXT,tags TEXT,author_id INTEGER,review_note TEXT,rewarded INTEGER);
        CREATE TABLE sources(id TEXT PRIMARY KEY,name TEXT,url TEXT,kind TEXT,category TEXT,enabled INTEGER,last_attempt TEXT,last_success TEXT,last_error TEXT DEFAULT '',last_count INTEGER DEFAULT 0);
        """)
        worker.collect.sync_sources(db, self.sources)
        db.close()

    def tearDown(self):
        self.temp.cleanup()

    @contextlib.contextmanager
    def database(self):
        connection = sqlite3.connect(str(self.path))
        try:
            with connection:
                yield connection
        finally:
            connection.close()

    def count(self):
        with self.database() as db:
            return db.execute("SELECT COUNT(*) FROM gaps").fetchone()[0]

    def process(self, payload, source=None):
        return worker.process_source(self.path, source or self.x, lambda unused: copy.deepcopy(payload))

    def test_real_account_configuration_loads(self):
        sources = worker.read_sources(worker.ROOT / "data" / "x-accounts.json")
        self.assertEqual(len(sources), 12)
        self.assertFalse(next(s for s in sources if s["id"] == "x-sciencemagazine")["enabled"])

    def test_all_twenty_one_categories_accept_config_and_candidate(self):
        self.assertEqual(len(worker.CATEGORIES), 21)
        for category in worker.CATEGORIES:
            account = dict(ACCOUNT)
            account["category"] = category
            self.accounts.write_text(json.dumps({"accounts": [account]}), encoding="utf-8")
            source = worker.read_sources(self.accounts)[1]
            self.assertEqual(worker.validate_candidate(candidate(), source)["category"], category)

    def test_failure_never_imports_even_with_candidates(self):
        for status in ("blocked", "login_required", "error"):
            report = self.process(result(self.x, status=status))
            self.assertEqual(report["status"], status)
            self.assertEqual(report["candidates"], [])
            self.assertEqual(self.count(), 0)
            with self.database() as db:
                error, success = db.execute("SELECT last_error,last_success FROM sources WHERE id=?", (self.x["id"],)).fetchone()
            self.assertIn("[" + status + "]", error)
            self.assertIsNone(success)

    def test_empty_page_and_invalid_status_are_not_success(self):
        for change in ({"http_status": 403}, {"diagnostics": {"visible_items": 0}}, {"source_id": "x-impostor"}, {"status": "readable"}):
            payload = result(self.x, [])
            payload.update(change)
            self.assertEqual(self.process(payload)["status"], "error")
        self.assertEqual(self.count(), 0)

    def test_visible_page_filtered_to_zero_can_succeed(self):
        report = self.process(result(self.x, []))
        self.assertEqual(report["status"], "ok")
        self.assertEqual(self.count(), 0)

    def test_both_browser_sources_use_shared_freshness_and_sort_after_validation(self):
        dates = ["2026-10-19T12:00:00Z", "2026-01-01T00:00:00Z", "",
                 "2026-11-01T00:00:00Z", "2026-10-20T12:00:00Z"]
        for source in (self.linux, self.x):
            items = []
            for i, date in enumerate(dates):
                item = candidate(source == self.linux, str(123456789 + i))
                item["published_at"] = date
                items.append(item)
            report = worker.validate_result(result(source, items), source)
            self.assertEqual(report["status"], "ok")
            self.assertEqual([item["published_at"] for item in report["candidates"]], [dates[4], dates[0]])
            self.assertEqual(report["diagnostics"]["filtered_reasons"], {"old": 1, "undated": 1, "future": 1, "editorial": 0})
            self.assertEqual(report["diagnostics"]["validated"], 5)
            self.assertEqual(report["diagnostics"]["eligible"], 2)
            self.assertEqual(report["diagnostics"]["max_age_days"], 180)
            self.assertNotIn("date", report["candidates"][0])
            self.assertNotIn("url", report["candidates"][0])

    def test_only_stale_or_unknown_candidates_are_success_without_writes_or_model_queue(self):
        for source in (self.linux, self.x):
            items = []
            for i, date in enumerate(("2026-01-01T00:00:00Z", "", "2026-11-01T00:00:00Z", "2026-02-30")):
                item = candidate(source == self.linux, str(123456789 + i))
                item["published_at"] = date
                if source == self.linux:
                    raw = "这是一份仅供日期过滤测试的主帖原文。" * 150
                    item.update(raw_text=raw, raw_text_length=len(raw), text_truncated=False, raw_text_scope="main_post_visible_dom")
                items.append(item)
            with mock.patch.object(worker.collect, "queue_summary") as queue:
                report = self.process(result(source, items), source)
                queue.assert_not_called()
            self.assertEqual(report["status"], "ok")
            self.assertEqual(report["candidates"], [])
            self.assertEqual(report["diagnostics"]["filtered_reasons"], {"old": 1, "undated": 2, "future": 1, "editorial": 0})
            self.assertEqual(report["diagnostics"]["unknown_date_policy"], "skip")
            self.assertEqual(self.count(), 0)
            with self.database() as db:
                state = db.execute("SELECT last_success,last_error,last_count FROM sources WHERE id=?", (source["id"],)).fetchone()
            self.assertEqual(state, (worker.collect.now(), "", 0))

    def test_browser_date_window_boundary_and_override(self):
        for days in (180, 30):
            source = dict(self.x, max_age_days=days)
            boundary = FIXTURE_NOW - dt.timedelta(days=days)
            items = []
            for i, date in enumerate((boundary, boundary - dt.timedelta(seconds=1))):
                item = candidate(identifier=str(123456789 + i))
                item["published_at"] = date.strftime("%Y-%m-%dT%H:%M:%SZ")
                items.append(item)
            report = worker.validate_result(result(source, items), source)
            self.assertEqual(len(report["candidates"]), 1)
            self.assertEqual(report["diagnostics"]["filtered_reasons"]["old"], 1)
            self.assertEqual(report["diagnostics"]["max_age_days"], days)

    def test_three_verified_linux_dates_remain_eligible_and_internal(self):
        # Dates observed in the earlier real browser run; clock is fixed so this
        # regression fixture does not become stale months after deployment.
        dates = ["2026-10-21T08:08:22.056Z", "2026-10-21T06:47:46.454Z", "2026-10-21T03:00:18.528Z"]
        items = []
        for i, date in enumerate(dates):
            item = candidate(True, str(123456789 + i))
            item["published_at"] = date
            item["body"] += "\n独立日期样本%d" % i
            items.append(item)
        report = self.process(result(self.linux, items), self.linux)
        self.assertEqual(report["status"], "ok")
        self.assertEqual(report["diagnostics"]["filtered"], 0)
        self.assertEqual(self.count(), 3)
        with self.database() as db:
            rows = db.execute("SELECT source_visibility,status,evidence,published_at FROM gaps ORDER BY id").fetchall()
        self.assertTrue(all(row[:3] == ("internal", "pending", "unverified") for row in rows))
        self.assertEqual([row[3] for row in rows], [worker.collect.stamp(date) for date in dates])

    def test_forged_author_or_status_url_is_rejected(self):
        for field, value in (("author_handle", "impostor"), ("author_handle", None), ("source_url", "https://x.com/impostor/status/123"), ("source_url", "https://x.com.evil.org/nature/status/123"), ("source_url", "https://x.com/nature/status/123/photo/1")):
            item = candidate()
            item[field] = value
            self.assertEqual(self.process(result(self.x, [item]))["status"], "error")
        self.assertEqual(self.count(), 0)

    def test_x_needs_direct_public_primary_evidence(self):
        for links in ([], ["https://t.co/abc"], ["https://x.com/nature/status/1"], ["https://nature.com.evil.org/article"], ["http://nature.com/article"], ["https://www.nature.com/"], ["https://www.nature.com/login"], ["https://www.nature.com/articles/a?access_token=secret"]):
            item = candidate()
            item["primary_evidence_urls"] = links
            self.assertEqual(self.process(result(self.x, [item]))["status"], "error")
        self.assertEqual(self.count(), 0)

    def test_linux_source_is_internal_and_pending(self):
        report = self.process(result(self.linux, [candidate(True)]), self.linux)
        self.assertEqual(report["status"], "ok")
        with self.database() as db:
            values = db.execute("SELECT source_visibility,status,level,evidence,source_name,review_note FROM gaps").fetchone()
        self.assertEqual(values[:4], ("internal", "pending", "ordinary", "unverified"))
        self.assertEqual(values[4], self.linux["name"])
        self.assertNotIn("不应信任", values[5])

    def test_x_is_public_pending_and_evidence_is_retained(self):
        item = candidate()
        item["source_visibility"] = "internal"
        self.process(result(self.x, [item]))
        with self.database() as db:
            visibility, status, level, evidence, body = db.execute("SELECT source_visibility,status,level,evidence,body FROM gaps").fetchone()
        self.assertEqual((visibility, status, level, evidence), ("public", "pending", "ordinary", "unverified"))
        self.assertIn("https://www.nature.com/articles/example-research", body)

    def test_url_canonicalization_and_content_are_idempotent(self):
        first = candidate()
        self.process(result(self.x, [first]))
        second = candidate(identifier="987654321")
        self.process(result(self.x, [second]))
        first["source_url"] += "?utm_source=share#fragment"
        self.process(result(self.x, [first]))
        self.assertEqual(self.count(), 1)

    def test_linux_paths_normalize_to_topic_identity(self):
        first = candidate(True)
        first["source_url"] = "https://linux.do/t/technical-slug/123456789/19"
        self.process(result(self.linux, [first]), self.linux)
        with self.database() as db:
            self.assertEqual(db.execute("SELECT source_url FROM gaps").fetchone()[0], "https://linux.do/t/topic/123456789")
        for link in ("http://linux.do/t/topic/1", "https://linux.do.evil.org/t/topic/1", "https://linux.do/admin", "https://linux.do/t/topic/../1", "https://user:pass@linux.do/t/topic/1"):
            item = candidate(True)
            item["source_url"] = link
            self.assertEqual(self.process(result(self.linux, [item]), self.linux)["status"], "error")

    def test_marketing_and_oversized_fields_rejected(self):
        for field, value in (("title", "抽奖活动：转发帖子即可获得奖品"), ("body", "这是带货推广。" * 30), ("summary", "字" * 301), ("body", "字" * 15001), ("tags", ["字" * 25])):
            item = candidate()
            item[field] = value
            self.assertEqual(self.process(result(self.x, [item]))["status"], "error")
        self.assertEqual(self.count(), 0)

    def test_max_eight_candidates_and_batch_rollback(self):
        items = []
        for i in range(12):
            item = candidate(identifier=str(123456789 + i))
            item["body"] += "\n不同进展%d" % i
            items.append(item)
        self.process(result(self.x, items))
        self.assertEqual(self.count(), 8)
        with self.database() as db:
            db.execute("DELETE FROM gaps")
        original = worker.collect.insert_gap
        calls = []
        def interrupted(db, item, **kwargs):
            calls.append(item)
            if len(calls) == 2:
                raise sqlite3.OperationalError("simulated insertion failure")
            return original(db, item, **kwargs)
        with mock.patch.object(worker.collect, "insert_gap", side_effect=interrupted):
            self.assertEqual(self.process(result(self.x, items[:2]))["status"], "error")
        self.assertEqual(self.count(), 0)

    def test_force_respects_disabled_and_intervals(self):
        arguments = argparse.Namespace(db=str(self.path), accounts=str(self.accounts), source=[self.x["id"]], force=True)
        calls = []
        def invoke(source):
            calls.append(source["id"])
            return result(source)
        with self.database() as db:
            db.execute("UPDATE sources SET enabled=0 WHERE id=?", (self.x["id"],))
        with contextlib.redirect_stdout(io.StringIO()):
            worker.run(arguments, invoke)
        self.assertEqual(calls, [])
        with self.database() as db:
            db.execute("UPDATE sources SET enabled=1 WHERE id=?", (self.x["id"],))
        with contextlib.redirect_stdout(io.StringIO()):
            worker.run(arguments, invoke)
            arguments.force = False
            worker.run(arguments, invoke)
        self.assertEqual(calls, [self.x["id"]])

    def test_midflight_pause_prevents_insert(self):
        def invoke(source):
            with self.database() as db:
                db.execute("UPDATE sources SET enabled=0 WHERE id=?", (source["id"],))
            return result(source)
        report = worker.process_source(self.path, self.x, invoke)
        self.assertEqual(report["status"], "error")
        self.assertEqual(self.count(), 0)

    def test_process_has_fixed_argv_no_inherited_secrets(self):
        def popen(argv, **kwargs):
            self.assertEqual(argv, [str(worker.ROOT / "deploy" / "browser-run.sh"), "--source", "x-nature"])
            self.assertNotIn("shell", kwargs)
            self.assertEqual(set(kwargs["env"]), {"PATH", "LANG", "HOME"})
            self.assertTrue(kwargs["start_new_session"])
            kwargs["stdout"].write(json.dumps(result(self.x)).encode("utf-8"))
            process = mock.Mock()
            process.wait.return_value = 0
            return process
        with mock.patch.object(worker.subprocess, "Popen", side_effect=popen):
            self.assertEqual(worker.invoke_browser(self.x)["status"], "ok")

    def test_timeout_fails_without_insert(self):
        def timeout(unused):
            raise subprocess.TimeoutExpired("fixed browser runner", 90)
        report = worker.process_source(self.path, self.x, timeout)
        self.assertEqual(report["status"], "error")
        self.assertEqual(self.count(), 0)

    def test_failed_run_preserves_last_success(self):
        with self.database() as db:
            db.execute("UPDATE sources SET last_success='2026-01-02T00:00:00Z' WHERE id=?", (self.x["id"],))
        self.process(result(self.x, status="blocked"))
        with self.database() as db:
            self.assertEqual(db.execute("SELECT last_success FROM sources WHERE id=?", (self.x["id"],)).fetchone()[0], "2026-01-02T00:00:00Z")

    def test_configuration_disabled_account_is_never_invoked(self):
        account = dict(ACCOUNT)
        account["enabled"] = False
        self.accounts.write_text(json.dumps({"accounts": [account]}), encoding="utf-8")
        arguments = argparse.Namespace(db=str(self.path), accounts=str(self.accounts), source=[self.x["id"]], force=True)
        invoke = mock.Mock()
        with contextlib.redirect_stdout(io.StringIO()):
            worker.run(arguments, invoke)
        invoke.assert_not_called()

    def test_child_timeout_terminates_process_group(self):
        process = mock.Mock(pid=12345)
        process.wait.side_effect = [subprocess.TimeoutExpired("browser", 90), 130]
        with mock.patch.object(worker.subprocess, "Popen", return_value=process), mock.patch.object(worker.os, "killpg") as kill:
            with self.assertRaisesRegex(RuntimeError, "90秒"):
                worker.invoke_browser(self.x)
            kill.assert_called_once_with(12345, worker.signal.SIGTERM)
            self.assertEqual(process.wait.call_args_list[0], mock.call(timeout=90))

    def test_owner_identity_restored_after_database_close(self):
        events = []
        owner = mock.Mock(st_uid=65534, st_gid=65534)
        connection = mock.Mock()
        connection.execute.side_effect = [[], [], [(0, "source_visibility"), (1, "released_at"), (2, "review_note")]]
        connection.close.side_effect = lambda: events.append("closed")
        with mock.patch.object(worker.os, "stat", return_value=owner), mock.patch.object(worker.os, "geteuid", return_value=0), mock.patch.object(worker.os, "getegid", return_value=0), mock.patch.object(worker.os, "getgroups", return_value=[0]), mock.patch.object(worker.os, "setgroups", side_effect=lambda value: events.append(("groups", value))), mock.patch.object(worker.os, "setegid", side_effect=lambda value: events.append(("gid", value))), mock.patch.object(worker.os, "seteuid", side_effect=lambda value: events.append(("uid", value))), mock.patch.object(worker.sqlite3, "connect", return_value=connection):
            with worker.database(self.path):
                events.append("database operation")
        self.assertLess(events.index(("uid", 65534)), events.index("database operation"))
        self.assertLess(events.index("closed"), events.index(("uid", 0)))
        self.assertEqual(events[-1], ("groups", [0]))


if __name__ == "__main__":
    unittest.main(verbosity=2)
