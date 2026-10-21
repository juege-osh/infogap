#!/usr/bin/env python3
"""Temporary SQLite fixtures only. No model, network, live account or production DB."""
import argparse
import ast
import contextlib
import datetime as dt
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import re
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

sys.dont_write_bytecode = True
HERE = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location("summary_worker", str(HERE / "summary-worker.py"))
worker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)
collect, bridge = worker.collect, worker.bridge
NOW = dt.datetime(2026, 10, 21, 12, 0, 0, tzinfo=dt.timezone.utc)
LONG_TEXT = "这份公开研究材料描述了方法、结果、样本限制和需要继续核验的问题。" * 90
SUMMARY = "研究团队公布了一种新的实验方法，说明了结果与适用范围。实际应用还需核对样本条件，并关注后续验证。"
BODY = "## 核心进展\n\n" + "原始材料描述了实验方法和对照结果，当前证据只适用于文中列出的样本与条件。" * 4


class SummaryWorkerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.path = Path(self.temp.name) / "fixture.sqlite"
        schema = re.search(r"const schema = `(.*?)`", (HERE.parent / "schema.go").read_text(encoding="utf-8"), re.S).group(1)
        with contextlib.closing(sqlite3.connect(str(self.path))) as db:
            db.executescript(schema)
        self.clock = mock.patch.object(collect, "utcnow", return_value=NOW)
        self.clock.start()
        self.serial = 0

    def tearDown(self):
        self.clock.stop()
        self.temp.cleanup()

    @contextlib.contextmanager
    def database(self):
        db = sqlite3.connect(str(self.path))
        try:
            with db:
                yield db
        finally:
            db.close()

    def seed(self, text=LONG_TEXT, truncated=False, status="pending", author=False, visibility="public"):
        self.serial += 1
        item = {"title": "公开研究进展与适用条件" + str(self.serial), "summary": "这是尚未精炼的采集摘要，具体结论与研究范围仍需要人工核验。", "body": "## 原始线索\n\n" + "原始候选正文需要进一步核对事实和来源。" * 6, "category": "前沿科技", "level": "ordinary", "evidence": "unverified", "source_name": "研究机构", "source_url": "https://www.nature.com/articles/fixture-" + str(self.serial), "published_at": "2026-10-20T00:00:00Z", "source_visibility": visibility, "tags": ["待核验"], "review_note": "采集候选，未发布。"}
        with self.database() as db:
            collect.insert_gap(db, item, status=status)
            gap_id = db.execute("SELECT id FROM gaps WHERE source_url=?", (item["source_url"],)).fetchone()[0]
            if author:
                db.execute("INSERT OR IGNORE INTO users(id,email,username,created_at) VALUES(1,'fixture@example.test','测试成员',?)", (collect.now(),))
                db.execute("UPDATE gaps SET author_id=1 WHERE id=?", (gap_id,))
            collect.queue_summary(db, item, text, truncated)
            row = db.execute("SELECT id FROM summary_jobs WHERE gap_id=?", (gap_id,)).fetchone()
        return item, gap_id, row[0] if row else None

    def row(self, table, identifier):
        with self.database() as db:
            db.row_factory = sqlite3.Row
            row = db.execute("SELECT * FROM " + table + " WHERE id=?", (identifier,)).fetchone()
            return dict(row) if row else None

    def successful(self, payload):
        return {"status": "ok", "model": "gpt-6.1-sol", "reasoning": "low", "content_hash": payload["content_hash"], "summary": SUMMARY, "body": BODY, "usage": {"input_tokens": 123, "output_tokens": 45}}

    def count_attempts(self):
        with self.database() as db:
            return db.execute("SELECT COUNT(*) FROM audit WHERE action='summary_attempt'").fetchone()[0]

    def run_worker(self, max_items=3, runner=None):
        output = io.StringIO()
        with mock.patch.object(worker, "invoke", side_effect=runner or self.successful), contextlib.redirect_stdout(output):
            code = worker.run(argparse.Namespace(db=str(self.path), max_items=max_items))
        return code, [json.loads(line) for line in output.getvalue().splitlines()]

    def test_english_title_queues_even_with_short_chinese_body(self):
        title = "Global emissions slow but warming continues"
        item = {"title": title, "summary": "中文摘要仍需核实。", "body": "现有材料仍需要核验。" * 30, "category": "前沿科技", "level": "ordinary", "source_url": "https://www.nature.com/articles/chinese-queue-fixture"}
        with self.database() as db:
            collect.insert_gap(db, item)
            self.assertEqual(collect.queue_summary(db, item, title + "\n" + item["body"]), 1)
            job = db.execute("SELECT id FROM summary_jobs ORDER BY id DESC LIMIT 1").fetchone()[0]
        def runner(payload):
            value = self.successful(payload)
            value["title_zh"] = "全球排放增速放缓但升温仍在继续"
            return value
        result = worker.process_job(self.path, job, runner)
        self.assertEqual(result["status"], "completed")
        with self.database() as db:
            translated = db.execute("SELECT title,status FROM gaps WHERE source_url=?", (item["source_url"],)).fetchone()
            self.assertEqual(translated, (title + "（全球排放增速放缓但升温仍在继续）", "pending"))

    def test_same_canonical_source_queues_once_without_replacing_original(self):
        item, gap_id, job = self.seed()
        before = self.row("summary_jobs", job)
        item["source_url"] += "?utm_source=repeat"
        with self.database() as db:
            self.assertEqual(collect.queue_summary(db, item, LONG_TEXT + "另一份内容"), 0)
            self.assertEqual(db.execute("SELECT COUNT(*) FROM summary_jobs").fetchone()[0], 1)
        self.assertEqual(before, self.row("summary_jobs", job))

    def test_short_text_never_queues_or_calls_model(self):
        for length in (0, 1, 1999, 2000):
            self.assertIsNone(self.seed("字" * length)[2])
        runner = mock.Mock(side_effect=AssertionError("must not call"))
        code, report = self.run_worker(runner=runner)
        self.assertEqual(code, 0)
        self.assertEqual(report[-1]["processed"], 0)
        runner.assert_not_called()

    def test_incomplete_short_excerpt_and_oversized_text_go_to_manual_queue(self):
        for text, truncated in (("字" * 20001, False), ("字" * 20000, True), ("短摘录", True)):
            item, gap_id, job = self.seed(text, truncated)
            self.assertEqual(self.row("summary_jobs", job)["state"], "too_long")
            self.assertLessEqual(len(self.row("summary_jobs", job)["source_text"]), 20000)
            self.assertIn("人工", self.row("gaps", gap_id)["review_note"])
            runner = mock.Mock(side_effect=AssertionError("must not call"))
            self.assertTrue(worker.process_job(self.path, job, runner)["skipped"])
            runner.assert_not_called()
        self.assertEqual(self.count_attempts(), 0)

    def test_member_submissions_and_already_published_content_do_not_queue(self):
        self.assertIsNone(self.seed(author=True)[2])
        self.assertIsNone(self.seed(status="published")[2])

    def test_success_only_updates_draft_and_never_publishes_or_rewards(self):
        item, gap_id, job = self.seed()
        before = self.row("gaps", gap_id)
        result = worker.process_job(self.path, job, self.successful)
        after = self.row("gaps", gap_id)
        self.assertEqual(result["status"], "completed")
        self.assertEqual(after["summary"], SUMMARY)
        self.assertIn(BODY, after["body"])
        self.assertIn(item["source_url"], after["body"])
        for key in ("status", "level", "evidence", "released_at", "rewarded", "author_id"):
            self.assertEqual(after[key], before[key])
        self.assertEqual(after["status"], "pending")
        self.assertEqual(self.row("summary_jobs", job)["attempts"], 1)
        self.assertEqual(self.count_attempts(), 1)

    def test_internal_source_remains_internal_pending_for_review(self):
        item, gap_id, job = self.seed(visibility="internal")
        worker.process_job(self.path, job, self.successful)
        after = self.row("gaps", gap_id)
        self.assertEqual(after["source_visibility"], "internal")
        self.assertEqual(after["evidence"], "unverified")
        self.assertEqual(after["status"], "pending")
        self.assertIn("内部核验参考", after["body"])

    def test_completed_job_does_not_reenter_or_change_completed_status(self):
        item, gap_id, job = self.seed()
        worker.process_job(self.path, job, self.successful)
        before = self.row("gaps", gap_id)
        runner = mock.Mock(side_effect=AssertionError("must not call twice"))
        self.assertTrue(worker.process_job(self.path, job, runner)["skipped"])
        self.assertEqual(self.row("summary_jobs", job)["state"], "completed")
        self.assertEqual(before, self.row("gaps", gap_id))
        runner.assert_not_called()

    def test_prior_edit_or_review_audit_blocks_even_same_second(self):
        for action in ("edit_gap", "approve", "reject", "dispute", "expire"):
            item, gap_id, job = self.seed()
            with self.database() as db:
                db.execute("UPDATE gaps SET summary=? WHERE id=?", ("同一秒人工修订摘要，正文和更新时间不变", gap_id))
                db.execute("INSERT INTO audit(action,object_id,note,created_at) VALUES(?,?,?,?)", (action, str(gap_id), "人工操作", collect.now()))
            before = self.row("gaps", gap_id)
            runner = mock.Mock(side_effect=AssertionError("must not overwrite editor"))
            self.assertEqual(worker.process_job(self.path, job, runner)["status"], "edited")
            self.assertEqual(before, self.row("gaps", gap_id))
            runner.assert_not_called()
        self.assertEqual(self.count_attempts(), 0)

    def test_changed_title_body_or_review_status_blocks_before_model(self):
        for column, value in (("title", "人工修订后的新标题"), ("body", "新的人工正文"), ("status", "published")):
            item, gap_id, job = self.seed()
            with self.database() as db:
                db.execute("UPDATE gaps SET " + column + "=? WHERE id=?", (value, gap_id))
            runner = mock.Mock(side_effect=AssertionError("must not call"))
            self.assertEqual(worker.process_job(self.path, job, runner)["status"], "edited")
            runner.assert_not_called()

    def test_edit_review_or_metadata_change_during_model_is_preserved(self):
        for column, value in (("summary", "人工摘要变更"), ("title", "新的人工标题"), ("status", "published"), ("source_visibility", "internal"), ("evidence", "official"), ("tags", '["人工标签"]'), ("review_note", "人工核验说明")):
            item, gap_id, job = self.seed()
            def runner(payload):
                with self.database() as db:
                    db.execute("UPDATE gaps SET " + column + "=? WHERE id=?", (value, gap_id))
                return self.successful(payload)
            result = worker.process_job(self.path, job, runner)
            self.assertEqual(result["status"], "edited")
            self.assertEqual(self.row("gaps", gap_id)[column], value)
            self.assertEqual(self.row("gaps", gap_id)["body"], item["body"])

    def test_reverted_same_second_edit_during_model_is_caught_by_audit(self):
        item, gap_id, job = self.seed()
        def runner(payload):
            with self.database() as db:
                db.execute("INSERT INTO audit(action,object_id,note,created_at) VALUES('edit_gap',?,'人工保存后恢复原样',?)", (str(gap_id), collect.now()))
            return self.successful(payload)
        self.assertEqual(worker.process_job(self.path, job, runner)["status"], "edited")
        self.assertEqual(self.row("gaps", gap_id)["body"], item["body"])

    def test_runner_failure_and_invalid_output_preserve_original(self):
        cases = [None, [], {"status": "error", "error": "模型不可用"}, {"status": "ok", "model": "wrong-model"}]
        for result in cases:
            item, gap_id, job = self.seed()
            before = self.row("gaps", gap_id)
            self.assertEqual(worker.process_job(self.path, job, lambda payload: result)["status"], "failed")
            self.assertEqual(before, self.row("gaps", gap_id))
            self.assertEqual(self.row("summary_jobs", job)["state"], "failed")
        item, gap_id, job = self.seed()
        before = self.row("gaps", gap_id)
        result = worker.process_job(self.path, job, mock.Mock(side_effect=OSError("sensitive provider detail")))
        self.assertEqual(result["status"], "failed")
        self.assertNotIn("sensitive", self.row("summary_jobs", job)["error"])
        self.assertEqual(before, self.row("gaps", gap_id))

    def test_worker_revalidates_chinese_draft_and_rejects_links_html_or_mismatched_hash(self):
        for changes in ({"body": BODY + " https://untrusted.test/a"}, {"summary": "this is not Chinese and has no sentence boundary"}, {"body": BODY + "<script>bad</script>"}, {"reasoning": "high"}, {"content_hash": "f" * 64}):
            item, gap_id, job = self.seed()
            def runner(payload):
                output = self.successful(payload)
                output.update(changes)
                return output
            self.assertEqual(worker.process_job(self.path, job, runner)["status"], "failed")
            self.assertEqual(self.row("gaps", gap_id)["body"], item["body"])

    def test_source_fingerprint_tampering_never_reaches_runner(self):
        item, gap_id, job = self.seed()
        with self.database() as db:
            db.execute("UPDATE summary_jobs SET source_text=source_text || '已变化' WHERE id=?", (job,))
        runner = mock.Mock(side_effect=AssertionError("must not call"))
        self.assertEqual(worker.process_job(self.path, job, runner)["status"], "invalid_source")
        runner.assert_not_called()
        self.assertEqual(self.count_attempts(), 0)

    def test_single_job_at_most_three_attempts_and_six_hour_cooldown(self):
        item, gap_id, job = self.seed()
        runner = mock.Mock(return_value={"status": "error", "error": "暂不可用"})
        for attempt in range(3):
            moment = NOW + dt.timedelta(hours=7 * attempt)
            with mock.patch.object(collect, "utcnow", return_value=moment):
                self.assertEqual(worker.process_job(self.path, job, runner)["status"], "failed")
                self.assertTrue(worker.process_job(self.path, job, runner)["skipped"])
                self.assertEqual(runner.call_count, attempt + 1)
        with mock.patch.object(collect, "utcnow", return_value=NOW + dt.timedelta(days=2)):
            self.assertTrue(worker.process_job(self.path, job, runner)["skipped"])
            code, report = self.run_worker(runner=runner)
        self.assertEqual(runner.call_count, 3)
        self.assertEqual(self.row("summary_jobs", job)["attempts"], 3)
        self.assertEqual(report[-1]["processed"], 0)

    def test_inflight_job_cannot_be_reentered_even_on_third_attempt(self):
        item, gap_id, job = self.seed()
        with self.database() as db:
            db.execute("UPDATE summary_jobs SET attempts=2 WHERE id=?", (job,))
        second = mock.Mock(side_effect=AssertionError("duplicate model call"))
        def runner(payload):
            duplicate = worker.process_job(self.path, job, second)
            self.assertTrue(duplicate["skipped"])
            self.assertEqual(duplicate["status"], "processing")
            return self.successful(payload)
        self.assertEqual(worker.process_job(self.path, job, runner)["status"], "completed")
        second.assert_not_called()
        self.assertEqual(self.row("summary_jobs", job)["attempts"], 3)

    def test_daily_limit_eight_and_each_run_max_three(self):
        for unused in range(12):
            self.seed()
        runner = mock.Mock(side_effect=self.successful)
        processed = []
        for unused in range(4):
            code, report = self.run_worker(runner=runner)
            processed.append(report[-1]["processed"])
        self.assertEqual(processed, [3, 3, 2, 0])
        self.assertEqual(runner.call_count, 8)
        self.assertEqual(self.count_attempts(), 8)
        with self.assertRaises(ValueError):
            self.run_worker(max_items=4, runner=runner)
        with self.database() as db:
            next_job = db.execute("SELECT id FROM summary_jobs WHERE state='pending' ORDER BY id LIMIT 1").fetchone()[0]
        self.assertEqual(worker.process_job(self.path, next_job, runner)["reason"], "daily_limit")
        self.assertEqual(runner.call_count, 8)

    def test_daily_budget_resets_at_beijing_midnight_not_utc_midnight(self):
        for unused in range(10):
            self.seed()
        with self.database() as db:
            for unused in range(8):
                db.execute("INSERT INTO audit(action,object_id,note,created_at) VALUES('summary_attempt','old','', '2026-10-21T15:59:59Z')")
        runner = mock.Mock(side_effect=self.successful)
        with mock.patch.object(collect, "utcnow", return_value=dt.datetime(2026, 10, 21, 15, 59, 59, tzinfo=dt.timezone.utc)):
            self.assertEqual(self.run_worker(runner=runner)[1][-1]["processed"], 0)
        with mock.patch.object(collect, "utcnow", return_value=dt.datetime(2026, 10, 21, 16, 0, 0, tzinfo=dt.timezone.utc)):
            self.assertEqual(self.run_worker(runner=runner)[1][-1]["processed"], 3)
        self.assertEqual(runner.call_count, 3)

    def test_stale_processing_at_limit_becomes_manual_failure(self):
        item, gap_id, job = self.seed()
        with self.database() as db:
            db.execute("UPDATE summary_jobs SET state='processing',attempts=3,last_attempt='2026-10-20T00:00:00Z' WHERE id=?", (job,))
        runner = mock.Mock(side_effect=AssertionError("must not call"))
        self.run_worker(runner=runner)
        self.assertEqual(self.row("summary_jobs", job)["state"], "failed")
        self.assertIn("3次", self.row("summary_jobs", job)["error"])
        runner.assert_not_called()

    def test_usage_output_contains_only_nonnegative_token_counts(self):
        item, gap_id, job = self.seed()
        def runner(payload):
            output = self.successful(payload)
            output["usage"] = {"input_tokens": 12, "raw_source": LONG_TEXT, "output_tokens": -1, "model_calls": True}
            return output
        self.assertEqual(worker.process_job(self.path, job, runner)["usage"], {"input_tokens": 12})

    def test_nonzero_child_exit_cannot_claim_success(self):
        payload = {"content_hash": hashlib.sha256(LONG_TEXT.encode("utf-8")).hexdigest()}
        child = mock.Mock(returncode=1)
        child.communicate.return_value = (json.dumps(self.successful(payload)).encode("utf-8"), b"private provider diagnostics")
        with mock.patch.object(worker.subprocess, "Popen", return_value=child):
            result = worker.invoke(payload)
        self.assertEqual(result["status"], "error")
        self.assertNotIn("private", json.dumps(result))

    def test_child_timeout_kills_process_group_and_keeps_error_private(self):
        child = mock.Mock(returncode=-9, pid=12345)
        child.communicate.side_effect = [subprocess.TimeoutExpired("fixed runner", 150), (b"", b"private diagnostics")]
        with mock.patch.object(worker.subprocess, "Popen", return_value=child), mock.patch.object(worker.os, "killpg") as kill:
            result = worker.invoke({})
        kill.assert_called_once_with(12345, worker.signal.SIGKILL)
        self.assertEqual(result["status"], "error")
        self.assertNotIn("private", json.dumps(result))

    def test_browser_raw_source_validation_and_truncation(self):
        source = dict(bridge.LINUX)
        item = {"title": "公开话题研究方法和适用条件", "summary": "公开主帖包含实验方法与适用边界，需要人工核对后才能发布。", "body": "公开候选的正文摘录仍然需要核验。" * 8, "source_url": "https://linux.do/t/topic/12345", "tags": [], "raw_text": LONG_TEXT, "raw_text_length": len(LONG_TEXT), "text_truncated": False, "raw_text_scope": "main_post_visible_dom"}
        valid = bridge.validate_candidate(item, source)
        self.assertEqual(valid["raw_text"], LONG_TEXT)
        self.assertEqual(valid["source_visibility"], "internal")
        self.assertEqual(valid["status"], "pending")
        for change in ({"raw_text": []}, {"raw_text": "字" * 20001}, {"raw_text_length": True}, {"raw_text_length": len(LONG_TEXT) + 1}, {"text_truncated": 1}, {"raw_text_scope": "entire_page"}):
            bad = dict(item)
            bad.update(change)
            with self.assertRaises(ValueError):
                bridge.validate_candidate(bad, source)
        item.update(raw_text="字" * 20000, raw_text_length=23000, text_truncated=True)
        self.assertTrue(bridge.validate_candidate(item, source)["text_truncated"])

    def test_browser_raw_source_cannot_be_imported_as_x_material(self):
        source = {"id": "x-nature", "handle": "nature", "category": "前沿科技", "name": "Nature", "primary_domains": ["nature.com"]}
        item = {"title": "公开研究进展需要进一步核验", "summary": "公开帖说明了研究方法与适用范围，详细结论仍需要核验。", "body": "原始研究材料需要核对方法、证据和适用边界。" * 7, "source_url": "https://x.com/nature/status/123456789", "author_handle": "nature", "primary_evidence_urls": ["https://www.nature.com/articles/example"], "tags": [], "raw_text": LONG_TEXT, "raw_text_length": len(LONG_TEXT), "text_truncated": False, "raw_text_scope": "main_post_visible_dom"}
        with self.assertRaises(ValueError):
            bridge.validate_candidate(item, source)

    def test_worker_and_test_remain_python36_syntax(self):
        if sys.version_info >= (3, 8):
            for file in (HERE / "summary-worker.py", Path(__file__)):
                ast.parse(file.read_text(encoding="utf-8"), feature_version=(3, 6))


if __name__ == "__main__":
    unittest.main(verbosity=2)
