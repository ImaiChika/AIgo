import test from 'node:test';import assert from 'node:assert/strict';import {readFileSync} from 'node:fs';import {fileURLToPath} from 'node:url';import {registerHooks} from 'node:module';import {parse,compileScript} from '@vue/compiler-sfc';import {JSDOM} from 'jsdom';
registerHooks({load(url,ctx,next){if(!url.endsWith('.vue'))return next(url,ctx);const {descriptor}=parse(readFileSync(fileURLToPath(url),'utf8'));return {format:'module',source:compileScript(descriptor,{id:url,inlineTemplate:true}).content,shortCircuit:true};}});
const dom=new JSDOM('<html><body></body></html>',{url:'http://localhost/'});for(const key of ['window','document','Document','Element','HTMLElement','SVGElement','Node','Event','localStorage','sessionStorage'])globalThis[key]=dom.window[key];globalThis.confirm=()=>true;Object.defineProperty(document,'visibilityState',{configurable:true,value:'visible'});window.HTMLDialogElement.prototype.showModal=function(){this.setAttribute('open','')};window.HTMLDialogElement.prototype.close=function(){this.removeAttribute('open')};
const{mount,flushPromises}=await import('@vue/test-utils'),{nextTick}=await import('vue'),{api}=await import('./api.js'),{setAuth,updateUser,currentUser}=await import('./auth.js');
const{default:Shared}=await import('./pages/SharedKnowledgeCardsPage.vue');
const{knowledgeRows,knowledgeExcerpt,completeKnowledgeCards}=await import('./knowledgeSummaries.js');
const{toKnowledgeEditor,fromKnowledgeEditor}=await import('./knowledgeBaseAccess.js');
const{default:Private}=await import('./components/PrivateKnowledgeCards.vue'),{default:Submissions}=await import('./components/KnowledgeSubmissions.vue');
const settle=async()=>{await flushPromises();await nextTick();};const button=(root,name)=>root.findAll('button').find(b=>b.text()===name);const point={id:'p',version_id:'v',revision:1,topic:'胃溃疡'};
const card=(id='pkc-1')=>({id,title:'本人知识卡',kind:'custom',status:'active',revision:1,knowledge_points:[point],content:{custom_text:'仅本人维护的两行\n第二行',facts:[]},source:'测试来源',origin:{kind:'learning',original_version:1,current_version:2,evidence:[{id:'r1',text:'选中来源规则',before:'选中前片段',after:'选中后片段'}]},note:'私人备注不得提交预览公开'});
function fixture(t){document.body.innerHTML='';localStorage.clear();setAuth('collection-session',{id:'u',role:'teacher',permissions:['experience:view','experience:manage','experience:use','question:generate','kb:view','kb:use','kb:submit','kb:review']});t.mock.method(api,'me',async()=>currentUser.value);t.mock.method(api,'privateKnowledge',async()=>({cards:[card()],total:1}));t.mock.method(api,'kpVersions',async()=>({versions:[{id:'v',name:'2026大纲'}]}));t.mock.method(api,'searchKPFiltered',async()=>({points:[point]}));}
async function page(t,Component=Private,props={}){const w=mount(Component,{props,attachTo:document.body,global:{stubs:{RouterLink:true}}});t.after(()=>w.unmount());await settle();return w;}

const fullCard=(id='pkc-1')=>({...card(id),source:'完整来源'+ '源'.repeat(1200),content:{custom_text:'完整正文'+ '文'.repeat(5000),facts:[{id:'f1',text:'不能丢失的事实',source:'原始来源',applies_when:'适用前提'}]},note:'完整私人备注'});
const summary=(id='pkc-1')=>({id,title:'本人知识卡',revision:1,kind:'custom',status:'active',knowledge_points:[point],is_summary:true,excerpt:'列表摘要',source_excerpt:'来源摘要',selected:false});
function summaryFixture(t){fixture(t);const f={full:fullCard(),row:summary(),reads:[],writes:[]};t.mock.method(api,'privateKnowledge',async()=>({view:'summary',items:[f.row],total:1}));t.mock.method(api,'privateKnowledgeCard',async id=>{f.reads.push(id);return f.full});t.mock.method(api,'savePrivateKnowledge',async p=>{f.writes.push(p);return {cards:[{...p.cards[0],id:'pkc-1',revision:2}]}});return f;}

test('summary and legacy envelopes remain distinct and DTOs cannot enter an editor/save converter',()=>{
 assert.deepEqual(knowledgeRows({view:'summary',items:[summary()]}),[summary()]);assert.deepEqual(knowledgeRows({cards:[fullCard()]}),[fullCard()]);
 assert.equal(knowledgeExcerpt(summary()),'列表摘要');assert.equal(knowledgeExcerpt(fullCard()),fullCard().content.custom_text);
 assert.throws(()=>toKnowledgeEditor(summary()),/完整知识卡/);assert.throws(()=>fromKnowledgeEditor(summary()),/摘要/);
});
test('full materialization is bounded to four reads, ordered and detached from shared references',async()=>{
 let active=0,max=0;const calls=[];const cards=Array.from({length:11},(_,i)=>summary('p'+i));
 const full=await completeKnowledgeCards(cards,async id=>{calls.push(id);active++;max=Math.max(max,active);await Promise.resolve();active--;return fullCard(id)});
 assert.ok(max<=4);assert.equal(calls.length,11);assert.deepEqual(full.map(c=>c.id),cards.map(c=>c.id));full[0].content.facts[0].text='本地修改';assert.equal(fullCard().content.facts[0].text,'不能丢失的事实');
 let reads=0;await completeKnowledgeCards([fullCard()],async()=>{reads++});assert.equal(reads,0);
});
test('changed revisions and malformed detail payloads fail closed rather than silently save excerpts',async()=>{
 await assert.rejects(completeKnowledgeCards([summary()],async()=>({...fullCard(),revision:2})),e=>e.status===409);
 await assert.rejects(completeKnowledgeCards([summary()],async()=>summary()),e=>e.status===502);
 await assert.rejects(completeKnowledgeCards([summary()],async()=>({...fullCard(),id:'other'})),e=>e.status===502);
});
test('expression summaries require complete expression fields before editing or changing state',async()=>{
 const row={...summary(),has_expression:true};
 await assert.rejects(completeKnowledgeCards([row],async()=>fullCard()),e=>e.status===502&&/表达要求/.test(e.message));
 const full=fullCard();full.content.expression={instructions:'先说明本例证据',explanation_note:'教学提示：核对证据。'};
 const [loaded]=await completeKnowledgeCards([row],async()=>full);assert.deepEqual(loaded.content.expression,full.content.expression);
 loaded.content.expression.instructions='本地编辑';assert.equal(full.content.expression.instructions,'先说明本例证据');
});
test('private summary list is lazy and editing loads the exact complete source/body/note',async t=>{
 const f=summaryFixture(t),w=await page(t);assert.match(w.text(),/列表摘要/);assert.doesNotMatch(w.text(),/完整正文|完整私人备注/);assert.equal(f.reads.length,0);
 await button(w,'编辑').trigger('click');await settle();const dialog=document.querySelector('dialog[open]');assert.ok(dialog);assert.match(dialog.textContent,/来源修改片段说明|标题|关联知识点/);
 assert.ok([...dialog.querySelectorAll('textarea')].some(x=>x.value===f.full.content.custom_text));assert.ok([...dialog.querySelectorAll('textarea')].some(x=>x.value===f.full.note));assert.equal(f.reads.length,1);
 dialog.querySelector('button.primary-button').click();await settle();assert.equal(f.writes[0].cards[0].content.custom_text,f.full.content.custom_text);assert.equal(f.writes[0].cards[0].note,f.full.note);
});
test('private state changes rehydrate summaries without opening an editor or overwriting the complete card',async t=>{
 const f=summaryFixture(t),w=await page(t);await button(w,'停用').trigger('click');await settle();assert.equal(document.querySelector('dialog'),null);assert.equal(f.reads.length,1);assert.equal(f.writes[0].cards[0].status,'draft');assert.deepEqual(f.writes[0].cards[0].content,f.full.content);assert.equal(f.writes[0].cards[0].source,f.full.source);assert.equal(f.writes[0].cards[0].note,f.full.note);
});
test('private sharing preview loads full evidence but excludes private note and posts only revision references',async t=>{
 const f=summaryFixture(t);let payload;t.mock.method(api,'submitKnowledgeCards',async x=>{payload=x;return {submissions:[{id:'s',status:'pending'}]}});const w=await page(t);await button(w,'申请共享').trigger('click');await settle();
 const dialog=document.querySelector('dialog[open]');assert.ok(dialog);assert.match(dialog.textContent,/完整正文/);assert.match(dialog.textContent,/选中前片段/);assert.doesNotMatch(dialog.textContent,/完整私人备注/);assert.equal(f.reads.length,1);dialog.querySelector('button.primary-button').click();await settle();assert.deepEqual(payload.cards,[{id:'pkc-1',revision:1}]);assert.equal(payload.cards[0].content,undefined);
});
test('private conflicting detail never opens an editor or sends a state write',async t=>{
 const f=summaryFixture(t);f.full.revision=2;const w=await page(t);await button(w,'编辑').trigger('click');await settle();assert.equal(document.querySelector('dialog'),null);assert.match(w.text(),/已更新/);await button(w,'停用').trigger('click');await settle();assert.equal(f.writes.length,0);
});
test('revocation while a private full body is delayed cannot repopulate the editor or preview',async t=>{
 const f=summaryFixture(t);let release;t.mock.method(api,'privateKnowledgeCard',()=>new Promise(r=>release=r));const w=await page(t);await button(w,'编辑').trigger('click');await settle();updateUser({...currentUser.value,permissions:[]});await settle();release(f.full);await settle();assert.equal(document.querySelector('dialog'),null);assert.doesNotMatch(w.text(),/完整正文|列表摘要/);assert.equal(f.writes.length,0);
});
function sharedFixture(t){
 fixture(t);setAuth('shared-summary-session',{...currentUser.value,role:'admin',permissions:['kb:view','kb:manage','kb:use']});const f={full:fullCard(),row:summary(),reads:0,writes:[]};
 t.mock.method(api,'knowledgeCards',async()=>({view:'summary',items:[f.row],total:1}));t.mock.method(api,'knowledgeCard',async()=>{f.reads++;return f.full});t.mock.method(api,'saveKnowledgeCard',async x=>{f.writes.push(x);return x});return f;
}
test('shared editing fetches full body and preserves source/note while list stays a summary',async t=>{
 const f=sharedFixture(t),w=await page(t,Shared);assert.match(w.text(),/来源摘要/);assert.equal(f.reads,0);await button(w,'编辑').trigger('click');await settle();const dialog=document.querySelector('dialog[open]');assert.ok(dialog);assert.ok([...dialog.querySelectorAll('textarea')].some(x=>x.value===f.full.content.custom_text));dialog.querySelector('button.primary-button').click();await settle();assert.equal(f.writes[0].content.custom_text,f.full.content.custom_text);assert.equal(f.writes[0].note,f.full.note);assert.equal(f.reads,1);
});
test('shared stale detail is rejected and cannot silently replace the listed revision',async t=>{
 const f=sharedFixture(t);f.full.revision=2;const w=await page(t,Shared);await button(w,'编辑').trigger('click');await settle();assert.equal(document.querySelector('dialog'),null);assert.match(w.text(),/已更新/);assert.equal(f.writes.length,0);
});
test('shared delayed detail is discarded after identity loses management permission',async t=>{
 const f=sharedFixture(t);let release;t.mock.method(api,'knowledgeCard',()=>new Promise(r=>release=r));const w=await page(t,Shared);await button(w,'编辑').trigger('click');await settle();updateUser({...currentUser.value,permissions:['kb:view']});await settle();release(f.full);await settle();assert.equal(document.querySelector('dialog'),null);assert.doesNotMatch(w.text(),/完整正文/);
});

test('a permission change during one group cancels later groups before more full-body requests start',async()=>{
 let allowed=true,calls=0;const rows=Array.from({length:9},(_,i)=>summary('p'+i));
 await assert.rejects(completeKnowledgeCards(rows,async id=>{calls++;await Promise.resolve();if(id==='p3')allowed=false;return fullCard(id)},()=>allowed),e=>e.staleSession===true);assert.equal(calls,4);
});
