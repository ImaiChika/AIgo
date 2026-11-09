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
for (const key of ['window', 'document', 'Document', 'Element', 'HTMLElement', 'SVGElement', 'Node', 'Event', 'history', 'location', 'localStorage', 'sessionStorage']) globalThis[key] = dom.window[key];
dom.window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
const { mount, flushPromises } = await import('@vue/test-utils'), { nextTick } = await import('vue');
const { api } = await import('./api.js'), { setAuth, updateUser, currentUser } = await import('./auth.js');
const { createRouter, createMemoryHistory } = await import('vue-router');
const { default: NewPage } = await import('./pages/NewQuestionsPage.vue');
const { default: BankPage } = await import('./pages/BankPage.vue');
const { default: Dashboard } = await import('./pages/MyDashboardPage.vue');
Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
globalThis.confirm = () => true;
const settle = async () => { await flushPromises(); await nextTick(); };
const deferred = () => { let resolve; const promise = new Promise(r => { resolve = r; }); return { promise, resolve }; };
const button = (w, re) => w.findAll('button').find(b => re.test(b.text()));
const question = (extra = {}) => ({ id: 'q1', owner_id: 'author', version: 2, review_revision: 4, library_revision: 7, library_state: 'unfiled', status: 'ai_reviewed', clinical_stem: '原题目', options: [{ label: 'A', text: '甲' }], answer: 'A', explanation: '', ...extra });
const expectations = { question_id: 'q1', expected_version: 2, expected_library_revision: 7, expected_review_revision: 4 };
function fixture(t, permissions = ['question:view', 'question:edit', 'question:confirm']) {
  localStorage.clear(); document.body.innerHTML = '';
  setAuth('c3-session', { id: 'author', role: 'teacher', permissions, account_permissions: permissions });
  const f = { q: question(), confirms: [], updates: [], deletes: [], restores: [] };
  t.mock.method(api, 'myNewQuestions', async () => ({ questions: f.q ? [f.q] : [], total: f.q ? 1 : 0 }));
  t.mock.method(api, 'availableReviewFlows', async () => ({ flows: [] }));
  t.mock.method(api, 'confirmQuestions', async items => { f.confirms.push(items); f.q = null; return { confirmed: items.length }; });
  t.mock.method(api, 'updateQuestion', async (id, p) => { f.updates.push(p); f.q = { ...f.q, ...p, version: f.q.version + 1, library_state: 'unfiled', confirmed_version: 0 }; return f.q; });
  t.mock.method(api, 'listQuestions', async (_page,_size,_bank,_tier,_scope,library) => { const included = f.q && (!library || f.q.library_state === library); return { questions: included ? [f.q] : [], total: included ? 1 : 0 }; });
  t.mock.method(api, 'listProfessions', async () => ({ professions: [] }));
  t.mock.method(api, 'listQuestionShares', async () => ({ items: [] }));
  t.mock.method(api, 'getTaskByQuestion', async () => null);
  t.mock.method(api, 'libraryDelete', async items => { f.deletes.push(items); f.q = null; return { deleted: 1 }; });
  t.mock.method(api, 'libraryRestore', async items => { f.restores.push(items); f.q = null; return { restored: 1 }; });
  t.mock.method(api, 'mySummary', async () => ({ new_questions_count: 1 }));
  return f;
}
async function mountPage(t, component, query = {}) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component }, { path: '/new-questions', component: NewPage }, { path: '/my', component: Dashboard }, { path: '/bank', component: BankPage }, { path: '/review', component: { template: '<div />' } }, { path: '/settings', component: { template: '<div />' } }] });
  await router.push({ path: '/', query }); await router.isReady();
  const w = mount(component, { attachTo: document.body, global: { plugins: [router], stubs: { AICheckScoreButton: true, QuestionDetailModal: true, PreviousReviewHistory: true, ReviewHistoryPanel: true } } });
  t.after(() => w.unmount()); await settle(); return { w, router };
}

async function learningDraftPage(t, review = false) {
 const permissions=['question:view','question:edit','question:confirm','learning:analyze','learning:save',...(review?['review:submit']:[])];
 const f=fixture(t,permissions);f.rows=[f.q,question({id:'q2',clinical_stem:'第二题'})];f.retained=null;f.imports=[];f.submits=[];
 f.job={id:'draft-job',question_id:'q1',status:'succeeded',revision:1,current_version:2,original_version:1,source_current:true,can_import:true,candidates:['a','b','c'].map((id,i)=>({id,style:['concise','detailed','checklist'][i],title:'候选'+id,scope:{kind:'all'},rules:[{id:id+'-rule',text:'原经验'+id,field:'clinical_stem',before:'原始',after:'当前',medical_change:false}]}))};
 t.mock.method(api,'me',async()=>currentUser.value);
 t.mock.method(api,'myNewQuestions',async()=>({questions:f.rows,total:f.rows.length}));
 t.mock.method(api,'getQuestion',async id=>f.retained?.id===id?f.retained:f.rows.find(q=>q.id===id));
 t.mock.method(api,'learningComparison',async id=>{const q=f.retained?.id===id?f.retained:f.rows.find(q=>q.id===id);return{original:{...q,version:1,clinical_stem:'原始'},current:q,changes:[{field:'clinical_stem',before:'原始',after:q.clinical_stem}],expectation:{question_id:id,expected_version:q.version,expected_library_revision:q.library_revision,expected_review_revision:q.review_revision},can_analyze:true}});
 t.mock.method(api,'questionLearningAnalyses',async()=>({jobs:[f.job]}));
 t.mock.method(api,'learningAnalysis',async()=>({job:f.job}));
 t.mock.method(api,'importLearningAnalysis',async(id,input)=>{f.imports.push(input);return{experiences:input.items.map((c,i)=>({id:'saved-'+i,source:{candidate_id:c.candidate_id}}))}});
 if(review){t.mock.method(api,'availableReviewFlows',async()=>({flows:[{id:'flow-one',name:'可选流程',round_count:1,completion_mode:'on_last_round'}]}));t.mock.method(api,'submitReviewBatch',async(...args)=>{f.submits.push(args);return{submitted:1}})}
 const page=await mountPage(t,NewPage),w=page.w;
 await button(w,/比较原始版/).trigger('click');await settle();
 await w.get('[aria-label="选择候选 a"]').setValue(true);await w.get('[aria-label="规则 a-rule"]').setValue('本窗口尚未导入的候选编辑');
 return{...page,f};
}

test('C6 checkbox-driven selection respects the current learning draft guard',async t=>{
 const {w}=await learningDraftPage(t);t.mock.method(globalThis,'confirm',()=>false);
 await w.findAll('.new-question-card')[1].get('input[type=checkbox]').setValue(true);await settle();
 assert.equal(w.get('[aria-label="规则 a-rule"]').element.value,'本窗口尚未导入的候选编辑');assert.match(w.get('.new-question-detail').text(),/原题目/);assert.equal(w.findAll('.new-question-card')[1].get('input[type=checkbox]').element.checked,false);
});
test('C6 a confirmed source moving out of arrangement retains its learning draft and refreshes current guards',async t=>{
 const {w,f}=await learningDraftPage(t);f.retained={...f.q,library_state:'active',confirmed_version:2,library_revision:8};f.rows=f.rows.filter(q=>q.id!=='q1');
 window.dispatchEvent(new Event('aigo:refresh-navigation-badges'));await settle();await settle();
 assert.equal(w.get('[aria-label="规则 a-rule"]').element.value,'本窗口尚未导入的候选编辑');assert.ok(w.findAll('[role="status"]').some(n=>/源题已移出待整理列表/.test(n.text())));assert.equal(w.get('.edit-textarea').element.disabled,true);assert.equal(w.get('.confirm-current-button').element.disabled,true);
 await button(w,/重新读取版本/).trigger('click');await settle();await button(w,/预览所选经验/).trigger('click');await button(w,/^保存草稿$/).trigger('click');await settle();
 assert.equal(f.imports[0].expected_library_revision,8);
});
test('C6 a remote v3 revision retains local candidate text but blocks importing its old v2 analysis',async t=>{
 const {w,f}=await learningDraftPage(t);f.rows=[{...f.q,version:3,clinical_stem:'远端第三版'},f.rows[1]];f.job={...f.job,source_current:false,can_import:false};
 window.dispatchEvent(new Event('aigo:refresh-navigation-badges'));await settle();await settle();
 assert.equal(w.get('[aria-label="规则 a-rule"]').element.value,'本窗口尚未导入的候选编辑');assert.equal(button(w,/预览所选经验/).element.disabled,true);
});
test('C6 optional review submission asks about learning draft before leaving the source',async t=>{
 const {w,f}=await learningDraftPage(t,true);const prompts=[];t.mock.method(globalThis,'confirm',text=>{prompts.push(text);return false});
 await w.get('.submit-current-button').trigger('click');await settle();assert.equal(f.submits.length,0);assert.match(prompts[0],/候选|个人知识库/);assert.equal(w.get('[aria-label="规则 a-rule"]').element.value,'本窗口尚未导入的候选编辑');
});

test('confirm sends the saved content, library and review revisions without review permission', async t => {
  const f = fixture(t), { w } = await mountPage(t, NewPage);
  await w.get('.confirm-current-button').trigger('click'); await settle();
  assert.deepEqual(f.confirms, [[expectations]]); assert.match(w.text(), /暂无待整理/);
});

test('view and confirm alone allow confirmation but do not expose editable inputs', async t => {
  fixture(t, ['question:view', 'question:confirm']); const { w } = await mountPage(t, NewPage);
  assert.equal(w.get('textarea').element.disabled, true); assert.equal(button(w, /保存微调/).element.disabled, true);
  assert.equal(w.get('.confirm-current-button').element.disabled, false);
});

test('library-only background change preserves dirty text and prevents confirmation and saving', async t => {
  const f = fixture(t), { w } = await mountPage(t, NewPage);
  await w.get('textarea').setValue('未保存内容');
  f.q = { ...f.q, library_revision: 8, library_state: 'active' };
  window.dispatchEvent(new Event('aigo:refresh-navigation-badges')); await settle();
  assert.equal(w.get('textarea').element.value, '未保存内容');
  assert.equal(button(w, /保存微调/).element.disabled, true); assert.match(w.get('[role="alert"]').text(), /变化|改变/);
});

test('switching identities discards the previous editor and ignores its late save response', async t => {
  const f = fixture(t), pending = deferred(); t.mock.method(api, 'updateQuestion', () => pending.promise);
  const { w } = await mountPage(t, NewPage); await w.get('textarea').setValue('旧身份编辑'); await button(w, /保存微调/).trigger('click');
  setAuth('c3-other', { id: 'author', role: 'expert', permissions: ['question:view'] }); await settle();
  pending.resolve({ ...f.q, clinical_stem: '旧身份迟到结果', version: 3 }); await settle();
  assert.doesNotMatch(w.text(), /旧身份迟到结果|原题目/); assert.equal(w.find('textarea').exists(), false);
});

test('active published owner can revise and return the saved version to arrangement', async t => {
  const f = fixture(t); f.q = question({ status: 'published', library_state: 'active', confirmed_version: 2, confirmation_source: 'legacy' });
  const { w } = await mountPage(t, BankPage, { scope: 'personal' }); await w.get('.q-content').trigger('click'); await settle();
  const edit = button(w, /修订题目/); assert.ok(edit, '普通库必须提供作者修订入口'); await edit.trigger('click');
  await w.get('textarea[aria-label="修订题干"]').setValue('修订后的题干'); await button(w, /保存修订/).trigger('click'); await settle();
  assert.equal(f.updates.length, 1); assert.equal(f.updates[0].expected_version, 2); assert.equal(f.updates[0].expected_library_revision, 7); assert.equal(f.updates[0].expected_review_revision, 4);
  assert.match(w.text(), /重新确认|题目整理/); assert.equal(w.find('.q-content').exists(), false);
});

test('personal published normal tab keeps legacy share and admin withdrawal actions', async t => {
  const f = fixture(t, ['question:view', 'question:share', 'user:manage']); f.q = question({ status: 'published', library_state: 'active', confirmed_version: 2, confirmation_source: 'legacy' });
  const { w } = await mountPage(t, BankPage, { scope: 'personal' }); await w.get('.q-content').trigger('click'); await settle();
  assert.ok(button(w, /申请分享至全局库/)); assert.ok(button(w, /^撤回$/)); assert.ok(button(w, /提交所选至全局库/));
});

test('delete binds all revisions and immediately clears selection so export cannot submit removed IDs', async t => {
  const f = fixture(t, ['question:view', 'question:download']); f.q = question({ library_state: 'active', confirmed_version: 2 });
  const { w } = await mountPage(t, BankPage, { scope: 'personal' }); await w.get('.q-checkbox').setValue(true); await w.get('.delete-btn').trigger('click'); await settle();
  assert.deepEqual(f.deletes, [[expectations]]); assert.doesNotMatch(w.text(), /已选 1 道/); assert.equal(button(w, /^导出 Excel$/).element.disabled, true);
});

test('restore binds all revisions and is available only for explicit recoverable owner deletes', async t => {
  const f = fixture(t); f.q = question({ library_state: 'deleted', deleted_reason: 'owner_delete' });
  const { w } = await mountPage(t, BankPage, { scope: 'personal', tier: 'deleted' }); await button(w, /^恢复$/).trigger('click'); await settle(); assert.deepEqual(f.restores, [[expectations]]);
});

test('deleted records without an explicit owner-delete reason do not offer recovery or another deletion', async t => {
  const f = fixture(t); f.q = question({ library_state: 'deleted', deleted_reason: '' });
  const { w } = await mountPage(t, BankPage, { scope: 'personal', tier: 'deleted' });
  assert.equal(button(w, /^恢复$/), undefined); assert.equal(w.find('.delete-btn').exists(), false);
});

test('pure reviewer dashboard does not advertise an inaccessible arrangement entry', async t => {
  fixture(t, ['question:view', 'review:do']); const { w } = await mountPage(t, Dashboard);
  assert.equal(w.findAll('a').filter(a => a.attributes('href') === '/new-questions').length, 0);
});

test('ordinary-library unsaved revisions survive background changes and block switching and stale saves', async t => {
  const f = fixture(t); f.q = question({ library_state: 'active', confirmed_version: 2 });
  const { w } = await mountPage(t, BankPage, { scope: 'personal' }); await w.get('.q-content').trigger('click'); await button(w, /修订题目/).trigger('click');
  await w.get('textarea[aria-label="修订题干"]').setValue('未保存修订');
  await button(w, /^删除题库$/).trigger('click'); await settle();
  assert.equal(w.get('textarea[aria-label="修订题干"]').element.value, '未保存修订');
  assert.equal(w.find('.tier-tab.active').text(), '普通题库');
  f.q = { ...f.q, library_revision: 8 };
  window.dispatchEvent(new Event('aigo:refresh-navigation-badges')); await settle();
  assert.match(w.get('[role="alert"]').text(), /变化/); assert.equal(button(w, /保存修订/).element.disabled, true); assert.equal(f.updates.length, 0);
});

test('a revoked view permission clears ordinary-library data and rejects its late list response', async t => {
  const f = fixture(t), pending = deferred(); t.mock.method(api, 'listQuestions', () => pending.promise);
  const { w } = await mountPage(t, BankPage, { scope: 'personal' });
  updateUser({ id: 'author', role: 'teacher', permissions: [] }); await settle();
  pending.resolve({ questions: [f.q], total: 1 }); await settle();
  assert.doesNotMatch(w.text(), /原题目/); assert.equal(w.find('.q-content').exists(), false);
});

test('late confirmation response cannot modify a newly selected identity workspace', async t => {
  const f = fixture(t), pending = deferred(); t.mock.method(api, 'confirmQuestions', () => pending.promise);
  const { w } = await mountPage(t, NewPage); await w.get('.confirm-current-button').trigger('click');
  f.q = question({ id: 'q2', owner_id: 'other', clinical_stem: '新身份题目' }); setAuth('c3-other', { id: 'other', role: 'teacher', permissions: ['question:view', 'question:confirm'] });
  await settle(); pending.resolve({ confirmed: 1 }); await settle();
  assert.match(w.text(), /新身份题目/); assert.doesNotMatch(w.text(), /已确认入库 1/);
});

test('file-download failure remains within the export operation and is visible to the teacher', async t => {
  const f = fixture(t, ['question:view', 'question:download']); f.q = question({ library_state: 'active', confirmed_version: 2 });
  t.mock.method(api, 'exportXlsx', async () => ({ filename: 'test.xlsx', count: 1 }));
  t.mock.method(api, 'downloadExport', async () => { throw new Error('授权已撤销'); });
  const { w } = await mountPage(t, BankPage, { scope: 'personal' }); await w.get('.q-checkbox').setValue(true); await button(w, /^导出 Excel$/).trigger('click'); await settle();
  assert.match(w.get('.toast').text(), /导出失败.*授权已撤销/); assert.equal(button(w, /^导出 Excel$/).element.disabled, false);
});

test('reviewer dashboard offers a switch only to an identity with the complete arrangement capability', async t => {
  fixture(t, ['question:view', 'review:do']);
  setAuth('c3-expert-session', { id: 'author', role: 'expert', permissions: ['question:view', 'review:do'], account_permissions: ['question:view', 'question:confirm', 'review:do'], roles: ['expert','teacher'], role_permissions: { expert: ['question:view','review:do'], teacher: ['question:view','question:confirm'] } });
  const { w } = await mountPage(t, Dashboard); assert.match(w.get('[data-kind="crossNewQuestions"]').text(), /命题教师/); assert.equal(w.find('[data-kind="newQuestions"]').exists(), false);
});

test('formal-only legacy identity keeps its explicitly restricted tab without new-library privileges', async t => {
  const f = fixture(t, ['question:view_formal', 'question:download']); f.q = question({ status: 'published', library_state: 'active' });
  const calls = []; t.mock.method(api, 'listQuestions', async (...args) => { calls.push(args); return { questions: [f.q], total: 1 }; });
  const { w } = await mountPage(t, BankPage, { scope: 'personal' });
  assert.match(w.text(), /正式题库（历史授权）/); assert.doesNotMatch(w.text(), /普通题库|删除题库/);
  assert.equal(calls[0][3], 'formal'); assert.equal(calls[0][5], ''); await w.get('.q-content').trigger('click'); assert.equal(button(w, /修订题目/), undefined);
});

test('legacy restricted dashboard retains an honest historical-tier entry', async t => {
  fixture(t, ['question:view_formal']); const { w } = await mountPage(t, Dashboard);
  assert.match(w.text(), /正式题库（历史授权）/); assert.doesNotMatch(w.text(), /普通题库|暂无题库权限/);
  assert.equal(w.findAll('a').filter(a => a.attributes('href')?.includes('tier=formal')).length, 1);
});

test('library API wrappers send the snapshot items through to all three write endpoints', async t => {
  setAuth('c3-wire', { id: 'author', role: 'teacher', permissions: ['question:view','question:confirm'] });
  const { api: wireAPI } = await import('./api.js?c3-library-wire');
  const writes = [];
  t.mock.method(globalThis, 'fetch', async (url, options) => { writes.push({ url, body: JSON.parse(options.body) }); return { ok: true, status: 200, json: async () => ({ status: 'ok' }) }; });
  await wireAPI.confirmQuestions([expectations]); await wireAPI.libraryDelete([expectations]); await wireAPI.libraryRestore([expectations]);
  assert.deepEqual(writes.map(x => x.body), [{ items: [expectations] }, { items: [expectations] }, { items: [expectations] }]);
  assert.deepEqual(writes.map(x => x.url), ['/api/questions/confirm','/api/questions/library-delete','/api/questions/library-restore']);
});

test('a zero-round teacher sees the arrangement navigation badge without any review permission', async t => {
  fixture(t); const { default: Sidebar } = await import('./components/SidebarNavigation.vue');
  const { w } = await mountPage(t, Sidebar); await button(w, /^命题工作/).trigger('click');
  const link = w.findAll('a').find(a => a.attributes('href') === '/new-questions');
  assert.ok(link); assert.equal(link.get('[aria-label="1 项待办"]').text(), '1');
});

function mockSuccessfulDownload(t) {
  t.mock.method(api, 'downloadExport', async () => new Blob(['test workbook']));
  t.mock.method(URL, 'createObjectURL', () => 'blob:report-test');
  t.mock.method(URL, 'revokeObjectURL', () => {});
  t.mock.method(dom.window.HTMLAnchorElement.prototype, 'click', () => {});
}

test('export-all uses applied keyword, profession and status instead of an unsubmitted search draft', async t => {
  const f = fixture(t, ['question:view', 'question:download']); f.q = question({ library_state: 'active', confirmed_version: 2 });
  t.mock.method(api, 'listProfessions', async () => ({ professions: ['内科', '外科'] }));
  t.mock.method(api, 'searchQuestions', async () => ({ questions: [f.q], total: 1 }));
  const exports = []; t.mock.method(api, 'exportXlsx', async p => { exports.push(p); return {filename:'filtered.xlsx',count:1}; }); mockSuccessfulDownload(t);
  const {w} = await mountPage(t, BankPage, {scope:'personal'});
  await w.get('.search-input').setValue('胃痛'); await w.get('.search-input').trigger('keyup.enter'); await settle();
  await w.get('select[title="按专业筛选"]').setValue('内科'); await settle();
  await w.findAll('.filter-row select')[1].setValue('published'); await settle();
  await w.get('.search-input').setValue('还没有点击搜索的草稿');
  await button(w, /^导出全部 Excel$/).trigger('click'); await settle();
  assert.deepEqual(exports, [{export_all:true,scope:'personal',q:'胃痛',status:'published',professions:['内科']}]);
  assert.match(w.get('.toast').text(), /筛选.*全部 1 道题/); assert.doesNotMatch(w.get('.toast').text(), /当前题库全部/);
});

test('export-all cannot run while a new applied filter request is still loading', async t => {
  const f = fixture(t, ['question:view', 'question:download']); f.q = question({library_state:'active',confirmed_version:2});
  const pending=deferred(),exports=[];
  t.mock.method(api, 'searchQuestions', () => pending.promise);
  t.mock.method(api, 'exportXlsx', async p => {exports.push(p);return {filename:'filtered.xlsx',count:1};}); mockSuccessfulDownload(t);
  const {w}=await mountPage(t,BankPage,{scope:'personal'});
  await w.get('.search-input').setValue('新筛选'); await w.get('.search-input').trigger('keyup.enter');
  assert.equal(button(w,/^导出全部 Excel$/).element.disabled,true);
  await button(w,/^导出全部 Excel$/).trigger('click'); await settle(); assert.equal(exports.length,0);
  pending.resolve({questions:[f.q],total:1});await settle();
  await button(w,/^导出全部 Excel$/).trigger('click');await settle();
  assert.equal(exports[0].q,'新筛选');
});

test('selected export remains an explicit ID set without applying current list filters', async t => {
  const f=fixture(t,['question:view','question:download']); f.q=question({library_state:'active',confirmed_version:2});
  t.mock.method(api,'searchQuestions',async()=>({questions:[f.q],total:1}));
  const exports=[];t.mock.method(api,'exportXlsx',async p=>{exports.push(p);return {filename:'selected.xlsx',count:1};});mockSuccessfulDownload(t);
  const {w}=await mountPage(t,BankPage,{scope:'personal'});
  await w.get('.search-input').setValue('已筛选');await w.get('.search-input').trigger('keyup.enter');await settle();
  await w.get('.q-checkbox').setValue(true);await button(w,/^导出 Excel$/).trigger('click');await settle();
  assert.deepEqual(exports,[{question_ids:['q1'],scope:'personal'}]);assert.match(w.get('.toast').text(),/所选 1 道题/);
});

test('legacy formal-only export-all preserves the legacy scope while carrying its applied search', async t => {
  const f=fixture(t,['question:view_formal','question:download']);f.q=question({status:'published',library_state:'active'});
  t.mock.method(api,'searchQuestions',async()=>({questions:[f.q],total:1}));
  const exports=[];t.mock.method(api,'exportXlsx',async p=>{exports.push(p);return {filename:'legacy.xlsx',count:1};});mockSuccessfulDownload(t);
  const {w}=await mountPage(t,BankPage,{scope:'personal'});
  await w.get('.search-input').setValue('旧正式题');await w.get('.search-input').trigger('keyup.enter');await settle();
  await button(w,/^导出全部 Excel$/).trigger('click');await settle();
  assert.deepEqual(exports,[{export_all:true,scope:'',q:'旧正式题',status:'',professions:[]}]);
});

test('legacy owner archive has an honest source label and is not offered as a recoverable owner delete', async t => {
  const f=fixture(t);f.q=question({library_state:'deleted',status:'archived',deleted_reason:'owner_archive'});
  const {w}=await mountPage(t,BankPage,{scope:'personal',tier:'deleted'});
  assert.match(w.get('.question-item').text(),/本人归档/);assert.equal(button(w,/^恢复$/),undefined);
});

test.beforeEach(t=>t.mock.method(questionReviewUi,"enabled",()=>true));
