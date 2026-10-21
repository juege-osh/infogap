#!/usr/bin/env python3
"""Apply bounded Codex drafts only to unchanged, automatically collected pending items."""
import argparse
import datetime as dt
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("browser_collect", str(ROOT / "scripts" / "browser-collect.py"))
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)
collect = bridge.collect
spec = importlib.util.spec_from_file_location("summary_validation", str(ROOT / "scripts" / "summarize.py"))
validation = importlib.util.module_from_spec(spec)
spec.loader.exec_module(validation)
MAX_ATTEMPTS = 3
DAILY_ATTEMPTS = 8
# The complete editable record is compared again inside the write transaction.
GAP_SNAPSHOT = "title,summary,body,category,level,evidence,tags,source_name,source_url,source_visibility,updated_at,status,author_id,review_note"


def invoke(payload):
    process = subprocess.Popen(["/bin/sh", str(ROOT / "deploy" / "summarize-run.sh")], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                               env={"PATH": "/usr/local/bin:/usr/bin:/bin", "LANG": "C.UTF-8"}, start_new_session=True)
    try:
        stdout, stderr = process.communicate(json.dumps(payload, ensure_ascii=False).encode("utf-8"), timeout=150)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.communicate()
        return {"status": "error", "error": "长文精炼超时，保留原待审核稿。"}
    if len(stdout) > 65536:
        return {"status": "error", "error": "精炼输出大小异常。"}
    try:
        result = json.loads(stdout.decode("utf-8"))
        if not isinstance(result, dict):
            return {"status": "error", "error": "精炼结果格式异常。"}
        if process.returncode != 0 and result.get("status") in ("ok", "cached"):
            return {"status": "error", "error": "精炼进程未正常结束，未改动正文。"}
        return result
    except (ValueError, UnicodeError):
        return {"status": "error", "error": "模型未返回有效精炼结果；未改动正文。"}


def windows():
    current = collect.utcnow()
    day_start = (current + dt.timedelta(hours=8)).replace(hour=0, minute=0, second=0, microsecond=0) - dt.timedelta(hours=8)
    return day_start.strftime("%Y-%m-%dT%H:%M:%SZ"), (current - dt.timedelta(hours=6)).strftime("%Y-%m-%dT%H:%M:%SZ")


def attempts_today(db, day_start):
    return db.execute("SELECT COUNT(*) FROM audit WHERE action='summary_attempt' AND created_at>=?", (day_start,)).fetchone()[0]


def touched_by_editor(db, gap_id):
    # Timestamps have second precision. An audit record also catches a summary-
    # only edit within the original timestamp, or an edit later reverted by hand.
    return db.execute("SELECT 1 FROM audit WHERE object_id=? AND action IN ('edit_gap','approve','reject','dispute','expire') LIMIT 1", (str(gap_id),)).fetchone() is not None


def set_state(db, job_id, state, error):
    db.execute("UPDATE summary_jobs SET state=?,error=?,updated_at=? WHERE id=?", (state, error, collect.now(), job_id))


def process_job(path, job_id, runner=None):
    runner = runner or invoke
    day_start, cutoff = windows()
    with bridge.database(path) as db:
        with db:
            # Claim and daily-budget reservation are atomic, even if this helper
            # is called without the scheduler's file lock.
            db.execute("BEGIN IMMEDIATE")
            row = db.execute("SELECT gap_id,content_hash,title,source_text,original_body_hash,original_updated_at,state,attempts,last_attempt FROM summary_jobs WHERE id=?", (job_id,)).fetchone()
            if not row:
                return {"job": job_id, "status": "missing"}
            gap_id, digest, title, text, expected, edited_at, state, attempts, last_attempt = row
            if state not in ("pending", "failed", "processing"):
                return {"job": job_id, "status": state, "skipped": True}
            if state in ("failed", "processing") and last_attempt and last_attempt >= cutoff:
                return {"job": job_id, "status": state, "skipped": True}
            if attempts >= MAX_ATTEMPTS:
                set_state(db, job_id, "failed", "已达到单篇最多3次尝试，保留原稿待人工处理。")
                return {"job": job_id, "status": "failed", "skipped": True}
            snapshot = db.execute("SELECT " + GAP_SNAPSHOT + " FROM gaps WHERE id=?", (gap_id,)).fetchone()
            if (not snapshot or snapshot[11] != "pending" or snapshot[12] is not None or snapshot[10] != edited_at or
                    snapshot[0] != title or hashlib.sha256(snapshot[2].encode("utf-8")).hexdigest() != expected or touched_by_editor(db, gap_id)):
                set_state(db, job_id, "edited", "稿件已被编辑或审核，自动精炼不会覆盖。")
                return {"job": job_id, "status": "edited"}
            if not isinstance(text, str) or len(text) <= 2000 or len(text) > 20000 or hashlib.sha256(text.encode("utf-8")).hexdigest() != digest:
                set_state(db, job_id, "failed", "原文长度或指纹不符合精炼条件。")
                return {"job": job_id, "status": "invalid_source"}
            if attempts_today(db, day_start) >= DAILY_ATTEMPTS:
                return {"job": job_id, "status": state, "skipped": True, "reason": "daily_limit"}
            claimed_attempt = attempts + 1
            db.execute("UPDATE summary_jobs SET state='processing',attempts=?,last_attempt=?,updated_at=? WHERE id=?", (claimed_attempt, collect.now(), collect.now(), job_id))
            db.execute("INSERT INTO audit(action,object_id,note,created_at) VALUES('summary_attempt',?,'gpt-6.1-sol / low',?)", (str(job_id), collect.now()))
    try:
        result = runner({"source_url": snapshot[8], "title": title, "text": text, "content_hash": digest})
    except Exception:
        # Never put provider errors or source text into worker logs.
        result = {"status": "error", "error": "精炼调用失败，保留原稿待重试或人工处理。"}
    if not isinstance(result, dict):
        result = {"status": "error", "error": "精炼结果格式异常，未改动正文。"}
    valid = result.get("status") in ("ok", "cached") and result.get("model") == "gpt-6.1-sol" and result.get("reasoning") == "low" and result.get("content_hash") == digest
    summary, body = "", ""
    if valid:
        try:
            draft = validation.validate_draft({"summary": result.get("summary"), "body": result.get("body")})
            summary, body = draft["summary"], draft["body"]
            heading = "内部核验参考 · 发布前需整理" if snapshot[9] == "internal" else "原始来源"
            body += "\n\n## " + heading + "\n\n[" + snapshot[7] + "](" + snapshot[8] + ")"
            valid = len(body) <= 2000
        except (ValueError, TypeError):
            valid = False
    with bridge.database(path) as db:
        with db:
            db.execute("BEGIN IMMEDIATE")
            claim = db.execute("SELECT state,attempts FROM summary_jobs WHERE id=?", (job_id,)).fetchone()
            if not claim or claim != ("processing", claimed_attempt):
                return {"job": job_id, "status": "superseded", "skipped": True}
            current = db.execute("SELECT " + GAP_SNAPSHOT + " FROM gaps WHERE id=?", (gap_id,)).fetchone()
            if current != snapshot or touched_by_editor(db, gap_id):
                state, error = "edited", "处理期间稿件已被编辑或审核，未覆盖。"
            elif valid:
                state, error = "completed", ""
                db.execute("UPDATE gaps SET summary=?,body=?,updated_at=?,review_note=review_note || ? WHERE id=?", (summary, body, collect.now(), " 已由gpt-6.1-sol低推理精炼；模型只整理现有材料，需审核事实、日期与来源后发布。", gap_id))
            else:
                state, error = "failed", str(result.get("error") or "模型结果未通过格式、字数或模型身份校验。")[:180]
            set_state(db, job_id, state, error)
    usage = result.get("usage", {})
    usage = {key: value for key, value in usage.items() if key in ("input_tokens", "cached_input_tokens", "output_tokens", "reasoning_output_tokens", "model_calls") and type(value) is int and value >= 0} if isinstance(usage, dict) else {}
    return {"job": job_id, "status": state, "model": "gpt-6.1-sol", "reasoning": "low", "cache_hit": result.get("status") == "cached", "usage": usage}


def run(args):
    if type(args.max_items) is not int or args.max_items not in (1, 2, 3):
        raise ValueError("每轮只能处理1至3篇")
    path, lock = Path(args.db), None
    try:
        with bridge.database_identity(path):
            lock = open(str(path) + ".summary.lock", "a")
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        day_start, cutoff = windows()
        with bridge.database(path) as db:
            with db:
                db.execute("UPDATE summary_jobs SET state='failed',error='已达到单篇最多3次尝试，保留原稿待人工处理。',updated_at=? WHERE state='processing' AND attempts>=? AND last_attempt<?", (collect.now(), MAX_ATTEMPTS, cutoff))
            used = attempts_today(db, day_start)
            budget = min(args.max_items, max(0, DAILY_ATTEMPTS-used))
            jobs = [row[0] for row in db.execute("SELECT id FROM summary_jobs WHERE attempts<? AND (state='pending' OR (state IN ('failed','processing') AND last_attempt<?)) ORDER BY id LIMIT ?", (MAX_ATTEMPTS, cutoff, budget))]
        for job_id in jobs:
            print(json.dumps(process_job(path, job_id), ensure_ascii=False), flush=True)
        with bridge.database(path) as db:
            used = attempts_today(db, windows()[0])
        print(json.dumps({"processed": len(jobs), "daily_attempt_limit": DAILY_ATTEMPTS, "remaining_daily_attempts": max(0, DAILY_ATTEMPTS-used)}), flush=True)
    finally:
        if lock is not None:
            lock.close()
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--db", default="/var/lib/infogap-osh/infogap.sqlite")
    parser.add_argument("--max-items", type=int, default=3)
    args = parser.parse_args()
    if args.max_items not in (1, 2, 3):
        parser.error("--max-items must be 1..3")
    try:
        return run(args)
    except BlockingIOError:
        print('{"skipped":"another summary worker is running"}')
        return 0
    except Exception:
        print('{"error":"精炼队列执行失败，请检查服务与数据库状态；未自动发布。"}', file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
