import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { registerHooks } from 'node:module';
import { parse, compileScript } from '@vue/compiler-sfc';
import { JSDOM } from 'jsdom';
registerHooks({ load(url, context, next) { if (!url.endsWith('.vue')) return next(url, context); const { descriptor } = parse(readFileSync(fileURLToPath(url), 'utf8')); return { format: 'module', source: compileScript(descriptor, { id: url, inlineTemplate: true }).content, shortCircuit: true }; } });
const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost/' });
for (const key of ['window', 'document', 'Document', 'ShadowRoot', 'Element', 'HTMLElement', 'SVGElement', 'Node', 'Event', 'XMLSerializer', 'localStorage', 'sessionStorage']) globalThis[key] = dom.window[key];
dom.window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
dom.window.HTMLDialogElement.prototype.close = function () { this.open = false; };
dom.window.HTMLElement.prototype.scrollTo = function () {};
dom.window.HTMLElement.prototype.scrollIntoView = function () {};
const { mount, flushPromises } = await import('@vue/test-utils'), { nextTick } = await import('vue');
const { api } = await import('./api.js'), { setAuth } = await import('./auth.js');
const { default: BatchPage } = await import('./pages/BatchPage.vue');
const settle = async () => { await flushPromises(); await nextTick(); };
const pointA = { id: 'ka', topic: '甲要点', version_id: 'v1', outline_code: 'ka', subject: '内科', category: '临床综合' };
const pointB = { id: 'kb', topic: '乙要点', version_id: 'v1', outline_code: 'kb', subject: '内科', category: '临床综合' };

async function setup(t, selected = [pointA, pointB], batchConfig = {}, settings = {}) {
  localStorage.clear(); document.body.innerHTML = '';
  const { saveBatchWorkspace } = await import('./batchWorkspace.js');
  saveBatchWorkspace('u1', '', localStorage, {
    selectedKPs: selected.map(({ id, version_id }) => ({ id, version_id })),
    batchConfig: { skip_existing: false, count: 1, job_name: '', mode: 'random_total', total_count: 3, ...batchConfig },
    ...settings,
  });
  setAuth('c2-session', { id: 'u1', permissions: ['batch:run', 'stats:view'] });
  t.mock.method(api, 'stats', async () => ({ tier_counts: { working: 0, formal: 0, eliminated: 0 } }));
  t.mock.method(api, 'kpVersions', async () => ({ versions: [{ id: 'v1', name: '2026 大纲', status: 'published', point_count: 2 }], default_version_id: 'v1' }));
  t.mock.method(api, 'kpTree', async () => ({ tree: [] }));
  t.mock.method(api, 'kpMeta', async () => ({ categories: [], subjects: [] }));
  t.mock.method(api, 'searchKPFiltered', async p => {
    const points = [pointA, pointB].filter(x => x.version_id === p.version_id);
    return { points, total: points.length, page: 1, page_size: 50, has_more: false };
  });
  t.mock.method(api, 'batchCapabilities', async () => ({ available: true, execution_mode: 'local_single_api', message: '可用' }));
  t.mock.method(api, 'batchHistory', async () => ({ jobs: [], total: 0 }));
  t.mock.method(api, 'batchPreview', async () => ({ candidate_count: selected.length, missing_card_count: 0, coverage_checked: 0, missing_card_samples: [] }));
  t.mock.method(api, 'batchStatus', async jobId => ({ job_id: jobId, owner_id: 'u1', status: 'completed', total_count: 3, completed: 3, failed: 0, items: [] }));
  t.mock.method(api, 'me', async () => ({ id: 'u1' }));
  const w = mount(BatchPage, { attachTo: document.body, global: { stubs: { RouterLink: true } } });
  t.after(() => w.unmount());
  await settle();
  return w;
}
const btn = (w, re) => w.findAll('button').find(b => re.test(b.text()));
const waitForCoverage = async () => { await new Promise(r => setTimeout(r, 260)); await settle(); };

async function pickPoints(t, w, topics) {
  // 初始即载入第一页结果；输入触发防抖搜索。
  const search = w.find('input[placeholder="搜索考点内容或大纲代码"]');
  await search.setValue('要点');
  await new Promise(r => setTimeout(r, 350));
  await settle();
  for (const label of w.findAll('label.kp-item')) {
    if (topics.some(topic => label.text().includes(topic))) {
      await label.get('input').setValue();
    }
  }
  await settle();
}

test('random mode is default and submits mode/total_count/request_key once', async t => {
  const w = await setup(t, [pointA, pointB], { mode: 'random_total', total_count: 3 });
  assert.match(w.text(), /范围随机 · 共 N 题/);
  assert.match(w.text(), /随机抽 \d+ 题/);
  assert.doesNotMatch(w.text(),/非普通题库入库数/);
  const hint=w.get('[aria-label="随机总量说明"]');await hint.trigger('mouseenter');await settle();
  assert.match(document.querySelector('[role="tooltip"]').textContent,/同一考点允许重复/);
  await hint.trigger('mouseleave');await settle();assert.equal(document.querySelector('[role="tooltip"]'),null);
  const submitted = [];
  t.mock.method(api, 'batchSubmit', async p => { submitted.push(p); return { job_id: 'job-r1', count: p.total_count, mode: p.mode }; });
  await btn(w, /提交随机生成任务/).trigger('click');
  await settle();
  assert.equal(submitted.length, 1);
  assert.equal(submitted[0].mode, 'random_total');
  assert.ok(Number.isInteger(submitted[0].total_count) && submitted[0].total_count >= 1);
  assert.ok(submitted[0].request_key, '应携带提交幂等键');
  assert.equal(submitted[0].count, undefined);
});

test('per_point mode keeps count input and omits total_count', async t => {
  const w = await setup(t, [pointA]);
  await w.findAll('input[type="radio"]').find(r => r.element.value === 'per_point').setValue();
  await settle();
  assert.match(w.text(), /每个要点生成 \d+ 道题/);
  const submitted = [];
  t.mock.method(api, 'batchSubmit', async p => { submitted.push(p); return { job_id: 'job-p1', count: p.count, mode: p.mode }; });
  await w.find('input[type="number"][min="1"][max="20"]').setValue('2');
  await settle();
  await btn(w, /提交生成任务/).trigger('click');
  await settle();
  assert.equal(submitted.length, 1);
  assert.equal(submitted[0].mode, 'per_point');
  assert.equal(submitted[0].count, 2);
  assert.equal(submitted[0].total_count, undefined);
});

test('replayed submission rotates request key', async t => {
  const w = await setup(t);
  const keys = [];
  t.mock.method(api, 'batchSubmit', async p => { keys.push(p.request_key); return { job_id: 'job-same', count: 3, replayed: true }; });
  await btn(w, /提交随机生成任务/).trigger('click');
  await settle();
  // 第二次是新意图（第一次已成功）：新键。
  await btn(w, /提交随机生成任务/).trigger('click');
  await settle();
  assert.equal(keys.length, 2);
  assert.notEqual(keys[0], keys[1], '成功提交后应更换幂等键');
});

test('candidate coverage is shown automatically without a preview button or large card', async t => {
  const w = await setup(t); const calls = [];
  t.mock.method(api, 'batchPreview', async p => {
    calls.push(p); return { candidate_count: 12, excluded_basic: 3, skipped_existing: 1, missing_card_count: 2, coverage_checked: 12,
      missing_card_samples: [{ topic: '缺卡考点A', outline_code: 'x.1' }, { topic: '缺卡考点B', outline_code: 'x.2' }] };
  });
  assert.equal(btn(w, /预览候选范围/), undefined);
  await waitForCoverage(); assert.equal(calls.length, 1);
  assert.equal(w.find('.batch-preview').exists(), false);
  assert.match(w.get('.batch-candidate-summary').text(), /可参与出题.*12.*个要点/);
  assert.doesNotMatch(w.text(), /缺少可用|x\.1、x\.2|缺卡时明确拒绝/);assert.equal(calls[0].workflow_mode,undefined);
});

test('mode choices avoid the browser fieldset frame and numeric/name controls share a row', async t => {
  const w = await setup(t); assert.equal(w.find('fieldset.batch-mode-row').exists(), false);
  assert.equal(w.get('[role="radiogroup"][aria-label="批量出题模式"]').findAll('input[type="radio"]').length, 2);
  const fields = w.get('.batch-job-fields'); assert.equal(fields.findAll('.field').length, 2);
  for (const id of ['batch-question-count','batch-job-name']) { assert.equal(fields.get(`label[for="${id}"]`).exists(),true); assert.equal(fields.get(`#${id}`).exists(),true); }
  assert.equal(fields.find('.field-hint').exists(), false);
});

test('legacy workspace without mode restores as per_point', async t => {
  const w = await setup(t, [pointA], { mode: undefined });
  assert.match(w.text(), /每个要点生成 \d+ 道题/);
});

const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };

test('a failed submission keeps its key after refresh and unchanged permission heartbeat', async t => {
  let w = await setup(t);
  const keys = [];
  t.mock.method(api, 'batchSubmit', async p => { keys.push(p.request_key); throw new Error('network interrupted after acceptance'); });
  await btn(w, /提交随机生成任务/).trigger('click'); await settle();
  setAuth('c2-session', { id: 'u1', permissions: ['batch:run', 'stats:view'] });
  await btn(w, /提交随机生成任务/).trigger('click'); await settle();
  assert.equal(keys[0], keys[1]);
  w.unmount();
  w = mount(BatchPage, { attachTo: document.body, global: { stubs: { RouterLink: true } } });
  t.after(() => w.unmount()); await settle();
  await btn(w, /提交随机生成任务/).trigger('click'); await settle();
  assert.equal(keys[0], keys[2], '刷新后重试必须复用已持久化的未确认请求键');
});

test('editing a failed submission creates a new intent key and restores the old key when returning', async t => {
  const w = await setup(t); const sent = [];
  t.mock.method(api, 'batchSubmit', async p => { sent.push(p); throw new Error('network interrupted'); });
  await btn(w, /提交随机生成任务/).trigger('click'); await settle();
  await w.get('input[type="number"]').setValue(4);
  await btn(w, /提交随机生成任务/).trigger('click'); await settle();
  assert.notEqual(sent[0].request_key, sent[1].request_key, '新参数不能复用已用于旧请求的键');
  await w.get('input[type="number"]').setValue(3);
  await btn(w, /提交随机生成任务/).trigger('click'); await settle();
  assert.equal(sent[0].request_key, sent[2].request_key, '回到未确认的旧意图应继续安全重试');
});

test('pending automatic coverage cannot show a stale point count after selection changes', async t => {
  const w = await setup(t, [pointA]); const response = deferred();
  t.mock.method(api, 'batchPreview', async () => response.promise); await waitForCoverage();
  await w.findAll('label.kp-item').find(x => x.text().includes('乙要点')).get('input').setValue();
  response.resolve({ candidate_count: 9, missing_card_count: 0, coverage_checked: 0, missing_card_samples: [] }); await settle();
  assert.doesNotMatch(w.get('.batch-candidate-summary').text(), /9 个要点/); assert.match(w.get('.batch-candidate-summary').text(), /正在核对/);
});

test('skip-existing triggers automatic fresh coverage without retaining the old count', async t => {
  const w = await setup(t); const requests = [];
  t.mock.method(api, 'batchPreview', async p => { requests.push(p); return { candidate_count: p.skip_existing ? 1 : 2, skipped_existing: p.skip_existing ? 1 : 0, missing_card_count: 0, coverage_checked: 0, missing_card_samples: [] }; });
  await waitForCoverage(); assert.match(w.get('.batch-candidate-summary').text(), /2.*个要点/);
  await w.findAll('label').find(x => x.text() === '跳过已有题目的大纲要点').get('input').setValue();
  assert.match(w.get('.batch-candidate-summary').text(), /正在核对/); await waitForCoverage();
  assert.match(w.get('.batch-candidate-summary').text(), /可参与出题.*1.*个要点/); assert.equal(requests.length, 2); assert.equal(requests[1].skip_existing, true);
});

test('double click while submission is pending sends one frozen request', async t => {
  const w = await setup(t); const response = deferred(); const sent = [];
  t.mock.method(api, 'batchSubmit', async p => { sent.push(p); return response.promise; });
  const submit = btn(w, /提交随机生成任务/);
  await submit.trigger('click'); await submit.trigger('click');
  assert.equal(sent.length, 1);
  response.reject(new Error('offline')); await settle();
});

test('knowledge workflow and personal-knowledge choice survive failed-submit refresh with the same key', async t => {
  let w = await setup(t, [pointA], {}, { workflowMode: 'knowledge', applyPersonalExperiences: false, experienceRole: '' }); const sent = [];
  t.mock.method(api, 'batchSubmit', async p => { sent.push(p); throw new Error('offline'); });
  await btn(w, /提交随机生成任务/).trigger('click'); await settle(); w.unmount();
  w = mount(BatchPage, { attachTo: document.body, global: { stubs: { RouterLink: true } } });
  t.after(() => w.unmount()); await settle();
  await btn(w, /提交随机生成任务/).trigger('click'); await settle();
  assert.equal(sent.length, 2); assert.equal(sent[0].workflow_mode, 'knowledge');
  assert.equal(sent[0].apply_personal_experiences, false); assert.deepEqual(sent[0], sent[1]);
});

test('pending intent keys are separated by role and returning to the old role recovers its key', async t => {
  const w = await setup(t); const keys = [];
  t.mock.method(api, 'batchSubmit', async p => { keys.push(p.request_key); throw new Error('offline'); });
  setAuth('teacher-token', { id: 'u1', role: 'teacher', permissions: ['batch:run', 'stats:view'] });
  await btn(w, /提交随机生成任务/).trigger('click'); await settle();
  setAuth('admin-token', { id: 'u1', role: 'admin', permissions: ['batch:run', 'stats:view'] });
  await btn(w, /提交随机生成任务/).trigger('click'); await settle();
  setAuth('teacher-new-token', { id: 'u1', role: 'teacher', permissions: ['batch:run', 'stats:view'] });
  await btn(w, /提交随机生成任务/).trigger('click'); await settle();
  assert.notEqual(keys[0], keys[1]); assert.equal(keys[0], keys[2]);
});

test('a submission response from the previous identity cannot replace the new workspace', async t => {
  const w = await setup(t); const response = deferred();
  t.mock.method(api, 'batchSubmit', async () => response.promise);
  await btn(w, /提交随机生成任务/).trigger('click');
  setAuth('admin-token', { id: 'u1', role: 'admin', permissions: ['batch:run', 'stats:view'] });
  response.resolve({ job_id: 'old-role-job', count: 3 }); await settle();
  assert.equal(w.find('.job-status-panel').exists(), false);
  assert.equal(JSON.parse(localStorage.getItem('aigo_batch_workspace_v1:u1')).jobId, '');
});

test('range counting ignores knowledge toggle and permission-order heartbeats', async t => {
 const w=await setup(t,[pointA],{},{workflowMode:'knowledge'});let calls=0;
 t.mock.method(api,'batchPreview',async p=>{calls++;assert.equal(p.workflow_mode,undefined);return {candidate_count:1};});await waitForCoverage();assert.equal(calls,1);
 setAuth('c2-session',{id:'u1',permissions:['stats:view','batch:run']});await waitForCoverage();assert.equal(calls,1);assert.match(w.get('.batch-candidate-summary').text(),/可参与出题.*1/);
});

test('automatic coverage coalesces rapid edits and keeps unchecked knowledge coverage explicit', async t => {
  const w = await setup(t); const calls = [];
  t.mock.method(api, 'batchPreview', async p => { calls.push(p); return { candidate_count: 2, missing_card_count: 0, coverage_checked: 1, coverage_unchecked: 1, missing_card_samples: [] }; });
  const count = w.get('input[type="number"]'); await count.setValue('4'); await count.setValue('5'); await count.setValue('6');
  await waitForCoverage(); assert.equal(calls.length, 1); assert.equal(calls[0].total_count, undefined); assert.doesNotMatch(w.text(), /尚未核对知识卡|缺卡时明确拒绝/);
});

test('coverage read failure is inline and does not pretend the selected range is eligible', async t => {
  const w = await setup(t); t.mock.method(api, 'batchPreview', async () => { throw new Error('暂时离线'); });
  await waitForCoverage(); assert.match(w.get('.batch-candidate-summary').text(), /范围暂时无法核对/); assert.doesNotMatch(w.get('.batch-candidate-summary').text(), /可参与出题/);
});

test('editing only the task name does not hide or refetch a known candidate range', async t => {
  const w = await setup(t); let calls = 0;
  t.mock.method(api, 'batchPreview', async () => { calls++; return { candidate_count: 2 }; });
  await waitForCoverage();
  await w.get('#batch-job-name').setValue('呼吸系统第一批');
  assert.match(w.get('.batch-candidate-summary').text(), /可参与出题.*2.*个要点/);
  await waitForCoverage(); assert.equal(calls, 1);
});

test('an unfinished numeric edit never sends invalid numeric data to the automatic scope reader', async t => {
  const w = await setup(t); let calls = 0;
  t.mock.method(api, 'batchPreview', async p => { calls++; assert.equal(p.total_count, undefined); assert.equal(p.count, undefined); return { candidate_count: 2 }; });
  await waitForCoverage(); await w.get('#batch-question-count').setValue(''); await waitForCoverage();
  assert.equal(calls, 1); assert.match(w.get('.batch-candidate-summary').text(), /可参与出题.*2/);
});

test('a lost-response batch intent created before the upgrade reuses its original receipt', async t => {
  let w = await setup(t); const sent = [];
  t.mock.method(api, 'batchSubmit', async p => { sent.push(p); throw new Error('accepted response lost'); });
  await btn(w, /提交随机生成任务/).trigger('click'); await settle();
  const originalKey = sent[0].request_key, cacheKey = 'aigo_batch_workspace_v1:u1';
  const saved = JSON.parse(localStorage.getItem(cacheKey));
  const historical = JSON.parse(saved.pendingBatchSubmissions[0].intent);
  historical.payload.apply_personal_rules = false;
  saved.pendingBatchSubmissions[0].intent = JSON.stringify(historical); saved.applyPersonalRules = false;
  w.unmount(); localStorage.setItem(cacheKey, JSON.stringify(saved));
  w = mount(BatchPage, { attachTo: document.body, global: { stubs: { RouterLink: true } } }); t.after(() => w.unmount()); await settle();
  await btn(w, /提交随机生成任务/).trigger('click'); await settle();
  assert.equal(sent[1].request_key, originalKey);
  assert.equal(Object.hasOwn(sent[1], 'apply_personal_rules'), false);
});

test('no coverage requests are started for empty selections, history-only views or after unmount', async t => {
  const original = await setup(t, []); let calls = 0;
  t.mock.method(api, 'batchPreview', async () => { calls++; return { candidate_count: 0 }; });
  await waitForCoverage(); assert.equal(calls, 0); original.unmount();
  const history = mount(BatchPage, { props: { historyOnly: true }, attachTo: document.body, global: { stubs: { RouterLink: true } } });
  t.after(() => history.unmount()); await waitForCoverage(); assert.equal(calls, 0); history.unmount();
  const pending = await setup(t, [pointA]); t.mock.method(api, 'batchPreview', async () => { calls++; return { candidate_count: 1 }; });
  pending.unmount(); await waitForCoverage(); assert.equal(calls, 0);
});

for (const mode of ['per_point', 'random_total']) test(`${mode} uses the same ordering semantics for payload and request key as the backend`, async t => {
  const w = await setup(t, [pointA, pointB], { mode }); const sent = [];
  t.mock.method(api, 'batchSubmit', async p => { sent.push(p); throw new Error('offline'); });
  await btn(w, mode === 'per_point' ? /提交生成任务/ : /提交随机生成任务/).trigger('click'); await settle();
  const firstPoint = () => w.findAll('label.kp-item').find(x => x.text().includes('甲要点')).get('input');
  await firstPoint().setValue(false); await firstPoint().setValue(true); await settle();
  await btn(w, mode === 'per_point' ? /提交生成任务/ : /提交随机生成任务/).trigger('click'); await settle();
  assert.deepEqual(sent[0].knowledge_point_ids, ['ka', 'kb']);
  if (mode === 'per_point') {
    assert.deepEqual(sent[1].knowledge_point_ids, ['kb', 'ka']);
    assert.notEqual(sent[0].request_key, sent[1].request_key);
  } else {
    assert.deepEqual(sent[1].knowledge_point_ids, ['ka', 'kb']);
    assert.equal(sent[0].request_key, sent[1].request_key);
  }
});

test('history and detail show frozen plan metadata independently of a custom job name and preserve legacy uncertainty', async t => {
  const original = await setup(t); original.unmount();
  const random = { job_id: 'random-job', owner_id: 'u1', job_name: '客户自定义名称', status: 'completed', total_count: 3, completed: 3, failed: 0, plan_mode: 'random_total', plan_candidates: 20, plan_requested_total: 3 };
  const legacy = { job_id: 'legacy-job', owner_id: 'u1', job_name: '历史任务', status: 'completed', total_count: 2, completed: 2, failed: 0 };
  t.mock.method(api, 'batchHistory', async () => ({ jobs: [random, legacy], total: 2 }));
  t.mock.method(api, 'batchStatus', async id => id === random.job_id ? random : legacy);
  const w = mount(BatchPage, { props: { historyOnly: true }, attachTo: document.body, global: { stubs: { RouterLink: true } } });
  t.after(() => w.unmount()); await settle();
  const rows = w.findAll('.history-job');
  assert.match(rows[0].text(), /范围随机.*20.*3/);
  assert.equal(rows[1].find('.job-plan-summary').exists(), false);
  await rows[0].trigger('click'); await settle();
  assert.match(w.get('.job-status-panel .job-plan-summary').text(), /范围随机.*20.*3/);
  await rows[1].trigger('click'); await settle();
  assert.equal(w.find('.job-status-panel .job-plan-summary').exists(), false);
});

test('submission rejection remains visible after the transient toast expires',async t=>{
 const w=await setup(t,[pointA],{mode:'random_total',total_count:1});
 let dismiss;const original=window.setTimeout.bind(window);
 t.mock.method(window,'setTimeout',(fn,ms,...args)=>ms===3000?(dismiss=fn,0):original(fn,ms,...args));
 t.mock.method(api,'batchSubmit',async()=>{throw Object.assign(new Error('缺少可用的已启用考点知识卡'),{status:400});});
 await btn(w,/提交随机生成任务/).trigger('click');await settle();assert.equal(typeof dismiss,'function');dismiss();await settle();
 assert.match(w.text(),/提交失败.*缺少可用的已启用考点知识卡/);assert.equal(w.find('[role="alert"]').exists(),true);
});
