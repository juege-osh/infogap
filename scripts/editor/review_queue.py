#!/usr/bin/env python3
"""Evidence review under the same shared daily official quota. Never publishes."""
import argparse,concurrent.futures,datetime,fcntl,hashlib,importlib.util,json,os,pathlib,signal,subprocess,sys,tempfile,time
ROOT=pathlib.Path(__file__).resolve().parents[2];HERE=pathlib.Path(__file__).parent
from review_contract import validate
from budget import BudgetGuard

def digest(r):return hashlib.sha256(json.dumps({k:r[k] for k in ('title_zh','summary','body','source_hash')},sort_keys=True,ensure_ascii=False).encode()).hexdigest()
def main():
 ap=argparse.ArgumentParser();ap.add_argument('--run-dir',required=True);ap.add_argument('--limit',type=int,default=1000);ap.add_argument('--workers',type=int,default=4);ap.add_argument('--batch-size',type=int,default=5);a=ap.parse_args();
 if not 1<=a.workers<=20 or not 1<=a.batch_size<=5:ap.error('workers 1..20; batch-size 1..5')
 out=pathlib.Path(a.run_dir).resolve();os.environ['INFOGAP_RUN_DIR']=str(out);assert 1<=a.limit<=1000
 state=ROOT/'state/official-editor';state.mkdir(parents=True,exist_ok=True)
 with (state/'editor.lock').open('a') as generation_lock, (state/'review.lock').open('a') as lock:
  try:
   fcntl.flock(generation_lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
   fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
  except BlockingIOError:print('{"skipped":"another_official_queue_running"}');return 2
  day=datetime.datetime.now(datetime.timezone(datetime.timedelta(hours=8))).date().isoformat();baseline=json.loads((state/(day+'.json')).read_text())['baseline'];guard=BudgetGuard.from_record(json.loads((state/(day+'.json')).read_text()),day=day)
  spec=importlib.util.spec_from_file_location('engine',HERE/'runner.py');engine=importlib.util.module_from_spec(spec);spec.loader.exec_module(engine)
  sources={s['id']:s for s in json.loads((out/'eligible-source-corpus.json').read_text())};drafts=[r for r in map(json.loads,(out/'results.jsonl').read_text().splitlines()) if r['usable']][:a.limit]
  saved=out/'reviews.jsonl';old=[json.loads(l) for l in saved.read_text().splitlines()] if saved.exists() else [];done={(r['id'],r['draft_hash']) for r in old};pending=[r for r in drafts if (r['id'],digest(r)) not in done]
  batches=[pending[i:i+a.batch_size] for i in range(0,len(pending),a.batch_size)];number=len(list(out.glob('review-*-events.jsonl')))+1
  def task(batch,home,n):
   payload=[{'id':r['id'],'title':sources[r['id']]['title'],'source_text':sources[r['id']]['text'],'source_date':sources[r['id']]['source_date'],'archive_context':sources[r['id']].get('archive_context',''),'topic':r['topic'],'evidence':r['evidence'],'draft_title':r['title_zh'],'draft_summary':r['summary'],'draft_body':r['body']} for r in batch]
   env=engine.safeenv(home);env['INFOGAP_REVIEW_MODE']='1';log=out/('review-%04d-events.jsonl'%n)
   with log.open('w') as output:
    process=subprocess.Popen([sys.executable,str(HERE/'infer_appserver.py')],stdin=subprocess.PIPE,stdout=output,stderr=subprocess.DEVNULL,text=True,env=env,start_new_session=True)
    try:process.communicate(json.dumps(payload,ensure_ascii=False),timeout=420)
    except subprocess.TimeoutExpired:os.killpg(process.pid,signal.SIGTERM);process.wait(timeout=10)
   events=[json.loads(l) for l in log.read_text().splitlines()];final=[e['item']['text'] for e in events if e.get('item',{}).get('type')=='agent_message'];usage=[e['usage'] for e in events if e.get('type')=='turn.completed'];records=[]
   if process.returncode==0 and final and usage:
    values=json.loads(final[-1])['items'];wanted={r['id']:r for r in batch};seen=set()
    for v in values:
     if v.get('id') not in wanted or v['id'] in seen:continue
     seen.add(v['id'])
     try:validate(v,sources[v['id']]['text'])
     except (AssertionError,KeyError,TypeError):continue
     v.update(draft_hash=digest(wanted[v['id']]),source_hash=wanted[v['id']]['source_hash'],model='gpt-6.1-sol',reasoning='low',reviewed_at=datetime.datetime.now(datetime.timezone.utc).isoformat());records.append(v)
   return records,{'batch':n,'requested':len(batch),'returned':len(records),'usage':usage,'exit_code':process.returncode}
  with tempfile.TemporaryDirectory(prefix='infogap-official-review-') as temp:
   homes=[]
   for i in range(a.workers+1):
    home=pathlib.Path(temp)/str(i);home.mkdir(mode=0o700);(home/'auth.json').write_bytes((pathlib.Path.home()/'.codex/auth.json').read_bytes());os.chmod(home/'auth.json',0o600);homes.append(home)
   monitor=engine.Quota(homes[-1]);active={};free=list(range(a.workers));reason='completed';stop=False;failures=0
   try:
    with concurrent.futures.ThreadPoolExecutor(max_workers=a.workers) as pool:
     while batches or active:
      while free and batches and not stop:
       try:q=monitor.read(max_age=2 if a.workers>4 else 0)
       except Exception:stop=True;reason='quota_unavailable';break
       allowed,reason_check=guard.check(q)
       if not allowed:stop=True;reason=reason_check;break
       i=free.pop(0);batch=batches.pop(0);f=pool.submit(task,batch,homes[i],number);active[f]=i;number+=1
      if not active:break
      ready,_=concurrent.futures.wait(active,timeout=30,return_when=concurrent.futures.FIRST_COMPLETED)
      if not ready:print(json.dumps({'in_flight':len(active),'remaining_batches':len(batches)}),flush=True);continue
      for f in ready:
       free.append(active.pop(f))
       try:records,record=f.result()
       except Exception as e:records=[];record={'error':type(e).__name__}
       with saved.open('a') as target:
        for r in records:target.write(json.dumps(r,ensure_ascii=False)+'\n')
       try:record['quota']=monitor.read(max_age=2 if a.workers>4 else 0)
       except Exception:record['quota']=None;stop=True;reason='quota_unavailable'
       print(json.dumps(record,ensure_ascii=False),flush=True)
       failures=failures+1 if not records else 0
       if failures>=3:stop=True;reason='repeated_review_output_failure'
    try:final=monitor.read()
    except Exception:final=None;reason='quota_unavailable'
    allrows=[json.loads(l) for l in saved.read_text().splitlines()] if saved.exists() else [];report={'at':datetime.datetime.now(datetime.timezone.utc).isoformat(),'reason':reason,'reviewed':len(allrows),'approved':sum(r['decision']=='approve' for r in allrows),'held':sum(r['decision']=='hold' for r in allrows),'quota':final,'published':0};(out/'review-summary.json').write_text(json.dumps(report,ensure_ascii=False,indent=2));print(json.dumps(report,ensure_ascii=False))
   finally:monitor.close()
if __name__=='__main__':raise SystemExit(main() or 0)
