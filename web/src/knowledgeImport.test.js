import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { registerHooks } from 'node:module';
import { parse,compileScript } from '@vue/compiler-sfc';
import { JSDOM } from 'jsdom';
registerHooks({load(url,context,next){if(!url.endsWith('.vue'))return next(url,context);const{descriptor}=parse(readFileSync(fileURLToPath(url),'utf8'));return{format:'module',source:compileScript(descriptor,{id:url,inlineTemplate:true}).content,shortCircuit:true};}});
const dom=new JSDOM('<!doctype html><html><body></body></html>',{url:'http://localhost/'});
for(const key of ['window','document','Element','HTMLElement','SVGElement','Node','Event','localStorage','sessionStorage'])globalThis[key]=dom.window[key];
dom.window.HTMLDialogElement.prototype.showModal=function(){this.open=true;};
const {mount,flushPromises}=await import('@vue/test-utils'),{nextTick}=await import('vue');
const {api}=await import('./api.js'),{setAuth,currentUser,updateUser}=await import('./auth.js');
const {default:Dialog}=await import('./components/KnowledgeImportDialog.vue');
const settle=async()=>{await flushPromises();await nextTick();};
const button=(w,name)=>w.findAll('button').find(b=>b.text().includes(name));
const rows=[{number:1,card:{title:'预览资料',kind:'custom',knowledge_points:[{id:'p',version_name:'2026大纲',outline_code:'001',topic:'肺炎'}],source:'模拟来源',note:'仅验证',content:{custom_text:'待核对内容'}}}];
async function setup(t){document.body.innerHTML='';setAuth('import-token',{id:'u',role:'teacher',permissions:['kb:view','kb:manage']});t.mock.method(api,'me',async()=>currentUser.value);t.mock.method(api,'kpVersions',async()=>({versions:[{id:'v',name:'2026大纲'},{id:'other',name:'2027大纲'}]}));const w=mount(Dialog,{attachTo:document.body});t.after(()=>w.unmount());await settle();await w.get('select').setValue('v');await w.get('textarea').setValue('{"format":"aigo-knowledge-import-v1","cards":[]}');return w;}

test('import requires preview and saves the reviewed token as drafts',async t=>{
 const w=await setup(t);let sent;
 t.mock.method(api,'previewKnowledgeImport',async()=>({valid:true,preview_token:'reviewed',rows}));
 t.mock.method(api,'confirmKnowledgeImport',async data=>{sent=data;return {saved:1,cards:[{id:'new',status:'draft'}]};});
 assert.equal(button(w,'保存全部').element.disabled,true);
 await button(w,'预览核对').trigger('click');await settle();assert.match(w.text(),/预览资料/);assert.match(w.text(),/模拟来源/);
 await button(w,'保存全部').trigger('click');await settle();assert.equal(sent.preview_token,'reviewed');assert.equal(sent.version_id,'v');assert.equal(w.emitted('saved')[0][0].saved,1);
});
test('editing a file or changing syllabus invalidates the preview',async t=>{
 const w=await setup(t);t.mock.method(api,'previewKnowledgeImport',async()=>({valid:true,preview_token:'old',rows}));
 await button(w,'预览核对').trigger('click');await settle();await w.get('textarea').setValue('{"changed":true}');assert.equal(button(w,'保存全部').element.disabled,true);assert.doesNotMatch(w.text(),/预览资料/);
 await button(w,'预览核对').trigger('click');await settle();await w.get('select').setValue('other');assert.equal(button(w,'保存全部').element.disabled,true);
});
test('permission revocation drops a late preview and requests close',async t=>{
 const w=await setup(t);let resolve;t.mock.method(api,'previewKnowledgeImport',()=>new Promise(r=>resolve=r));await button(w,'预览核对').trigger('click');await settle();updateUser({...currentUser.value,permissions:['kb:view']});await settle();resolve({valid:true,preview_token:'private',rows});await settle();assert.doesNotMatch(w.text(),/预览资料/);assert.ok(w.emitted('close'));assert.equal(w.find('textarea').exists(),false);
});
test('temporary confirmation failure retries the same token and conflict requires preview again',async t=>{
 const w=await setup(t);const tokens=[];let conflict=false;t.mock.method(api,'previewKnowledgeImport',async()=>({valid:true,preview_token:'same',rows}));t.mock.method(api,'confirmKnowledgeImport',async data=>{tokens.push(data.preview_token);throw Object.assign(new Error(conflict?'重新预览':'暂时故障'),{status:conflict?409:503});});
 await button(w,'预览核对').trigger('click');await settle();await button(w,'保存全部').trigger('click');await settle();assert.equal(button(w,'保存全部').element.disabled,false);conflict=true;await button(w,'保存全部').trigger('click');await settle();assert.deepEqual(tokens,['same','same']);assert.equal(button(w,'保存全部').element.disabled,true);assert.match(w.text(),/重新预览/);
});
