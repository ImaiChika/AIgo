import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { registerHooks } from 'node:module';
import { parse, compileScript, compileTemplate } from '@vue/compiler-sfc';
import { JSDOM } from 'jsdom';
registerHooks({ load(url, context, next) { if (!url.endsWith('.vue')) return next(url, context); const { descriptor } = parse(readFileSync(fileURLToPath(url), 'utf8')); return { format: 'module', source: descriptor.script || descriptor.scriptSetup ? compileScript(descriptor, { id: url, inlineTemplate: true }).content : compileTemplate({source:descriptor.template.content,filename:url,id:url}).code + '\nexport default {render};', shortCircuit: true }; } });
const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost/' });
for (const key of ['window', 'document', 'Document', 'Element', 'HTMLElement', 'SVGElement', 'Node', 'Event', 'history', 'location', 'localStorage', 'sessionStorage']) globalThis[key] = dom.window[key];
dom.window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
window.scrollTo=()=>{};
const { mount, flushPromises } = await import('@vue/test-utils'), { nextTick } = await import('vue');
const { api } = await import('./api.js'), { setAuth, updateUser, currentUser } = await import('./auth.js');
const { createRouter, createMemoryHistory } = await import('vue-router');

const {default:Guide}=await import('./components/AuthoringGuide.vue');
const {journeyTask,journeyProgress,rememberJourney,restoreJourney,clearJourney,journeyParams,journeyNext}=await import('./authoringJourney.js');
const settle=async()=>{await flushPromises();await nextTick()};
const permissions=['question:view','question:generate','question:confirm','question:download','batch:run','experience:view','kb:view','learning:analyze'];
function fixture(t,p=permissions){document.body.innerHTML='';sessionStorage.clear();localStorage.clear();setAuth('guide-session',{id:'teacher',role:'teacher',permissions:p});clearJourney(currentUser.value);const f={calls:[],data:{task:{id:'run-one',kind:'single',name:'胃溃疡',total:10,status:'succeeded'},counts:{arrange:6,confirmed:2,checking:1,rejected:1,deleted:0,check_failed:0},generating:0}};t.mock.method(api,'authoringJourney',async params=>{f.calls.push(params);return {...f.data,task:{...f.data.task,id:params.task_id,kind:params.task_kind}}});return f;}
async function page(t,path='/my'){
 const routes=['/my','/generate','/generate/batch','/new-questions','/bank','/knowledge','/knowledge-base','/my-experiences','/batch-history','/settings'].map(path=>({path,component:{template:'<div />'}}));const router=createRouter({history:createMemoryHistory(),routes});await router.push(path);await router.isReady();const w=mount(Guide,{attachTo:document.body,global:{plugins:[router]}});t.after(()=>w.unmount());await settle();return {w,router};
}
const button=(w,re)=>w.findAll('button').find(b=>re.test(b.text()));
test('overview retains four steps and explains why the task-dependent second step is unavailable',async t=>{fixture(t);const {w}=await page(t);assert.equal(w.findAll('.guide-steps li').length,4);assert.equal(w.findAll('li.done').length,0);assert.match(w.text(),/开始命题/);assert.equal(journeyNext(null),0);const second=w.findAll('.guide-steps li')[1];assert.match(second.text(),/需有出题任务/);assert.equal(second.get('button').element.disabled,true);});
test('partial checks and confirmed items coexist, next action opens all arrangement questions',async t=>{
 fixture(t);rememberJourney(currentUser.value,{kind:'single',id:'run-one'});const {w,router}=await page(t);assert.match(w.text(),/去题目整理/);assert.match(w.text(),/2 题已确认/);assert.equal(w.findAll('li.done')[0].text().includes('选择考点'),true);assert.equal(w.findAll('li')[1].classes().includes('done'),false);await button(w,/去题目整理/).trigger('click');await settle();assert.equal(router.currentRoute.value.path,'/new-questions');assert.equal(router.currentRoute.value.query.task_id,undefined);assert.equal(router.currentRoute.value.query.task_kind,undefined);
});
test('confirmation leads explicitly to personal bank and respects download permissions',async t=>{
 const f=fixture(t,permissions.filter(p=>p!=='question:download'));f.data.counts={...f.data.counts,arrange:0,confirmed:8,checking:0};rememberJourney(currentUser.value,{kind:'single',id:'run-one'});const {w,router}=await page(t);assert.match(w.text(),/去个人题库/);assert.doesNotMatch(w.text(),/已导出/);await button(w,/去个人题库/).trigger('click');await settle();assert.equal(router.currentRoute.value.path,'/bank');assert.equal(router.currentRoute.value.query.scope,'personal');
 await button(w,/^继续出题$/).trigger('click');await settle();assert.equal(router.currentRoute.value.path,'/generate');assert.equal(router.currentRoute.value.hash,'#authoring-points');
});
test('no active authoring permissions show no misleading main workflow',async t=>{const f=fixture(t,['kb:view','kb:review']);const {w}=await page(t);assert.equal(w.find('.authoring-guide').exists(),false);assert.equal(f.calls.length,0);});
test('knowledge-review-only identities see no extra guide above their working page',async t=>{const f=fixture(t,['kb:view','kb:review']);const {w}=await page(t,'/knowledge-base');assert.equal(w.find('.authoring-guide').exists(),false);assert.equal(f.calls.length,0);});
test('main guide contains only the four-step workflow without an optional-branch menu',async t=>{fixture(t);const {w}=await page(t);assert.equal(w.findAll('.guide-steps li').length,4);assert.doesNotMatch(w.text(),/可选操作|可选支线|AI 总结修改经验/);assert.ok(button(w,/开始命题/));});
test('auxiliary pages stay free of top guidance and task polling even with an active task',async t=>{
 const f=fixture(t);rememberJourney(currentUser.value,{kind:'single',id:'run-one'});const {w,router}=await page(t,'/my-experiences');
 for(const path of ['/my-experiences','/knowledge-base','/knowledge','/batch-history','/settings']){await router.push(path);await settle();assert.equal(w.find('.authoring-guide').exists(),false,path);}
 assert.equal(f.calls.length,0);await router.push('/my');await settle();assert.equal(w.findAll('.guide-steps li').length,4);assert.equal(f.calls.length,1);
});
for(const kind of ['single','batch'])test(`the second step opens the accepted ${kind} task`,async t=>{
 fixture(t);rememberJourney(currentUser.value,{kind,id:'run-one'});const {w,router}=await page(t);const second=w.findAll('.guide-steps li')[1].get('button');assert.equal(second.element.disabled,false);await second.trigger('click');await settle();assert.equal(router.currentRoute.value.path,kind==='batch'?'/generate/batch':'/generate');assert.equal(router.currentRoute.value.query[kind==='batch'?'job_id':'run_id'],'run-one');
});
test('late progress never repopulates after a role change and task storage holds identifiers only',async t=>{
 fixture(t);let release;t.mock.method(api,'authoringJourney',()=>new Promise(r=>release=r));rememberJourney(currentUser.value,{kind:'single',id:'run-one',label:'不得持久化正文'});const {w}=await page(t);const stored=sessionStorage.getItem('aigo_journey:teacher:teacher');assert.deepEqual(JSON.parse(stored),{kind:'single',id:'run-one'});updateUser({...currentUser.value,permissions:[]});await settle();release({task:{kind:'single',id:'run-one',total:3},counts:{arrange:3}});await settle();assert.equal(w.find('.authoring-guide').exists(),false);assert.equal(journeyProgress.value,null);
});
test('task changes reject a prior response and do not mix two tasks',async t=>{
 fixture(t);const pending=[];t.mock.method(api,'authoringJourney',params=>new Promise(resolve=>pending.push({params,resolve})));rememberJourney(currentUser.value,{kind:'single',id:'run-one'});const {w}=await page(t);rememberJourney(currentUser.value,{kind:'single',id:'run-two'});await settle();pending[0].resolve({task:{kind:'single',id:'run-one',total:99},counts:{arrange:99}});await settle();assert.doesNotMatch(w.text(),/99/);pending.at(-1).resolve({task:{kind:'single',id:'run-two',total:2},counts:{arrange:2}});await settle();assert.match(w.text(),/去题目整理/);
});
test('failed progress reads preserve navigation and stop automatic retry storms',async t=>{
 const f=fixture(t);let reads=0;t.mock.method(api,'authoringJourney',async()=>{reads++;throw new Error('offline')});rememberJourney(currentUser.value,{kind:'single',id:'run-one'});const {w}=await page(t);assert.match(w.text(),/暂时无法核对/);const before=reads;window.dispatchEvent(new Event('focus'));await settle();assert.equal(reads,before);await button(w,/重新读取/).trigger('click');await settle();assert.equal(reads,before+1);
});

test('canceling task tracking on an unfiltered page clears progress without a navigation change',async t=>{
 fixture(t);rememberJourney(currentUser.value,{kind:'batch',id:'batch-old'});const {w,router}=await page(t,'/my');
 await button(w,/取消本次定位/).trigger('click');await settle();assert.equal(router.currentRoute.value.path,'/my');assert.equal(journeyTask.value,null);assert.equal(journeyProgress.value,null);assert.equal(w.findAll('li.done').length,0);
});
