import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { registerHooks } from 'node:module';
import { parse, compileScript } from '@vue/compiler-sfc';
import { JSDOM } from 'jsdom';
registerHooks({load(url,context,next){if(!url.endsWith('.vue'))return next(url,context);const {descriptor}=parse(readFileSync(fileURLToPath(url),'utf8'));return {format:'module',source:compileScript(descriptor,{id:url,inlineTemplate:true}).content,shortCircuit:true};}});
const dom=new JSDOM('<!doctype html><html><body></body></html>',{url:'http://localhost/'});
for(const key of ['window','document','Element','HTMLElement','SVGElement','Node','Event','localStorage','sessionStorage'])globalThis[key]=dom.window[key];
dom.window.HTMLDialogElement.prototype.showModal=function(){this.setAttribute('open','')};
dom.window.HTMLDialogElement.prototype.close=function(){this.removeAttribute('open')};
const {mount,flushPromises}=await import('@vue/test-utils');const {nextTick}=await import('vue');
const {api}=await import('./api.js');const {setAuth,updateUser}=await import('./auth.js');
const {default:Page}=await import('./pages/ReviewRestorePage.vue');
const settle=async()=>{await flushPromises();await nextTick()};
const q=(n,eligible=true)=>({question:{id:`q${n}`,clinical_stem:`患者${n}，最可能的诊断是`,version:2,status:'reviewing',knowledge_points:[{topic:'胸痛',outline_code:'abc'}]},task:{id:`t${n}`,flow_id:'f1',status:'reviewing',current_round:2},flow:{id:'f1',name:'三轮流程',rounds:[{round_number:1,expert_ids:['e1']},{round_number:2,expert_ids:['e2']}],final_reviewer_ids:['a1']},creator_username:'teacher-test1',creator_name:'出题人',eligible,reason:eligible?'':'缺少首次AI检查通过记录',record_count:1,task_count:1,records:[{id:'r1',task_id:`t${n}`,round_number:1,expert_id:'e1',opinion:'保留细节',review_status:'approved'}],people:{e1:'专家一',e2:'专家二',a1:'把关人'}});
function fixture(t){
 document.body.innerHTML='';setAuth('restore-session',{id:'admin-a',role:'admin',permissions:['review:restore']});
 const f={items:[q(1),q(2,false)],requests:[],restores:[]};
 t.mock.method(api,'restoreCandidates',async()=>({items:f.items,total:f.items.length}));
 t.mock.method(api,'restoreOptions',async()=>({flows:[{id:'f1',name:'三轮流程',count:101}],owners:[{id:'u1',name:'出题人',count:101}]}));
 t.mock.method(api,'restoreCandidate',async id=>f.items.find(x=>x.task.id===id));
 t.mock.method(api,'previewReviewRestore',async p=>{f.requests.push(p);return {token:'signed-token',count:p.flow_id?101:1,record_count:2,task_count:1,owners:{'teacher-test1':1},expires_at:new Date(Date.now()+300000).toISOString()}});
 t.mock.method(api,'restoreReview',async p=>{f.restores.push(p);f.items=[];return {restored:1,deleted_records:2,deleted_tasks:1,deleted_logs:3}});
 return f;
}
async function page(t){const w=mount(Page,{attachTo:document.body});t.after(()=>w.unmount());await settle();return w;}
const button=(w,text)=>w.findAll('button').find(x=>x.text().includes(text));

test('candidate details explain assignment and invalid rows cannot be selected',async t=>{
 fixture(t);const w=await page(t);assert.match(w.text(),/缺少首次AI检查通过记录/);
 assert.equal(w.findAll('.restore-row input[type="checkbox"]')[1].element.disabled,true);
 await button(w,'查看详情').trigger('click');await settle();assert.match(w.get('.restore-detail').text(),/专家一/);assert.match(w.get('.restore-detail').text(),/把关人/);assert.match(w.get('.restore-detail').text(),/保留细节/);assert.match(w.get('.restore-detail').text(),/胸痛/);
});
test('restoration requires signed preview and reason, then clears selection and refreshes',async t=>{
 const f=fixture(t);const w=await page(t);await w.get('.restore-row input[type="checkbox"]').setValue(true);await button(w,'还原所选').trigger('click');await settle();
 assert.deepEqual(f.requests,[{task_ids:['t1']}]);assert.equal(button(w,'确认还原').element.disabled,true);await w.get('dialog textarea').setValue('测试重新安排');await button(w,'确认还原').trigger('click');await settle();assert.equal(f.restores.length,1);assert.equal(f.restores[0].preview_token,'signed-token');assert.ok(f.restores[0].operation_id);assert.match(w.text(),/已还原 1 道题/);assert.equal(w.find('dialog').exists(),false);
});
test('whole-flow action requests the flow across all pages, ignoring other display filters',async t=>{
 const f=fixture(t);const w=await page(t);await w.get('[aria-label="按审核流程筛选"]').setValue('f1');await w.get('[aria-label="题目关键词"]').setValue('胸痛');await button(w,'还原此流程全部').trigger('click');await settle();assert.deepEqual(f.requests.at(-1),{flow_id:'f1'});assert.match(w.get('dialog').text(),/101/);assert.match(w.get('dialog').text(),/全部人工审核/);
});
test('permission loss clears sensitive detail and signed confirmation',async t=>{
 fixture(t);const w=await page(t);await button(w,'查看详情').trigger('click');await settle();await button(w,'还原当前题').trigger('click');await settle();assert.equal(w.find('dialog').exists(),true);
 updateUser({id:'admin-a',role:'admin',permissions:[]});await settle();assert.equal(w.find('dialog').exists(),false);assert.equal(w.find('.restore-detail').exists(),false);assert.doesNotMatch(w.text(),/保留细节/);
});
test('a changed preview causes atomic conflict and forces a new confirmation',async t=>{
 fixture(t);t.mock.method(api,'restoreReview',async()=>{const e=new Error('审核状态已变化，请刷新后重试');e.status=409;throw e});const w=await page(t);await w.get('.restore-row input[type="checkbox"]').setValue(true);await button(w,'还原所选').trigger('click');await settle();await w.get('dialog textarea').setValue('重分配');await button(w,'确认还原').trigger('click');await settle();assert.equal(w.find('dialog').exists(),false);assert.match(w.text(),/审核状态已变化/);
});

test('an uncertain response retries the exact same operation without creating a new restore',async t=>{
 const f=fixture(t);let first=true;t.mock.method(api,'restoreReview',async p=>{f.restores.push({...p});if(first){first=false;throw new Error('network interrupted')}f.items=[];return {restored:1,deleted_tasks:1,deleted_records:2,deleted_logs:1}});
 const w=await page(t);await w.get('.restore-row input[type="checkbox"]').setValue(true);await button(w,'还原所选').trigger('click');await settle();await w.get('dialog textarea').setValue('重新安排');await button(w,'确认还原').trigger('click');await settle();assert.equal(w.get('dialog textarea').element.disabled,true);await button(w,'重试本次还原').trigger('click');await settle();assert.deepEqual(f.restores[0],f.restores[1]);assert.equal(w.find('dialog').exists(),false);
});

test('selected tasks persist across page changes and are only pruned by a refresh of the same page',async t=>{
 fixture(t);t.mock.method(api,'restoreCandidates',async params=>({items:params.page===1?Array.from({length:20},(_,i)=>q(i+1)):[q(21)],total:21}));
 const w=await page(t);await w.get('.restore-list-heading input').setValue(true);await w.get('button[aria-label="下一页"]').trigger('click');await settle();assert.match(w.get('.restore-batchbar').text(),/已选 20 道/);await w.get('.restore-list-heading input').setValue(true);assert.match(w.get('.restore-batchbar').text(),/已选 21 道/);
});

test('refreshing account metadata for the same identity does not clear selections or confirmation',async t=>{
 fixture(t);const w=await page(t);await w.get('.restore-row input[type="checkbox"]').setValue(true);await button(w,'还原所选').trigger('click');await settle();updateUser({id:'admin-a',role:'admin',permissions:['review:restore'],display_name:'刚刷新资料'});await settle();assert.equal(w.find('dialog').exists(),true);assert.match(w.get('.restore-batchbar').text(),/已选 1 道/);
});

test('a restore response arriving after the page is closed cannot start new management reads',async t=>{
 const f=fixture(t);let reads=0,release;t.mock.method(api,'restoreCandidates',async()=>{reads++;return {items:f.items,total:f.items.length}});t.mock.method(api,'restoreReview',()=>new Promise(resolve=>release=resolve));
 const w=mount(Page,{attachTo:document.body});await settle();await w.get('.restore-row input[type="checkbox"]').setValue(true);await button(w,'还原所选').trigger('click');await settle();await w.get('dialog textarea').setValue('重排审核');await button(w,'确认还原').trigger('click');await settle();w.unmount();release({restored:1,deleted_tasks:1,deleted_records:1,deleted_logs:1});await settle();assert.equal(reads,1);
});
