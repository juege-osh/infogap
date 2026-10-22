"""Conservative primary-source fetcher. Unknown hosts/unsafe links are held.
DNS answers must all be public; TLS connects to the validated address with the
original hostname as SNI. Redirects repeat the exact validation. No proxy/cookie.
"""
import html,http.client,ipaddress,re,socket,ssl,urllib.parse
from html.parser import HTMLParser
PRIMARY_DOMAINS=('nature.com','science.org','arxiv.org','github.com','raw.githubusercontent.com','nasa.gov','esa.int','who.int','un.org','fao.org','worldbank.org','oecd.org','gov.cn','edu.cn','cas.cn','nsfc.gov.cn','nih.gov','noaa.gov','usgs.gov','energy.gov','europa.eu','gov.uk','go.jp','ac.jp','doi.org')
REDIRECT_KEYS={'url','redirect','redirect_uri','redirect_url','next','target','dest','destination','callback','return','return_to','continue','link','u'}
URL_RE=re.compile(r'https?://[^\s<>\[\]"\u3000]+',re.I)
class UnsafeSource(ValueError):pass

def checked_url(value):
 if not isinstance(value,str) or len(value)>2000 or any(ord(c)<33 for c in value) or '\\' in value:raise UnsafeSource('来源链接格式异常')
 try:u=urllib.parse.urlsplit(value);host=(u.hostname or '').lower();port=u.port
 except ValueError:raise UnsafeSource('来源链接无法解析')
 if u.scheme!='https' or u.username or u.password or port not in (None,443) or not host or not host.isascii() or host.endswith('.'):raise UnsafeSource('仅自动读取无账号参数的标准HTTPS公开来源')
 try:ipaddress.ip_address(host)
 except ValueError:pass
 else:raise UnsafeSource('IP地址链接需要站长确认')
 if not re.fullmatch(r'[a-z0-9.-]+',host) or not any(host==d or host.endswith('.'+d) for d in PRIMARY_DOMAINS):raise UnsafeSource('该来源尚未进入自动核验范围，需要站长确认')
 query=urllib.parse.parse_qs(u.query,keep_blank_values=True)
 if any(k.lower() in REDIRECT_KEYS for k in query) or any('://' in urllib.parse.unquote(v) for vs in query.values() for v in vs):raise UnsafeSource('链接含跳转参数，需要站长确认')
 if u.fragment:raise UnsafeSource('带片段定位或客户端路由的链接需要站长确认')
 path=urllib.parse.unquote(u.path).lower()
 if re.search(r'/(?:login|signin|sign-in|oauth|authorize|auth|download|downloads)(?:/|$)',path) or re.search(r'\.(?:exe|msi|dmg|pkg|app|bat|cmd|ps1|sh|apk|ipa|zip|rar|7z|tar|gz)(?:$|/)',path):raise UnsafeSource('链接涉及登录引流或可执行下载，需要站长确认')
 if any(k.lower() in ('token','access_token','password','passwd','secret','api_key','key') for k in query):raise UnsafeSource('链接含认证或敏感参数，需要站长确认')
 return u,host

def validate_links(submission):
 urls=[submission['source_url']]
 for field in ('title','summary','body'):
  text=html.unescape(urllib.parse.unquote(submission.get(field,'')))
  if re.search(r'(?:javascript|vbscript|data|file|ftp|tg|intent):',text,re.I) or re.search(r'<\s*(?:a|iframe|script|img|object|embed)\b',text,re.I) or '![' in text:raise UnsafeSource('正文包含需进一步确认的网页、跳转或嵌入内容')
  urls.extend(x.rstrip(').,;，。；！!') for x in URL_RE.findall(text))
  # Markdown targets without HTTPS (including relative/obfuscated URLs) are
  # not silently skipped. Full Markdown syntax is deliberately not executed.
  for target in re.findall(r'\]\(([^)]+)\)',text):
   if not target.strip().startswith('https://'):raise UnsafeSource('正文链接不是明确的HTTPS公开来源')
 if len(set(urls))>20:raise UnsafeSource('链接数量过多，需要站长逐项确认')
 for url in set(urls):checked_url(url)
 return urls

class PinnedHTTPS(http.client.HTTPSConnection):
 def connect(self):
  answers=socket.getaddrinfo(self.host,self.port,type=socket.SOCK_STREAM)
  if not answers or any(not ipaddress.ip_address(a[4][0]).is_global for a in answers):raise UnsafeSource('来源域名解析到非公开地址')
  last=None
  for family,kind,proto,_,address in answers:
   sock=socket.socket(family,kind,proto);sock.settimeout(self.timeout)
   try:sock.connect(address);self.sock=self._context.wrap_socket(sock,server_hostname=self.host);return
   except OSError as e:last=e;sock.close()
  raise last or UnsafeSource('来源连接失败')
class TextParser(HTMLParser):
 def __init__(self):super().__init__();self.parts=[];self.skip=0
 def handle_starttag(self,tag,attrs):
  if tag in ('script','style','nav','footer','header','noscript','svg'):self.skip+=1
 def handle_endtag(self,tag):
  if tag in ('script','style','nav','footer','header','noscript','svg') and self.skip:self.skip-=1
 def handle_data(self,data):
  if not self.skip and data.strip():self.parts.append(data.strip())

def fetch_source(url,max_bytes=1500000):
 for hop in range(4):
  u,host=checked_url(url);c=PinnedHTTPS(host,timeout=15,context=ssl.create_default_context())
  try:
   c.request('GET',urllib.parse.urlunsplit(('', '',u.path or '/',u.query,'')),headers={'User-Agent':'InfoGapSourceReview/1.0 (+https://infogap.osh.lol)','Accept':'text/html,text/plain,application/json','Accept-Encoding':'identity'})
   r=c.getresponse()
   if r.status in (301,302,303,307,308):
    loc=r.getheader('Location');
    if not loc:raise UnsafeSource('来源跳转未给出目标')
    url=urllib.parse.urljoin(url,loc);continue
   if r.status!=200:raise UnsafeSource('来源当前无法读取（HTTP %s）'%r.status)
   mime=r.getheader('Content-Type','').lower()
   if not any(t in mime for t in ('text/html','text/plain','application/json','application/xhtml')):raise UnsafeSource('来源不是可自动核对的文本网页')
   if r.getheader('Content-Encoding','identity').lower() not in ('','identity'):raise UnsafeSource('来源压缩格式需人工确认')
   raw=r.read(max_bytes+1)
   if len(raw)>max_bytes:raise UnsafeSource('原始材料超过本次安全读取上限')
   m=re.search(r'charset=([a-zA-Z0-9_-]+)',mime);text=raw.decode(m.group(1) if m else 'utf-8',errors='replace')
   if 'html' in mime:p=TextParser();p.feed(text);text='\n'.join(p.parts)
   if len(text)<500:raise UnsafeSource('原始材料过短，无法独立核对')
   return {'text':text[:40000],'url':url,'truncated':len(text)>40000}
  finally:c.close()
 raise UnsafeSource('来源跳转次数过多')
