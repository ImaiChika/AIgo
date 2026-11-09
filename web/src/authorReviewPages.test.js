import {questionReviewUi} from "./questionReviewUi.js";
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { registerHooks } from 'node:module';
import { parse, compileScript } from '@vue/compiler-sfc';
import { JSDOM } from 'jsdom';
registerHooks({load(url,context,next){if(!url.endsWith('.vue'))return next(url,context);const {descriptor}=parse(readFileSync(fileURLToPath(url),'utf8'));return {format:'module',source:compileScript(descriptor,{id:url,inlineTemplate:true}).content,shortCircuit:true};}});
const dom=new JSDOM('<!doctype html><html><body></body></html>',{url:'http://localhost/'});
for(const key of ['window','document','Element','HTMLElement','SVGElement','Node','Event','history','location','localStorage','sessionStorage'])globalThis[key]=dom.window[key];
dom.window.HTMLDialogElement.prototype.showModal=function(){this.setAttribute('open','')};
dom.window.HTMLDialogElement.prototype.close=function(){this.removeAttribute('open')};
const {mount,flushPromises}=await import('@vue/test-utils');const {nextTick}=await import('vue');
Object.defineProperty(document,'visibilityState',{configurable:true,value:'visible'});
globalThis.confirm=()=>true;
const {api}=await import('./api.js');const {setAuth,updateUser}=await import('./auth.js');
const {default:NewPage}=await import('./pages/NewQuestionsPage.vue');const {default:RevisionsPage}=await import('./pages/MyRevisionsPage.vue');
const {createRouter,createMemoryHistory}=await import('vue-router');
const settle=async()=>{await flushPromises();await nextTick()};
const question=()=>({id:'q1',version:2,review_revision:1,library_revision:7,clinical_stem:'原题目',options:[{label:'A',text:'甲'}],answer:'A',explanation:''});
const button=(w,text)=>w.findAll('button').find(x=>x.text().includes(text));
function fixture(t){document.body.innerHTML='';setAuth('author-session',{id:'author',role:'teacher',permissions:['question:view','review:submit','question:edit']});const f={q:question(),submits:[],updates:[]};t.mock.method(api,'availableReviewFlows',async()=>({flows:[{id:'f2',name:'新流程',available:true,round_count:1}]}));t.mock.method(api,'myNewQuestions',async()=>({questions:f.q?[f.q]:[],total:f.q?1:0}));t.mock.method(api,'submitReviewBatch',async(...args)=>{f.submits.push(args);f.q=null;return {submitted:1,failed:[]}});t.mock.method(api,'updateQuestion',async(id,p)=>{f.updates.push(p);f.q={...f.q,...p,version:f.q.version+1};return f.q});return f;}
async function mountPage(t,component){const router=createRouter({history:createMemoryHistory(),routes:[{path:'/',component}]});await router.push('/');await router.isReady();const w=mount(component,{attachTo:document.body,global:{plugins:[router],stubs:{AICheckScoreButton:true,QuestionDetailModal:true}}});t.after(()=>w.unmount());await settle();return w;}

test('restored new questions send the actual revision and current content version when submitted',async t=>{
 const f=fixture(t),w=await mountPage(t,NewPage);await button(w,'提交当前题').trigger('click');await settle();assert.equal(f.submits.length,1);assert.deepEqual(f.submits[0][2],{q1:{expected_version:2,expected_review_revision:1}});assert.match(w.text(),/暂无待整理/);
});
test('background changes preserve unsaved new-question text and block old writes',async t=>{
 const f=fixture(t),w=await mountPage(t,NewPage);await w.get('textarea').setValue('尚未保存的输入');f.q={...f.q,version:3,clinical_stem:'他人更改的题目'};window.dispatchEvent(new Event('aigo:refresh-navigation-badges'));await settle();assert.equal(w.get('textarea').element.value,'尚未保存的输入');assert.match(w.get('[role="alert"]').text(),/状态已变化|已被其他操作改变/);assert.equal(button(w,'保存微调').element.disabled,true);assert.equal(f.updates.length,0);
});
test('pending revisions choose a new flow only after a saved content version and preserve the reason',async t=>{
 const f=fixture(t);f.q.version=3;const task={id:'old-task',flow_id:'f1',flow_name:'旧流程',current_round:1,question_version:2};
 t.mock.method(api,'myRevisions',async()=>({items:f.q?[{question:f.q,task,modified:f.q.version>2,reason:'补充临床症状'}]:[]}));
 t.mock.method(api,'resubmitRevisions',async(...args)=>{f.submits.push(args);f.q=null;return {submitted:1,failed:[]}});
 const w=await mountPage(t,RevisionsPage);assert.match(w.text(),/补充临床症状/);await w.get('[aria-label="重新送审流程"]').setValue('f2');await w.get('.revision-checkbox').setValue(true);await button(w,'批量重新送审').trigger('click');await settle();assert.equal(f.submits.length,1);assert.equal(f.submits[0][1],'f2');assert.deepEqual(f.submits[0][2],{q1:{expected_version:3,expected_review_revision:1,previous_task_id:'old-task'}});
});

test('saving a revision adopts its own response without marking the edit as an external conflict',async t=>{
 const f=fixture(t);f.q.version=1;const task={id:'pending-task',flow_id:'f1',flow_name:'旧流程',current_round:1,question_version:1};
 t.mock.method(api,'myRevisions',async()=>({items:[{question:f.q,task,modified:f.q.version>1,reason:'完善题干'}]}));
 const w=await mountPage(t,RevisionsPage);await w.get('.revision-card').trigger('click');await w.get('textarea').setValue('保存后的新题干');await button(w,'保存修改').trigger('click');await settle();assert.equal(w.findAll('button').find(x=>x.text()==='重新送审').element.disabled,false);assert.equal(w.find('[role="alert"]').exists(),false);assert.equal(f.updates[0].expected_library_revision,7);assert.equal(f.updates[0].expected_version,1);assert.equal(f.updates[0].expected_review_revision,1);
});

// Retain complete regression coverage for the reversible legacy UI.
test.beforeEach(t=>t.mock.method(questionReviewUi,"enabled",()=>true));
