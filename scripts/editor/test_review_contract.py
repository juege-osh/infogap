import unittest
from review_contract import validate
class ReviewContractTests(unittest.TestCase):
 def item(self):return {'id':1,'decision':'approve','supported':True,'readable':True,'valuable':True,'checks':[{'claim':'第一项事实对应来源','quote':'The reported result improves accuracy.'},{'claim':'第二项限制对应来源','quote':'This remains a preprint study.'}],'issues':[],'note':'已核对来源中的主要结果和预印本限制，建议与研究结论分开。'}
 def test_quotes_must_be_in_original(self):
  r=self.item()
  with self.assertRaises(AssertionError):validate(r,'An unrelated source without the claimed evidence.')
 def test_issues_prevent_approval(self):
  r=self.item();r['issues']=['数字无依据']
  with self.assertRaises(AssertionError):validate(r,'The reported result improves accuracy. This remains a preprint study.')
 def test_independent_support_required(self):
  r=self.item();r['supported']=False
  with self.assertRaises(AssertionError):validate(r,'The reported result improves accuracy. This remains a preprint study.')
if __name__=='__main__':unittest.main()
