"""Use the official Codex CLI app-server as a text-only editor, with bounded output."""
import json,os,pathlib,queue,signal,subprocess,sys,threading,time
if os.environ.get('INFOGAP_REVIEW_MODE')=='1':
 from review_contract import MODEL,REASONING,SCHEMA,INSTRUCTION
else:
 from contract import MODEL,REASONING,SCHEMA,INSTRUCTION
CLI='/Users/rengang/.local/bin/codex'

def emit(event):print(json.dumps(event,ensure_ascii=False),flush=True)
def main():
 payload=json.loads(sys.stdin.read())
 sources={x['id']:x for x in json.loads((pathlib.Path(os.environ['INFOGAP_RUN_DIR'])/'eligible-source-corpus.json').read_text())}
 for item in payload:
  if item.get('id') in sources:item['source_name']=sources[item['id']]['source_name']
 cmd=[CLI,'--no-daemon','-a','never','app-server','--listen','stdio://']
 configs={'model_provider':'infogap_official','model_providers.infogap_official.name':'OpenAI','model_providers.infogap_official.base_url':'https://chatgpt.com/backend-api/codex','model_providers.infogap_official.wire_api':'responses','model_providers.infogap_official.requires_openai_auth':True,'model_providers.infogap_official.supports_websockets':False,'model_providers.infogap_official.request_max_retries':0,'model_providers.infogap_official.stream_max_retries':0,'web_search':'disabled','mcp_servers':{},'project_doc_max_bytes':0,'features.skip_host_skill_discovery':True}
 for key,value in configs.items():cmd+=['-c',key+'='+json.dumps(value)]
 for feature in ('shell_tool','unified_exec','multi_agent','goals','apps','plugins','hooks','browser_use','browser_use_external','computer_use','image_generation','view_image','skill_search','workspace_dependencies','memories','in_app_browser','code_mode','code_mode_host','daemon_auto_start','unbounded_connection_retries'):cmd+=['--disable',feature]
 events=queue.Queue();process=subprocess.Popen(cmd,stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True,env=os.environ.copy())
 def read():
  try:
   for line in process.stdout:
    try:events.put(json.loads(line))
    except ValueError:events.put({'internal_error':'invalid_event'})
  finally:events.put({'internal_error':'closed'})
 threading.Thread(target=read,daemon=True).start();counter=0;deadline=time.monotonic()+400;usage=None;final=None
 def receive():
  left=deadline-time.monotonic()
  if left<=0:raise TimeoutError()
  value=events.get(timeout=left)
  if 'internal_error' in value:raise RuntimeError('app_server_connection_closed')
  return value
 def rpc(method,params):
  nonlocal counter
  counter+=1;ident=counter;process.stdin.write(json.dumps({'id':ident,'method':method,'params':params},ensure_ascii=False)+'\n');process.stdin.flush()
  while True:
   value=receive()
   if value.get('id')==ident:
    if 'error' in value:
     print(json.dumps({'rpc_method':method,'error':value['error']},ensure_ascii=False)[:500],file=sys.stderr,flush=True)
     raise RuntimeError('app_server_request_rejected')
    return value['result']
 try:
  rpc('initialize',{'clientInfo':{'name':'infogap_longform_benchmark','version':'2.0.0'},'capabilities':{'experimentalApi':True}})
  process.stdin.write('{"method":"initialized","params":{}}\n');process.stdin.flush()
  thread=rpc('thread/start',{'model':MODEL,'modelProvider':'infogap_official','ephemeral':True,'cwd':os.getcwd(),'sandbox':'read-only','approvalPolicy':'never','baseInstructions':INSTRUCTION,'developerInstructions':'只整理提供的资料，禁止使用任何工具。严格区分事实、例子和建议。','config':{'model_reasoning_effort':REASONING}})
  if thread.get('model') not in (None,MODEL):raise RuntimeError('model_rerouted')
  tid=thread['thread']['id'];emit({'type':'thread.started','thread_id':tid})
  rpc('turn/start',{'threadId':tid,'input':[{'type':'text','text':json.dumps(payload,ensure_ascii=False)}],'effort':REASONING,'outputSchema':SCHEMA})
  while True:
   event=receive();method=event.get('method','');params=event.get('params',{})
   if method=='thread/tokenUsage/updated':
    raw=params.get('tokenUsage',{}).get('last') or params.get('tokenUsage',{}).get('total') or {};usage={out:raw.get(key,0) for key,out in [('inputTokens','input_tokens'),('cachedInputTokens','cached_input_tokens'),('outputTokens','output_tokens'),('reasoningOutputTokens','reasoning_output_tokens')]}
   elif method=='item/completed':
    item=params.get('item',{});kind=item.get('type')
    if kind=='agentMessage':final=item.get('text')
    elif kind=='error':
     if 'model rerouted' in json.dumps(item).lower():raise RuntimeError('model_rerouted')
    elif kind not in ('reasoning','userMessage','contextCompaction',None):
     print('unexpected_item_kind='+str(kind),file=sys.stderr,flush=True)
     raise RuntimeError('unexpected_tool_or_item')
   elif method=='error':raise RuntimeError('official_provider_error')
   elif method=='turn/completed':
    turn=params.get('turn',{})
    if turn.get('status')!='completed' or not isinstance(final,str) or not usage:raise RuntimeError('incomplete_turn')
    emit({'type':'item.completed','item':{'type':'agent_message','text':final}});emit({'type':'turn.completed','usage':usage});return 0
 except Exception as error:
  # Fixed categories only; never echo auth material or raw provider responses.
  category=str(error) if str(error) in ('app_server_connection_closed','app_server_request_rejected','model_rerouted','unexpected_tool_or_item','official_provider_error','incomplete_turn') else type(error).__name__
  emit({'type':'turn.failed','message':category});return 1
 finally:
  process.terminate()
  try:process.wait(timeout=5)
  except subprocess.TimeoutExpired:process.kill();process.wait()
if __name__=='__main__':raise SystemExit(main())
