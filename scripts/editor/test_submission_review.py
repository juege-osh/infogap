import unittest
from unittest.mock import patch
from submission_sources import checked_url,validate_links,UnsafeSource,PinnedHTTPS
from submission_contract import validate
class SubmissionSecurity(unittest.TestCase):
 def test_unsafe_urls_are_held(self):
  for value in ['http://nature.com/a','https://127.0.0.1/a','https://169.254.169.254/latest','https://nature.com.evil.test/a','https://evilnature.com/a','https://nature.com@evil.test/a','https://nature.com:444/a','https://nature.com/a?url=http%3A%2F%2F127.0.0.1','https://bit.ly/a','https://nature.com/a#token','https://nature.com\\@evil.test/a','https://github.com/login','https://www.nature.com/download/a','https://www.nature.com/a.exe','https://github.com/a?access_token=secret']:
   with self.subTest(value=value),self.assertRaises(UnsafeSource):checked_url(value)
 def test_primary_domains_are_boundary_matched(self):
  for value in ['https://www.nature.com/articles/id','https://www.gov.cn/zhengce/a','https://github.com/owner/repo','https://www.nasa.gov/a']:self.assertTrue(checked_url(value))
 def test_hidden_markdown_link_is_checked(self):
  with self.assertRaises(UnsafeSource):validate_links({'source_url':'https://www.nature.com/a','body':'请看[材料](https://evil.test/a)','summary':'正常摘要'})
 def test_obfuscated_or_nonhttps_markdown_is_held(self):
  for text in ['[下载](javascript:alert(1))','[跳转](java&#x73;cript:alert(1))','[资料](/relative/path)','<a href="https://evil.test">跳转</a>','![图片](https://www.nature.com/a)']:
   with self.subTest(text=text),self.assertRaises(UnsafeSource):validate_links({'source_url':'https://www.nature.com/a','body':text})
 def test_mixed_dns_answers_fail_closed(self):
  with patch('submission_sources.socket.getaddrinfo',return_value=[(2,1,6,'',('1.1.1.1',443)),(2,1,6,'',('127.0.0.1',443))]),self.assertRaises(UnsafeSource):PinnedHTTPS('www.nature.com',timeout=1).connect()
 def test_approval_needs_verbatim_source_checks(self):
  item={'id':1,'decision':'approve','level':'ordinary','supported':True,'readable':True,'valuable':True,'checks':[{'claim':'说明结果的具体条件','quote':'these experimental conditions apply here'},{'claim':'说明结果适用的范围','quote':'the results do not apply to other systems'}],'issues':[],'note':'已核对实验条件、主要结果和适用范围，没有发现待解决的问题。'}
  source='these experimental conditions apply here\nthe results do not apply to other systems';validate(item,source)
  with self.assertRaises(AssertionError):validate(item,'unrelated source')
if __name__=='__main__':unittest.main()
