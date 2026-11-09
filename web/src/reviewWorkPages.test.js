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
const {default:ReviewPage}=await import('./pages/ReviewPage.vue');const {default:DecisionPage}=await import('./pages/ReviewDecisionsPage.vue');
const settle=async()=>{await flushPromises();await nextTick()};
const button=(w,text)=>w.findAll('button').find(x=>x.text().includes(text));
function fixture(t,status){
 document.body.innerHTML='';setAuth('reviewer-session',{id:'expert-a',role:'expert',permissions:['review:do','review:final']});
 const f={item:{question:{id:'q1',version:1,clinical_stem:'待审核的题目',options:[{label:'A',text:'甲'}],answer:'A'},task:{id:'t1',current_round:1,question_version:1,status,assigned_to:['expert-a'],round_results:[{round_number:1,reviews:[]}]},flow_name:'本地流程',round_count:1},gone:false,writes:[]};
 t.mock.method(api,'myTasks',async()=>({tasks:f.gone?[]:[f.item],total:f.gone?0:1}));t.mock.method(api,'myDecisions',async()=>({tasks:f.gone?[]:[f.item],total:f.gone?0:1}));
 t.mock.method(api,'listReviewers',async()=>({reviewers:[]}));t.mock.method(api,'reviewRecords',async()=>({records:[]}));
 t.mock.method(api,'getReviewTask',async()=>{if(f.gone){const e=new Error('任务已不存在');e.status=404;throw e}return f.item.task});
 t.mock.method(api,'reviewAction',async p=>f.writes.push(p));t.mock.method(api,'finalizeReview',async p=>f.writes.push(p));return f;
}
for(const [name,component,status,card,submit] of [['轮审',ReviewPage,'reviewing','.my-task-card','提交审核'],['决断',DecisionPage,'conflict','.decision-card','提交决断']]){
 test(`${name} removes restored tasks while retaining unsent comments and disabling stale actions`,async t=>{
  const f=fixture(t,status),w=mount(component,{attachTo:document.body,global:{stubs:{QuestionDetailModal:true}}});t.after(()=>w.unmount());await settle();await w.get(card).trigger('click');await settle();await w.get('textarea').setValue('不能丢失的未提交意见');f.gone=true;window.dispatchEvent(new Event('aigo:refresh-navigation-badges'));await settle();assert.equal(w.find(card).exists(),false);assert.match(w.get('[role="alert"]').text(),/已被还原/);assert.equal(w.get('textarea').element.value,'不能丢失的未提交意见');assert.equal(button(w,submit).element.disabled,true);assert.equal(f.writes.length,0);
 });
}
