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
dom.window.HTMLElement.prototype.scrollIntoView = function () {};
dom.window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
dom.window.HTMLDialogElement.prototype.close = function () { this.open = false; };
const { mount, flushPromises, DOMWrapper } = await import('@vue/test-utils'), { nextTick, h } = await import('vue');
const { createRouter, createMemoryHistory, RouterView } = await import('vue-router');
const { api } = await import('./api.js'), { setAuth, currentUser } = await import('./auth.js');
const { default: Page } = await import('./pages/PersonalExperiencesPage.vue');
const { readPersonalKnowledgeDraft, savePersonalKnowledgeDraft } = await import('./personalKnowledgeDraft.js');
const settle = async () => { await flushPromises(); await nextTick(); };
const button = (w, text) => w.findAll('button').find(b => b.text() === text);
const permissions = ['experience:view', 'experience:manage'];
const deferred = () => { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; };

function fixture(t, p = permissions) {
  Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' });
  document.body.innerHTML = ''; localStorage.clear(); sessionStorage.clear();
  setAuth('manual-token', { id: 'author', role: 'manual', permissions: p });
  globalThis.confirm = () => true;
  const f = { cards: [], creates: [], updates: [], analyses: 0, reply: null, fail: null };
  t.mock.method(api, 'me', async () => currentUser.value);
  t.mock.method(api, 'personalExperiences', async () => ({ experiences: f.cards, total: f.cards.length }));
  t.mock.method(api, 'personalExperienceHistory', async () => ({ versions: f.cards }));
  t.mock.method(api, 'createPersonalExperience', async input => {
    f.creates.push(JSON.parse(JSON.stringify(input)));
    if (f.fail) throw f.fail;
    if (f.reply) return f.reply;
    const card = { id: 'manual-card', revision: 1, ...input, source: { kind: 'manual' } };
    f.cards = [card]; return card;
  });
  t.mock.method(api, 'savePersonalExperience', async (id, input) => { f.updates.push({ id, input }); return { id, revision: 2, ...input }; });
  t.mock.method(api, 'createLearningAnalysis', async () => { f.analyses++; throw new Error('manual knowledge must not call AI'); });
  return f;
}
async function page(t) {
  const w = mount(Page, { attachTo: document.body, global: { stubs: { RouterLink: { template: '<a><slot /></a>' } } } });
  t.after(() => w.unmount()); await settle(); const body=new DOMWrapper(document.body);body.unmount=()=>w.unmount();return body;
}
async function fill(w, content = '按选项分别解释干扰项') {
  await button(w, '新增个人知识').trigger('click');
  await w.get('[aria-label="知识标题"]').setValue('本人的命题习惯');
  await w.get('[aria-label="命题要求或经验"]').setValue(content);
}

test('manual-only knowledge manager creates a draft without question-view or paid analysis permissions', async t => {
  const f = fixture(t), w = await page(t); await fill(w);
  await button(w, '保存草稿').trigger('click'); await settle();
  assert.equal(f.creates.length, 1); assert.ok(f.creates[0].create_key);
  assert.equal(f.creates[0].status, 'draft'); assert.equal(f.creates[0].scope.kind, 'all');
  assert.equal(f.analyses, 0); assert.match(w.text(), /手工维护/); assert.doesNotMatch(w.text(), /原始 v1/);
  assert.match(w.text(), /个人知识草稿已保存/);
});

test('manual knowledge splits 2000 Unicode characters losslessly into four bounded rules', async t => {
  const f = fixture(t), w = await page(t), text = '医😀'.repeat(1000); await fill(w, text);
  await button(w, '保存并启用').trigger('click'); await settle();
  const rules = f.creates[0].rules; assert.equal(rules.length, 4);
  assert.equal(rules.map(r => r.text).join(''), text);
  assert.ok(rules.every(r => Array.from(r.text).length <= 600)); assert.equal(f.creates[0].status, 'active');
});

test('manual medical changes require explicit verification before activation', async t => {
  const f = fixture(t), w = await page(t); await fill(w);
  await w.get('[aria-label="包含医学判断变化"]').setValue(true);
  assert.equal(button(w, '保存并启用').element.disabled, true);
  await w.get('[aria-label="已核对手工知识医学变化"]').setValue(true);
  await button(w, '保存并启用').trigger('click'); await settle();
  assert.equal(f.creates[0].confirm_medical_changes, true); assert.ok(f.creates[0].rules.every(r => r.medical_change));
});

test('ambiguous creation freezes the original request and survives refresh with its same receipt', async t => {
  const f = fixture(t); f.fail = new Error('response lost'); let w = await page(t); await fill(w);
  await button(w, '保存草稿').trigger('click'); await settle();
  assert.equal(w.get('[aria-label="知识标题"]').element.matches(':disabled'), true);
  const original = f.creates[0]; w.unmount(); w = await page(t);
  assert.equal(w.get('[aria-label="命题要求或经验"]').element.value, '按选项分别解释干扰项');
  f.fail = null;
  await button(w, '重试核对保存').trigger('click'); await settle();
  assert.deepEqual(f.creates[1], original); assert.equal(readPersonalKnowledgeDraft(currentUser.value), null);
});

test('HTTP success without an actual knowledge card cannot clear a pending creation', async t => {
  const f = fixture(t); f.reply = {}; const w = await page(t); await fill(w);
  await button(w, '保存草稿').trigger('click'); await settle();
  assert.match(w.text(), /缺少知识记录/); assert.equal(button(w, '重试核对保存').exists(), true);
  assert.ok(readPersonalKnowledgeDraft(currentUser.value).pending.create_key);
  assert.doesNotMatch(w.text(), /个人知识草稿已保存/);
});

test('manual draft guards refresh and explicit abandonment removes only that draft', async t => {
  fixture(t); const w = await page(t); await fill(w);
  const event = new Event('beforeunload', { cancelable: true }); window.dispatchEvent(event); assert.equal(event.defaultPrevented, true);
  t.mock.method(globalThis, 'confirm', () => false);
  await button(w, '放弃本地草稿').trigger('click'); assert.equal(w.find('.personal-knowledge-create').exists(), true);
  globalThis.confirm = () => true; await button(w, '放弃本地草稿').trigger('click'); await settle();
  assert.equal(readPersonalKnowledgeDraft(currentUser.value), null); assert.equal(w.find('.personal-knowledge-create').exists(), false);
});

test('late create response cannot restore private draft or success state after identity changes', async t => {
  const f = fixture(t), response = deferred(), w = await page(t); await fill(w, 'A的私有输入');
  t.mock.method(api, 'createPersonalExperience', async () => response.promise);
  await button(w, '保存草稿').trigger('click');
  savePersonalKnowledgeDraft({ id: 'other', role: 'manual' }, { createKey: 'other-key', title: 'B本人的未完成草稿', content: 'B正文', scope: { kind: 'all' }, pending: null });
  setAuth('other-token', { id: 'other', role: 'manual', permissions }); await settle();
  response.resolve({ id: 'late-A', revision: 1 }); await settle();
  assert.doesNotMatch(w.text(), /A的私有输入|个人知识草稿已保存/); assert.equal(f.cards.length, 0);
  assert.equal(readPersonalKnowledgeDraft({ id: 'author', role: 'manual' }), null);
  assert.equal(readPersonalKnowledgeDraft(currentUser.value).title, 'B本人的未完成草稿');
});

test('revoked manual management clears pending private contents immediately', async t => {
  fixture(t); const w = await page(t); await fill(w, '撤权后不可见的私有输入');
  t.mock.method(api, 'createPersonalExperience', async () => { const e = new Error('revoked'); e.status = 403; throw e; });
  t.mock.method(api, 'me', async () => ({ ...currentUser.value, permissions: ['experience:view'] }));
  await button(w, '保存草稿').trigger('click'); await settle();
  assert.equal(w.find('.personal-knowledge-create').exists(), false);
  assert.doesNotMatch(w.text(), /撤权后不可见的私有输入/); assert.equal(readPersonalKnowledgeDraft(currentUser.value), null);
});

test('read-only knowledge access has no create button and migrated sources never claim question provenance', async t => {
  const f = fixture(t, ['experience:view']); f.cards = [{ id: 'migrated', title: '迁入偏好', status: 'active', revision: 4, scope: { kind: 'all' }, rules: [{ id: 'r', text: '原内容' }], source: { kind: 'legacy_personal_preference' } }];
  const w = await page(t); assert.equal(button(w, '新增个人知识'), undefined); assert.match(w.text(), /原个人偏好迁入/); assert.doesNotMatch(w.text(), /原始 v1/);
  assert.equal(button(w, '修订'), undefined);
});

test('route-based identity switching can be canceled before discarding a manual draft', async t => {
  fixture(t);
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: Page }, { path: '/my', component: { template: '<p>workbench</p>' } }] });
  await router.push('/'); await router.isReady();
  const w = mount({ render: () => h(RouterView) }, { attachTo: document.body, global: { plugins: [router], stubs: { Teleport: true } } });
  t.after(() => w.unmount()); await settle(); await fill(w);
  t.mock.method(globalThis, 'confirm', () => false); await router.push('/my'); assert.equal(router.currentRoute.value.path, '/');
  globalThis.confirm = () => true; await router.push('/my'); assert.equal(router.currentRoute.value.path, '/my');
  assert.equal(readPersonalKnowledgeDraft(currentUser.value), null);
});

test('retired feature URLs resolve to the missing-page screen and expose no old API helpers', async t => {
  fixture(t);
  const router = (await import('./router.js')).default;
  t.after(() => router.options.history.destroy());
  for (const path of ['/generation-rules', '/system/rag']) {
    const route = router.resolve(path);
    assert.equal(route.meta.title, '页面不存在');
    assert.equal(route.matched.some(record => record.path === path), false);
    assert.equal(route.matched.at(-1).redirect, undefined);
  }
  assert.equal(api.generationRules, undefined); assert.equal(api.generationRulesPreview, undefined);
  assert.equal(typeof api.createPersonalExperience, 'function');
});

for (const populated of [false, true]) {
  test(`background refresh keeps ${populated ? 'cards and editor' : 'empty state'} stable while awaiting the server`, async t => {
    const f = fixture(t);
    if (populated) f.cards = [{ id: 'stable', title: '已有知识', status: 'active', revision: 1, scope: { kind: 'all' }, rules: [{ id: 'r', text: '已有正文' }], source: { kind: 'manual' } }];
    const w = await page(t);
    if (populated) { await button(w, '修订').trigger('click'); await w.get('[aria-label="经验标题"]').setValue('未保存标题'); }
    const anchor = w.get(populated ? '.personal-experience-card' : '.experience-empty').element;
    const response = deferred();
    t.mock.method(api, 'personalExperiences', () => response.promise);
    window.dispatchEvent(new Event('focus')); await settle();
    try {
      assert.doesNotMatch(w.text(), /读取中…/, 'background checks must not insert a layout-shifting loading row');
      assert.equal(button(w, '查询').element.disabled, false);
      assert.equal(w.get(populated ? '.personal-experience-card' : '.experience-empty').element, anchor);
      if (populated) assert.equal(w.get('[aria-label="经验标题"]').element.value, '未保存标题');
    } finally { response.resolve({ experiences: f.cards, total: f.cards.length }); await settle(); }
  });
}

test('explicit refresh still gives loading feedback and live checks do not supersede it', async t => {
  fixture(t); const w = await page(t), response = deferred(); let reads = 0;
  t.mock.method(api, 'personalExperiences', () => { reads++; return response.promise; });
  await button(w, '刷新').trigger('click'); await settle();
  assert.match(w.text(), /读取中…/); assert.equal(button(w, '查询').element.disabled, true);
  window.dispatchEvent(new Event('focus')); await settle(); assert.equal(reads, 1);
  response.resolve({ experiences: [], total: 0 }); await settle();
  assert.doesNotMatch(w.text(), /读取中…/); assert.equal(w.find('.experience-empty').exists(), true);
});

test('live permission revocation clears private cards and editor before a delayed list can restore them', async t => {
  const f = fixture(t); f.cards = [{ id: 'private', title: '本人私有知识', status: 'active', revision: 1, scope: { kind: 'all' }, rules: [{ id: 'r', text: '本人私有正文' }], source: { kind: 'manual' } }];
  const w = await page(t); await button(w, '修订').trigger('click');
  const response = deferred(); t.mock.method(api, 'personalExperiences', () => response.promise);
  window.dispatchEvent(new Event('focus')); await settle();
  setAuth('revoked-token', { ...currentUser.value, permissions: [] }); await settle();
  assert.doesNotMatch(w.text(), /本人私有知识|本人私有正文/);
  response.resolve({ experiences: f.cards, total: 1 }); await settle();
  assert.doesNotMatch(w.text(), /本人私有知识|本人私有正文/);
  assert.match(w.text(), /没有查看/);
});

test('repeated background failures keep the error visible until recovery without flickering the empty state', async t => {
  fixture(t); const w = await page(t);
  t.mock.method(api, 'personalExperiences', async () => { throw new Error('暂时无法读取'); });
  window.dispatchEvent(new Event('focus')); await settle();
  const error = w.get('[role="alert"]').element, empty = w.get('.experience-empty').element;
  const response = deferred(); t.mock.method(api, 'personalExperiences', () => response.promise);
  window.dispatchEvent(new Event('focus')); await settle();
  assert.equal(w.get('[role="alert"]').element, error); assert.equal(w.get('.experience-empty').element, empty);
  assert.doesNotMatch(w.text(), /读取中…/);
  response.resolve({ experiences: [], total: 0 }); await settle();
  assert.equal(w.find('[role="alert"]').exists(), false);
});

test('quiet refresh still detects a remote revision without replacing unsaved edits', async t => {
  const f = fixture(t); f.cards = [{ id: 'edited', title: '原始标题', status: 'active', revision: 1, scope: { kind: 'all' }, rules: [{ id: 'r', text: '原始正文' }], source: { kind: 'manual' } }];
  const w = await page(t); await button(w, '修订').trigger('click');
  await w.get('[aria-label="经验标题"]').setValue('本窗口的未保存标题');
  f.cards = [{ ...f.cards[0], revision: 2, title: '另一窗口的标题' }];
  window.dispatchEvent(new Event('focus')); await settle();
  assert.match(w.get('.personal-experience-card').text(), /另一窗口的标题/);
  assert.equal(w.get('[aria-label="经验标题"]').element.value, '本窗口的未保存标题');
  assert.match(w.text(), /经验已被另一窗口修改/);
  assert.equal(button(w, '保存为草稿').element.disabled, true);
  assert.doesNotMatch(w.text(), /读取中…/);
});

test('explicit edit opens a focused modal without scrolling or refresh reopening it',async t=>{
 const f=fixture(t);f.cards=[{id:'scroll-card',title:'要修订的知识',revision:1,status:'active',scope:{kind:'all'},rules:[{id:'r',text:'原正文'}],source:{kind:'manual'}}];
 const calls=[];const original=HTMLElement.prototype.scrollIntoView;HTMLElement.prototype.scrollIntoView=function(options){calls.push({element:this,options});};t.after(()=>HTMLElement.prototype.scrollIntoView=original);
 const w=await page(t);await button(w,'修订').trigger('click');await settle();assert.ok(w.get('dialog[open]'));assert.ok(w.get('dialog').element.contains(document.activeElement));assert.equal(calls.length,0,'modal editing must not scroll the underlying page');
 const count=calls.length;window.dispatchEvent(new Event('focus'));await settle();assert.equal(calls.length,count);
});

const existingRule=(id='one')=>({id,title:'命题规则'+id,status:'active',revision:2,scope:{kind:'all'},rules:[{id:'r-'+id,text:'保留本例线索'}],source:{kind:'manual'}});
test('rule history opens as one modal instead of an unnoticed page-bottom section',async t=>{
 const f=fixture(t);f.cards=[existingRule()];const w=await page(t);await button(w,'查看历史').trigger('click');await settle();
 const dialog=w.get('dialog[open]');assert.match(dialog.attributes('aria-labelledby'),/rule-dialog/);assert.match(dialog.text(),/命题规则one/);assert.ok(dialog.find('[aria-label="查看经验历史版本"]').exists());assert.equal(w.findAll('dialog[open]').length,1);await button(w,'关闭历史').trigger('click');await settle();assert.equal(w.find('dialog').exists(),false);
});
test('opening revision while history is pending invalidates its late result and keeps one operation',async t=>{
 const f=fixture(t);f.cards=[existingRule()];const pending=deferred();t.mock.method(api,'personalExperienceHistory',()=>pending.promise);const w=await page(t);await button(w,'查看历史').trigger('click');await settle();assert.match(w.get('dialog[open]').text(),/正在读取历史/);
 await button(w,'修订').trigger('click');await settle();pending.resolve({versions:[{...existingRule(),title:'迟到的旧历史内容'}]});await settle();assert.equal(w.findAll('dialog[open]').length,1);assert.ok(w.get('dialog').find('[aria-label="经验标题"]').exists());assert.doesNotMatch(w.get('dialog').text(),/迟到的旧历史内容|正在读取历史/);
});
test('canceling a dirty revision keeps it open, accepting discard closes it without saving',async t=>{
 const f=fixture(t);f.cards=[existingRule()];const w=await page(t);await button(w,'修订').trigger('click');await w.get('[aria-label="经验标题"]').setValue('不可丢失的编辑');let confirmations=0;t.mock.method(globalThis,'confirm',()=>{confirmations++;return false});
 w.get('dialog').element.dispatchEvent(new Event('cancel',{cancelable:true}));await settle();assert.equal(confirmations,1);assert.equal(w.get('[aria-label="经验标题"]').element.value,'不可丢失的编辑');assert.ok(w.get('dialog[open]'));
 globalThis.confirm=()=>true;w.get('dialog').element.dispatchEvent(new Event('cancel',{cancelable:true}));await settle();assert.equal(w.find('dialog').exists(),false);assert.equal(f.updates.length,0);
});
test('closing pending history cannot reopen it when the response arrives',async t=>{
 const f=fixture(t);f.cards=[existingRule()];const pending=deferred();t.mock.method(api,'personalExperienceHistory',()=>pending.promise);const w=await page(t);await button(w,'查看历史').trigger('click');await settle();w.get('dialog').element.dispatchEvent(new Event('cancel',{cancelable:true}));await settle();pending.resolve({versions:[existingRule()]});await settle();assert.equal(w.find('dialog').exists(),false);assert.equal(w.find('.experience-history').exists(),false);
});
test('a forbidden history response clears the private modal immediately',async t=>{
 const f=fixture(t);f.cards=[existingRule()];const w=await page(t);t.mock.method(api,'personalExperienceHistory',async()=>{throw Object.assign(new Error('已撤销查看权限'),{status:403})});t.mock.method(api,'me',async()=>({...currentUser.value,permissions:[]}));
 await button(w,'查看历史').trigger('click');await settle();assert.equal(w.find('dialog').exists(),false);assert.doesNotMatch(w.text(),/命题规则one/);
});
test('an in-flight rule save blocks modal cancellation until the result is known',async t=>{
 const f=fixture(t);f.cards=[existingRule()];const pending=deferred();t.mock.method(api,'savePersonalExperience',()=>pending.promise);const w=await page(t);await button(w,'修订').trigger('click');await w.get('[aria-label="经验标题"]').setValue('正在保存的标题');await button(w,'保存为草稿').trigger('click');await settle();w.get('dialog').element.dispatchEvent(new Event('cancel',{cancelable:true}));await settle();assert.ok(w.get('dialog[open]'));pending.resolve({id:'one',revision:3});await settle();assert.equal(w.find('dialog').exists(),false);
});

test('rule success feedback stays beside the affected row and expires without changing its status',async t=>{
 const f=fixture(t);f.cards=[existingRule()];const w=await page(t);t.mock.timers.enable({apis:['setTimeout']});await button(w,'停用').trigger('click');await settle();
 assert.ok(w.get('.personal-experience-card .experience-success'));assert.equal(w.find('.panel > .experience-success').exists(),false);t.mock.timers.tick(4000);await settle();assert.equal(w.find('.experience-success').exists(),false);assert.equal(f.updates[0].input.status,'draft');
});
test('a later action replaces the prior feedback and gets a fresh expiration',async t=>{
 const f=fixture(t);f.cards=[existingRule()];const w=await page(t);t.mock.timers.enable({apis:['setTimeout']});await button(w,'停用').trigger('click');await settle();t.mock.timers.tick(3000);await button(w,'归档').trigger('click');await settle();assert.match(w.get('.experience-success').text(),/已归档/);t.mock.timers.tick(1000);await settle();assert.ok(w.get('.experience-success'));t.mock.timers.tick(3000);await settle();assert.equal(w.find('.experience-success').exists(),false);
});
test('filtered-out archived rules retain a temporary result at their former list position',async t=>{
 const f=fixture(t);f.cards=[existingRule()];const w=await page(t);t.mock.method(api,'savePersonalExperience',async(id,input)=>{f.cards=[];return{id,revision:3,...input}});await button(w,'归档').trigger('click');await settle();assert.match(w.get('.experience-action-result').text(),/命题规则one.*已归档/);assert.equal(w.find('.panel > .experience-success:not(.experience-action-result)').exists(),false);
});
