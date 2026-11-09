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

const {default:Binding}=await import('./components/KnowledgePointBinding.vue');
const {default:Editor}=await import('./components/KnowledgeCardEditor.vue');
const {default:Private}=await import('./components/PrivateKnowledgeCards.vue');
const {updateUser}=await import('./auth.js');
const settle=async()=>{await flushPromises();await nextTick();};
const deferred=()=>{let resolve;const promise=new Promise(r=>resolve=r);return {resolve,promise};};
const point=(id='p1',version_id='v1')=>({id,version_id,revision:3,version_name:version_id==='v1'?'2026临床大纲':'2025临床大纲',category:'临床',subject:'消化',unit:'胃',sub_item:'溃疡',topic:'胃溃疡 '+id,outline_code:'110.'+id});
const tree=[{id:'c',label:'临床',path:['临床'],count:2,children:[{id:'s',label:'消化',path:['临床','消化'],count:2,children:[{id:'u',label:'胃',path:['临床','消化','胃'],count:2,children:[{id:'i',label:'溃疡',path:['临床','消化','胃','溃疡'],count:2,children:[]}]}]}]}];
function fixture(t){
 document.body.innerHTML='';localStorage.clear();setAuth('binding-test',{id:'teacher',role:'teacher',permissions:['experience:view','experience:manage','experience:use','question:generate']});
 window.HTMLDialogElement.prototype.showModal=function(){this.open=true};
 const f={reads:[],points:[point()],versions:[{id:'v1',name:'2026临床大纲',status:'published'},{id:'v2',name:'2025临床大纲',status:'published'}]};
 t.mock.method(api,'kpVersions',async()=>({versions:f.versions,default_version_id:'v1'}));
 t.mock.method(api,'kpTree',async()=>({tree}));
 t.mock.method(api,'searchKPFiltered',async q=>{f.reads.push(q);return {points:f.points.map(p=>({...p,version_id:q.version_id,version_name:q.version_id==='v1'?'2026临床大纲':'2025临床大纲'})),total:f.points.length,page:q.page}});
 return f;
}
async function mountBinding(t,initial=[]){let w;w=mount(Binding,{attachTo:document.body,props:{modelValue:initial,'onUpdate:modelValue':value=>w.setProps({modelValue:value})}});t.after(()=>w.unmount());await settle();return w;}
const open=async w=>{await w.get('.binding-trigger').trigger('click');await settle();};
const check=(w,id='p1')=>w.get(`input[aria-label="绑定 胃溃疡 ${id}（110.${id}）"]`);
test('binding is collapsed and lazy by default, then browses a four-level directory without a known code',async t=>{
 const f=fixture(t),w=await mountBinding(t);assert.equal(f.reads.length,0);assert.equal(w.find('select').exists(),false);await open(w);
 for(const name of ['临床','消化','胃','溃疡']){const b=w.findAll('.directory-label').find(x=>x.text().startsWith(name));assert.ok(b,name);await b.trigger('click');await settle();}
 assert.deepEqual(JSON.parse(f.reads.at(-1).path),['临床','消化','胃','溃疡']);await check(w).setValue(true);await settle();assert.equal(w.props('modelValue')[0].revision,3);assert.equal(w.props('modelValue')[0].version_id,'v1');assert.match(w.get('.binding-chips').text(),/2026临床大纲.*消化/);
 await w.get('.binding-footer button').trigger('click');assert.equal(w.find('.binding-workspace').exists(),false);assert.equal(w.props('modelValue').length,1);
});
test('related-word search is debounced and not restricted to exact topic names',async t=>{
 const f=fixture(t),w=await mountBinding(t);await open(w);t.mock.timers.enable({apis:['setTimeout']});await w.get('[aria-label="搜索绑定知识点"]').setValue('上腹痛');t.mock.timers.tick(301);await settle();assert.equal(f.reads.at(-1).q,'上腹痛');assert.equal(f.reads.at(-1).match,undefined);assert.equal(f.reads.at(-1).page_size,20);
});
test('paging retains checked mappings and does not replace earlier-page choices',async t=>{
 fixture(t);t.mock.method(api,'searchKPFiltered',async q=>({points:[point('p'+q.page)],total:41}));const w=await mountBinding(t);await open(w);await check(w).setValue(true);await w.get('[aria-label="绑定考点下一页"]').trigger('click');await settle();await check(w,'p2').setValue(true);await settle();assert.deepEqual(w.props('modelValue').map(p=>p.id),['p1','p2']);await w.get('[aria-label="绑定考点上一页"]').trigger('click');await settle();assert.equal(check(w).element.checked,true);
});
test('browsing another version preserves previous mappings and counts only the visible version',async t=>{
 const f=fixture(t),w=await mountBinding(t,[point()]);await open(w);f.points=[point('p2','v2')];await w.get('select').setValue('v2');await settle();assert.equal(w.props('modelValue')[0].id,'p1');assert.doesNotMatch(w.findAll('.directory-label')[0].text(),/1\//);await check(w,'p2').setValue(true);await settle();assert.deepEqual(w.props('modelValue').map(p=>p.version_id),['v1','v2']);assert.match(w.get('.binding-chips').text(),/2025临床大纲/);
});
test('twenty-card mapping limit disables only unchecked points and allows removal',async t=>{
 const f=fixture(t);f.points=[point(),point('new')];const w=await mountBinding(t,Array.from({length:20},(_,i)=>point('p'+(i+1))));await open(w);assert.equal(check(w).element.disabled,false);assert.equal(check(w,'new').element.disabled,true);await check(w).setValue(false);await settle();assert.equal(check(w,'new').element.disabled,false);await check(w,'new').setValue(true);await settle();assert.equal(w.props('modelValue').length,20);assert.equal(new Set(w.props('modelValue').map(p=>p.id)).size,20);
});
test('a previous version late tree and result cannot overwrite a newer browsing version',async t=>{
 fixture(t);const oldTree=deferred(),oldSearch=deferred();t.mock.method(api,'kpTree',v=>v==='v1'?oldTree.promise:Promise.resolve({tree}));t.mock.method(api,'searchKPFiltered',q=>q.version_id==='v1'?oldSearch.promise:Promise.resolve({points:[point('new','v2')],total:1}));const w=await mountBinding(t);await open(w);await w.get('select').setValue('v2');await settle();oldTree.resolve({tree:[{id:'old',label:'过期旧目录',path:[],children:[],count:1}]});oldSearch.resolve({points:[{...point(),topic:'旧版本迟到内容'}],total:1});await settle();assert.doesNotMatch(w.text(),/过期旧目录|旧版本迟到内容/);assert.match(w.text(),/胃溃疡 new/);
});
test('closing while a request is in flight keeps mappings and cannot refill a reopened list',async t=>{
 fixture(t);const late=deferred();let calls=0;t.mock.method(api,'searchKPFiltered',()=>++calls===1?late.promise:Promise.resolve({points:[point('fresh')],total:1}));const w=await mountBinding(t,[point()]);await open(w);await w.get('.binding-trigger').trigger('click');await open(w);late.resolve({points:[{...point(),topic:'已关闭请求的内容'}],total:1});await settle();assert.doesNotMatch(w.text(),/已关闭请求的内容/);assert.equal(w.props('modelValue').length,1);assert.match(w.text(),/胃溃疡 fresh/);
});
test('permission-order heartbeats and focus refresh do not replace a currently open directory or list',async t=>{
 const f=fixture(t),w=await mountBinding(t);await open(w);const anchor=w.get('.binding-point').element,requests=f.reads.length;
 updateUser({...currentUser.value,permissions:[...currentUser.value.permissions].reverse()});window.dispatchEvent(new Event('focus'));await settle();assert.equal(w.get('.binding-point').element,anchor);assert.equal(f.reads.length,requests);assert.equal(w.get('select').element.disabled,false);
});
test('identity changes cancel late data and close the picker before another user can see it',async t=>{
 fixture(t);const late=deferred();t.mock.method(api,'searchKPFiltered',()=>late.promise);const w=await mountBinding(t);await open(w);setAuth('another-session',{...currentUser.value,id:'another-user'});late.resolve({points:[{...point(),topic:'原身份迟到资料'}],total:1});await settle();assert.equal(w.find('.binding-workspace').exists(),false);assert.doesNotMatch(w.text(),/原身份迟到资料/);
});
test('disabled editor preserves existing mappings, closes pending UI and blocks reopening',async t=>{
 fixture(t);const w=await mountBinding(t,[point()]);await open(w);await w.setProps({disabled:true});await settle();assert.equal(w.find('.binding-workspace').exists(),false);assert.equal(w.get('.binding-trigger').element.disabled,true);assert.equal(w.props('modelValue').length,1);assert.equal(w.get('.binding-chip button').element.disabled,true);
});
test('search errors retain old bindings and retry does not render a false empty result',async t=>{
 fixture(t);let fail=true;t.mock.method(api,'searchKPFiltered',async()=>{if(fail)throw new Error('暂时断开');return {points:[point()],total:1}});const w=await mountBinding(t,[point()]);await open(w);assert.match(w.get('[role="alert"]').text(),/暂时断开/);assert.doesNotMatch(w.get('.binding-results').text(),/没有找到相关考点/);fail=false;await w.get('[role="alert"] button').trigger('click');await settle();assert.equal(check(w).element.checked,true);
});
test('no available versions never clears an existing historic mapping',async t=>{
 const f=fixture(t);f.versions=[];const w=await mountBinding(t,[point('historic','removed')]);await open(w);assert.equal(f.reads.length,0);assert.equal(w.props('modelValue').length,1);assert.match(w.text(),/暂无大纲版本/);
});
test('manual private-card creation, enabling and selection need no AI analysis or shared KB permissions',async t=>{
 fixture(t);let saved,chosen;let rows=[];t.mock.method(api,'me',async()=>currentUser.value);t.mock.method(api,'privateKnowledge',async()=>({cards:rows,total:rows.length}));t.mock.method(api,'savePrivateKnowledge',async req=>{saved=req;rows=[{...req.cards[0],id:'manual-card',revision:1}];return {cards:rows}});t.mock.method(api,'chooseKnowledgeCard',async req=>{chosen=req;return {selected:true}});const analysis=t.mock.method(api,'createLearningAnalysis',async()=>{throw new Error('不得调用AI')});
 const w=mount(Private,{attachTo:document.body,global:{stubs:{RouterLink:true}}});t.after(()=>w.unmount());await settle();await w.findAll('button').find(b=>b.text()==='新增知识卡').trigger('click');await settle();
 const editor=w.findComponent(Editor);await editor.get('input').setValue('手工自用资料');await editor.get('select').setValue('custom');await editor.findAll('textarea').find(x=>x.element.maxLength===6000).setValue('手工整理的自定义规则');await editor.get('.binding-trigger').trigger('click');await settle();await editor.get('.binding-point input').setValue(true);await settle();
 document.querySelector('dialog button.primary-button').click();await settle();assert.equal(saved.cards[0].status,'active');assert.equal(saved.cards[0].knowledge_points[0].id,'p1');assert.equal(analysis.mock.callCount(),0);await w.get('[aria-label="用于出题 手工自用资料"]').setValue(true);await settle();assert.deepEqual(chosen,{scope:'private',id:'manual-card',selected:true});
});

test('version read failure has an explicit retry and keeps the card mapping until it succeeds',async t=>{
 fixture(t);let fail=true;t.mock.method(api,'kpVersions',async()=>{if(fail)throw new Error('版本读取断开');return {versions:[{id:'v1',name:'2026临床大纲'}],default_version_id:'v1'}});const w=await mountBinding(t,[point()]);await open(w);assert.match(w.get('[role="alert"]').text(),/版本读取断开/);assert.equal(w.props('modelValue').length,1);fail=false;await w.get('[role="alert"] button').trigger('click');await settle();assert.equal(w.find('[role="alert"]').exists(),false);assert.equal(check(w).element.checked,true);
});
