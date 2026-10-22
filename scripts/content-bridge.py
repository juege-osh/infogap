#!/usr/bin/env python3
"""Narrow SSH content bridge: no shell, user, payment, invite or session operations."""
import datetime,hashlib,json,os,re,sqlite3,sys
DB='/var/lib/infogap-osh/infogap.sqlite'
CATEGORIES=set('前沿科技 人工智能 生命科学 物理 化学 地球与地理 天文与空间 材料科学 开源技术 军事与国防 政治与公共政策 农业与食品 社会民生 法律法规 航空航天 经济与产业 能源与环境 医疗健康 教育与就业 交通与基建 公共安全 生活实用 省钱优惠 招聘求职 职业成长 住房居住 出行旅行 商业创业'.split())
def now():return datetime.datetime.now(datetime.timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
def count(s):
 s=re.split(r'\n## (?:原始来源|内部核验参考)[^\n]*\n',s)[0];s=re.sub(r'\[([^\]]+)\]\([^)]*\)',r'\1',s);s=re.sub(r'https?://\S+','',s);return len(re.sub(r'[\s*`_>#]+','',s))
def validate(x):
 assert type(x.get('id')) is int and x['id']>0
 for k in ('title','summary','body','expected_updated_at','expected_body_hash','review_note'):assert isinstance(x.get(k),str)
 assert 2<=len(x['title'])<=300 and 15<=len(x['summary'])<=300 and 300<=count(x['body'])<=1000
 assert len(re.findall(r'[\u3400-\u9fff]',x['body']))>=150 and not re.search(r'<\s*/?\s*[a-zA-Z]|!\[',x['body'])
 assert len(x['review_note'])<=2000

def main():
 request=json.loads(sys.stdin.read(20*1024*1024));action=request.get('action');db=sqlite3.connect(DB,timeout=30);db.row_factory=sqlite3.Row
 if action=='export':
  out={'gaps':[dict(r) for r in db.execute('SELECT * FROM gaps')],'jobs':[dict(r) for r in db.execute('SELECT * FROM summary_jobs')],'at':now()}
 elif action=='apply':
  rows=request['items'];assert isinstance(rows,list) and len(rows)<=1500
  for x in rows:validate(x)
  changed=[];conflicts=[]
  with db:
   db.execute('BEGIN IMMEDIATE')
   for x in rows:
    old=db.execute('SELECT * FROM gaps WHERE id=?',(x['id'],)).fetchone()
    if not old or old['updated_at']!=x['expected_updated_at'] or hashlib.sha256(old['body'].encode()).hexdigest()!=x['expected_body_hash']:
     conflicts.append(x['id']);continue
    db.execute('UPDATE gaps SET title=?,summary=?,body=?,review_note=?,updated_at=? WHERE id=?',(x['title'],x['summary'],x['body'],x['review_note'],now(),x['id']))
    db.execute("UPDATE summary_jobs SET state='completed',error='',updated_at=? WHERE gap_id=?",(now(),x['id']))
    db.execute("INSERT INTO audit(action,object_id,note,created_at) VALUES('content_standard_refresh',?,?,?)",(str(x['id']),'中文300-1000字规范；保留状态、等级、编号与解锁关系。',now()));changed.append(x['id'])
  out={'updated':changed,'conflicts':conflicts,'at':now()}
 elif action=='needs_source':
  rows=request['items'];assert isinstance(rows,list) and len(rows)<=1000;flagged=[];conflicts=[]
  with db:
   for x in rows:
    assert type(x.get('id')) is int and isinstance(x.get('reason'),str) and 5<=len(x['reason'])<=500
    old=db.execute('SELECT * FROM gaps WHERE id=?',(x['id'],)).fetchone()
    if not old or old['status']!='pending' or old['updated_at']!=x.get('expected_updated_at') or hashlib.sha256(old['body'].encode()).hexdigest()!=x.get('expected_body_hash'):
     conflicts.append(x['id']);continue
    title=x.get('title') or old['title'];summary=x.get('summary') or old['summary'];assert 2<=len(title)<=300 and 10<=len(summary)<=300
    body='' if x.get('clear_mismatched_body') is True else old['body']
    db.execute('UPDATE gaps SET title=?,summary=?,body=?,review_note=?,updated_at=? WHERE id=?',(title,summary,body,'需补充来源材料：'+x['reason'],now(),x['id']))
    db.execute("UPDATE summary_jobs SET state='failed',error=?,updated_at=? WHERE gap_id=?",(x['reason'],now(),x['id']))
    db.execute("INSERT INTO audit(action,object_id,note,created_at) VALUES('source_material_needed',?,?,?)",(str(x['id']),x['reason'],now()));flagged.append(x['id'])
  out={'flagged':flagged,'conflicts':conflicts,'at':now()}
 elif action=='import':
  rows=request['items'];assert isinstance(rows,list) and len(rows)<=1000;added=[]
  for x in rows:
   validate(dict(x,id=1,expected_updated_at='',expected_body_hash=''))
   assert x.get('category') in CATEGORIES and x.get('evidence') in ('preprint','peer_reviewed','official','repository','unverified')
   assert re.match(r'^https://[^/@\s]+/',x.get('source_url','')) and x.get('source_visibility','public') in ('public','internal')
   assert isinstance(x.get('tags'),list) and len(x['tags'])<=10 and all(isinstance(t,str) and len(t)<=40 for t in x['tags'])
  with db:
   for x in rows:
    existing=db.execute('SELECT id FROM gaps WHERE source_url=?',(x['source_url'],)).fetchone()
    if existing:continue
    t=now();cur=db.execute("INSERT INTO gaps(title,summary,body,category,level,evidence,source_name,source_url,published_at,created_at,updated_at,status,tags,review_note,source_visibility) VALUES(?,?,?,?,'ordinary',?,?,?,?,?,?,'pending',?,?,?)",(x['title'],x['summary'],x['body'],x['category'],x['evidence'],x['source_name'],x['source_url'],x.get('published_at',''),t,t,json.dumps(x['tags'],ensure_ascii=False),x['review_note'],x.get('source_visibility','public')));added.append(cur.lastrowid)
  out={'inserted':added,'status':'pending','at':now()}
 elif action=='publish_reviewed':
  rows=request['items'];assert len(rows)<=1000;published=[];conflicts=[]
  with db:
   for x in rows:
    assert type(x['id']) is int and len(x.get('review_note',''))>=20
    g=db.execute('SELECT * FROM gaps WHERE id=?',(x['id'],)).fetchone()
    if not g or g['status']!='pending' or g['author_id'] is not None or g['updated_at']!=x.get('expected_updated_at') or hashlib.sha256(g['body'].encode()).hexdigest()!=x.get('expected_body_hash'):
     conflicts.append(x['id']);continue
    assert 300<=count(g['body'])<=1000
    t=now();db.execute("UPDATE gaps SET status='published',released_at=?,updated_at=?,review_note=review_note || ? WHERE id=?",(t,t,' '+x['review_note'],g['id']))
    db.execute("INSERT INTO audit(action,object_id,note,created_at) VALUES('editorial_publish',?,?,?)",(str(g['id']),x['review_note'],t));published.append(g['id'])
  out={'published':published,'conflicts':conflicts,'at':now()}
 else:raise ValueError('Unsupported content operation')
 print(json.dumps(out,ensure_ascii=False))
if __name__=='__main__':
 try:main()
 except Exception as e:print(json.dumps({'error':type(e).__name__}));sys.exit(1)
