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
const { default: NewPage } = await import('./pages/NewQuestionsPage.vue');
const { default: BankPage } = await import('./pages/BankPage.vue');
const { default: UsersPage } = await import('./pages/UsersPage.vue');
const { default: RolesPage } = await import('./pages/RolesPage.vue');
const { default: StatsPage } = await import('./pages/StatsPage.vue');
const { default: Dashboard } = await import('./pages/MyDashboardPage.vue');
Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
globalThis.confirm = () => true;
const settle = async () => { await flushPromises(); await nextTick(); };
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };
const button = (w, re) => w.findAll('button').find(b => re.test(b.text()));
const question = (extra = {}) => ({ id: 'q1', owner_id: 'author', version: 2, review_revision: 4, library_revision: 7, library_state: 'unfiled', status: 'ai_reviewed', clinical_stem: '原题目', options: [{ label: 'A', text: '甲' }], answer: 'A', explanation: '', ...extra });
const expectations = { question_id: 'q1', expected_version: 2, expected_library_revision: 7, expected_review_revision: 4 };

function scopedFixture(t,active=false){
 document.body.innerHTML='';sessionStorage.clear();setAuth('scope-pages',{id:'author',role:'teacher',permissions:['question:view','question:edit','question:confirm','question:download']});globalThis.confirm=()=>true;
 const f={rows:[question({id:'a',clinical_stem:'任务甲正文',library_state:active?'active':'unfiled',confirmed_version:active?2:0,status:'ai_reviewed'}),question({id:'b',clinical_stem:'任务乙正文',library_state:active?'active':'unfiled',confirmed_version:active?2:0,status:'ai_reviewed'})],reads:[],confirms:[],marker:true};
 t.mock.method(api,'me',async()=>currentUser.value);t.mock.method(api,'availableReviewFlows',async()=>({flows:[]}));
 const read=(task,library)=>{f.reads.push(task);const rows=f.rows.filter(q=>(!task.task_id||q.id===task.task_id)&&(!library||q.library_state===library));return {questions:rows,total:rows.length,...(f.marker?{task:{kind:task.task_kind,id:task.task_id}}:{})}};
 t.mock.method(api,'myNewQuestions',async(_p,_s,task={})=>read(task,'unfiled'));t.mock.method(api,'listQuestions',async(_p,_s,_b,_t,_scope,library,task={})=>read(task,library));t.mock.method(api,'listProfessions',async()=>({professions:[]}));t.mock.method(api,'listQuestionShares',async()=>({items:[]}));t.mock.method(api,'getQuestion',async id=>f.rows.find(q=>q.id===id));
 t.mock.method(api,'confirmQuestions',async items=>{f.confirms.push(items);for(const item of items){const q=f.rows.find(q=>q.id===item.question_id);q.library_state='active';q.confirmed_version=q.version;}return{confirmed:items.length}});return f;
}
async function routed(t,path){const {h}=await import('vue');const {RouterView}=await import('vue-router');const router=createRouter({history:createMemoryHistory(),routes:[{path:'/new-questions',component:NewPage},{path:'/bank',component:BankPage}]});await router.push(path);await router.isReady();const w=mount({render:()=>h(RouterView)},{attachTo:document.body,global:{plugins:[router],stubs:{AICheckScoreButton:true,PersonalExperienceProvenance:true,ReviewHistoryPanel:true}}});t.after(()=>w.unmount());await settle();return{w,router};}
test('arrangement sends task scope and retains unsaved text when same-page task navigation is cancelled',async t=>{
 const f=scopedFixture(t);const {w,router}=await routed(t,'/new-questions?task_kind=single&task_id=a');assert.deepEqual(f.reads[0],{task_kind:'single',task_id:'a'});await w.get('textarea').setValue('未保存的甲任务');globalThis.confirm=()=>false;await router.push('/new-questions?task_kind=single&task_id=b');await settle();assert.equal(router.currentRoute.value.query.task_id,'a');assert.equal(w.get('textarea').element.value,'未保存的甲任务');globalThis.confirm=()=>true;await router.push('/new-questions?task_kind=single&task_id=b');await settle();assert.equal(w.get('textarea').element.value,'任务乙正文');
});
test('confirmed arrangement opens the full personal bank',async t=>{
 const f=scopedFixture(t);const {w}=await routed(t,'/new-questions?task_kind=single&task_id=a');await w.get('.confirm-current-button').trigger('click');await settle();assert.equal(f.confirms.length,1);assert.match(w.get('.authoring-confirmed').text(),/已确认 1 题/);const href=w.get('.authoring-confirmed a').attributes('href');assert.match(href,/scope=personal/);assert.doesNotMatch(href,/task_id|task_kind/);
 updateUser({...currentUser.value,permissions:['question:view']});await settle();assert.equal(w.find('.authoring-confirmed').exists(),false);
});

test('a saved revision notice stays with its task and clears on task navigation',async t=>{
 const f=scopedFixture(t,true);t.mock.method(api,'updateQuestion',async(id,data)=>{const q=f.rows.find(q=>q.id===id);Object.assign(q,data,{version:q.version+1,library_state:'unfiled'});return q;});
 const {w,router}=await routed(t,'/bank?scope=personal&task_kind=single&task_id=a');await w.get('.q-content').trigger('click');await settle();await button(w,/修订题目/).trigger('click');await w.get('[aria-label="修订题干"]').setValue('任务甲的新修订');await button(w,/保存修订/).trigger('click');await settle();assert.match(w.get('.authoring-confirmed').text(),/修订已保存/);
 await router.push('/bank?scope=personal&task_kind=single&task_id=b');await settle();assert.equal(w.find('.authoring-confirmed').exists(),false);assert.match(w.get('.q-content').text(),/任务乙正文/);
});
test('a server without a task marker cannot populate scoped arrangement from all history',async t=>{
 const f=scopedFixture(t);f.marker=false;const {w}=await routed(t,'/new-questions?task_kind=single&task_id=a');assert.equal(w.find('.new-question-card').exists(),false);assert.match(w.text(),/尚不支持本次题目定位/);
});
test('bank query-scope changes honour unsaved revision and clear editor only after confirmation',async t=>{
 scopedFixture(t,true);const {w,router}=await routed(t,'/bank?scope=personal&task_kind=single&task_id=a');await w.get('.q-content').trigger('click');await settle();await button(w,/修订题目/).trigger('click');await w.get('[aria-label="修订题干"]').setValue('尚未保存的甲修订');globalThis.confirm=()=>false;await router.push('/bank?scope=personal&task_kind=single&task_id=b');await settle();assert.equal(router.currentRoute.value.query.task_id,'a');assert.equal(w.get('[aria-label="修订题干"]').element.value,'尚未保存的甲修订');globalThis.confirm=()=>true;await router.push('/bank?scope=personal&task_kind=single&task_id=b');await settle();assert.equal(w.find('.revision-editor').exists(),false);assert.match(w.get('.q-content').text(),/任务乙正文/);
});
