"""复用审核还原验收夹具，检查题库/审核记录/统计/操作日志的浏览器自动同步；不出题。"""
import argparse
import json
import os
from pathlib import Path
import re
import uuid
from urllib.parse import urlparse
from playwright.sync_api import sync_playwright, expect

parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--base-url',required=True)
parser.add_argument('--fixture-json',required=True)
args=parser.parse_args()
assert urlparse(args.base_url).hostname in ('127.0.0.1','localhost')
base=args.base_url.rstrip('/')
fixture=json.loads(Path(args.fixture_json).read_text())
suffix=fixture['suffix'];assert re.fullmatch('[0-9a-f]{8}',suffix)
password=os.environ['AIGO_E2E_ADMIN_PASSWORD']
errors=[]

def auth(page):return {'Authorization':'Bearer '+page.evaluate("localStorage.getItem('aigo_token')")}
def api(page,method,path,body=None):
 r=page.request.fetch(base+'/api'+path,method=method,headers=auth(page),data=body);assert r.status in (200,201),(path,r.status,r.text());return r.json()
def go(page,path):page.goto(base+path,wait_until='networkidle')
def login(browser,name):
 c=browser.new_context(viewport={'width':1440,'height':1000});p=c.new_page();p.on('pageerror',lambda e:errors.append(str(e)));go(p,'/login');p.get_by_placeholder('请输入用户名').fill(name);p.get_by_placeholder('请输入密码').fill(password);p.get_by_role('button',name='登录',exact=True).click();p.wait_for_url('**/my');return p

with sync_playwright() as p:
 browser=p.chromium.launch(headless=True,channel='chrome')
 try:
  root=login(browser,'admin');author=login(browser,'restore_author_'+suffix);reviewer=login(browser,'restore_author_'+suffix)
  switched=api(reviewer,'POST','/auth/switch-role',{'role':'expert'})
  reviewer.evaluate("data=>{localStorage.setItem('aigo_token',data.token);localStorage.setItem('aigo_user',JSON.stringify(data.user));}",switched)
  target=fixture['question_ids'][2];assert target.startswith('q-RESTORE-'+suffix+'-')
  q=api(author,'GET','/questions/'+target)
  task=api(author,'POST','/review/submit',{'question_id':target,'flow_id':'restore-bulk-'+suffix,'expected_version':q['version'],'expected_review_revision':q.get('review_revision',0)})
  keyword=re.search(r'持续胸痛\d+小时',q['clinical_stem']).group()
  go(author,'/bank?scope=personal&tier=working')
  author.locator('.search-input').fill(keyword);author.locator('.search-input').press('Enter')
  expect(author.locator('.question-item')).to_have_count(1)
  author.locator('.question-item .q-content').click()
  expect(author.locator('.question-item .q-status')).to_have_text('审核中')
  expect(author.locator('.review-info-field')).to_contain_text('第 1 次送审')
  go(reviewer,'/review-results');reviewer.get_by_role('button',name='我的题库',exact=True).click()
  reviewer.get_by_placeholder('搜索题干、选项、解析、专业、系统、大纲要点或ID...').fill(keyword)
  reviewer.get_by_placeholder('搜索题干、选项、解析、专业、系统、大纲要点或ID...').press('Enter')
  expect(reviewer.locator('.result-head')).to_have_count(1)
  expect(reviewer.locator('.result-head')).to_contain_text('审核中')
  stats=author.context.new_page();go(stats,'/stats')
  stats_before=api(author,'GET','/stats?scope=personal')
  chart=stats.locator('.chart-card').filter(has=stats.locator('h3').filter(has_text='审核流程现状'))
  reviewing_count=chart.locator('.bar-row').filter(has=stats.get_by_text('审核中',exact=True)).locator('.bar-count')
  expect(reviewing_count).to_have_text(str(stats_before['status_distribution'].get('reviewing',0)))
  audit=root.context.new_page();go(audit,'/audit');audit.get_by_placeholder('按题目ID').fill(target);audit.get_by_placeholder('按题目ID').press('Enter')
  expect(audit.locator('.log-row .action-tag').filter(has_text=re.compile('^送审$'))).to_have_count(1)
  preview=api(root,'POST','/review/restore-preview',{'task_ids':[task['id']]})
  restored=api(root,'POST','/review/restore',{'operation_id':'views-restore-'+uuid.uuid4().hex,'preview_token':preview['token'],'reason':'跨模块页面自动同步验收'})
  assert restored['restored']==1
  expect(author.locator('.question-item .q-status')).to_have_text('AI已检查',timeout=8000)
  expect(author.locator('.review-info-field')).to_have_count(0,timeout=8000)
  expect(reviewer.locator('.result-head')).to_contain_text('未提交审核',timeout=8000)
  expect(reviewing_count).to_have_text(str(stats_before['status_distribution'].get('reviewing',0)-1),timeout=8000)
  expect(audit.locator('.log-row .action-tag').filter(has_text=re.compile('^送审$'))).to_have_count(0,timeout=8000)
  expect(audit.locator('.log-row .action-tag').filter(has_text=re.compile('^审核还原$')).first).to_be_visible(timeout=8000)
  print('PASS | 个人题库状态与旧审核详情、审核记录汇总、数据统计和操作日志均自动收敛',flush=True)
  assert not errors,errors
  print('PASS | 跨模块页面无浏览器JavaScript异常',flush=True)
 finally:
  for context in browser.contexts:context.close()
  browser.close()
