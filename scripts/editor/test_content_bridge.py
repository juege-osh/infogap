import contextlib,hashlib,importlib.util,io,json,pathlib,sqlite3,tempfile,unittest
spec=importlib.util.spec_from_file_location('bridge',pathlib.Path(__file__).parents[1]/'content-bridge.py');b=importlib.util.module_from_spec(spec);spec.loader.exec_module(b)
class BridgeTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();b.DB=self.tmp.name+'/test.sqlite';c=sqlite3.connect(b.DB)
  c.executescript("CREATE TABLE gaps(id INTEGER PRIMARY KEY,title,summary,body,category,level DEFAULT 'ordinary',evidence,source_name,source_url UNIQUE,published_at DEFAULT '',released_at DEFAULT '',created_at,updated_at,status DEFAULT 'pending',tags,author_id,review_note DEFAULT '',source_visibility DEFAULT 'public');CREATE TABLE summary_jobs(gap_id,state,error,updated_at);CREATE TABLE audit(action,object_id,note,created_at);CREATE TABLE billing_orders(id,status);INSERT INTO billing_orders VALUES(1,'paid');")
  c.execute("INSERT INTO gaps(id,title,summary,body,category,status,updated_at) VALUES(1,'原题','原摘要',?,'人工智能','published','old')",('原正文',));c.commit();c.close()
 def tearDown(self):self.tmp.cleanup()
 def call(self,r):
  import unittest.mock
  o=io.StringIO()
  with unittest.mock.patch('sys.stdin',io.StringIO(json.dumps(r))),contextlib.redirect_stdout(o):b.main()
  return json.loads(o.getvalue())
 def patch(self):return {'id':1,'title':'中文标题','summary':'这是足够长且清楚的中文摘要，用来解释材料的具体内容。','body':'具体事实与适用方法。'*40,'expected_updated_at':'old','expected_body_hash':hashlib.sha256('原正文'.encode()).hexdigest(),'review_note':'按已核验材料整理，保留原状态。'}
 def test_patch_preserves_status_and_billing(self):
  self.assertEqual(self.call({'action':'apply','items':[self.patch()]})['updated'],[1]);c=sqlite3.connect(b.DB);self.assertEqual(c.execute('SELECT status FROM gaps').fetchone()[0],'published');self.assertEqual(c.execute('SELECT status FROM billing_orders').fetchone()[0],'paid');c.close()
 def test_concurrent_edit_not_overwritten(self):
  p=self.patch();p['expected_body_hash']='wrong';self.assertEqual(self.call({'action':'apply','items':[p]})['conflicts'],[1])
 def test_short_body_rejected(self):
  p=self.patch();p['body']='太短'
  with self.assertRaises(AssertionError):self.call({'action':'apply','items':[p]})
 def test_import_never_publishes(self):
  x=self.patch();x.update(category='人工智能',evidence='preprint',source_name='arXiv',source_url='https://arxiv.org/abs/test',tags=['测试'])
  self.assertEqual(self.call({'action':'import','items':[x]})['status'],'pending')
 def test_non_pending_never_republished(self):
  p=self.patch();p['review_note']='已逐条核对原始证据、数字和条件，确认适合公开发布。';self.assertEqual(self.call({'action':'publish_reviewed','items':[p]})['conflicts'],[1])
 def test_unknown_operation_rejected(self):
  with self.assertRaises(ValueError):self.call({'action':'shell','command':'whoami'})
if __name__=='__main__':unittest.main()
