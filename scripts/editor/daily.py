#!/usr/bin/env python3
"""Fetch public feeds without model tools; generate and stage up to 1000 unique drafts/day.
Publishing is a separate, evidence-reviewed operation by the scheduled editor.
"""
import argparse,fcntl,collections,concurrent.futures,datetime,hashlib,importlib.util,json,os,pathlib,subprocess,sys
ROOT=pathlib.Path(__file__).resolve().parents[2];sys.path.insert(0,str(pathlib.Path(__file__).parent))
from contract import validate
SSH=['ssh','-i',str(pathlib.Path.home()/'.ssh/infogap_content_editor'),'-o','BatchMode=yes','-o','ConnectTimeout=15','-p','16328','infogap-content@43.242.200.72']
CST=datetime.timezone(datetime.timedelta(hours=8))
def bridge(payload):
 p=subprocess.run(SSH,input=json.dumps(payload,ensure_ascii=False),capture_output=True,text=True,timeout=60)
 if p.returncode:raise RuntimeError('content_bridge_unavailable')
 r=json.loads(p.stdout)
 if 'error' in r:raise RuntimeError('content_bridge_rejected')
 return r

def collect(out,known):
 spec=importlib.util.spec_from_file_location('feeds',ROOT/'scripts/collect.py');c=importlib.util.module_from_spec(spec);spec.loader.exec_module(c)
 sources=[dict(x,max_age_days=7) for x in json.loads((ROOT/'data/sources.json').read_text()) if x.get('enabled',True) and x['kind'] in ('rss','github')]
 for field,topic in [('cs.AI','人工智能'),('cs.LG','人工智能'),('cs.CL','人工智能'),('cs.CV','人工智能'),('cs.RO','前沿科技'),('cs.SE','开源技术'),('q-bio','生命科学'),('physics','物理'),('physics.chem-ph','化学'),('physics.geo-ph','地球与地理'),('astro-ph','天文与空间'),('cond-mat','材料科学'),('quant-ph','物理'),('econ','经济与产业'),('stat','前沿科技')]:
  sources.append({'id':'daily-arxiv-'+field,'name':'arXiv '+field,'category':topic,'url':'https://rss.arxiv.org/rss/'+field,'kind':'rss','evidence':'preprint','max_age_days':7})
 groups=collections.defaultdict(list)
 for s in sources:groups[c.urllib.parse.urlsplit(s['url']).hostname].append(s)
 def host(items):
  rows=[];logs=[]
  for source in items:
   try:
    delay=c.robot_rules(source['url']) if source['kind']!='github' else 2;raw=c.fetch(source['url'],max(2,delay));(out/'raw'/ (source['id']+'.data')).write_bytes(raw)
    parsed=c.parse_items(raw,source);eligible,_,_=c.recent_items(parsed,source)
    for item in eligible:
     title=c.text_only(item['title']);text=c.text_only(item.get('description',''));url=c.canonical_url(item['url'])
     if url in known or not 350<=len(text)<=16000:continue
     rows.append({'title':title,'text':text,'source_url':url,'source_name':source['name'],'source_date':c.published_date(item,source),'topic':source['category'],'evidence':source.get('evidence','unverified'),'content_hash':hashlib.sha256((title+'\n'+text).encode()).hexdigest()})
    logs.append({'source':source['id'],'ok':True,'eligible':len(eligible)})
   except Exception as e:logs.append({'source':source['id'],'ok':False,'error':type(e).__name__})
  return rows,logs
 rows=[];logs=[]
 with concurrent.futures.ThreadPoolExecutor(max_workers=6) as pool:
  for r,l in pool.map(host,groups.values()):rows+=r;logs+=l
 buckets=collections.defaultdict(list)
 for r in rows:buckets[r['topic']].append(r)
 unique=[];seen=set(known);hashes=set()
 while any(buckets.values()):
  for topic,bucket in buckets.items():
   if not bucket:continue
   r=bucket.pop(0)
   if r['source_url'] in seen or r['content_hash'] in hashes:continue
   seen.add(r['source_url']);hashes.add(r['content_hash']);r['id']=len(unique)+1;unique.append(r)
 (out/'collection.json').write_text(json.dumps({'at':datetime.datetime.now(CST).isoformat(),'unique':len(unique),'sources':logs,'model_calls':0},ensure_ascii=False,indent=2))
 return unique

def main():
 ap=argparse.ArgumentParser();ap.add_argument('--target',type=int,default=84);ap.add_argument('--stage-only',action='store_true');a=ap.parse_args();assert 1<=a.target<=1000
 lockdir=ROOT/'state/official-editor';lockdir.mkdir(parents=True,exist_ok=True)
 with (lockdir/'daily-pipeline.lock').open('a') as lock:
  try:fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
  except BlockingIOError:print('{"skipped":"daily_pipeline_running"}');return 0
  return run(a)
def run(a):
 date=datetime.datetime.now(CST).date().isoformat();out=ROOT/'state/daily-content'/date;out.mkdir(parents=True,exist_ok=True);(out/'raw').mkdir(exist_ok=True)
 live=bridge({'action':'export'});known={g['source_url'] for g in live['gaps']}
 sourcefile=out/'eligible-source-corpus.json'
 if not sourcefile.exists():
  samples=collect(out,known);sourcefile.write_text(json.dumps(samples,ensure_ascii=False));(out/'requirements.json').write_text('{"status":"approved_by_user"}')
 # Reuse only identical source hashes from the same approved writing contract.
 cachefile=ROOT/'evidence/official-1000-20261022/longform-v2/results.jsonl'
 cache={r['source_hash']:r for r in map(json.loads,cachefile.read_text().splitlines()) if r['usable']} if cachefile.exists() else {}
 resultfile=out/'results.jsonl';existing=[json.loads(l) for l in resultfile.read_text().splitlines()] if resultfile.exists() else [];done={r['id'] for r in existing};usable=sum(r['usable'] for r in existing)
 with resultfile.open('a') as saved:
  for source in json.loads(sourcefile.read_text()):
   if usable>=a.target:break
   if source['id'] in done or source['content_hash'] not in cache:continue
   r=dict(cache[source['content_hash']]);r.update(id=source['id'],source_url=source['source_url'],source_name=source['source_name'],source_date=source['source_date'],topic=source['topic'],evidence=source['evidence'],cache_hit=True)
   validate({k:r[k] for k in ('id','title_zh','summary','body','usable','missing_info','tags')});saved.write(json.dumps(r,ensure_ascii=False)+'\n');usable+=1
 if not a.stage_only and usable<a.target:
  run=subprocess.run([sys.executable,str(pathlib.Path(__file__).with_name('run_queue.py')),'--run-dir',str(out),'--target',str(a.target),'--workers','4'])
  if run.returncode not in (0,2):return run.returncode
 rows=[json.loads(l) for l in (out/'results.jsonl').read_text().splitlines()] if (out/'results.jsonl').exists() else []
 items=[]
 for r in rows:
  if not r['usable'] or r['source_url'] in known:continue
  validate({k:r[k] for k in ('id','title_zh','summary','body','usable','missing_info','tags')})
  items.append({'title':r['title_zh'],'summary':r['summary'],'body':r['body'],'category':r['topic'],'evidence':r['evidence'],'source_name':r['source_name'],'source_url':r['source_url'],'published_at':r['source_date'],'source_visibility':'public','tags':r['tags'],'review_note':'官方公开材料中文整理；gpt-6.1-sol / low；正文300-1000字。待核对原始证据与用途后发布，订阅日期不代表首次发表或生效日期。'})
 # Bridge does not publish or reward model-generated candidates.
 result=bridge({'action':'import','items':items[:1000]}) if items else {'inserted':[]}
 report={'date':date,'target':a.target,'usable':sum(r['usable'] for r in rows),'new_staged':len(result['inserted']),'ids':result['inserted'],'status':'pending_editorial_review','run_dir':str(out)};(out/'staging.json').write_text(json.dumps(report,ensure_ascii=False,indent=2));print(json.dumps(report,ensure_ascii=False));return 0
if __name__=='__main__':raise SystemExit(main())
