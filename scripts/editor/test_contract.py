import copy,unittest
import contract
class ContractTests(unittest.TestCase):
 def item(self,count=500):
  return {'id':1,'title_zh':'中文研究解读标题','summary':'研究团队报告了新的实验结果，具体适用条件仍需要核对。','body':'研究方法和实验条件需要独立核验。'*(count//17+1),'usable':True,'missing_info':'','tags':['科研进展']}
 def test_short_body_cannot_be_padded_by_summary_or_links(self):
  x=self.item();x['body']='正文资料仍然不足。'*20
  x['summary']='研究团队报告了新的实验结果。'*10
  with self.assertRaises(ValueError):contract.validate(x)
  self.assertEqual(contract.body_count('[来源](https://example.org/'+'x'*500+')'),2)
 def test_boundaries_and_chinese(self):
  for size,ok in [(299,False),(300,True),(1000,True),(1001,False)]:
   x=self.item();x['body']='研'*size
   if ok:contract.validate(x)
   else:
    with self.assertRaises(ValueError):contract.validate(x)
  x=self.item();x['body']='English '*80
  with self.assertRaises(ValueError):contract.validate(x)
 def test_insufficient_material_needs_a_reason_without_filler(self):
  x=self.item();x.update(usable=False,body='',missing_info='只有标题，需要补充原始摘要和实验条件。');contract.validate(x)
  x['body']='资料不足，因此不能判断。'*40
  with self.assertRaises(ValueError):contract.validate(x)
 def test_markup_does_not_count_as_reading_content(self):
  self.assertEqual(contract.body_count('## 标题\n\n**中文** [来源](https://example.org/)'),6)
if __name__=='__main__':unittest.main()
