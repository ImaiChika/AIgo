import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { registerHooks } from 'node:module';
import { parse, compileScript } from '@vue/compiler-sfc';
import { JSDOM } from 'jsdom';

// Compile the real SFCs so watchers, child mounting and rendered messages are tested together.
registerHooks({ load(url, context, nextLoad) {
  if (!url.endsWith('.vue')) return nextLoad(url, context);
  const { descriptor } = parse(readFileSync(fileURLToPath(url), 'utf8'), { filename: fileURLToPath(url) });
  return { format: 'module', source: compileScript(descriptor, { id: url, inlineTemplate: true }).content, shortCircuit: true };
} });
const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost/' });
for (const key of ['window', 'document', 'Element', 'HTMLElement', 'SVGElement', 'Node', 'Document', 'HTMLInputElement', 'HTMLSelectElement', 'Event', 'XMLSerializer', 'history', 'location', 'localStorage', 'sessionStorage']) globalThis[key] = dom.window[key];
dom.window.HTMLElement.prototype.scrollTo = () => {};
dom.window.HTMLElement.prototype.scrollIntoView = () => {};
dom.window.HTMLDialogElement.prototype.showModal = function () { this.setAttribute('open', ''); };
dom.window.HTMLDialogElement.prototype.close = function () { this.removeAttribute('open'); };
const { mount, flushPromises } = await import('@vue/test-utils');
const { createRouter, createMemoryHistory } = await import('vue-router');
const { nextTick, ref, defineComponent, h } = await import('vue');
const { default: GeneratePage } = await import('./pages/GeneratePage.vue');
const { default: Picker } = await import('./components/KnowledgePointPicker.vue');
const { api } = await import('./api.js');
const { setAuth } = await import('./auth.js');
const { saveGenerationWorkspace, loadGenerationWorkspace } = await import('./generationWorkspaceState.js');
const versions = [{id:'v1',name:'2026大纲',year:2026,point_count:2,status:'published'}, {id:'v2',name:'2025大纲',year:2025,point_count:1,status:'published'}];
const kp = (id, topic, version_id='v1') => ({id,topic,version_id,outline_code:id,subject:'内科',category:'临床综合'});
const pointA=kp('ka','甲要点'), pointB=kp('kb','乙要点'), oldPoint=kp('old','旧要点','v2');
const settle = async () => { await flushPromises(); await nextTick(); };

async function helpText(w,label){const button=w.findAll('button').find(b=>b.attributes('aria-label')===label);assert.ok(button,`missing help ${label}`);await button.trigger('mouseenter');await flushPromises();await nextTick();return document.querySelector('[role="tooltip"]').textContent;}
function fixture(t, cached) {
  localStorage.clear(); document.body.innerHTML='';
  setAuth('test-session', {id:'u1',permissions:['question:generate','stats:view']});
  const f={points:[pointA,pointB,oldPoint],versions:[...versions],run:null,progress:{total:1,counts:{passed:1},items:[]}};
  if(cached) saveGenerationWorkspace('u1',cached);
  const mock=(name, fn)=>t.mock.method(api,name,fn);
  mock('stats',async()=>({tier_counts:{working:0,formal:0,eliminated:0}}));
  mock('kpVersions',async()=>({versions:f.versions,default_version_id:f.versions[0]?.id||''}));
  mock('kpTree',async()=>({tree:[]})); mock('kpMeta',async()=>({total:f.points.length}));
  mock('searchKPFiltered',async p=>{ const ids=p.ids?.split(',');const points=f.points.filter(x=>x.version_id===p.version_id&&(!ids||ids.includes(x.id)));return {points,total:points.length,page:1}; });
  mock('getGenerationRun',async()=>f.run);
  mock('aiCheckProgress',async()=>f.progress);
  mock('generate',async p=>{f.run={run:{id:p.run_id,status:'failed',attempts:3,question_ids:[],started_at:new Date().toISOString(),completed_at:new Date().toISOString(),error:'生成失败：受控故障'},questions:[],request_summary:{knowledge_points:[pointA]}};return f.run;});
  return f;
}
async function page(t) {
  const router=createRouter({history:createMemoryHistory(),routes:[{path:'/generate',component:GeneratePage}]});
  await router.push('/generate');await router.isReady();
  const w=mount(GeneratePage,{attachTo:document.body,global:{plugins:[router],stubs:{AICheckScoreButton:true,DiscardDetailModal:true}}});
  t.after(()=>w.unmount());await settle();return w;
}
const button=(w,text)=>w.findAll('button').find(x=>x.text()===text);
const workflow=w=>w.get('.workflow-panel').text();
const oldFailure={runId:'gen-failed-old',workspaceStatus:'failed',selectedKPs:[pointA],questionIds:[],progressMsg:'生成失败：旧故障',generationStartedAt:1000,generationCompletedAt:2000,checkStartedAt:1000,checkCompletedAt:0,aiProgress:null};

test('single multi-question submission relies on server-frozen mandatory order',async t=>{
 fixture(t);setAuth('test-session',{id:'u1',permissions:['question:generate','stats:view','kb:view','kb:use']});
 const calls=[];t.mock.method(api,'generate',async p=>{calls.push(JSON.parse(JSON.stringify(p)));return {run:{id:p.run_id,status:'failed',question_ids:[],error:'受控生成故障'},questions:[],request_summary:{knowledge_points:[pointA]}}});
 const w=await page(t);await w.findAll('label.kp-item').find(x=>x.text().includes('甲要点')).get('input').setValue();await w.get('.count-input').setValue(2);
 await w.get('[aria-label="使用知识库工作流"]').setValue(true);assert.equal(w.find('[aria-label="本次病例分组顺序"]').exists(),false);
 await button(w,'生成试题').trigger('click');await settle();assert.equal(calls.length,1);assert.equal(calls[0].count,2);assert.equal(calls[0].presentation,undefined);
 await w.get('[aria-label="使用知识库工作流"]').setValue(false);await button(w,'生成试题').trigger('click');await settle();assert.equal(calls.length,2);assert.equal(calls[1].presentation,undefined);
});
test('basic outline points stay visible but cannot submit generation',async t=>{
 const f=fixture(t);f.points.push({...pointA,id:'basic',topic:'基础可见要点',category:'基础医学',subject:'生理学'});let calls=0;t.mock.method(api,'generate',async()=>{calls++;return {}});
 const w=await page(t);await w.findAll('label.kp-item').find(x=>x.text().includes('基础可见要点')).get('input').setValue();await settle();assert.match(w.text(),/所选包含基础医学知识点/);assert.equal(button(w,'生成试题').attributes('disabled'),'');assert.equal(calls,0);
});

test('ready first-check results do not show a stale checking toast',async t=>{
 const f=fixture(t);
 const q={id:'q-ready',status:'ai_reviewed',clinical_stem:'已经通过检查的题目',answer:'A',options:[{label:'A',text:'选项'}],knowledge_points:[pointA]};
 f.progress={total:1,counts:{passed:1},items:[{question_id:q.id,task_status:'succeeded',verdict:'pass'}]};
 t.mock.method(api,'generate',async p=>{f.run={count:1,run:{id:p.run_id,status:'succeeded',question_ids:[q.id],completed_at:new Date().toISOString()},questions:[q],request_summary:{knowledge_points:[pointA]}};return f.run;});
 const w=await page(t);await w.findAll('label.kp-item').find(x=>x.text().includes('甲要点')).get('input').setValue();
 await button(w,'生成试题').trigger('click');await settle();
 assert.doesNotMatch(w.text(),/我的需求反馈|填写或查看个人需求反馈|个人需求满足程度/);assert.equal(api.preferenceFeedback,undefined);assert.equal(api.preferenceSummary,undefined);assert.equal(api.savePreferenceFeedback,undefined);assert.doesNotMatch(w.text(),/非普通题库入库数/);assert.match(await helpText(w,'题目统计口径'),/非普通题库入库数/);
 assert.match(w.text(),/成功出题 1 道（首检通过，待整理确认）/);assert.match(w.text(),/进入题目整理/);assert.match(w.text(),/本人题目（含待整理）/);assert.doesNotMatch(w.text(),/进入个人题库|待审核题库|转入个人题库/);assert.doesNotMatch(w.get('.toast').text(),/质量检查进行中/);
});

test('a failed multi-question contract cannot restore stale success or start first check',async t=>{
 const cache={...oldFailure,workspaceStatus:'checking',selectedCount:2,questionIds:['stale-question'],aiProgress:{total:1,counts:{passed:1},items:[{question_id:'stale-question',task_status:'succeeded',verdict:'pass'}]}};
 const f=fixture(t,cache);let checks=0;t.mock.method(api,'aiCheckProgress',async()=>{checks++;return f.progress;});
 f.run={run:{id:cache.runId,status:'failed',requested_count:2,attempts:3,max_attempts:3,question_ids:[],error:'生成失败: 生成结果数量不一致: 请求2道，返回1道',started_at:new Date(1000).toISOString(),completed_at:new Date(2000).toISOString()},questions:[],request_summary:{knowledge_points:[pointA]}};
 const w=await page(t);assert.match(workflow(w),/生成失败/);assert.doesNotMatch(w.text(),/stale-question/);assert.equal(checks,0);assert.match(w.findAll('.workflow-step')[1].text(),/处理耗时—/);
});

test('failed restored run never starts an AI-check clock, even with a previous bad cache',async t=>{
  const f=fixture(t,oldFailure);f.run={run:{id:oldFailure.runId,status:'failed',question_ids:[],started_at:new Date(1000).toISOString(),completed_at:new Date(2000).toISOString(),error:'生成失败：旧故障'},questions:[],request_summary:{knowledge_points:[pointA]}};
  const w=await page(t);assert.match(w.findAll('.workflow-step')[1].text(),/处理耗时—/);
  t.mock.timers.enable({apis:['setInterval','Date']});t.mock.timers.tick(120000);await settle();assert.match(w.findAll('.workflow-step')[1].text(),/处理耗时—/);
  assert.match(w.get('[role="alert"]').text(),/生成失败/);assert.doesNotMatch(w.findAll('.workflow-step')[0].text(),/已完成/);
});
test('successful checking followed by a failed generation resets checking and shows a failure',async t=>{
  const f=fixture(t,{runId:'gen-success',workspaceStatus:'completed',selectedKPs:[pointA],questionIds:['q1'],generationCompletedAt:2000,checkStartedAt:2000,checkCompletedAt:3000,aiProgress:{total:1,passed:1,checking:0,items:[]}});
  f.run={run:{id:'gen-success',status:'succeeded',question_ids:['q1'],completed_at:new Date(2000).toISOString()},questions:[],request_summary:{knowledge_points:[pointA]}};
  const w=await page(t);await w.findAll('label.kp-item').find(x=>x.text().includes('甲要点')).get('input').setValue();await button(w,'生成试题').trigger('click');await settle();
  assert.match(w.get('[role="alert"]').text(),/生成失败/);assert.doesNotMatch(w.findAll('.workflow-step')[0].text(),/已完成/);assert.match(w.findAll('.workflow-step')[1].text(),/处理耗时—/);
});
test('submitted topic stays frozen when changing the next form and when restoring a failed run',async t=>{
  const f=fixture(t,oldFailure);f.run={run:{id:oldFailure.runId,status:'failed',question_ids:[],completed_at:new Date().toISOString(),error:'生成失败'},questions:[],request_summary:{knowledge_points:[pointA]}};
  const w=await page(t);assert.match(workflow(w),/甲要点/);await w.findAll('label.kp-item').find(x=>x.text().includes('乙要点')).get('input').setValue();await settle();assert.match(w.findAll('.workflow-step')[0].text(),/甲要点/);assert.doesNotMatch(w.findAll('.workflow-step')[0].text(),/乙要点/);
  const saved=JSON.parse(localStorage.getItem('aigo_generation_workspace_v1:u1'));assert.equal(saved.runKnowledgePoints[0].id,'ka');
});
for (const version_id of ['v1','v2']) test(`picker restores valid cached selection from ${version_id}`,async t=>{
  fixture(t);const selected=ref([version_id==='v1'?pointA:oldPoint]);
  const Host=defineComponent({setup:()=>()=>h(Picker,{modelValue:selected.value,'onUpdate:modelValue':v=>selected.value=v})});const w=mount(Host,{attachTo:document.body});t.after(()=>w.unmount());await settle();
  assert.equal(selected.value.length,1);assert.match(w.get('label.kp-item.checked').text(),new RegExp(selected.value[0].topic));
});
test('picker revalidates deleted selections and clears selection when switching versions explicitly',async t=>{
  const f=fixture(t);const selected=ref([pointA,pointB]);f.points=[pointA,oldPoint];
  const Host=defineComponent({setup:()=>()=>h(Picker,{modelValue:selected.value,'onUpdate:modelValue':v=>selected.value=v})});const w=mount(Host,{attachTo:document.body});t.after(()=>w.unmount());await settle();assert.deepEqual(selected.value.map(x=>x.id),['ka']);
  await button(w,'切换版本').trigger('click');await settle();await w.get('input[value="v2"]').setValue();await button(w,'切换到此版本').trigger('click');await settle();assert.equal(selected.value.length,0);
});

test('real check exhaustion remains attention with a frozen duration after refresh',async t=>{
 const f=fixture(t,{runId:'gen-check-exhausted',workspaceStatus:'attention',questionIds:['qfail'],runKnowledgePoints:[pointA],generationCompletedAt:1000,checkStartedAt:2000,checkCompletedAt:3000,aiProgress:{total:1,passed:0,exhausted:1,checking:0,items:[{question_id:'qfail',task_status:'exhausted',check_failed:true,check_completed_at:new Date(3000).toISOString()}]}});
 f.run={run:{id:'gen-check-exhausted',status:'succeeded',question_ids:['qfail'],completed_at:new Date(1000).toISOString()},questions:[],request_summary:{knowledge_points:[pointA]}};
 const w=await page(t);assert.match(w.findAll('.workflow-step')[1].text(),/需关注/);assert.match(w.findAll('.workflow-step')[1].text(),/1.0 秒/);
 t.mock.timers.enable({apis:['setInterval','Date']});t.mock.timers.tick(120000);await settle();assert.match(w.findAll('.workflow-step')[1].text(),/1.0 秒/);assert.match(w.get('[role="alert"]').text(),/已丢弃 1 道题/);
});
test('invalid restored version falls back without leaking its selected points',async t=>{
 fixture(t);const selected=ref([kp('gone','失效版本要点','removed-version')]);
 const Host=defineComponent({setup:()=>()=>h(Picker,{modelValue:selected.value,'onUpdate:modelValue':v=>selected.value=v})});const w=mount(Host,{attachTo:document.body});t.after(()=>w.unmount());await settle();assert.equal(selected.value.length,0);assert.equal(w.find('.kp-selected').exists(),false);
});
test('batch pipeline report calls a failed generation a generation failure and restores form selections',async t=>{
 fixture(t);setAuth('test-session',{id:'u1',permissions:['batch:run','stats:view']});
 const {saveBatchWorkspace}=await import('./batchWorkspace.js');saveBatchWorkspace('u1','batch-failed',localStorage,{selectedKPs:[{id:pointA.id,version_id:'v1'}],batchConfig:{count:2,job_name:'下次任务'}});
 const job={job_id:'batch-failed',owner_id:'u1',pipeline:true,status:'completed',total_count:1,completed:0,failed:1,job_name:'上次任务',pipeline_result:{saved:0,failed:1,question_ids:[],items:[{status:'failed',outline_code:'ka',error:'生成失败'}]}};
 t.mock.method(api,'batchStatus',async()=>job);t.mock.method(api,'batchCapabilities',async()=>({available:true}));t.mock.method(api,'batchHistory',async()=>({jobs:[job],total:1}));
 const {default:BatchPage}=await import('./pages/BatchPage.vue');const w=mount(BatchPage,{attachTo:document.body,global:{stubs:{RouterLink:true}}});t.after(()=>w.unmount());await settle();
 assert.match(w.get('.notice-title').text(),/生成失败/);assert.equal(w.findAll('label.kp-item.checked').length,1);assert.match(w.get('label.kp-item.checked').text(),/甲要点/);assert.match(w.text(),/上次任务/);assert.equal(w.find('input[type="number"]').element.value,'2');
});
for (const independent of [true,false]) test(`AI provider page explains and edits the effective ${independent?'independent':'shared'} check route`,async t=>{
 fixture(t);
 const provider={id:'p1',name:'生成服务',deployment:'local',base_url:'http://localhost/v1',generation_model:'generation-model',check_model:'database-check',active:true};
 t.mock.method(api,'listAIProviders',async()=>({providers:[provider],independent_check:independent,independent_check_model:independent?'independent-model':''}));
 const {default:AIProviderPage}=await import('./pages/AIProviderPage.vue');const w=mount(AIProviderPage,{attachTo:document.body});t.after(()=>w.unmount());await settle();
 if(independent){assert.match(w.get('[role="status"]').text(),/independent-model/);assert.match(await helpText(w,'模型配置生效说明'),/首检继续使用独立环境配置/);assert.doesNotMatch(await helpText(w,'模型配置生效说明'),/新进入队列的 AI 检查会使用新配置/);}
 else {assert.equal(w.find('[role="status"]').exists(),false);assert.match(await helpText(w,'模型配置生效说明'),/新进入队列的 AI 检查会使用新配置/);}
 await button(w,'编辑').trigger('click');await settle();
 const field=w.findAll('label.field').find(x=>x.text().includes(independent?'备用检查模型':'AI 检查模型'));assert.equal(field.get('input').element.disabled,independent);assert.equal(w.findAll('label.field').find(x=>x.text().includes('AI 生成模型')).get('input').element.disabled,false);
});

test('check progress reconnect notice preserves already readable questions and clears on recovery',async t=>{
 const q={id:'q-read',status:'ai_reviewed',clinical_stem:'已经通过检查的题目',answer:'A',options:[{label:'A',text:'选项'}],knowledge_points:[pointA]};
 const f=fixture(t,{runId:'gen-network',workspaceStatus:'checking',questionIds:['q-read','q-wait'],generatedQuestions:[q],generationCompletedAt:1000,checkStartedAt:1000,aiProgress:{total:2,passed:1,checking:1,items:[{question_id:'q-read',verdict:'pass'},{question_id:'q-wait',task_status:'pending'}]}});
 f.run={run:{id:'gen-network',status:'succeeded',question_ids:['q-read','q-wait'],completed_at:new Date(1000).toISOString()},questions:[q],request_summary:{knowledge_points:[pointA]}};
 let offline=true;t.mock.method(api,'aiCheckProgress',async()=>{if(offline)throw new Error('network down');return {total:2,counts:{passed:1,pending:1},items:[{question_id:'q-read',verdict:'pass'},{question_id:'q-wait',task_status:'pending'}]};});
 t.mock.timers.enable({apis:['setInterval']});const w=await page(t);assert.match(w.text(),/暂时无法获取检查进度/);assert.match(w.text(),/已经通过检查的题目/);
 offline=false;t.mock.timers.tick(5000);await settle();assert.doesNotMatch(w.text(),/暂时无法获取检查进度/);assert.match(w.text(),/已经通过检查的题目/);
});

test('batch shows retained work and automatic system recovery instead of generic queue waiting',async t=>{
 fixture(t);setAuth('test-session',{id:'u1',permissions:['batch:run','stats:view']});const {saveBatchWorkspace}=await import('./batchWorkspace.js');saveBatchWorkspace('u1','recover-job');
 const job={job_id:'recover-job',owner_id:'u1',status:'in_progress',pipeline:true,total_count:2,completed:1,failed:0,recovery:{state:'waiting',message:'系统暂时异常，任务已保留并将自动恢复。',attempts:1},items:[],pipeline_result:{saved:1,failed:0,question_ids:['q1'],items:[]}};
 t.mock.method(api,'batchStatus',async()=>job);t.mock.method(api,'batchCapabilities',async()=>({available:true}));t.mock.method(api,'batchHistory',async()=>({jobs:[job],total:1}));
 const {default:BatchPage}=await import('./pages/BatchPage.vue');const w=mount(BatchPage,{attachTo:document.body,global:{stubs:{RouterLink:true}}});t.after(()=>w.unmount());await settle();
 assert.match(w.get('[role="alert"]').text(),/自动恢复/);assert.match(w.get('[role="alert"]').text(),/已生成 1 道/);assert.doesNotMatch(w.text(),/正在等待全局并发名额/);
});

test('batch exposes per-unit retry attempts without claiming whole-batch failure',async t=>{
 fixture(t);setAuth('test-session',{id:'u1',permissions:['batch:run','stats:view']});const {saveBatchWorkspace}=await import('./batchWorkspace.js');saveBatchWorkspace('u1','retry-job');
 const job={job_id:'retry-job',owner_id:'u1',status:'in_progress',pipeline:true,total_count:2,completed:1,failed:0,items:[{outline_code:'ka',topic:'甲要点',status:'pending',attempts:1,max_attempts:3,last_error:'暂时故障',next_attempt_at:Date.now()+5000}],pipeline_result:{saved:1,failed:0,question_ids:['q1'],items:[]}};
 t.mock.method(api,'batchStatus',async()=>job);t.mock.method(api,'batchCapabilities',async()=>({available:true}));t.mock.method(api,'batchHistory',async()=>({jobs:[job],total:1}));
 const {default:BatchPage}=await import('./pages/BatchPage.vue');const w=mount(BatchPage,{attachTo:document.body,global:{stubs:{RouterLink:true}}});t.after(()=>w.unmount());await settle();assert.match(w.text(),/1 项生成正在自动重试，其他题目继续处理/);assert.match(w.text(),/已尝试 1\/3 次/);assert.doesNotMatch(w.text(),/生成失败（/);
});

test('fully failed batch is visibly failed and never claims first questions are still generating',async t=>{
 fixture(t);setAuth('test-session',{id:'u1',permissions:['batch:run','stats:view']});const {saveBatchWorkspace}=await import('./batchWorkspace.js');saveBatchWorkspace('u1','all-failed');
 const items=[{outline_code:'ka',status:'failed',error:'生成超时'},{outline_code:'kb',status:'failed',error:'生成故障'}];const job={job_id:'all-failed',owner_id:'u1',status:'completed',pipeline:true,total_count:2,completed:0,failed:2,items,pipeline_result:{saved:0,failed:2,question_ids:[],items}};
 t.mock.method(api,'batchStatus',async()=>job);t.mock.method(api,'batchCapabilities',async()=>({available:true}));t.mock.method(api,'batchHistory',async()=>({jobs:[job],total:1}));
 const {default:BatchPage}=await import('./pages/BatchPage.vue');const w=mount(BatchPage,{attachTo:document.body,global:{stubs:{RouterLink:true}}});t.after(()=>w.unmount());await settle();
 assert.equal(w.get('.job-status-panel .q-status').text(),'生成失败');assert.equal(w.get('.job-status-panel .q-status').classes().includes('status-bad'),true);assert.match(w.get('.result-summary').text(),/尚未进入首检/);assert.doesNotMatch(w.text(),/正在生成首批题目/);
});

test('batch endpoint cooldown is presented as waiting for recovery without a false queue explanation',async t=>{
 fixture(t);setAuth('test-session',{id:'u1',permissions:['batch:run','stats:view']});const {saveBatchWorkspace}=await import('./batchWorkspace.js');saveBatchWorkspace('u1','provider-wait');const job={job_id:'provider-wait',owner_id:'u1',status:'in_progress',pipeline:true,provider_waiting:true,total_count:3,completed:0,failed:0,items:[],pipeline_result:{saved:0,failed:0,question_ids:[],items:[]}};
 t.mock.method(api,'batchStatus',async()=>job);t.mock.method(api,'batchCapabilities',async()=>({available:true}));t.mock.method(api,'batchHistory',async()=>({jobs:[job],total:1}));const {default:BatchPage}=await import('./pages/BatchPage.vue');const w=mount(BatchPage,{attachTo:document.body,global:{stubs:{RouterLink:true}}});t.after(()=>w.unmount());await settle();assert.equal(w.get('.job-status-panel .q-status').text(),'等待模型恢复');assert.match(w.text(),/生成模型服务暂时不稳定/);assert.doesNotMatch(w.text(),/正在等待全局并发名额/);
});

test('single persistence recovery gives an actionable explanation without database internals',async()=>{
 const {generationPendingMessage,generationFailureMessage}=await import('./aiCheckProgressBatch.js');const error='生成失败: 保存生成结果及首检任务失败: pq: internal transaction detail';assert.match(generationPendingMessage({attempts:2,max_attempts:3,error}),/暂时无法保存.*自动恢复.*2\/3/);assert.doesNotMatch(generationFailureMessage(error),/pq:|transaction/);assert.match(generationFailureMessage(error),/稍后重新生成/);
});

test('manual batch retry times the new generation phase instead of idle hours',async t=>{
 fixture(t);t.mock.timers.enable({apis:['Date'],now:Date.now()});setAuth('test-session',{id:'u1',permissions:['batch:run','stats:view']});const {saveBatchWorkspace}=await import('./batchWorkspace.js');saveBatchWorkspace('u1','timed-retry');const job={job_id:'timed-retry',owner_id:'u1',status:'in_progress',pipeline:true,total_count:1,completed:0,failed:0,created_at:Math.floor(Date.now()/1000)-7200,generation_started_at:Date.now()-1000,items:[],pipeline_result:{saved:0,failed:0,question_ids:[],items:[]}};t.mock.method(api,'batchStatus',async()=>job);t.mock.method(api,'batchCapabilities',async()=>({available:true}));t.mock.method(api,'batchHistory',async()=>({jobs:[job],total:1}));const {default:BatchPage}=await import('./pages/BatchPage.vue');const w=mount(BatchPage,{attachTo:document.body,global:{stubs:{RouterLink:true}}});t.after(()=>w.unmount());await settle();assert.equal(w.get('.elapsed-time').text(),'00:01');assert.match(w.text(),/本轮生成用时/);
});

test('single page shows each check retry countdown, actual attempt and terminal result after refresh',async t=>{
 const now=Date.now();t.mock.timers.enable({apis:['setInterval','Date'],now});t.mock.method(window,'setInterval',globalThis.setInterval);t.mock.method(window,'clearInterval',globalThis.clearInterval);
 const f=fixture(t,{runId:'gen-check-retry',workspaceStatus:'checking',questionIds:['q1','q2'],runKnowledgePoints:[pointA]});
 f.run={run:{id:'gen-check-retry',status:'succeeded',question_ids:['q1','q2'],completed_at:new Date(now-1000).toISOString()},questions:[],request_summary:{knowledge_points:[pointA]}};
 f.progress={total:2,counts:{pending:2},items:[{question_id:'q1',task_status:'pending',attempts:1,max_attempts:3,last_error:'context deadline exceeded',next_attempt_at:new Date(now+15000).toISOString()},{question_id:'q2',task_status:'pending',attempts:0,max_attempts:3}]};
 const w=await page(t);let row=w.get('[data-question-id="q1"]');assert.match(row.text(),/等待自动重试/);assert.match(row.text(),/约 15 秒后/);assert.match(row.text(),/已尝试 1\/3 次/);assert.match(row.text(),/检查超时/);assert.match(w.get('[data-question-id="q2"]').text(),/等待首次检查/);assert.doesNotMatch(w.text(),/context deadline/);
 t.mock.timers.tick(16000);await settle();assert.match(w.get('[data-question-id="q1"]').text(),/等待重试名额/);assert.doesNotMatch(w.get('[data-question-id="q1"]').text(),/自动重试 2\/3/);
 f.progress={total:2,counts:{running:1,pending:1},items:[{question_id:'q1',task_status:'running',attempts:2,max_attempts:3,last_error:'invalid JSON'},{question_id:'q2',task_status:'pending',attempts:0,max_attempts:3}]};t.mock.timers.tick(5000);await settle();assert.match(w.get('[data-question-id="q1"]').text(),/自动重试 2\/3/);assert.doesNotMatch(w.get('[data-question-id="q1"]').text(),/秒后/);
 f.progress={total:2,counts:{exhausted:1,passed:1},items:[{question_id:'q1',task_status:'exhausted',attempts:3,max_attempts:3,check_failed:true,discarded:true},{question_id:'q2',task_status:'succeeded',verdict:'pass',attempts:1,max_attempts:3}]};t.mock.timers.tick(5000);await settle();assert.match(w.get('[data-question-id="q1"]').text(),/检查失败，已丢弃/);assert.match(w.get('[data-question-id="q2"]').text(),/已通过/);assert.doesNotMatch(w.get('[data-question-id="q1"]').text(),/秒后|等待自动重试/);assert.match(w.get('[role="alert"]').text(),/已丢弃 1 道题/);
});

test('batch row identifies its topic and check retry while passed questions remain readable',async t=>{
 const now=Date.now();t.mock.timers.enable({apis:['setInterval','Date'],now});const f=fixture(t);setAuth('test-session',{id:'u1',permissions:['batch:run','stats:view']});const {saveBatchWorkspace}=await import('./batchWorkspace.js');saveBatchWorkspace('u1','check-retry-batch');
 const job={job_id:'check-retry-batch',owner_id:'u1',status:'completed',pipeline:true,total_count:2,completed:2,failed:0,items:[],pipeline_result:{saved:2,failed:0,question_ids:['q1','q2'],items:[{status:'ok',outline_code:'ka',topic:'甲要点'},{status:'ok',outline_code:'kb',topic:'乙要点'}]}};
 f.progress={total:2,counts:{pending:1,passed:1},items:[{question_id:'q1',task_status:'pending',attempts:1,max_attempts:3,last_error:'invalid JSON',next_attempt_at:new Date(now+15000).toISOString()},{question_id:'q2',task_status:'succeeded',verdict:'pass',question_status:'ai_reviewed',stem_summary:'已通过可阅读的题目'}]};
 t.mock.method(api,'batchStatus',async()=>job);t.mock.method(api,'batchCapabilities',async()=>({available:true}));t.mock.method(api,'batchHistory',async()=>({jobs:[job],total:1}));const {default:BatchPage}=await import('./pages/BatchPage.vue');const w=mount(BatchPage,{attachTo:document.body,global:{stubs:{RouterLink:true}}});t.after(()=>w.unmount());await settle();
 let row=w.get('[data-question-id="q1"]');assert.equal(row.get('.row-state').text(),'等待自动重试');assert.match(row.text(),/甲要点/);assert.match(row.text(),/约 15 秒后/);assert.match(row.text(),/检查结果格式异常/);assert.equal(row.classes().includes('clickable'),false);assert.equal(w.get('[data-question-id="q2"]').classes().includes('clickable'),true);assert.match(w.text(),/已通过可阅读的题目/);
 f.progress={total:2,counts:{running:1,passed:1},items:[{question_id:'q1',task_status:'running',attempts:2,max_attempts:3,last_error:'invalid JSON'},{question_id:'q2',task_status:'succeeded',verdict:'pass',question_status:'ai_reviewed',stem_summary:'已通过可阅读的题目'}]};t.mock.timers.tick(5000);await settle();row=w.get('[data-question-id="q1"]');assert.equal(row.get('.row-state').text(),'自动重试 2/3');assert.doesNotMatch(row.text(),/秒后/);assert.match(w.text(),/已通过可阅读的题目/);
});

for (const arrangementAllowed of [true, false]) {
 test(`batch completion directs ${arrangementAllowed ? 'an authorized teacher' : 'a generate-only identity'} to an appropriate next step`, async t => {
  const f=fixture(t);
  setAuth('test-session',{id:'u1',permissions:['batch:run','stats:view',...(arrangementAllowed?['question:view','question:confirm']:[])]});
  const {saveBatchWorkspace}=await import('./batchWorkspace.js');saveBatchWorkspace('u1','completed-for-confirm');
  const job={job_id:'completed-for-confirm',owner_id:'u1',status:'completed',pipeline:true,total_count:1,completed:1,failed:0,items:[],pipeline_result:{saved:1,failed:0,question_ids:['q1'],items:[{status:'ok',topic:'甲要点'}]}};
  f.progress={total:1,counts:{passed:1},items:[{question_id:'q1',task_status:'succeeded',verdict:'pass',question_status:'ai_reviewed',stem_summary:'待确认题目'}]};
  t.mock.method(api,'batchStatus',async()=>job);t.mock.method(api,'batchCapabilities',async()=>({available:true}));t.mock.method(api,'batchHistory',async()=>({jobs:[job],total:1}));
  const {default:BatchPage}=await import('./pages/BatchPage.vue');
  const w=mount(BatchPage,{attachTo:document.body,global:{stubs:{RouterLink:{props:['to'],template:'<a :href="typeof to===\'string\'?to:to.path"><slot /></a>'}}}});t.after(()=>w.unmount());await settle();
  assert.match(w.text(),/本人题目（含待整理）/);assert.match(w.text(),/确认后可在普通题库导出/);
  assert.equal(w.find('a[href="/new-questions"]').exists(),arrangementAllowed);
  assert.equal(w.find('a[href="/bank"]').exists(),false);assert.doesNotMatch(w.text(),/进入个人题库|待审核题库/);
 });
}

test('restoring a generated run removes known deleted cached bodies without changing original check totals',async t=>{
 const deleted={id:'q-deleted',status:'ai_reviewed',clinical_stem:'已经删除的缓存正文',answer:'A',options:[{label:'A',text:'甲'}]},kept={id:'q-kept',status:'ai_reviewed',clinical_stem:'仍然可见的题目',answer:'A',options:[{label:'A',text:'乙'}]};
 const f=fixture(t,{runId:'gen-library-deleted',workspaceStatus:'completed',questionIds:[deleted.id,kept.id],generatedQuestions:[deleted,kept],currentIndex:0,generationStartedAt:1000,generationCompletedAt:2000,checkStartedAt:2000,checkCompletedAt:3000,aiProgress:{total:2,passed:2,checking:0,items:[]}});
 let generations=0;t.mock.method(api,'generate',async()=>{generations++;return {}});
 f.run={run:{id:'gen-library-deleted',status:'succeeded',question_ids:[deleted.id,kept.id],completed_at:new Date(2000).toISOString()},questions:[kept],hidden_deleted_count:1,hidden_deleted_ids:[deleted.id]};
 const w=await page(t);assert.doesNotMatch(w.text(),/已经删除的缓存正文/);assert.match(w.get('.readonly-stem').text(),/仍然可见/);assert.match(w.text(),/有 1 道题已移入删除题库，本页不再展示/);assert.equal(generations,0);
 const {loadGenerationWorkspace}=await import('./generationWorkspaceState.js');const saved=loadGenerationWorkspace('u1');assert.deepEqual(saved.questionIds,[deleted.id,kept.id]);assert.equal(saved.aiProgress.passed,2);assert.equal(saved.hiddenDeletedCount,1);assert.deepEqual(saved.generatedQuestions.map(q=>q.id),[kept.id]);
});

test('a run whose passed questions were all deleted explains the empty preview without suggesting regeneration',async t=>{
 const q={id:'q-deleted-all',status:'ai_reviewed',clinical_stem:'不应从缓存复活的题干',answer:'A',options:[{label:'A',text:'甲'}]};
 const f=fixture(t,{runId:'gen-all-deleted',workspaceStatus:'completed',questionIds:[q.id],generatedQuestions:[q],generationStartedAt:1000,generationCompletedAt:2000,checkStartedAt:2000,checkCompletedAt:3000,aiProgress:{total:1,passed:1,checking:0,items:[]}});
 f.run={run:{id:'gen-all-deleted',status:'succeeded',question_ids:[q.id],completed_at:new Date(2000).toISOString()},questions:[],hidden_deleted_count:1,hidden_deleted_ids:[q.id]};
 const w=await page(t);assert.match(w.text(),/有 1 道题已移入删除题库，本页不再展示/);assert.doesNotMatch(w.text(),/不应从缓存复活的题干|本次没有可入库的题目|可重新生成新题/);assert.equal(w.find('.readonly-stem').exists(),false);assert.match(w.findAll('.workflow-step')[0].text(),/已完成/);
});


for (const outcome of ['stale_session', 'server_error', 'success']) {
 test(`C6 unmounted generation ${outcome} never recovers or persists the former author into another account`, async t => {
  fixture(t);let resolve, reject, lookups=0;
  t.mock.method(api,'generate',()=>new Promise((yes,no)=>{resolve=yes;reject=no}));
  t.mock.method(api,'getGenerationRun',async()=>{lookups++;throw Object.assign(new Error('任务不存在'),{status:404})});
  const w=await page(t);await w.findAll('label.kp-item').find(x=>x.text().includes('甲要点')).get('input').setValue();await button(w,'生成试题').trigger('click');await settle();
  const oldRun=loadGenerationWorkspace('u1').runId;assert.ok(oldRun);
  w.unmount();setAuth('other-session',{id:'u2',role:'teacher',permissions:['question:generate']});
  if(outcome==='success')resolve({run:{id:oldRun,status:'succeeded',question_ids:['private-a-q']},questions:[{id:'private-a-q',clinical_stem:'A的私有正文',knowledge_points:[pointA]}]});
  else reject(Object.assign(new Error('旧会话已结束'),outcome==='server_error'?{status:403}:{staleSession:true}));
  await settle();await settle();assert.equal(lookups,0);assert.equal(loadGenerationWorkspace('u2'),null);assert.equal(loadGenerationWorkspace('u1'),null);
 });
}

test('C6 a stale run-recovery error after account switch cannot create another author workspace',async t=>{
 fixture(t,oldFailure);let reject;
 t.mock.method(api,'getGenerationRun',()=>new Promise((_,no)=>{reject=no}));
 const w=await page(t);w.unmount();setAuth('other-session',{id:'u2',permissions:['question:generate']});
 reject(Object.assign(new Error('旧任务不可访问'),{status:404}));await settle();
 assert.equal(loadGenerationWorkspace('u2'),null);
});


test('C6 clearing a pending workspace is respected even when its accepted response arrives later',async t=>{
 fixture(t);let resolve;let lookups=0;t.mock.method(api,'generate',()=>new Promise(yes=>{resolve=yes}));t.mock.method(api,'getGenerationRun',async()=>{lookups++;return null});t.mock.method(window,'confirm',()=>true);
 const w=await page(t);await w.findAll('label.kp-item').find(x=>x.text().includes('甲要点')).get('input').setValue();await button(w,'生成试题').trigger('click');await settle();const runId=loadGenerationWorkspace('u1').runId;
 await button(w,'清空本次记录').trigger('click');await settle();resolve({run:{id:runId,status:'pending',question_ids:[]}});await settle();assert.equal(loadGenerationWorkspace('u1'),null);assert.equal(w.find('.workflow-clear').exists(),false);assert.equal(lookups,0);
});

test('cancelling workspace cleanup preserves the current journey, accepting clears it',async t=>{
 fixture(t,oldFailure);const {journeyTask,rememberJourney}=await import('./authoringJourney.js');
 const w=await page(t);rememberJourney({id:'u1'},{kind:'single',id:oldFailure.runId});
 t.mock.method(window,'confirm',()=>false);await button(w,'清空本次记录').trigger('click');await settle();
 assert.equal(journeyTask.value?.id,oldFailure.runId);assert.equal(loadGenerationWorkspace('u1').runId,oldFailure.runId);
 window.confirm=()=>true;await button(w,'清空本次记录').trigger('click');await settle();assert.equal(journeyTask.value,null);assert.equal(loadGenerationWorkspace('u1'),null);
});

test('switching run links during recovery reads the new task after the old response finishes',async t=>{
 fixture(t,oldFailure);const pending=[];t.mock.method(api,'getGenerationRun',id=>new Promise(resolve=>pending.push({id,resolve})));
 const w=await page(t);assert.equal(pending[0].id,oldFailure.runId);
 await w.vm.$router.push('/generate?run_id=next-run');await settle();
 pending[0].resolve({run:{id:oldFailure.runId,status:'failed',question_ids:[]}});await settle();
 assert.equal(pending.at(-1).id,'next-run');pending.at(-1).resolve({run:{id:'next-run',status:'succeeded',question_ids:[]}});await settle();
 assert.equal(loadGenerationWorkspace('u1').runId,'next-run');
});

test('batch task links reject an older response after a newer task is selected',async t=>{
 fixture(t);setAuth('test-session',{id:'u1',permissions:['batch:run']});const pending=[];
 t.mock.method(api,'batchStatus',id=>new Promise(resolve=>pending.push({id,resolve})));t.mock.method(api,'batchCapabilities',async()=>({available:true}));t.mock.method(api,'batchHistory',async()=>({jobs:[],total:0}));
 const {default:BatchPage}=await import('./pages/BatchPage.vue');const {journeyTask}=await import('./authoringJourney.js');
 const router=createRouter({history:createMemoryHistory(),routes:[{path:'/generate/batch',component:BatchPage}]});await router.push('/generate/batch?job_id=job-a');await router.isReady();
 const w=mount(BatchPage,{attachTo:document.body,global:{plugins:[router]}});t.after(()=>w.unmount());await settle();
 await router.push('/generate/batch?job_id=job-b');await settle();
 const job=id=>({job_id:id,owner_id:'u1',status:'failed',total_count:1,completed:0,failed:1,items:[]});
 pending.find(p=>p.id==='job-b').resolve(job('job-b'));await settle();pending.find(p=>p.id==='job-a').resolve(job('job-a'));await settle();
 assert.equal(journeyTask.value?.id,'job-b');
});

test('fresh run polling never labels the task as restored',async t=>{
 const f=fixture(t);let poll;
 const original=window.setTimeout.bind(window);
 t.mock.method(window,'setTimeout',(fn,ms,...args)=>ms===2500?(poll=fn,0):original(fn,ms,...args));
 t.mock.method(api,'generate',async p=>{f.run={run:{id:p.run_id,status:'running',attempts:1,max_attempts:3,question_ids:[],started_at:new Date().toISOString()},questions:[],request_summary:{knowledge_points:[pointA]}};return f.run;});
 const w=await page(t);await w.findAll('label.kp-item').find(x=>x.text().includes('甲要点')).get('input').setValue();await button(w,'生成试题').trigger('click');await settle();
 assert.equal(typeof poll,'function');poll();await settle();assert.doesNotMatch(workflow(w),/已恢复命题任务/);assert.match(workflow(w),/正在生成/);
});

test('question detail keeps check results without the retired subjective feedback form',async t=>{
 fixture(t);t.mock.method(api,'aiCheckResult',async()=>({verdict:'pass',question_version:1}));
 const {default:Detail}=await import('./components/QuestionDetailModal.vue');
 const w=mount(Detail,{props:{question:{id:'detail-q',version:1,status:'ai_reviewed',stem:'合格题目',options:[]}},attachTo:document.body});t.after(()=>w.unmount());await settle();
 assert.match(w.text(),/检查通过/);assert.doesNotMatch(w.text(),/个人需求反馈|个人需求满足程度|我的需求反馈/);
});

test('clearing a completed batch also resets the top guide and removes a restoring URL',async t=>{
 fixture(t);sessionStorage.clear();setAuth('test-session',{id:'u1',role:'teacher',permissions:['batch:run','question:view','question:confirm']});
 const {journeyTask,journeyProgress,clearJourney}=await import('./authoringJourney.js');const {currentUser}=await import('./auth.js');clearJourney(currentUser.value);
 const {default:BatchPage}=await import('./pages/BatchPage.vue');const {default:Guide}=await import('./components/AuthoringGuide.vue');
 const job={job_id:'completed-batch',owner_id:'u1',status:'completed',total_count:1,completed:1,failed:0,items:[]};
 t.mock.method(api,'batchStatus',async()=>job);t.mock.method(api,'batchCapabilities',async()=>({available:true}));t.mock.method(api,'batchHistory',async()=>({jobs:[],total:0}));
 t.mock.method(api,'authoringJourney',async p=>({task:{id:p.task_id,kind:p.task_kind,total:1},counts:{arrange:1,confirmed:0,checking:0},generating:0}));
 const router=createRouter({history:createMemoryHistory(),routes:[{path:'/generate/batch',component:BatchPage},{path:'/new-questions',component:{template:'<p>全部待整理题目</p>'}}]});
 await router.push('/generate/batch?job_id=completed-batch');await router.isReady();
 const Host=defineComponent({components:{Guide},template:'<Guide/><RouterView/>'});
 const w=mount(Host,{attachTo:document.body,global:{plugins:[router]}});t.after(()=>w.unmount());await settle();
 assert.equal(journeyTask.value?.id,job.job_id);assert.ok(w.find('.guide-steps li.done').exists());
 t.mock.method(window,'confirm',()=>false);await button(w,'清空本次记录').trigger('click');await settle();assert.equal(journeyTask.value?.id,job.job_id);
 window.confirm=()=>true;await button(w,'清空本次记录').trigger('click');await settle();
 assert.equal(journeyTask.value,null);assert.equal(journeyProgress.value,null);assert.equal(w.findAll('.guide-steps li.done').length,0);assert.ok(w.get('.guide-steps li').classes().includes('here'));assert.equal(router.currentRoute.value.query.job_id,undefined);
 assert.equal(sessionStorage.getItem('aigo_journey:u1:teacher'),null);assert.equal(JSON.parse(localStorage.getItem('aigo_batch_workspace_v1:u1')).jobId,'');
});
