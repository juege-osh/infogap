#!/usr/bin/env python3
"""Run a resumable local official-account queue under one shared CST daily budget."""
import argparse,datetime,fcntl,importlib.util,json,os,pathlib,subprocess,tempfile
ROOT=pathlib.Path(__file__).resolve().parents[2]
STATE=ROOT/'state/official-editor'
CST=datetime.timezone(datetime.timedelta(hours=8))
def day():return datetime.datetime.now(CST).date().isoformat()
def main():
 ap=argparse.ArgumentParser();ap.add_argument('--run-dir',required=True);ap.add_argument('--target',type=int,required=True);ap.add_argument('--workers',type=int,default=4);a=ap.parse_args()
 if not 1<=a.target<=1000 or not 1<=a.workers<=8:ap.error('target 1..1000; workers 1..8')
 out=pathlib.Path(a.run_dir).resolve();assert (out/'eligible-source-corpus.json').is_file()
 STATE.mkdir(parents=True,exist_ok=True);os.chmod(STATE,0o700)
 with (STATE/'editor.lock').open('a') as lock:
  try:fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
  except BlockingIOError:print('{"skipped":"another_official_queue_running"}');return 0
  os.environ['INFOGAP_RUN_DIR']=str(out)
  spec=importlib.util.spec_from_file_location('queue_engine',pathlib.Path(__file__).with_name('runner.py'));engine=importlib.util.module_from_spec(spec);spec.loader.exec_module(engine)
  with tempfile.TemporaryDirectory(prefix='infogap-quota-') as tmp:
   h=pathlib.Path(tmp);auth=pathlib.Path.home()/'.codex/auth.json';(h/'auth.json').write_bytes(auth.read_bytes());os.chmod(h/'auth.json',0o600);q=engine.Quota(h)
   try:initial=q.read()
   finally:q.close()
  ledger=STATE/(day()+'.json')
  if ledger.exists():baseline=json.loads(ledger.read_text())['baseline']
  else:
   baseline=initial
   # Include today's already-authorized benchmark expenditure in the same cap.
   historical=ROOT/'evidence/official-1000-20261022/longform-v2/quota.jsonl'
   if day()=='2026-10-22' and historical.exists():baseline=json.loads(historical.read_text().splitlines()[0])
   ledger.write_text(json.dumps({'day':day(),'baseline':baseline,'max_points':5,'reserve_points':1},ensure_ascii=False,indent=2))
  from budget import BudgetGuard
  ok,reason=BudgetGuard.from_record(json.loads(ledger.read_text()),day=day()).check(initial)
  if not ok:print(json.dumps({'stopped':reason}));return 2
  env=dict(os.environ,INFOGAP_DAY_ANCHOR=str(baseline['used_percent']),INFOGAP_MAX_DONE=str(a.target),INFOGAP_WORKERS=str(a.workers),INFOGAP_BATCH_SIZE='10')
  with (out/'run.log').open('a') as log:r=subprocess.run([os.sys.executable,str(pathlib.Path(__file__).with_name('runner.py'))],env=env,stdout=log,stderr=subprocess.STDOUT)
  if (out/'summary.json').exists():print((out/'summary.json').read_text())
  return r.returncode
if __name__=='__main__':raise SystemExit(main())
