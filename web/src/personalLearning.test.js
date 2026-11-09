import {questionReviewUi} from "./questionReviewUi.js";
import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { registerHooks } from 'node:module';
import { parse, compileScript } from '@vue/compiler-sfc';
import { JSDOM } from 'jsdom';
registerHooks({ load(url, context, next) { if (!url.endsWith('.vue')) return next(url, context); const { descriptor } = parse(readFileSync(fileURLToPath(url), 'utf8')); return { format: 'module', source: compileScript(descriptor, { id: url, inlineTemplate: true }).content, shortCircuit: true }; } });
const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost/' });
globalThis.confirm = () => true;
Object.defineProperty(dom.window.document, 'visibilityState', { configurable: true, value: 'visible' });
Object.defineProperty(dom.window.document, 'hidden', { configurable: true, value: false });
for (const key of ['window', 'document', 'Document', 'Element', 'HTMLElement', 'SVGElement', 'Node', 'Event', 'history', 'location', 'localStorage', 'sessionStorage']) globalThis[key] = dom.window[key];
dom.window.HTMLElement.prototype.scrollIntoView = function () {};
dom.window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
dom.window.HTMLDialogElement.prototype.close = function () { this.open = false; };
const { mount, flushPromises } = await import('@vue/test-utils'), { nextTick } = await import('vue');
const { api } = await import('./api.js'), { setAuth, updateUser, currentUser } = await import('./auth.js');
const { visibleNavigation } = await import('./navigation.js');
const settle = async () => { await flushPromises(); await nextTick(); };

async function helpText(w,label){const button=w.findAll('button').find(b=>b.attributes('aria-label')===label);assert.ok(button,`missing help ${label}`);await button.trigger('mouseenter');await flushPromises();await nextTick();return document.querySelector('[role="tooltip"]').textContent;}
const button = (w, re) => w.findAll('button').find(b => re.test(b.text()));
const q = { id: 'q1', owner_id: 'author', version: 3, library_revision: 4, review_revision: 2, status: 'ai_reviewed', library_state: 'unfiled', clinical_stem: '当前已保存题干', options: [{ label: 'A', text: '答案甲' }], answer: 'A', explanation: '', knowledge_points: [{ id: 'p1', topic: '临床考点', subject: '内科', version_id: 'v1' }] };
const original = { ...q, version: 1, clinical_stem: '原始AI题干' };
const candidate = (id, style = 'concise') => ({ id, style, title: `候选${id}`, scope: { kind: 'point', point_ids: ['p1'] }, rules: [{ id: `${id}-r`, text: `经验${id}`, reason: '说明', field: 'clinical_stem', before: '原始AI题干', after: '当前已保存题干', medical_change: false }] });
const job = (extra = {}) => ({ id: 'job1', question_id: 'q1', status: 'succeeded', revision: 1, original_version: 1, current_version: 3, can_import: true, source_current: true, candidates: [candidate('a'), candidate('b', 'detailed'), candidate('c', 'checklist')], ...extra });
function fixture(t, permissions = ['question:view', 'question:edit', 'question:confirm', 'question:generate', 'learning:analyze', 'learning:save', 'learning:use']) {
  document.body.innerHTML = ''; localStorage.clear(); setAuth('learning-session', { id: 'author', role: 'teacher', permissions });
  const f = { comparisons: 0, analyses: [], imports: [], updates: [], row: job(), cards: [{ id: 'e1', title: '已启用的本人经验', revision: 2, status: 'active', scope: { kind: 'all' }, rules: [{ id: 'r1', text: '使用患者已给出的信息', medical_change: false }], source_job_id: 'job1', source_candidate_id: 'a', current_version: 3 }] };
  function mock(name, implementation) { if (!(name in api)) { api[name] = implementation; t.after(() => delete api[name]); } else t.mock.method(api, name, implementation); }
  mock('me', async () => currentUser.value);
  mock('learningComparison', async () => { f.comparisons++; return { original, current: q, can_analyze: true, changes: [{ field: 'clinical_stem', before: original.clinical_stem, after: q.clinical_stem }], expectation: { question_id: 'q1', expected_version: 3, expected_library_revision: 4, expected_review_revision: 2 } }; });
  mock('questionLearningAnalyses', async () => ({ jobs: [] }));
  mock('createLearningAnalysis', async (id, input) => { f.analyses.push({ id, input }); return { job: f.row }; });
  mock('learningAnalysis', async () => ({ job: f.row }));
  mock('importLearningAnalysis', async (id, input) => { f.imports.push({ id, input }); const experiences=input.items.map((item,index)=>({...item,id:`saved-${index}`,revision:1,source:{job_id:id,candidate_id:item.candidate_id}}));f.row.imported_experiences={...(f.row.imported_experiences||{}),...Object.fromEntries(experiences.map(c=>[c.candidate_id,c.id]))};return { experiences }; });
  mock('personalExperiences', async () => ({ experiences: f.cards, total: f.cards.length }));
  mock('personalExperienceHistory', async () => ({ versions: f.cards }));
  mock('savePersonalExperience', async (id, input) => { f.updates.push({ id, input }); return { ...f.cards[0], ...input, revision: 3 }; });
  mock('questionPersonalExperiences', async () => ({ snapshot: { version: 'personal-experience-v1', entries: [{ ...f.cards[0], revision: 1, match_reason: '本人全部临床题' }], excluded_count: 2 } }));
  return f;
}
async function component(t, path, props = {}) { const { default: Component } = await import(path); const w = mount(Component, { props, attachTo: document.body, global: { stubs: { Teleport: true, RouterLink: { template: '<a><slot /></a>' } } } }); t.after(() => w.unmount()); await settle(); return w; }
async function openPanel(t, props = {}) { const w = await component(t, './components/QuestionLearningPanel.vue', { question: q, ...props }); await button(w, /比较原始版/).trigger('click'); await settle(); return w; }

test('a zero-review teacher reaches personal experience navigation without shared knowledge permission', t => {
  fixture(t); const paths = visibleNavigation(p => currentUser.value.permissions.includes(p)).flatMap(g => g.items.map(i => i.path));
  assert.ok(paths.includes('/my-experiences')); assert.equal(paths.includes('/knowledge-base'), false);
});
test('comparison opens on demand and compares immutable v1 to the saved current version without calling AI', async t => {
  const f = fixture(t), w = await component(t, './components/QuestionLearningPanel.vue', { question: q }); assert.equal(f.comparisons, 0);
  await button(w, /比较原始版/).trigger('click'); await settle(); assert.match(w.text(), /原始AI题干/); assert.match(w.text(), /当前已保存题干/); assert.match(w.text(), /v1/); assert.match(w.text(), /v3/); assert.equal(f.analyses.length, 0);
});
test('unsaved question edits block paid analysis and explain saving first', async t => {
  const f = fixture(t), w = await openPanel(t, { dirty: true }); assert.equal(button(w, /AI总结修改经验/).element.disabled, true); assert.match(w.text(), /未保存/); assert.equal(f.analyses.length, 0);
});
test('one explicit AI action produces three unchecked editable alternatives and imports only selected edited rules as drafts', async t => {
  const f = fixture(t), w = await openPanel(t); await button(w, /AI总结修改经验/).trigger('click'); await settle();
  assert.equal(f.analyses.length, 1); assert.deepEqual(Object.keys(f.analyses[0].input).sort(), ['expected_library_revision', 'expected_review_revision', 'expected_version', 'note', 'request_key']);
  assert.equal(w.findAll('.learning-candidate').length, 3); assert.equal(w.findAll('.learning-candidate > label input:checked').length, 0);
  await w.get('[aria-label="选择候选 a"]').setValue(true); await w.get('[aria-label="规则 a-r"]').setValue('老师编辑的经验'); await button(w, /预览所选经验/).trigger('click'); await settle();
  await button(w, /^保存草稿$/).trigger('click'); await settle(); assert.equal(f.imports.length, 1); assert.equal(f.imports[0].input.items[0].status, 'draft'); assert.equal(f.imports[0].input.items.length, 1); assert.equal(f.imports[0].input.items[0].rules[0].text, '老师编辑的经验'); assert.equal(f.imports[0].input.expected_revision, 1); assert.equal(f.analyses.length, 1);
});
test('analysis from a previous source version stays visibly old and cannot be imported', async t => {
  const f = fixture(t); f.row = job({ current_version: 2, source_current: false, can_import: false }); const w = await openPanel(t); await button(w, /AI总结修改经验/).trigger('click'); await settle(); assert.match(w.text(), /不是当前|旧版|已变化/); assert.equal(button(w, /预览所选经验/)?.element.disabled, true); assert.equal(f.imports.length, 0);
});
test('a timed-out learning job is explicitly terminal even after only one of two maximum attempts',async t=>{
 const f=fixture(t);f.row=job({status:'failed',attempts:1,max_attempts:2,error_kind:'timeout',error:'学习分析总预算已耗尽',candidates:[]});const w=await openPanel(t);await button(w,/AI总结修改经验/).trigger('click');await settle();assert.match(w.get('.learning-job').text(),/不会再自动重试/);assert.doesNotMatch(w.get('.learning-job').text(),/已尝试 1 \/ 2/);assert.equal(f.analyses.length,1);
});
test('running learning status describes maximum attempts rather than promising another retry',async t=>{
 const f=fixture(t);f.row=job({status:'running',attempts:1,max_attempts:2,candidates:[]});const w=await openPanel(t);await button(w,/AI总结修改经验/).trigger('click');await settle();assert.match(w.get('.learning-job').text(),/最多 2 次/);assert.match(await helpText(w,'分析执行预算说明'),/共用120秒/);assert.equal(f.analyses.length,1);
});

test('new learning jobs explain independent attempt time and preserve visible retry status',async t=>{
 const f=fixture(t);f.row=job({status:'pending',attempts:1,max_attempts:2,execution_budget_seconds:420,attempt_timeout_seconds:180,candidates:[],attempt_history:[{attempt:1,status:'pending',error_kind:'attempt_timeout'}]});const w=await openPanel(t);await button(w,/AI总结修改经验/).trigger('click');await settle();assert.match(w.get('.learning-job').text(),/等待自动重试/);const hint=await helpText(w,'分析执行预算说明');assert.match(hint,/单次模型分析最多180秒/);assert.match(hint,/共用420秒/);assert.match(w.get('.learning-job').text(),/本次模型分析超时/);assert.equal(f.analyses.length,1);
});

test('teacher explicitly converts selected candidates into expression cards without another model request',async t=>{
 const f=fixture(t);const comparison=api.learningComparison;let saved;
 t.mock.method(api,'learningComparison',async(...args)=>({...await comparison(...args),expression_cards_supported:true}));
 t.mock.method(api,'learningKnowledgeCards',async(id,input)=>{saved=input;return{cards:input.items.map((item,i)=>({id:'pkc-expression-'+i,content:{expression:{instructions:item.rules[0].text}}}))}});
 const w=await openPanel(t);await button(w,/AI总结修改经验/).trigger('click');await settle();await w.get('[aria-label="选择候选 a"]').setValue(true);await button(w,/预览所选经验/).trigger('click');await w.get('[aria-label="候选保存位置"]').setValue('cards');await settle();assert.equal(w.get('[aria-label="将候选作为表达要求保存"]').element.checked,false);
 await w.get('[aria-label="将候选作为表达要求保存"]').setValue(true);await button(w,/^保存草稿$/).trigger('click');await settle();assert.equal(saved.items[0].as_expression,true);assert.equal(f.analyses.length,1);assert.match(w.text(),/已保存 1 条个人知识卡/);
});
test('candidate conversion that drops expression fields preserves the selected draft and reports failure',async t=>{
 const f=fixture(t);const comparison=api.learningComparison;
 t.mock.method(api,'learningComparison',async(...args)=>({...await comparison(...args),expression_cards_supported:true}));
 t.mock.method(api,'learningKnowledgeCards',async()=>({cards:[{id:'pkc-old-server',content:{custom_text:'旧服务只保留了参考内容'}}]}));
 const w=await openPanel(t);await button(w,/AI总结修改经验/).trigger('click');await settle();await w.get('[aria-label="选择候选 a"]').setValue(true);await w.get('[aria-label="规则 a-r"]').setValue('保留本窗口的表达编辑');await button(w,/预览所选经验/).trigger('click');await w.get('[aria-label="候选保存位置"]').setValue('cards');await w.get('[aria-label="将候选作为表达要求保存"]').setValue(true);await button(w,/^保存草稿$/).trigger('click');await settle();
 assert.match(w.text(),/未保留表达要求/);assert.doesNotMatch(w.text(),/已保存 1 条个人知识卡/);assert.equal(w.get('[aria-label="规则 a-r"]').element.value,'保留本窗口的表达编辑');assert.equal(w.get('[aria-label="选择候选 a"]').element.checked,true);assert.ok(w.find('.learning-import-preview').exists());assert.equal(f.analyses.length,1);
});
test('a teacher can disable their experience using its displayed revision without KB management', async t => {
  const f = fixture(t), w = await component(t, './pages/PersonalExperiencesPage.vue'); assert.match(w.text(), /已启用的本人经验/); await button(w, /^停用$/).trigger('click'); await settle(); assert.equal(f.updates.length, 1); assert.equal(f.updates[0].input.expected_revision, 2); assert.equal(f.updates[0].input.status, 'draft');
});
test('experience application is independent of knowledge mode and defaults on for a use-authorized teacher', async t => {
  fixture(t); const w = await component(t, './components/PersonalExperienceSelector.vue', { modelValue: true }); assert.equal(w.get('[aria-label="应用命题规则"]').element.checked, true); assert.match(await helpText(w,'命题规则采用说明'),/按本人的启用状态、考点和专业匹配/); await w.get('input').setValue(false); assert.deepEqual(w.emitted('update:modelValue').at(-1), [false]);
});
test('adoption shows the frozen personal revision and clears private data immediately after revocation', async t => {
  fixture(t); const w = await component(t, './components/PersonalExperienceProvenance.vue', { question: q }); await button(w, /查看采用的个人经验/).trigger('click'); await settle(); assert.match(w.text(), /版本 1/); assert.match(w.text(), /2.*未纳入/); updateUser({ ...currentUser.value, permissions: ['question:view'] }); await settle(); assert.doesNotMatch(w.text(), /已启用的本人经验/);
});

test('closing a panel with edited candidates asks before discarding the unsaved work', async t => {
  fixture(t); const w=await openPanel(t); await button(w,/AI总结修改经验/).trigger('click'); await settle();
  await w.get('[aria-label="选择候选 a"]').setValue(true); await w.get('[aria-label="规则 a-r"]').setValue('不能悄悄丢失的候选编辑');
  t.mock.method(globalThis,'confirm',()=>false); await button(w,/收起版本比较与经验/).trigger('click'); await settle();
  assert.equal(w.find('.learning-content').exists(),true); assert.equal(w.get('[aria-label="规则 a-r"]').element.value,'不能悄悄丢失的候选编辑');
});
test('same-question source changes preserve candidate edits while blocking stale writes',async t=>{
  fixture(t);const w=await openPanel(t);await button(w,/AI总结修改经验/).trigger('click');await settle();await w.get('[aria-label="选择候选 a"]').setValue(true);await w.get('[aria-label="规则 a-r"]').setValue('保留旧候选供本人核对');
  await w.setProps({question:{...q,library_revision:8}});await settle();assert.equal(w.find('.learning-content').exists(),true);assert.equal(w.get('[aria-label="规则 a-r"]').element.value,'保留旧候选供本人核对');assert.equal(button(w,/预览所选经验/).element.disabled,true);assert.match(w.text(),/已变化|已更新/);
});
test('candidate edits warn before browser refresh instead of silently dropping teacher work',async t=>{
  fixture(t);const w=await openPanel(t);await button(w,/AI总结修改经验/).trigger('click');await settle();await w.get('[aria-label="规则 a-r"]').setValue('尚未导入的修改');
  const event=new Event('beforeunload',{cancelable:true});window.dispatchEvent(event);assert.equal(event.defaultPrevented,true);
});
test('a use-only generation identity never requests private management data without question view',async t=>{
 fixture(t,['question:generate','learning:use']);let reads=0;t.mock.method(api,'personalExperiences',async()=>{reads++;return{experiences:[],total:0}});
 const w=await component(t,'./components/PersonalExperienceSelector.vue',{modelValue:true});assert.equal(w.get('[aria-label="应用命题规则"]').element.checked,true);assert.equal(reads,0);assert.doesNotMatch(w.text(),/管理个人知识库/);
});
test('an inactive use permission does not expose personal experience navigation',t=>{
 fixture(t,['question:view','learning:use']);const paths=visibleNavigation(p=>currentUser.value.permissions.includes(p)).flatMap(g=>g.items.map(i=>i.path));assert.equal(paths.includes('/my-experiences'),false);
});
test('medical candidates require explicit verification before activation and send the acknowledgement',async t=>{
 const f=fixture(t);f.row.candidates[0].rules[0].medical_change=true;const w=await openPanel(t);await button(w,/AI总结修改经验/).trigger('click');await settle();await w.get('[aria-label="选择候选 a"]').setValue(true);await button(w,/预览所选经验/).trigger('click');await settle();
 assert.equal(button(w,/保存并启用/).element.disabled,true);await w.get('[aria-label="已核对医学变化"]').setValue(true);await button(w,/保存并启用/).trigger('click');await settle();assert.equal(f.imports[0].input.items[0].confirm_medical_changes,true);assert.equal(f.imports[0].input.items[0].status,'active');
});
test('saving a selected candidate does not mark an unrelated edited candidate as saved',async t=>{
 fixture(t);const w=await openPanel(t);await button(w,/AI总结修改经验/).trigger('click');await settle();await w.get('[aria-label="选择候选 a"]').setValue(true);await w.get('[aria-label="规则 b-r"]').setValue('另外一版还没导入的修改');await button(w,/预览所选经验/).trigger('click');await button(w,/^保存草稿$/).trigger('click');await settle();
 assert.equal(w.get('[aria-label="选择候选 a"]').element.disabled,true);assert.match(w.text(),/尚未保存/);const e=new Event('beforeunload',{cancelable:true});window.dispatchEvent(e);assert.equal(e.defaultPrevented,true);
});
test('archived experience can be restored as a draft without another AI analysis',async t=>{
 const f=fixture(t);f.cards[0].status='archived';const w=await component(t,'./pages/PersonalExperiencesPage.vue');await button(w,/恢复为草稿/).trigger('click');await settle();assert.equal(f.updates[0].input.status,'draft');assert.equal(f.analyses.length,0);
});
test('an empty successful HTTP response never claims that edited experiences were saved',async t=>{
 fixture(t);t.mock.method(api,'importLearningAnalysis',async()=>({experiences:[]}));const w=await openPanel(t);await button(w,/AI总结修改经验/).trigger('click');await settle();await w.get('[aria-label="选择候选 a"]').setValue(true);await button(w,/预览所选经验/).trigger('click');await button(w,/^保存草稿$/).trigger('click');await settle();assert.match(w.text(),/保存结果缺少经验记录/);assert.equal(w.get('[aria-label="选择候选 a"]').element.disabled,false);assert.match(w.text(),/尚未保存/);
});
test('renaming an already verified medical experience preserves verification without a repeated acknowledgement',async t=>{
 const f=fixture(t);f.cards[0].medical_confirmed=true;f.cards[0].rules[0].medical_change=true;const w=await component(t,'./pages/PersonalExperiencesPage.vue');await button(w,/^修订$/).trigger('click');await w.get('[aria-label="经验标题"]').setValue('只改标题');assert.equal(w.find('[aria-label="核对修订的医学变化"]').exists(),false);assert.equal(button(w,/保存并启用/).element.disabled,false);
});
test('successful analysis can still explain a previous bounded protocol retry',async t=>{
 const f=fixture(t);f.row.attempt_history=[{attempt:1,status:'pending',error_kind:'protocol_error'},{attempt:2,status:'succeeded'}];const w=await openPanel(t);await button(w,/AI总结修改经验/).trigger('click');await settle();assert.match(w.text(),/查看分析尝试记录/);assert.match(w.text(),/候选结构不符合要求/);assert.match(w.text(),/第 2 次/);
});


test('C6 another window importing a locally edited candidate preserves its dirty copy until explicit discard',async t=>{
 const f=fixture(t);const w=await openPanel(t);await button(w,/AI总结修改经验/).trigger('click');await settle();
 await w.get('[aria-label="选择候选 a"]').setValue(true);await w.get('[aria-label="规则 a-r"]').setValue('本窗口独有但尚未保存的文字');
 f.row={...f.row,imported_experiences:{a:'other-window-e1'}};window.dispatchEvent(new Event('aigo:refresh-navigation-badges'));await settle();await settle();
 assert.equal(w.get('[aria-label="规则 a-r"]').element.value,'本窗口独有但尚未保存的文字');assert.match(w.text(),/另一窗口已导入/);
 let e=new Event('beforeunload',{cancelable:true});window.dispatchEvent(e);assert.equal(e.defaultPrevented,true);
 t.mock.method(globalThis,'confirm',()=>false);await button(w,/放弃本地副本/).trigger('click');await settle();assert.equal(w.get('[aria-label="规则 a-r"]').element.value,'本窗口独有但尚未保存的文字');
 t.mock.method(globalThis,'confirm',()=>true);await button(w,/放弃本地副本/).trigger('click');await settle();assert.equal(w.get('[aria-label="规则 a-r"]').element.value,'经验a');
 e=new Event('beforeunload',{cancelable:true});window.dispatchEvent(e);assert.equal(e.defaultPrevented,false);assert.equal(f.imports.length,0);
});

test('C6 learning revocation clears private drafts and ignores a response already in flight',async t=>{
 const f=fixture(t);const w=await openPanel(t);await button(w,/AI总结修改经验/).trigger('click');await settle();await w.get('[aria-label="规则 a-r"]').setValue('撤权后不应可见的私有编辑');
 let resolve;t.mock.method(api,'learningAnalysis',()=>new Promise(yes=>{resolve=yes}));
 window.dispatchEvent(new Event('aigo:refresh-navigation-badges'));await settle();
 updateUser({...currentUser.value,permissions:['question:generate']});await settle();assert.equal(w.find('.learning-content').exists(),false);
 resolve({job:job()});await settle();assert.equal(w.find('.learning-content').exists(),false);assert.doesNotMatch(w.text(),/撤权后|经验a/);
 const e=new Event('beforeunload',{cancelable:true});window.dispatchEvent(e);assert.equal(e.defaultPrevented,false);assert.equal(f.imports.length,0);
});

test('C6 a forbidden learning refresh clears saved comparison and candidate caches',async t=>{
 fixture(t);const w=await openPanel(t);await button(w,/AI总结修改经验/).trigger('click');await settle();await w.get('[aria-label="规则 a-r"]').setValue('禁止保留的私有编辑');
 t.mock.method(api,'learningAnalysis',async()=>{throw Object.assign(new Error('身份已撤权'),{status:403})});window.dispatchEvent(new Event('aigo:refresh-navigation-badges'));await settle();await settle();
 assert.equal(w.find('.learning-content').exists(),false);assert.doesNotMatch(w.text(),/禁止保留|原始AI题干/);
});


for (const action of ['analyze','import']) {
 test(`C6 a forbidden ${action} response clears the private learning draft immediately`,async t=>{
  fixture(t);const w=await openPanel(t);
  if(action==='import'){
   await button(w,/AI总结修改经验/).trigger('click');await settle();await w.get('[aria-label="选择候选 a"]').setValue(true);await w.get('[aria-label="规则 a-r"]').setValue('授权窗口内尚未保存的文字');await button(w,/预览所选经验/).trigger('click');
   t.mock.method(api,'importLearningAnalysis',async()=>{throw Object.assign(new Error('已撤销保存权限'),{status:403})});await button(w,/^保存草稿$/).trigger('click');
  }else{
   t.mock.method(api,'createLearningAnalysis',async()=>{throw Object.assign(new Error('已撤销学习权限'),{status:403})});await button(w,/AI总结修改经验/).trigger('click');
  }
  await settle();assert.equal(w.find('.learning-content').exists(),false);assert.doesNotMatch(w.text(),/原始AI题干|尚未保存的文字/);
 });
}


test('C6 a remote import without local edits does not add an unsaved-work confirmation',async t=>{
 const f=fixture(t);const w=await openPanel(t);await button(w,/AI总结修改经验/).trigger('click');await settle();
 f.row={...f.row,imported_experiences:{a:'saved-remotely'}};window.dispatchEvent(new Event('aigo:refresh-navigation-badges'));await settle();await settle();
 assert.equal(w.findAll('button').some(b=>/放弃本地副本/.test(b.text())),false);const e=new Event('beforeunload',{cancelable:true});window.dispatchEvent(e);assert.equal(e.defaultPrevented,false);
 let prompts=0;t.mock.method(globalThis,'confirm',()=>{prompts++;return false});await button(w,/收起版本比较与经验/).trigger('click');await settle();assert.equal(prompts,0);assert.equal(w.find('.learning-content').exists(),false);
});

test('selected AI candidates can be saved as private cards without the legacy rule import',async t=>{
 const f=fixture(t);let imported;t.mock.method(api,'learningKnowledgeCards',async(id,input)=>{imported={id,input};return {cards:input.items.map((item,i)=>({...item,id:`pkc-${i}`,revision:1}))};});
 const w=await openPanel(t);await button(w,/AI总结修改经验/).trigger('click');await settle();await w.get('[aria-label="选择候选 a"]').setValue(true);await w.get('[aria-label="规则 a-r"]').setValue('只把选改内容转换成卡');await button(w,/预览所选经验/).trigger('click');await w.get('[aria-label="候选保存位置"]').setValue('cards');await button(w,/^保存草稿$/).trigger('click');await settle();
 assert.equal(imported.input.items.length,1);assert.equal(imported.input.items[0].rules[0].text,'只把选改内容转换成卡');assert.equal(imported.input.items[0].status,'draft');assert.equal(f.imports.length,0);assert.match(w.text(),/个人知识卡/);
});

// Retain complete regression coverage for the reversible legacy UI.
test.beforeEach(t=>t.mock.method(questionReviewUi,"enabled",()=>true));
