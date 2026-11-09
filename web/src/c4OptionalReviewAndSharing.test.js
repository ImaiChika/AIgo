import {questionReviewUi} from "./questionReviewUi.js";
import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {registerHooks} from 'node:module';
import {parse,compileScript} from '@vue/compiler-sfc';
import {JSDOM} from 'jsdom';
registerHooks({load(url,context,next){if(!url.endsWith('.vue'))return next(url,context);const{descriptor}=parse(readFileSync(fileURLToPath(url),'utf8'));return{format:'module',source:compileScript(descriptor,{id:url,inlineTemplate:true}).content,shortCircuit:true}}});
const dom=new JSDOM('<!doctype html><html><body></body></html>',{url:'http://localhost/'});
for(const key of ['window','document','Element','HTMLElement','SVGElement','Node','Event','history','location','localStorage','sessionStorage'])globalThis[key]=dom.window[key];
dom.window.HTMLDialogElement.prototype.showModal=function(){this.open=true};dom.window.HTMLDialogElement.prototype.close=function(){this.open=false};
Object.defineProperty(document,'visibilityState',{configurable:true,value:'visible'});globalThis.confirm=()=>true;
const{mount,flushPromises}=await import('@vue/test-utils'),{nextTick}=await import('vue');
const{createRouter,createMemoryHistory}=await import('vue-router');
const{api}=await import('./api.js'),{setAuth}=await import('./auth.js');
const{default:NewPage}=await import('./pages/NewQuestionsPage.vue');
const{default:FlowPage}=await import('./pages/ReviewFlowPage.vue'),{default:BankPage}=await import('./pages/BankPage.vue'),{default:SharePage}=await import('./pages/ShareRequestsPage.vue'),{default:ReviewPage}=await import('./pages/ReviewPage.vue');
const settle=async()=>{await flushPromises();await nextTick()};
const button=(w,re)=>w.findAll('button').find(b=>re.test(b.text()));
const q=(extra={})=>({id:'q1',owner_id:'author',version:2,review_revision:3,library_revision:7,library_state:'active',confirmed_version:2,confirmation_source:'self',status:'ai_reviewed',clinical_stem:'本人确认的题目',options:[{label:'A',text:'选项甲'}],answer:'A',explanation:'',...extra});
function fixture(t,permissions=['question:view','question:edit','question:confirm','question:share']){
 document.body.innerHTML='';localStorage.clear();history.replaceState({},'','/');setAuth('c4-session',{id:'author',role:'teacher',permissions,account_permissions:permissions});
 const f={row:q(),shares:[],creates:[],approvals:[],flows:[],flowWrites:[],taskReads:[],detailReads:[]};
 t.mock.method(api,'listQuestions',async(_p,_s,_b,_t,_scope,library)=>({questions:!library||f.row.library_state===library?[f.row]:[],total:1}));
 t.mock.method(api,'listProfessions',async()=>({professions:[]}));t.mock.method(api,'listQuestionShares',async()=>({items:f.shares}));
 t.mock.method(api,'getQuestion',async(...args)=>{f.detailReads.push(args);return f.row});
 t.mock.method(api,'getTaskByQuestion',async id=>{f.taskReads.push(id);return null});t.mock.method(api,'reviewRecords',async()=>({records:[]}));
 t.mock.method(api,'createQuestionShare',async(...args)=>{f.creates.push(args);return{request:{status:'pending',question_version:2,revision:1}}});
 t.mock.method(api,'reviewQuestionShare',async(...args)=>{f.approvals.push(args);return{}});
 t.mock.method(api,'listFlows',async()=>({flows:f.flows}));t.mock.method(api,'createFlow',async p=>{f.flowWrites.push(p);return{}});
 t.mock.method(api,'listReviewers',async()=>({reviewers:[{id:'reviewer',display_name:'审题人甲'}]}));t.mock.method(api,'listUsers',async()=>({users:[{id:'finalizer',display_name:'把关人乙',permissions:['review:final']}]}));return f;
}
async function page(t,component,query={}){
 const router=createRouter({history:createMemoryHistory(),routes:[{path:'/',component},{path:'/new-questions',component:{template:'<div />'}},{path:'/bank',component:BankPage}]});await router.push({path:'/',query});await router.isReady();
 const w=mount(component,{attachTo:document.body,global:{plugins:[router],stubs:{AICheckScoreButton:true,QuestionDetailModal:true,PreviousReviewHistory:true,ReviewHistoryPanel:true}}});t.after(()=>w.unmount());await settle();return w;
}
async function fillFlow(w){await button(w,/新建流程/).trigger('click');await w.get('input[placeholder="如：临床医学双轮审核"]').setValue('可选一轮审核');await w.get('.round-card .expert-chip').trigger('click')}

test('new optional flow defaults to one last-round completion and sends no final-reviewer field',async t=>{
 const f=fixture(t),w=await page(t,FlowPage);await fillFlow(w);
 assert.equal(w.get('select[aria-label="审核完成方式"]').element.value,'on_last_round');assert.equal(w.find('.final-reviewers').exists(),false);
 await button(w,/^创建流程$/).trigger('click');await settle();assert.equal(f.flowWrites.length,1);assert.equal(f.flowWrites[0].completion_mode,'on_last_round');assert.equal(f.flowWrites[0].rounds.length,1);assert.equal('final_reviewer_ids'in f.flowWrites[0],false);
});
test('external-final remains an explicit compatible flow mode with named final reviewers',async t=>{
 const f=fixture(t),w=await page(t,FlowPage);await fillFlow(w);await w.get('select[aria-label="审核完成方式"]').setValue('external_final');await w.get('.final-chip').trigger('click');await button(w,/^创建流程$/).trigger('click');await settle();
 assert.equal(f.flowWrites[0].completion_mode,'external_final');assert.deepEqual(f.flowWrites[0].final_reviewer_ids,['finalizer']);
});
test('flow list distinguishes legacy final decision from explicit last-round completion',async t=>{
 const f=fixture(t);f.flows=[{id:'legacy',name:'原有流程',rounds:[{round_number:1,expert_ids:['reviewer']}]},{id:'new',name:'新流程',completion_mode:'on_last_round',rounds:[{round_number:1,expert_ids:['reviewer']},{round_number:2,expert_ids:['reviewer']}]}];const w=await page(t,FlowPage);
 assert.match(w.findAll('.flow-card')[0].text(),/1轮.*最终决断/);assert.match(w.findAll('.flow-card')[1].text(),/2轮末轮完成/);assert.doesNotMatch(w.findAll('.flow-card')[1].text(),/最终把关：/);
});
test('a current self-confirmed zero-round question can request sharing with all three guards',async t=>{
 const f=fixture(t),w=await page(t,BankPage,{scope:'personal'});await w.get('.q-content').trigger('click');await settle();const share=button(w,/申请分享至全局库/);assert.ok(share);await share.trigger('click');await settle();
 assert.deepEqual(f.creates,[['q1',{expected_version:2,expected_library_revision:7,expected_review_revision:3}]]);
});
test('approved sharing permits author revision but still prevents deleting the source',async t=>{
 const f=fixture(t);f.shares=[{request:{id:'s1',question_id:'q1',status:'approved',question_version:1,revision:2}}];const w=await page(t,BankPage,{scope:'personal'});await w.get('.q-content').trigger('click');await settle();
 assert.ok(button(w,/修订题目/));assert.equal(w.find('.delete-btn').exists(),false);assert.match(w.text(),/共享.*v1/);
});
test('global view locates the fixed shared version and does not fetch private current review data',async t=>{
 const f=fixture(t,['question:view_global','question:view_formal','question:download']);f.row=q({version:1,clinical_stem:'共享获批旧版'});history.replaceState({},'','/?edit=q1');const w=await page(t,BankPage,{scope:'global'});await w.get('.q-content').trigger('click');await settle();
 assert.deepEqual(f.detailReads,[['q1','global']]);assert.deepEqual(f.taskReads,[]);assert.match(w.text(),/共享已获批/);assert.doesNotMatch(w.text(),/审核入库/);
});
test('share approval binds the displayed request revision without claiming expert review',async t=>{
 const f=fixture(t,['question:share_review']);f.shares=[{request:{id:'s1',question_id:'q1',question_version:2,revision:7,status:'pending',created_at:new Date().toISOString()},question:f.row}];const w=await page(t,SharePage);assert.match(w.text(),/v2/);assert.doesNotMatch(w.text(),/只有本人已通过专家审核/);
 await w.get('.approve-button').trigger('click');await settle();assert.deepEqual(f.approvals,[['s1','approved','',7]]);
});
test('review task displays its frozen completion mode instead of inferring final decision',async t=>{
 fixture(t,['review:do']);const task={id:'t1',flow_id:'flow1',completion_mode:'on_last_round',current_round:1,question_version:2,status:'reviewing',assigned_to:['author'],round_results:[{round_number:1,reviews:[]}]};t.mock.method(api,'myTasks',async()=>({tasks:[{question:q(),task,flow_name:'一轮可选',round_count:1,round_index:1}],total:1}));
 const w=await page(t,ReviewPage);await w.get('.my-task-card').trigger('click');await settle();assert.match(w.text(),/1轮末轮完成/);
});


test('author can reopen optional review progress after refresh and cancel only a first-round empty vote',async t=>{
 const f=fixture(t,['question:view','question:confirm','review:submit']);f.row=q({status:'reviewing',library_state:'unfiled',confirmed_version:0});
 const reads=[],cancels=[];let pending=true;
 t.mock.method(api,'myNewQuestions',async()=>({questions:[],total:0}));t.mock.method(api,'availableReviewFlows',async()=>({flows:[]}));
 t.mock.method(api,'searchQuestions',async(...args)=>{reads.push(args);return{questions:pending?[f.row]:[],total:pending?1:0}});
 t.mock.method(api,'getTaskByQuestion',async()=>({id:'t1',status:'reviewing',current_round:1,question_version:2,completion_mode:'on_last_round',can_cancel_submission:true,round_results:[{round_number:1,reviews:[],approved_count:0,rejected_count:0,revision_count:0}]}));
 t.mock.method(api,'cancelReview',async payload=>{cancels.push(payload);pending=false;return{question:q(),task:{id:'t1',status:'archived'}}});
 const w=await page(t,NewPage);assert.ok(w.find('.author-review-progress').exists());assert.equal(reads.length,0);
 w.get('.author-review-progress').element.open=true;await w.get('.author-review-progress').trigger('toggle');await settle();
 await w.get('.author-review-row').trigger('click');await settle();assert.match(w.text(),/末轮完成/);await button(w,/取消本次可选审核/).trigger('click');await settle();
 assert.equal(reads[0][10],'personal');assert.deepEqual(cancels,[{task_id:'t1',expected_version:2,expected_library_revision:7,expected_review_revision:3}]);assert.match(w.text(),/恢复.*确认|回.*整理/);
});

test('pending sharing locks source revision and an unconfirmed version cannot request sharing',async t=>{
 const f=fixture(t);f.shares=[{request:{question_id:'q1',status:'pending',question_version:2,revision:1}}];const w=await page(t,BankPage,{scope:'personal'});await w.get('.q-content').trigger('click');await settle();assert.equal(button(w,/修订题目/),undefined);assert.equal(w.find('.delete-btn').exists(),false);
});
test('bulk selected sharing freezes the current guards rather than posting unversioned IDs',async t=>{
 const f=fixture(t),calls=[];t.mock.method(api,'createQuestionShares',async items=>{calls.push(items);return{submitted:1,question_ids:['q1']}});const w=await page(t,BankPage,{scope:'personal'});
 await w.get('.q-checkbox').setValue(true);await button(w,/提交所选至全局库/).trigger('click');await settle();const dialog=w.get('.bulk-share-dialog');await dialog.findAll('button').at(-1).trigger('click');await settle();
 assert.deepEqual(calls,[[{question_id:'q1',expected_version:2,expected_library_revision:7,expected_review_revision:3}]]);
});
test('a share request with unknown historic version cannot be approved by guessing the current version',async t=>{
 const f=fixture(t,['question:share_review']);f.shares=[{request:{id:'legacy',question_id:'q1',status:'pending',revision:1,question_version:0},question:f.row}];const w=await page(t,SharePage);assert.equal(w.get('.approve-button').element.disabled,true);assert.match(w.text(),/版本.*核对/);assert.equal(f.approvals.length,0);
});
test('sanitized zero vote counts never override the server cancellation capability',async t=>{
 fixture(t,['question:view','review:submit']);t.mock.method(api,'myNewQuestions',async()=>({questions:[],total:0}));t.mock.method(api,'availableReviewFlows',async()=>({flows:[]}));t.mock.method(api,'searchQuestions',async()=>({questions:[q({status:'reviewing',library_state:'unfiled'})],total:1}));
 t.mock.method(api,'getTaskByQuestion',async()=>({id:'t1',status:'reviewing',current_round:1,question_version:2,can_cancel_submission:false,round_results:[{round_number:1,reviews:[],approved_count:0}]}));
 const w=await page(t,NewPage);w.get('.author-review-progress').element.open=true;await w.get('.author-review-progress').trigger('toggle');await settle();await w.get('.author-review-row').trigger('click');await settle();assert.equal(button(w,/取消本次可选审核/),undefined);
});

test('global shared snapshot remains read-only even for an administrator with source deletion permissions',async t=>{
 const f=fixture(t,['question:view_global','question:view_formal','question:download','question:delete','question:delete_formal']);f.row=q({status:'published',version:1});const w=await page(t,BankPage,{scope:'global'});await w.get('.q-content').trigger('click');await settle();assert.equal(w.find('.delete-btn').exists(),false);assert.equal(button(w,/修订题目/),undefined);
});
test('an explicit unknown completion mode is not presented as a legacy final-decision flow',async t=>{
 const f=fixture(t);f.flows=[{id:'future',name:'未知历史流程',completion_mode:'unrecognized',available:false,unavailable_reason:'完成方式不支持',rounds:[{round_number:1,expert_ids:['reviewer']}]}];const w=await page(t,FlowPage);assert.match(w.text(),/未知完成方式/);assert.doesNotMatch(w.text(),/最终把关：/);
});

test('a fixed global snapshot without optional parameters has no empty parameter panel or personal history component',async t=>{
 const f=fixture(t,['question:view_global','question:view_formal']);f.row=q({version:1});const w=await page(t,BankPage,{scope:'global'});await w.get('.q-content').trigger('click');await settle();assert.equal(w.find('.params-bar').exists(),false);assert.equal(w.find('previous-review-history-stub').exists(),false);
});

test('logged-out sidebar never requests personal summary before authentication is ready',async t=>{
 fixture(t);setAuth('',null);let calls=0;t.mock.method(api,'mySummary',async()=>{calls++;return{new_questions_count:9}});const{default:Sidebar}=await import('./components/SidebarNavigation.vue');const w=await page(t,Sidebar);window.dispatchEvent(new Event('aigo:refresh-navigation-badges'));await settle();assert.equal(calls,0);assert.equal(w.find('.navigation-badge').exists(),false);
});
test('cached user without a token does not authorize sidebar summary requests',async t=>{
 fixture(t);setAuth('',{id:'author',role:'teacher',permissions:['question:view','question:confirm']});let calls=0;t.mock.method(api,'mySummary',async()=>{calls++;return{}});const{default:Sidebar}=await import('./components/SidebarNavigation.vue');await page(t,Sidebar);assert.equal(calls,0);
});
test('logout invalidates late sidebar summary and keeps the next identity badges empty until its own reply',async t=>{
 fixture(t);let oldResolve,newResolve,calls=0;const old=new Promise(r=>oldResolve=r),fresh=new Promise(r=>newResolve=r);t.mock.method(api,'mySummary',()=>{calls++;return calls===1?old:fresh});const{default:Sidebar}=await import('./components/SidebarNavigation.vue');const w=await page(t,Sidebar);assert.equal(calls,1);
 setAuth('',null);await settle();assert.equal(calls,1);
 setAuth('next-session',{id:'next-author',role:'teacher',permissions:['question:view','question:confirm']});await settle();assert.equal(calls,2);
 oldResolve({new_questions_count:99});await settle();await button(w,/^命题工作/).trigger('click');assert.equal(w.find('.navigation-badge').exists(),false);
 newResolve({new_questions_count:2});await settle();assert.equal(w.get('.navigation-badge').text(),'2');
});

// Retain complete regression coverage for the reversible legacy UI.
test.beforeEach(t=>t.mock.method(questionReviewUi,"enabled",()=>true));
