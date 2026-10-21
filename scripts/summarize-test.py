#!/usr/bin/env python3
"""Offline fixtures: no model calls, network, credentials or production database."""
import copy
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

sys.dont_write_bytecode = True
spec = importlib.util.spec_from_file_location("summarizer", str(Path(__file__).with_name("summarize.py")))
worker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(worker)


def payload(text=None):
    if text is None:
        text = "研究人员在受控实验中比较了两种方法。研究范围有限，目前不能推广到所有情形。" * 70
    return {"source_url": "https://example.org/original", "title": "实验方法研究进展",
            "text": text, "content_hash": hashlib.sha256(text.encode("utf-8")).hexdigest()}


def draft():
    return {"summary": "研究人员比较了两种实验方法，并报告了观察到的差异。这项结果有明确的实验条件，还需要更多验证。",
            "body": "## 发生了什么\n\n研究团队在受控条件下比较两种实验方法，记录了各自的结果。\n\n## 怎样理解\n\n就像在相同条件下比较两种做法，研究首先要排除其他因素的干扰，才能判断观察到的差异来自哪里。\n\n## 适用范围\n\n原文只讨论这一组实验，不能据此判断所有情形都会有相同结果。后续仍需更多验证。"}


class SummarizerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.cache = self.root / "cache"
        self.calls = []
        self.now = 10000

    def tearDown(self):
        self.temp.cleanup()

    def runner(self, data, cache_dir):
        self.calls.append(data["content_hash"])
        return draft(), {"input_tokens": 2000, "output_tokens": 200}

    def run_job(self, data=None, runner=None):
        return worker.summarize(data or payload(), self.cache, runner or self.runner, lambda: self.now)

    def test_short_and_oversize_never_call_or_truncate(self):
        for size, status in ((2000, "skipped_short"), (20001, "too_long")):
            result = self.run_job(payload("字" * size))
            self.assertEqual(result["status"], status)
            self.assertEqual(result["body"], "")
        self.assertEqual(self.calls, [])
        self.assertFalse(self.cache.exists())

    def test_short_english_requires_translation_and_cache_includes_title(self):
        data = payload("Global emissions are slowing, but warming continues.")
        data["title"] = "Global emissions slow but warming continues"
        def translated(data, cache):
            self.calls.append(data["title"])
            value = draft()
            value["title_zh"] = "全球排放增速放缓，但升温仍在继续"
            return value, {}
        self.assertEqual(self.run_job(data, translated)["status"], "ok")
        self.assertEqual(self.run_job(data, translated)["status"], "cached")
        data["title"] += " today"
        self.assertEqual(self.run_job(data, translated)["status"], "ok")
        self.assertEqual(len(self.calls), 2)
        self.assertTrue(worker.needs_chinese(data["title"], "中文说明" * 200))

    def test_boundaries_are_processed(self):
        for size in (2001, 20000):
            self.assertEqual(self.run_job(payload("字" * size))["status"], "ok")
        self.assertEqual(len(self.calls), 2)

    def test_invalid_hash_and_metadata_rejected_before_model(self):
        for changes in ({"content_hash": "../credentials"}, {"source_url": "file:///etc/passwd"}, {"extra": True}, {"title": ""}, {"text": 8}):
            data = payload()
            data.update(changes)
            with self.assertRaises(ValueError):
                self.run_job(data)
        self.assertEqual(self.calls, [])

    def test_success_cache_avoids_repeat_tokens(self):
        first, second = self.run_job(), self.run_job()
        self.assertEqual(first["status"], "ok")
        self.assertEqual(second["status"], "cached")
        self.assertEqual(second["body"], first["body"])
        self.assertEqual(second["usage"]["model_calls"], 0)
        self.assertEqual(len(self.calls), 1)
        self.assertEqual(self.cache.stat().st_mode & 0o777, 0o700)
        for file in self.cache.glob("*.json"):
            self.assertEqual(file.stat().st_mode & 0o777, 0o600)
            self.assertNotIn(payload()["text"], file.read_text())

    def test_changed_text_gets_separate_cache(self):
        self.run_job()
        self.run_job(payload(payload()["text"] + "新发现仅适用于这一组实验。"))
        self.assertEqual(len(self.calls), 2)

    def test_provider_failure_has_global_six_hour_cooldown(self):
        calls = []
        def unavailable(data, cache_dir):
            calls.append(True)
            raise worker.ProviderFailure("指定模型当前不可用")
        first = self.run_job(runner=unavailable)
        self.assertEqual(first["status"], "error")
        other = payload(payload()["text"] + "新增原文")
        self.assertEqual(self.run_job(other, unavailable)["status"], "cooldown")
        self.assertEqual(len(calls), 1)
        self.now += worker.COOLDOWN_SECONDS + 1
        self.assertEqual(self.run_job(other)["status"], "ok")

    def test_cached_success_can_be_read_during_provider_outage(self):
        self.run_job()
        worker.write_cache(self.cache / "provider-cooldown.json", {"retry_after": self.now + 10000})
        self.assertEqual(self.run_job()["status"], "cached")
        self.assertEqual(len(self.calls), 1)

    def test_invalid_output_is_not_saved_as_success_or_retried(self):
        calls = []
        def invalid(data, cache_dir):
            calls.append(True)
            value = draft()
            value["body"] = "字" * 1801
            return value, {}
        self.assertEqual(self.run_job(runner=invalid)["status"], "error")
        self.assertEqual(self.run_job(runner=invalid)["status"], "cooldown")
        self.assertEqual(len(calls), 1)

    def test_draft_must_be_chinese_short_sentences_without_links(self):
        for field, value in (("summary", "只有一个中文句子且没有第二个句子。"), ("summary", "字" * 181), ("body", "字" * 1801), ("body", draft()["body"] + "https://example.org"), ("body", draft()["body"] + "linux.do"), ("body", draft()["body"] + "[来源](example)"), ("body", draft()["body"] + "<script>alert(1)</script>")):
            data = draft()
            data[field] = value
            with self.assertRaises(ValueError):
                worker.validate_draft(data)

    def test_command_freezes_model_and_disables_tools(self):
        command = worker.codex_command(self.root)
        self.assertEqual(command[0], "/usr/local/bin/codex")
        self.assertIn("gpt-6.1-sol", command)
        self.assertIn("model_reasoning_effort=low", command)
        self.assertIn("mcp_servers={}", command)
        self.assertIn('web_search="disabled"', command)
        for feature in ("shell_tool", "unified_exec", "apps", "plugins", "hooks", "browser_use", "multi_agent"):
            i = command.index(feature)
            self.assertEqual(command[i-1], "--disable")
        self.assertNotIn("--search", command)
        self.assertNotIn("danger-full-access", command)

    def test_events_reject_tools_and_missing_completion(self):
        for event in ({"type": "item.started", "item": {"type": "command_execution", "command": "bad"}}, {"type": "item.completed", "item": {"type": "mcp_tool_call"}}):
            with self.assertRaises(ValueError):
                worker.parse_events(json.dumps(event).encode())
        with self.assertRaises(worker.ProviderFailure):
            worker.parse_events(b'{"type":"turn.started"}\n')

    def test_cli_warning_is_not_tool_but_model_reroute_is_rejected(self):
        events = [{"type": "item.completed", "item": {"type": "error", "message": "A configuration option is deprecated"}},
                  {"type": "item.completed", "item": {"type": "agent_message", "text": json.dumps(draft())}},
                  {"type": "turn.completed", "usage": {"input_tokens": 200}}]
        raw = "\n".join(json.dumps(event) for event in events).encode()
        self.assertEqual(worker.parse_events(raw)[0], draft())
        events[0]["item"]["message"] = "model rerouted: gpt-6.1-sol -> another-model (unavailable)"
        with self.assertRaises(worker.ProviderFailure):
            worker.parse_events("\n".join(json.dumps(event) for event in events).encode())

    def test_validation_errors_are_fixed_local_messages(self):
        def invalid(data, cache_dir):
            worker.validate_draft({"summary": "short", "body": "short"})
        result = self.run_job(runner=invalid)
        self.assertIn("字数不符合要求", result["error"])
        self.assertNotIn("short", result["error"])

    def test_cli_has_clean_environment_and_never_receives_source_url(self):
        events = [{"type": "item.completed", "item": {"type": "agent_message", "text": json.dumps(draft())}}, {"type": "turn.completed", "usage": {"input_tokens": 200, "output_tokens": 80, "unexpected": "secret"}}]
        def popen(command, **kwargs):
            self.assertEqual(set(kwargs["env"]), {"HOME", "PATH", "LANG"})
            self.assertNotIn("shell", kwargs)
            self.assertTrue(kwargs["start_new_session"])
            process = mock.Mock(returncode=0)
            def communicate(value, timeout):
                self.assertEqual(timeout, 120)
                self.assertNotIn(payload()["source_url"].encode(), value)
                kwargs["stdout"].write("\n".join(json.dumps(e) for e in events).encode())
            process.communicate.side_effect = communicate
            return process
        self.cache.mkdir()
        with mock.patch.object(worker.subprocess, "Popen", side_effect=popen):
            result, usage = worker.invoke_codex(payload(), self.cache)
        self.assertEqual(result, draft())
        self.assertNotIn("unexpected", usage)

    def test_timeout_kills_process_group_without_retry(self):
        self.cache.mkdir()
        process = mock.Mock(pid=34567, returncode=0)
        process.communicate.side_effect = subprocess.TimeoutExpired("codex", 120)
        with mock.patch.object(worker.subprocess, "Popen", return_value=process) as popen, mock.patch.object(worker.os, "killpg") as kill:
            with self.assertRaises(worker.ProviderFailure):
                worker.invoke_codex(payload(), self.cache)
        self.assertEqual(popen.call_count, 1)
        kill.assert_called_once_with(34567, signal.SIGTERM)

    def test_lock_blocks_concurrent_model_runs(self):
        self.cache.mkdir()
        with open(str(self.cache / "worker.lock"), "w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.assertEqual(self.run_job()["status"], "error")
        self.assertEqual(self.calls, [])

    def test_schema_has_local_limits(self):
        schema = json.loads(worker.SCHEMA.read_text())
        self.assertEqual(schema["properties"]["summary"]["maxLength"], 180)
        self.assertEqual(schema["properties"]["body"]["maxLength"], 1800)
        self.assertFalse(schema["additionalProperties"])


if __name__ == "__main__":
    unittest.main(verbosity=2)
