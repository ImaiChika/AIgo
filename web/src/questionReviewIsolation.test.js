import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { registerHooks } from 'node:module';
import { parse, compileScript, compileTemplate } from '@vue/compiler-sfc';
import { JSDOM } from 'jsdom';
registerHooks({ load(url, context, next) { if (!url.endsWith('.vue')) return next(url, context); const { descriptor } = parse(readFileSync(fileURLToPath(url), 'utf8')); return { format: 'module', source: descriptor.script || descriptor.scriptSetup ? compileScript(descriptor, { id: url, inlineTemplate: true }).content : compileTemplate({source:descriptor.template.content,filename:url,id:url}).code + '\nexport default {render};', shortCircuit: true }; } });
const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost/' });
for (const key of ['window', 'document', 'Document', 'Element', 'HTMLElement', 'SVGElement', 'Node', 'Event', 'history', 'location', 'localStorage', 'sessionStorage']) globalThis[key] = dom.window[key];
dom.window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
window.scrollTo=()=>{};
const { mount, flushPromises } = await import('@vue/test-utils'), { nextTick } = await import('vue');
const { api } = await import('./api.js'), { setAuth, updateUser, currentUser } = await import('./auth.js');
const { createRouter, createMemoryHistory } = await import('vue-router');
const { default: NewPage } = await import('./pages/NewQuestionsPage.vue');
const { default: BankPage } = await import('./pages/BankPage.vue');
const { default: UsersPage } = await import('./pages/UsersPage.vue');
const { default: RolesPage } = await import('./pages/RolesPage.vue');
const { default: StatsPage } = await import('./pages/StatsPage.vue');
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


const policy=await import('./questionReviewUi.js');
const {visibleNavigation,canAccessQuestionArrangement}=await import('./navigation.js');
const {permissionVisible}=await import('./permissionPresentation.js');
const roles=[{id:'teacher',name:'命题教师',permissions:['question:view','question:confirm','review:submit']},{id:'expert',name:'审题老师',permissions:['review:do','experience:manage']},{id:'legacy',name:'历史流程处理员',permissions:['review:do','question:view']},{id:'kb-reviewer',name:'知识卡审核人',permissions:['kb:view','kb:review']}];
const allPerms=['question:view','question:edit','question:confirm','question:download','question:view_formal','question:view_global','question:share','question:share_review','review:do','review:submit','review:final','review:view_results','review:restore','flow:manage','expert:manage','kb:view','kb:review','user:manage','role:manage','stats:view'];
test('default navigation, route policy and permission catalogue isolate only legacy question review',()=>{
 assert.equal(policy.questionReviewUi.enabled(),false);
 const groups=visibleNavigation(()=>true),paths=groups.flatMap(g=>g.items.map(i=>i.path));
 assert.equal(groups.some(g=>g.id==='review'),false);
 for(const path of policy.legacyQuestionReviewPaths){assert.equal(paths.includes(path),false);assert.equal(policy.questionReviewPathVisible(path+'?id=x#task'),false);assert.equal(policy.questionReviewPathVisible(path.toUpperCase()),false);}
 for(const code of allPerms.filter(policy.isLegacyQuestionReviewPermission))assert.equal(permissionVisible(code),false,code);
 for(const code of ['kb:submit','kb:review','question:confirm','question:edit'])assert.equal(permissionVisible(code),true,code);
 assert.equal(canAccessQuestionArrangement(c=>['question:view','review:submit'].includes(c)),false);
 assert.equal(canAccessQuestionArrangement(c=>['question:view','question:confirm'].includes(c)),true);
});
test('restore switch exposes the preserved paths, identities and permission points',t=>{
 t.mock.method(policy.questionReviewUi,'enabled',()=>true);
 assert.ok(visibleNavigation(()=>true).some(g=>g.id==='review'));
 for(const path of policy.legacyQuestionReviewPaths)assert.equal(policy.questionReviewPathVisible(path),true);
 assert.equal(permissionVisible('review:do'),true);assert.equal(policy.reviewRoleVisible(roles[1]),true);
});
test('real router rejects every legacy URL even with all administrator permissions',async t=>{
 fixture(t,allPerms);const {default:router}=await import('./router.js');
 for(const path of policy.legacyQuestionReviewPaths){for(const variant of [path,path.toUpperCase(),path+'/']){await router.push(variant+'?task=old#review');assert.equal(router.currentRoute.value.path,'/my');}}
 await router.push('/knowledge-base');assert.equal(router.currentRoute.value.path,'/knowledge-base');
});
test('default question arrangement keeps confirmation and edit without mounting human review or loading flows',async t=>{
 const f=fixture(t,allPerms);const reads=t.mock.method(api,'availableReviewFlows',async()=>({flows:[]}));
 const {w}=await mountPage(t,NewPage);assert.ok(button(w,/确认.*库/));assert.equal(button(w,/提交.*审核|提交当前题/),undefined);assert.equal(w.findComponent({name:'AuthorReviewProgress'}).exists(),false);assert.equal(reads.mock.callCount(),0);
 await w.get('.confirm-current-button').trigger('click');await settle();assert.equal(f.confirms.length,1);
});
test('bank hides sharing, rollback and review reads but preserves pending-share edit and delete locks',async t=>{
 const f=fixture(t,allPerms);f.q=question({library_state:'active',confirmed_version:2});
 t.mock.method(api,'listQuestionShares',async()=>({items:[{request:{question_id:'q1',status:'pending'}}]}));
 t.mock.method(api,'getQuestion',async()=>f.q);const reads=t.mock.method(api,'getTaskByQuestion',async()=>null);
 const {w}=await mountPage(t,BankPage,{scope:'personal'});await w.get('.q-content').trigger('click');await settle();
 assert.equal(button(w,/分享|撤回|修订题目/),undefined);assert.equal(w.find('.delete-btn').exists(),false);assert.equal(reads.mock.callCount(),0);assert.equal(w.findComponent({name:'PreviousReviewHistory'}).exists(),false);
});
test('dashboard and statistics keep authoring and AI checks without human-review cards',async t=>{
 fixture(t,allPerms);t.mock.method(api,'mySummary',async()=>({pending_review_count:7,reviewed_count:9}));
 const shares=t.mock.method(api,'listQuestionShares',async()=>({items:[]}));const {w}=await mountPage(t,Dashboard);
 assert.doesNotMatch(w.text(),/待我审核|待我决断|分享审批|我已审核|待我修改/);assert.equal(shares.mock.callCount(),0);
 t.mock.method(api,'stats',async()=>({question_count:3,knowledge_count:1,tier_counts:{working:3,formal:1},ai_check:{verdicts:{pass:3}}}));const {w:stats}=await mountPage(t,StatsPage);assert.match(stats.text(),/AI 质量检查/);assert.doesNotMatch(stats.text(),/审核流程现状|审核状态分布|待审核题库/);
});
function adminFixture(t){
 fixture(t,allPerms);const writes=[];
 const users=[{id:'mixed',username:'mixed',role:'teacher',roles:['teacher','expert'],permissions:['question:view','review:do','review:restore'],direct_permissions:['review:restore'],enabled:true},{id:'reviewer',username:'pure-reviewer',role:'expert',roles:['expert'],permissions:['review:do'],enabled:true},{id:'knowledge',username:'knowledge-reviewer',role:'kb-reviewer',roles:['kb-reviewer'],permissions:['kb:view','kb:review'],enabled:true}];
 t.mock.method(api,'listRoles',async()=>({roles}));t.mock.method(api,'listUsers',async()=>({users}));t.mock.method(api,'listPermissions',async()=>({permissions:allPerms.map(code=>({code,name:code,group:'测试'}))}));
 t.mock.method(api,'updateUser',async(id,p)=>{writes.push({id,...p});return {...users.find(u=>u.id===id),...p,direct_permissions:p.permissions}});
 return {writes,users};
}
test('user controls hide pure question reviewers, keep knowledge reviewers and preserve isolated grants on save',async t=>{
 const f=adminFixture(t);const {w}=await mountPage(t,UsersPage);
 assert.doesNotMatch(w.text(),/pure-reviewer|审题老师/);assert.match(w.text(),/knowledge-reviewer/);
 const mixed=w.findAll('tbody > tr').find(r=>r.text().includes('mixed'));
 await mixed.get('select').setValue('');await settle();assert.ok(f.writes[0].roles.includes('expert'));assert.ok(f.writes[0].permissions.includes('review:restore'));
 await button(w,/分配权限/).trigger('click');await settle();assert.doesNotMatch(w.get('.perm-matrix').text(),/review:|flow:manage|expert:manage|question:share/);assert.match(w.get('.perm-matrix').text(),/kb:review/);
});
test('editing a mixed role preserves hidden review permissions while knowledge review remains assignable',async t=>{
 adminFixture(t);const writes=[];t.mock.method(api,'updateRole',async(id,p)=>{writes.push({id,...p});return p});
 const {w}=await mountPage(t,RolesPage);assert.doesNotMatch(w.text(),/审题老师|历史流程处理员/);
 const teacher=w.findAll('.role-card').find(r=>r.text().includes('命题教师'));await teacher.get('.edit-btn').trigger('click');await settle();
 assert.doesNotMatch(w.get('.perm-picker').text(),/review:|flow:manage|expert:manage|question:share/);assert.match(w.get('.perm-picker').text(),/kb:review/);
 await button(w,/保存修改/).trigger('click');await settle();assert.ok(writes[0].permissions.includes('review:submit'));
});
test('hidden identity classification does not hide delegated knowledge reviewers or elevate old sessions',()=>{
 assert.equal(policy.reviewRoleVisible(roles[1]),false);assert.equal(policy.reviewRoleVisible(roles[2]),false);assert.equal(policy.reviewRoleVisible(roles[3]),true);
 assert.deepEqual(policy.preserveIsolatedRoleIDs(['teacher','expert'],['teacher'],roles),['teacher','expert']);
 assert.equal(policy.presentedWorkRoleName('expert','审题老师'),'已保留身份');
 assert.equal(policy.reviewAccountVisible({id:'self',role:'expert'},roles,'self'),true);
});

test('authoring dashboard reuses summary library counts without fetching question lists',async t=>{
 fixture(t,allPerms);const modes=[];t.mock.method(api,'mySummary',async mode=>{modes.push(mode);return{generated_count:10,new_questions_count:3,library_active_count:7,library_deleted_count:2}});const lists=t.mock.method(api,'listQuestions',async()=>({questions:[],total:0}));const {w}=await mountPage(t,Dashboard);assert.deepEqual(modes,['authoring']);assert.equal(lists.mock.callCount(),0);assert.match(w.text(),/7/);
});
