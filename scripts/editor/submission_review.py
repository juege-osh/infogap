#!/usr/bin/env python3
"""Review user submissions locally with official Codex. No external paid API.
--apply explicitly applies decisions via a separate constrained SSH identity.
Unknown/suspicious sources stay pending and notify the owner. Model output is
never trusted until structure, evidence quotes and the original snapshot pass.
"""
import argparse,datetime,fcntl,hashlib,importlib.util,json,os,pathlib,re,signal,subprocess,sys,tempfile
from budget import BudgetGuard
from submission_contract import validate
from submission_sources import validate_links,fetch_source,UnsafeSource
HERE=pathlib.Path(__file__).resolve().parent;ROOT=HERE.parents[1];CST=datetime.timezone(datetime.timedelta(hours=8))
SSH=['ssh','-i',str(pathlib.Path.home()/'.ssh/infogap_submission_reviewer'),'-o','BatchMode=yes','-o','ConnectTimeout=15','-p','16328','infogap-reviewer@43.242.200.72']

def bridge(payload):
 p=subprocess.run(SSH,input=json.dumps(payload,ensure_ascii=False),capture_output=True,text=True,timeout=100)
 if p.returncode:raise RuntimeError('submission_bridge_unavailable')
 r=json.loads(p.stdout)
 if not isinstance(r,dict) or r.get('error'):raise RuntimeError('submission_bridge_rejected')
 return r

def append(path,row):
 with path.open('a') as f:f.write(json.dumps(row,ensure_ascii=False)+'\n');f.flush();os.fsync(f.fileno())
def stamp():return datetime.datetime.now(datetime.timezone.utc).isoformat()
def request(row,decision,note,level='ordinary',source_hash='',checks=None):
 return {'action':'review','id':row['id'],'expected_updated_at':row['updated_at'],'expected_submission_hash':row['submission_hash'],'decision':decision,'level':level,'note':note,'source_hash':source_hash,'checks':checks or []}
def body_length(text):
 text=re.sub(r'\[([^\]]+)\]\([^)]*\)',r'\1',text);text=re.sub(r'https?://\S+','',text);return len(re.sub(r'\s|[#*_`>~-]','',text))
def prepare(row):
 # Unclear formatting or missing evidence is held rather than invented/fixed.
 if row.get('owner_submission'):return None,'站长本人提交的稿件需要另行确认，系统不自动审批自己的投稿。'
 if not 300<=body_length(row['body'])<=1000:return None,'正文应为300～1000字，当前稿件需要先补充或精简后再审核。'
 if row.get('level') in ('rare','exclusive'):return None,'这条投稿申请稀有或独家等级，需要站长核对价值、独家依据与奖励档位。'
 if len(row.get('tags',[]))>10:return None,'标签超过10个，需要站长确认并调整。'
 if not re.search('[\u4e00-\u9fff]',row['title']) or not re.search('[\u4e00-\u9fff]',row['summary']):return None,'标题和摘要缺少中文说明，需要先补齐。'
 try:
  validate_links(row);source=fetch_source(row['source_url'])
 except UnsafeSource as e:return None,str(e)+'，原稿已保留，等待站长确认。'
 except (OSError,ValueError,TimeoutError):return None,'原始来源当前无法安全读取，暂时不能独立核对事实，等待站长确认。'
 if source['truncated']:return None,'原始材料过长，本次无法完整核对，需要站长确认关键出处。'
 source['source_hash']=hashlib.sha256(source['text'].encode()).hexdigest();return source,None

def main():
 ap=argparse.ArgumentParser();ap.add_argument('--limit',type=int,default=30);ap.add_argument('--apply',action='store_true');a=ap.parse_args()
 if not 1<=a.limit<=100:ap.error('limit must be 1..100')
 day=datetime.datetime.now(CST).date().isoformat();state=ROOT/'state/official-editor';state.mkdir(parents=True,exist_ok=True);out=ROOT/'state/submission-review'/day;out.mkdir(parents=True,exist_ok=True);os.chmod(out,0o700)
 locks=[]
 try:
  for name in ('editor.lock','review.lock','submission.lock'):
   f=(state/name).open('a');locks.append(f)
   try:fcntl.flock(f,fcntl.LOCK_EX|fcntl.LOCK_NB)
   except BlockingIOError:print(json.dumps({'skipped':'official_content_queue_busy'}));return 0
  # Never create/reset a baseline here. Existing daily ledger owns the budget.
  baseline_file=state/(day+'.json')
  if not baseline_file.exists():print(json.dumps({'stopped':'daily_baseline_unavailable'}));return 2
  guard=BudgetGuard.from_record(json.loads(baseline_file.read_text()),day=day)
  auth=pathlib.Path.home()/'.codex/auth.json'
  auth_data=json.loads(auth.read_text())
  if auth_data.get('auth_mode')!='chatgpt' or not auth_data.get('tokens'):print(json.dumps({'stopped':'official_account_unavailable'}));return 2
  exported=bridge({'action':'export'});pending=exported['items'][:a.limit]
  if not pending:print(json.dumps({'reviewed':0,'message':'no_new_user_submissions','notifications_pending':exported.get('notifications_pending',0)}));return 0
  results=[];prepared=[];sources=[]
  materials=out/'source-material';materials.mkdir(exist_ok=True);os.chmod(materials,0o700)
  for row in pending:
   source,reason=prepare(row)
   snapshot=materials/('%d-%s.json'%(row['id'],row['submission_hash']))
   if not snapshot.exists():
    snapshot.write_text(json.dumps({'submission':row,'source':source,'hold_reason':reason,'captured_at':stamp()},ensure_ascii=False));os.chmod(snapshot,0o600)
   if reason:results.append(request(row,'hold',reason));continue
   prepared.append((row,source));sources.append({'id':row['id'],'source_name':row['source_name'],'text':source['text']})
  (out/'eligible-source-corpus.json').write_text(json.dumps(sources,ensure_ascii=False));os.environ['INFOGAP_RUN_DIR']=str(out)
  spec=importlib.util.spec_from_file_location('submission_engine',HERE/'runner.py');engine=importlib.util.module_from_spec(spec);spec.loader.exec_module(engine)
  report={'at':stamp(),'notifications_pending':exported.get('notifications_pending',0),'pending':len(pending),'reviewed':0,'approved':0,'held':0,'rejected':0,'applied':0,'notifications_failed':0,'model':'gpt-6.1-sol','reasoning':'low','dry_run':not a.apply}
  with tempfile.TemporaryDirectory(prefix='infogap-submission-review-') as temp:
   home=pathlib.Path(temp);(home/'auth.json').write_bytes(auth.read_bytes());os.chmod(home/'auth.json',0o600);quota=engine.Quota(home)
   try:
    before=quota.read();report['quota_before']=before;ok,reason=guard.check(before)
    if not ok:report['stopped']=reason;prepared=[];results=[]
    for row,source in prepared:
     q=quota.read();ok,reason=guard.check(q)
     if not ok:report['stopped']=reason;break
     payload=[{'id':row['id'],'source_text':source['text'],'source_url':source['url'],'source_date':row['published_at'],'draft_title':row['title'],'draft_summary':row['summary'],'draft_body':row['body'],'topic':row['category'],'evidence':row['evidence'],'tags':row['tags']}]
     log=out/('%d-%s-events.jsonl'%(row['id'],row['submission_hash'][:12]));env=engine.safeenv(home);env['INFOGAP_SUBMISSION_REVIEW_MODE']='1'
     with log.open('w') as f:
      p=subprocess.Popen([sys.executable,str(HERE/'infer_appserver.py')],stdin=subprocess.PIPE,stdout=f,stderr=subprocess.DEVNULL,text=True,env=env,start_new_session=True)
      try:p.communicate(json.dumps(payload,ensure_ascii=False),timeout=420)
      except subprocess.TimeoutExpired:os.killpg(p.pid,signal.SIGTERM);p.wait(timeout=10)
     try:
      events=[json.loads(l) for l in log.read_text().splitlines()];final=[e['item']['text'] for e in events if e.get('item',{}).get('type')=='agent_message'];usage=[e['usage'] for e in events if e.get('type')=='turn.completed']
      if p.returncode or not final or not usage:raise ValueError('incomplete_review')
      values=json.loads(final[-1])['items'];assert len(values)==1 and values[0]['id']==row['id'];v=validate(values[0],source['text'])
      results.append(request(row,v['decision'],v['note'],v['level'],source['source_hash'],v['checks']));report['reviewed']+=1
     except (AssertionError,KeyError,TypeError,ValueError):
      # A model failure does not establish a content fault. Leave it pending.
      report['stopped']='review_output_unusable';break
    report['quota_after']=quota.read()
   finally:quota.close()
  # Apply snapshot-checked decisions only; a concurrent edit remains pending.
  for result in results:
   action=result['decision'];report[{'approve':'approved','hold':'held','reject':'rejected'}[action]]+=1
   record={'at':stamp(),'request':result,'dry_run':not a.apply}
   if a.apply:
    try:
     receipt=bridge(result);record['receipt']=receipt;report['applied']+=bool(receipt.get('applied'));report['notifications_failed']+=receipt.get('notification')=='failed'
    except (RuntimeError,ValueError,subprocess.TimeoutExpired):record['error']='apply_failed';report['stopped']='submission_apply_failed';append(out/'decisions.jsonl',record);break
   append(out/'decisions.jsonl',record)
  (out/'summary.json').write_text(json.dumps(report,ensure_ascii=False,indent=2));print(json.dumps(report,ensure_ascii=False));return 2 if report.get('stopped') else 0
 finally:
  for lock in reversed(locks):lock.close()
if __name__=='__main__':raise SystemExit(main())
