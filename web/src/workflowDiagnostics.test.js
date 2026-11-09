import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {fileURLToPath} from 'node:url';
import {registerHooks} from 'node:module';
import {parse,compileScript} from '@vue/compiler-sfc';
import {JSDOM} from 'jsdom';
registerHooks({load(url,ctx,next){if(!url.endsWith('.vue'))return next(url,ctx);const{descriptor}=parse(readFileSync(fileURLToPath(url),'utf8'));return {format:'module',source:compileScript(descriptor,{id:url,inlineTemplate:true}).content,shortCircuit:true};}});
const dom=new JSDOM('<!doctype html><html><body></body></html>',{url:'http://localhost/'});
for(const key of ['window','document','Element','HTMLElement','SVGElement','Node','Event','localStorage','sessionStorage'])globalThis[key]=dom.window[key];
const {mount,flushPromises}=await import('@vue/test-utils');const {nextTick}=await import('vue');
const {api}=await import('./api.js');const {setAuth,updateUser,currentUser}=await import('./auth.js');
const {default:Diagnostics}=await import('./components/WorkflowDiagnostics.vue');
const settle=async()=>{await flushPromises();await nextTick();};

async function helpText(w,label){const button=w.findAll('button').find(b=>b.attributes('aria-label')===label);assert.ok(button,`missing help ${label}`);await button.trigger('mouseenter');await flushPromises();await nextTick();return document.querySelector('[role="tooltip"]').textContent;}
const find=(w,text)=>w.findAll('button').find(b=>b.text().includes(text));
function setup(t){setAuth('diagnostic-session',{id:'author',role:'teacher',permissions:['question:generate','batch:run','kb:view']});t.mock.method(api,'me',async()=>currentUser.value);}
function data(id='row'){return {availability:'available',entries:[{id,phase:'generation',attempt:1,outcome:'committed',at:'2026-10-23T00:00:00Z',duration_ms:20,complete:true,steps:[{name:'组织题目呈现',status:'rendered',item:0,duration_ms:0}],call:{observed:true,search_calls:0,extractor_calls:0}}],next_cursor:''};}
test('diagnostics are on-demand, distinguish handoff and pass and page without regeneration',async t=>{
 setup(t);const calls=[];t.mock.method(api,'workflowDiagnostics',async(kind,id,cursor)=>{calls.push([kind,id,cursor]);const result=data(cursor?'second':'first');result.next_cursor=cursor?'':'cursor-two';return result;});
 const w=mount(Diagnostics,{props:{taskId:'run-one'}});t.after(()=>w.unmount());await settle();assert.equal(calls.length,0);
 await find(w,'查看执行记录').trigger('click');await settle();assert.match(w.text(),/已提交首次检查/);assert.match(await helpText(w,'执行记录说明'),/不代表题目已通过/);assert.match(w.text(),/搜索 0 次/);
 await find(w,'下一页').trigger('click');await settle();assert.deepEqual(calls[1],['single','run-one','cursor-two']);
 await find(w,'上一页').trigger('click');await settle();assert.equal(calls[2][2],'');
});
test('switching task or revoking rights discards in-flight diagnostic content',async t=>{
 setup(t);let resolve;t.mock.method(api,'workflowDiagnostics',()=>new Promise(r=>resolve=r));const w=mount(Diagnostics,{props:{taskId:'old-run'}});t.after(()=>w.unmount());await find(w,'查看执行记录').trigger('click');await settle();
 await w.setProps({taskId:'new-run'});const old=data();old.entries[0].steps[0].name='OLD-PRIVATE-CONTENT';resolve(old);await settle();assert.doesNotMatch(w.text(),/OLD-PRIVATE-CONTENT/);
 await find(w,'查看执行记录').trigger('click');await settle();updateUser({...currentUser.value,permissions:[]});await settle();resolve(old);await settle();assert.equal(w.text(),'');
});
test('missing evidence and denied reads do not claim question failure',async t=>{
 setup(t);t.mock.method(api,'workflowDiagnostics',async()=>({availability:'not_recorded',entries:[],next_cursor:''}));const w=mount(Diagnostics,{props:{taskId:'legacy'}});t.after(()=>w.unmount());await find(w,'查看执行记录').trigger('click');await settle();assert.match(w.text(),/这不表示出题失败/);
 t.mock.method(api,'workflowDiagnostics',async()=>{throw new Error('当前身份无权查看知识工作流记录')});await find(w,'刷新记录').trigger('click');await settle();assert.match(w.text(),/无权查看/);assert.equal(w.findAll('.diagnostic-entry').length,0);
});
test('foreign batch owner hides the diagnostic control',async t=>{
 setup(t);const w=mount(Diagnostics,{props:{kind:'batch',taskId:'foreign',ownerId:'someone-else'}});t.after(()=>w.unmount());assert.equal(w.text(),'');
});
test('closing and reopening rechecks access rather than reusing cached contents',async t=>{
 setup(t);let calls=0;t.mock.method(api,'workflowDiagnostics',async()=>{calls++;return data()});const w=mount(Diagnostics,{props:{taskId:'owned'}});t.after(()=>w.unmount());await find(w,'查看执行记录').trigger('click');await settle();await find(w,'收起执行记录').trigger('click');await find(w,'查看执行记录').trigger('click');await settle();assert.equal(calls,2);
});
