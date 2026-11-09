"""本地审核还原端到端测试专用假模型；只监听127.0.0.1，不连接外部服务。"""
import argparse,json,re,threading,time
from http.server import BaseHTTPRequestHandler,ThreadingHTTPServer
lock=threading.Lock(); state={'generation_calls':0,'check_calls':0,'questions':0,'check_failures':0,'reject_next':False,'generation_delay':0,'active_generation':0}
class Handler(BaseHTTPRequestHandler):
 def log_message(self,*args):pass
 def send(self,data,status=200):
  raw=json.dumps(data,ensure_ascii=False).encode();self.send_response(status);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
 def do_GET(self):self.send(state)
 def do_POST(self):
  body=json.loads(self.rfile.read(int(self.headers.get('Content-Length','0'))))
  if self.path=='/control':
   with lock:state.update(body)
   self.send(state);return
  messages=body.get('messages',[]);system=messages[0].get('content','') if messages else '';prompt=messages[-1].get('content','') if messages else ''
  if '质检专家' in system:
   with lock:
    state['check_calls']+=1;fail=state['check_failures']>0
    if fail:state['check_failures']-=1
   if fail:self.send({'error':{'message':'controlled temporary model fault'}},500);return
   reject='99岁' in prompt
   content={'verdict':'reject' if reject else 'pass','scores':{'scientific':45 if reject else 95,'logic':95,'a2_fit':95,'answer':95},'issues':[{'severity':'error','field':'clinical_stem','message':'受控质量淘汰对照'}] if reject else [],'suggestion':'受控淘汰' if reject else '质量良好'}
  else:
   match=re.search(r'数量[：:]\s*(\d+)道',prompt);count=int(match.group(1)) if match else 1
   with lock:
    start=state['questions'];state['questions']+=count;state['generation_calls']+=1;reject=state['reject_next'];state['reject_next']=False;delay=state['generation_delay'];state['active_generation']+=1
   if delay:time.sleep(delay)
   with lock:state['active_generation']-=1
   content=[]
   for i in range(count):
    age=99 if reject else 40+(start+i)%40
    content.append({'clinical_stem':f'患者，男，{age}岁。持续胸痛{start+i+1}小时，伴大汗。既往高血压病史5年。BP 110/70mmHg，心电图示相邻导联ST段抬高，肌钙蛋白升高。该患者最可能的诊断是','options':[{'label':label,'text':text} for label,text in zip('ABCDE',['急性心肌梗死','急性心包炎','肺栓塞','主动脉夹层','自发性气胸'])],'answer':'A','explanation':'正确答案为A。患者持续胸痛伴大汗，心电图和肌钙蛋白结果支持急性心肌梗死。B项常有体位相关疼痛；C项常以呼吸困难为主；D项常有撕裂样胸痛和双侧血压差；E项常有患侧呼吸音减弱。故选A。','difficulty':'0.65','cognitive_level':'应用','exam_points':'诊断与鉴别诊断，临床表现，辅助检查'})
  self.send({'id':'local-fixture','choices':[{'message':{'role':'assistant','content':json.dumps(content,ensure_ascii=False)},'finish_reason':'stop'}]})
parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--port',type=int,default=18123);args=parser.parse_args()
ThreadingHTTPServer(('127.0.0.1',args.port),Handler).serve_forever()
