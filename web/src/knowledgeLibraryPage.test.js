import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { registerHooks } from 'node:module';
import { parse, compileScript, compileTemplate } from '@vue/compiler-sfc';
import { JSDOM } from 'jsdom';
registerHooks({ load(url, context, next) {
  if (!url.endsWith('.vue')) return next(url, context);
  const { descriptor } = parse(readFileSync(fileURLToPath(url), 'utf8'));
  const source = descriptor.script || descriptor.scriptSetup ? compileScript(descriptor, { id: url, inlineTemplate: true }).content
    : compileTemplate({ source: descriptor.template.content, filename: url, id: url }).code.replace('export function render', 'function render') + '\nexport default { render };';
  return { format: 'module', source, shortCircuit: true };
} });
const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost/' });
for (const key of ['window', 'document', 'Document', 'Element', 'HTMLElement', 'SVGElement', 'Node', 'Event', 'XMLSerializer', 'history', 'location', 'localStorage', 'sessionStorage']) globalThis[key] = dom.window[key];
const { mount, flushPromises } = await import('@vue/test-utils'), { nextTick, h } = await import('vue');
const { createRouter, createMemoryHistory, RouterView } = await import('vue-router');
const { api } = await import('./api.js'), { setAuth, currentUser } = await import('./auth.js');
const { default: Page } = await import('./pages/KnowledgeBasePage.vue');

const settle=async()=>{await flushPromises();await nextTick();};

async function helpText(w,label){const button=document.querySelector(`button[aria-label="${label}"]`);assert.ok(button,`missing help ${label}`);button.dispatchEvent(new Event('mouseenter'));await flushPromises();await nextTick();return document.querySelector('[role="tooltip"]').textContent;}
function fixture(t,kind='medical'){
 localStorage.clear();sessionStorage.clear();document.body.innerHTML='';
 setAuth('kb-token',{id:'kb-user',role:'admin',permissions:['kb:view','kb:manage']});
 const card={id:'card',revision:1,kind,title:'已保存的知识卡',status:'active',knowledge_points:[],source:'官方来源',content:{custom_text:'方法正文',facts:[]}};
 t.mock.method(api,'me',async()=>currentUser.value);
 t.mock.method(api,'knowledgeCards',async()=>({cards:[card],total:1}));
 t.mock.method(api,'knowledgeCardHistory',async()=>({versions:[card]}));
 t.mock.method(api,'kpVersions',async()=>({versions:[]}));
 return card;
}
for(const kind of ['medical','authoring_guideline'])test(`knowledge card ${kind} details remain readable without an open editor`,async t=>{
 fixture(t,kind);const w=mount(Page,{attachTo:document.body});t.after(()=>w.unmount());
 const old=window.HTMLDialogElement.prototype.showModal;
 window.HTMLDialogElement.prototype.showModal=function(){this.setAttribute('open','');};t.after(()=>window.HTMLDialogElement.prototype.showModal=old);
 await settle();await w.get('.kb-title').trigger('click');await settle();
 assert.match(document.querySelector('dialog').textContent,/官方来源/);
});
test('global method editor hides point-specific medical controls',async t=>{
 fixture(t);const w=mount(Page,{attachTo:document.body});t.after(()=>w.unmount());
 const old=window.HTMLDialogElement.prototype.showModal;window.HTMLDialogElement.prototype.showModal=function(){this.setAttribute('open','');};t.after(()=>window.HTMLDialogElement.prototype.showModal=old);
 await settle();await w.findAll('button').find(b=>b.text()==='新增知识卡').trigger('click');await settle();
 const select=document.querySelector('dialog select');select.value='authoring_guideline';select.dispatchEvent(new Event('change',{bubbles:true}));await settle();
 const dialog=document.querySelector('dialog');assert.doesNotMatch(dialog.textContent,/关联知识点|添加事实|关键考点与目标/);assert.match(await helpText(w,'全局规范填写说明'),/全局命题方法/);
});

const deferred=()=>{let resolve;const promise=new Promise(r=>resolve=r);return {promise,resolve};};
for(const populated of [false,true])for(const event of ['focus','timer'])test(`shared knowledge ${event} refresh preserves ${populated?'card positions':'empty state'} without a loading row`,async t=>{
 const card=fixture(t),list=populated?[card]:[];
 Object.defineProperty(document,'hidden',{configurable:true,value:false});
 let tick; t.mock.method(globalThis,'setInterval',(fn,ms)=>{assert.equal(ms,30000);tick=fn;return 0;});
 t.mock.method(api,'knowledgeCards',async()=>({cards:list,total:list.length}));
 const w=mount(Page,{attachTo:document.body});t.after(()=>w.unmount());await settle();
 const anchor=w.get(populated?'.kb-card':'.kb-empty').element,response=deferred();let calls=0;
 t.mock.method(api,'knowledgeCards',()=>{calls++;return response.promise;});
 if(event==='focus')window.dispatchEvent(new Event('focus'));else tick();await settle();
 try{
  assert.equal(calls,1,'the real background path must request the list');
  assert.doesNotMatch(w.text(),/加载中…/,'live checks must not insert a layout-shifting loading row');
  assert.equal(w.get('.kb-filters button').element.disabled,false);
  assert.equal(w.get(populated?'.kb-card':'.kb-empty').element,anchor);
 }finally{response.resolve({cards:list,total:list.length});await settle();}
});

test('explicit KB query shows loading while background notifications are coalesced',async t=>{
 const card=fixture(t);Object.defineProperty(document,'hidden',{configurable:true,value:false});
 let tick;t.mock.method(globalThis,'setInterval',(fn)=>{tick=fn;return 0;});
 const w=mount(Page,{attachTo:document.body});t.after(()=>w.unmount());await settle();
 const response=deferred();let calls=0;t.mock.method(api,'knowledgeCards',()=>{calls++;return response.promise;});
 await w.get('.kb-filters').trigger('submit');await settle();assert.match(w.text(),/加载中…/);
 window.dispatchEvent(new Event('focus'));tick();await settle();assert.equal(calls,1);
 response.resolve({cards:[card],total:1});await settle();assert.doesNotMatch(w.text(),/加载中…/);
});

test('focus and timer share one background request until it completes',async t=>{
 const card=fixture(t);Object.defineProperty(document,'hidden',{configurable:true,value:false});
 let tick;t.mock.method(globalThis,'setInterval',(fn)=>{tick=fn;return 0;});
 const w=mount(Page,{attachTo:document.body});t.after(()=>w.unmount());await settle();
 const response=deferred();let calls=0;t.mock.method(api,'knowledgeCards',()=>{calls++;return response.promise;});
 window.dispatchEvent(new Event('focus'));await settle();
 for(let i=0;i<5;i++){tick();window.dispatchEvent(new Event('focus'));}await settle();assert.equal(calls,1);
 response.resolve({cards:[card],total:1});await settle();
});

test('background KB refresh keeps errors visible between checks and clears them on recovery',async t=>{
 const card=fixture(t);const w=mount(Page,{attachTo:document.body});t.after(()=>w.unmount());await settle();
 t.mock.method(api,'knowledgeCards',async()=>{throw new Error('暂时无法读取知识卡');});
 window.dispatchEvent(new Event('focus'));await settle();const error=w.get('[role="alert"]').element;
 const response=deferred();t.mock.method(api,'knowledgeCards',()=>response.promise);
 window.dispatchEvent(new Event('focus'));await settle();assert.equal(w.get('[role="alert"]').element,error);assert.doesNotMatch(w.text(),/加载中…/);
 response.resolve({cards:[card],total:1});await settle();assert.equal(w.find('[role="alert"]').exists(),false);
});

test('background permission check closes private editor and removes cached cards on revocation',async t=>{
 fixture(t);const w=mount(Page,{attachTo:document.body});t.after(()=>w.unmount());
 const old=window.HTMLDialogElement.prototype.showModal;window.HTMLDialogElement.prototype.showModal=function(){this.setAttribute('open','');};t.after(()=>window.HTMLDialogElement.prototype.showModal=old);
 await settle();await w.findAll('button').find(b=>b.text()==='编辑').trigger('click');await settle();
 const input=document.querySelector('dialog input');input.value='撤权前的未保存内容';input.dispatchEvent(new Event('input',{bubbles:true}));await settle();
 t.mock.method(api,'me',async()=>({...currentUser.value,permissions:[]}));window.dispatchEvent(new Event('focus'));await settle();
 assert.equal(document.querySelector('dialog'),null);assert.doesNotMatch(w.text(),/已保存的知识卡|撤权前的未保存内容/);
});
