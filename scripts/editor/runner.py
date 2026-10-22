#!/usr/bin/env python3
"""Resumable benchmark on public source candidates. No production writes."""
import sys,concurrent.futures,datetime,decimal,hashlib,json,os,pathlib,selectors,signal,subprocess,tempfile,time
ROOT=pathlib.Path(__file__).resolve().parents[2]
OUT=pathlib.Path(os.environ['INFOGAP_RUN_DIR']).resolve();OUT.mkdir(parents=True,exist_ok=True)
CLI='/Users/rengang/.local/bin/codex';MODEL='gpt-6.1-sol';ANCHOR=int(os.environ.get('INFOGAP_DAY_ANCHOR','0'));STOP_AT=ANCHOR+4 # reserve one displayed percentage point within the user's 5-point budget
SAMPLES=json.loads((OUT/'eligible-source-corpus.json').read_text())
from contract import SCHEMA, INSTRUCTION as PROMPT, validate as validate_longform
from budget import BudgetGuard

def stamp():return datetime.datetime.now(datetime.timezone.utc).isoformat()
def append(name,value):
 with (OUT/name).open('a') as f:f.write(json.dumps(value,ensure_ascii=False)+'\n');f.flush();os.fsync(f.fileno())
def safeenv(home):
 env=dict(os.environ,CODEX_HOME=str(home));
 for key in ('OPENAI_API_KEY','CODEX_API_KEY','OPENAI_BASE_URL'):env.pop(key,None)
 return env
class Quota:
 def __init__(self,home):
  self.p=subprocess.Popen([CLI,'app-server','--listen','stdio://'],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True,env=safeenv(home));self.n=0
  self.rpc('initialize',{'clientInfo':{'name':'infogap_quota_benchmark','version':'1.0.0'}})
 def rpc(self,method,params):
  self.n+=1;ident=self.n;self.p.stdin.write(json.dumps({'id':ident,'method':method,'params':params})+'\n');self.p.stdin.flush();sel=selectors.DefaultSelector();sel.register(self.p.stdout,selectors.EVENT_READ);deadline=time.monotonic()+45
  try:
   while time.monotonic()<deadline:
    if not sel.select(max(0,deadline-time.monotonic())):break
    line=self.p.stdout.readline()
    if not line:raise RuntimeError('quota monitor closed')
    d=json.loads(line)
    if d.get('id')==ident:
     if 'error' in d:raise RuntimeError('quota read refused')
     return d['result']
   raise RuntimeError('quota read timeout')
  finally:sel.close()
 def read(self):
  d=self.rpc('account/rateLimits/read',{});r=d.get('rateLimitsByLimitId',{}).get('codex') or d['rateLimits'];w=[r.get('primary'),r.get('secondary')];weekly=next((x for x in w if x and x.get('windowDurationMins')==10080),None)
  if not weekly:raise RuntimeError('weekly quota not available')
  result={'at':stamp(),'used_percent':weekly['usedPercent'],'resets_at':weekly['resetsAt'],'credits':(r.get('credits') or {}).get('balance'),'quota_reached':bool(r.get('rateLimitReachedType')),'allowed':d.get('ordinaryUsageAllowed')};append('quota.jsonl',result);return result
 def close(self):
  self.p.terminate()
  try:self.p.wait(timeout=5)
  except subprocess.TimeoutExpired:self.p.kill();self.p.wait()

def run_batch(batch,home,work,number):
 schema=work/'schema.json';schema.write_text(json.dumps(SCHEMA));payload=[{k:x[k] for k in ('id','title','text','topic','evidence')} for x in batch]
 command=[sys.executable,str(pathlib.Path(__file__).with_name('infer_appserver.py'))];start=time.monotonic();log=OUT/('batch-%04d-events.jsonl'%number);err=OUT/('batch-%04d-stderr.txt'%number)
 with log.open('w') as stdout,err.open('w') as stderr:
  p=subprocess.Popen(command,stdin=subprocess.PIPE,stdout=stdout,stderr=stderr,env=safeenv(home),text=True,start_new_session=True)
  timed_out=False
  try:p.communicate(json.dumps(payload,ensure_ascii=False),timeout=420)
  except subprocess.TimeoutExpired:
   timed_out=True;os.killpg(p.pid,signal.SIGTERM)
   try:p.wait(timeout=5)
   except subprocess.TimeoutExpired:os.killpg(p.pid,signal.SIGKILL);p.wait()
 events=[]
 for line in log.read_text().splitlines():
  try:events.append(json.loads(line))
  except ValueError:pass
 usages=[e.get('usage',{}) for e in events if e.get('type')=='turn.completed'];completed=bool(usages) and p.returncode==0 and not timed_out
 rerouted=any('model rerouted:' in json.dumps(e).lower() for e in events)
 tools=any(e.get('type') in ('item.started','item.completed') and e.get('item',{}).get('type') not in ('agent_message','reasoning','error') for e in events)
 finals=[e['item']['text'] for e in events if e.get('type')=='item.completed' and e.get('item',{}).get('type')=='agent_message'];items=[]
 if completed and finals and not rerouted and not tools:
  try:
   values=json.loads(finals[-1])['items'];wanted={x['id']:x for x in batch};seen=set()
   for v in values:
    ident=v.get('id')
    if ident not in wanted or ident in seen:continue
    seen.add(ident)
    try:
     validate_longform(v)
    except ValueError:
     continue
    han=lambda s:sum('\u3400'<=ch<='\u9fff' for ch in s)
    if not 2<=len(v['title_zh'])<=140 or han(v['title_zh'])<2 or not 15<=len(v['summary'])<=200 or han(v['summary'])<10 or (v['usable'] and han(v['body'])<150):continue
    if type(v.get('usable')) is not bool or not isinstance(v.get('tags'),list) or not 1<=len(v['tags'])<=10 or any(not isinstance(x,str) or not 1<=len(x)<=24 for x in v['tags']):continue
    original=wanted[ident];v.update(source_url=original['source_url'],source_name=original['source_name'],source_date=original['source_date'],topic=original['topic'],evidence=original['evidence'],source_hash=original['content_hash'],model=MODEL,reasoning='low',review_status='pending',batch=number);items.append(v)
  except (ValueError,KeyError,TypeError):pass
 record={'at':stamp(),'batch':number,'requested':len(batch),'returned_valid':len(items),'usable':sum(x['usable'] for x in items),'seconds':round(time.monotonic()-start,2),'exit_code':p.returncode,'timeout':timed_out,'rerouted':rerouted,'tools_used':tools,'usage':usages,'ids':[x['id'] for x in batch]}
 return record,items

def main():
 requirements=json.loads((OUT/'requirements.json').read_text())
 if requirements.get('status')!='approved_by_user':
  print(json.dumps({'status':'awaiting_user_sample_approval','model_calls':0}));return 3
 done={}
 if (OUT/'results.jsonl').exists():
  for line in (OUT/'results.jsonl').read_text().splitlines():
   x=json.loads(line);done[x['id']]=x
 pending=[x for x in SAMPLES if x['id'] not in done];attempts=0;max_done=int(os.environ.get('INFOGAP_MAX_DONE','1000'));batch_size=int(os.environ.get('INFOGAP_BATCH_SIZE','10'));workers=int(os.environ.get('INFOGAP_WORKERS','2'))
 number=len(list(OUT.glob('batch-*-events.jsonl')))+1
 with tempfile.TemporaryDirectory(prefix='infogap-official-1000-') as tmp:
  base=pathlib.Path(tmp);auth=(pathlib.Path.home()/'.codex/auth.json').read_bytes();homes=[]
  for n in range(workers+1):
   p=base/('home'+str(n));p.mkdir(mode=0o700);(p/'auth.json').write_bytes(auth);os.chmod(p/'auth.json',0o600);homes.append(p)
  work=OUT/'empty-work';work.mkdir(exist_ok=True);monitor=Quota(homes[-1]);active={};reason='completed';failures=0
  try:
   initial=monitor.read();reset=initial['resets_at'];credit=initial['credits'];guard=BudgetGuard(dict(initial,used_percent=ANCHOR))
   if initial['used_percent']<ANCHOR:raise RuntimeError('quota baseline or reset changed')
   print(json.dumps({'start':initial,'already_completed':len(done),'pending':len(pending),'target':max_done,'stop_at':STOP_AT}),flush=True)
   with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
    free=list(range(workers));stop=False
    while (pending and sum(x['usable'] for x in done.values())<max_done and not stop) or active:
     while pending and free and sum(x['usable'] for x in done.values())+sum(len(v[1]) for v in active.values())<max_done and not stop:
      try:q=monitor.read()
      except Exception:reason='quota_unavailable';stop=True;break
      if q['resets_at']!=reset and abs(q['resets_at']-reset)>5:reason='quota_reset';stop=True;break
      may_dispatch,guard_reason=guard.check(q)
      calibration_stop=int(os.environ.get('INFOGAP_CALIBRATION_STOP_PERCENT','101'))
      if q['used_percent']>=calibration_stop:reason='calibration_tick_reached';stop=True;break
      if not may_dispatch:reason=guard_reason;stop=True;break
      if credit is not None and q['credits'] is not None and decimal.Decimal(q['credits'])<decimal.Decimal(credit):reason='credit_balance_changed';stop=True;break
      n=free.pop(0);take=min(batch_size,max_done-sum(x['usable'] for x in done.values())-sum(len(v[1]) for v in active.values()));batch=pending[:take];pending=pending[take:];f=pool.submit(run_batch,batch,homes[n],work,number);active[f]=(n,batch);number+=1;attempts+=1
     if not active:break
     ready,_=concurrent.futures.wait(active,timeout=30,return_when=concurrent.futures.FIRST_COMPLETED)
     if not ready:
      print(json.dumps({'heartbeat':stamp(),'completed':len(done),'in_flight':len(active),'pending':len(pending)}),flush=True);continue
     for f in ready:
      n,batch=active.pop(f);free.append(n)
      try:record,items=f.result()
      except Exception as e:record={'at':stamp(),'batch_error':type(e).__name__,'requested':len(batch),'returned_valid':0,'ids':[x['id'] for x in batch]};items=[]
      append('batches.jsonl',record)
      for item in items:done[item['id']]=item;append('results.jsonl',item)
      failures=failures+1 if not items else 0
      q=monitor.read();record.update(total_completed=len(done),total_usable=sum(x['usable'] for x in done.values()),quota=q);print(json.dumps(record,ensure_ascii=False),flush=True)
      state={'at':stamp(),'completed':len(done),'usable':sum(x['usable'] for x in done.values()),'target':max_done,'quota':q,'pending':len(SAMPLES)-len(done),'last_batch':record};(OUT/'state.json').write_text(json.dumps(state,ensure_ascii=False,indent=2))
      if failures>=3 or record.get('rerouted') or record.get('tools_used'):reason='provider_failure';stop=True
      # Missing outputs are retained as failed queue items; no blind retries.
    if sum(x['usable'] for x in done.values())<max_done and not pending and reason=='completed':reason='failed_items_pending'
   final=monitor.read();summary={'at':stamp(),'reason':reason,'completed':len(done),'usable':sum(x['usable'] for x in done.values()),'candidate_count':len(SAMPLES),'anchor_used_percent':ANCHOR,'final_quota':final,'model':MODEL,'reasoning':'low','input_chars':sum(len(x['text']) for x in SAMPLES),'body_chars':sum(len(x['body']) for x in done.values()),'note':'Official same-account quota; no API key or production writes. Integer quota readings are not exact fractional cost.'};(OUT/'summary.json').write_text(json.dumps(summary,ensure_ascii=False,indent=2));print(json.dumps(summary,ensure_ascii=False),flush=True)
  finally:monitor.close()
if __name__=='__main__':raise SystemExit(main() or 0)
