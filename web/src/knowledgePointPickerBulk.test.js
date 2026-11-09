import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { registerHooks } from 'node:module';
import { parse, compileScript } from '@vue/compiler-sfc';
import { JSDOM } from 'jsdom';
registerHooks({ load(url, context, next) { if (!url.endsWith('.vue')) return next(url, context); const { descriptor } = parse(readFileSync(fileURLToPath(url), 'utf8')); return { format: 'module', source: compileScript(descriptor, { id: url, inlineTemplate: true }).content, shortCircuit: true }; } });
const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost/' });
for (const key of ['window', 'document', 'Document', 'ShadowRoot', 'Element', 'HTMLElement', 'SVGElement', 'Node', 'Event', 'localStorage', 'sessionStorage']) globalThis[key] = dom.window[key];
dom.window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
dom.window.HTMLDialogElement.prototype.close = function () { this.open = false; };
dom.window.HTMLElement.prototype.scrollTo = function () {};
const { mount, flushPromises } = await import('@vue/test-utils');
const { nextTick, ref, defineComponent, h } = await import('vue');
const { api } = await import('./api.js');
const { setAuth } = await import('./auth.js');
const { default: Picker } = await import('./components/KnowledgePointPicker.vue');
const settle = async () => { await flushPromises(); await nextTick(); };
const btn = (w, re) => w.findAll('button').find(b => re.test(b.text()));
const point = (id, version_id = 'v1') => ({ id, version_id, topic: id, outline_code: id, subject: '内科', category: '临床综合' });
const points = [point('甲'), point('乙')];
const deferred = () => { let resolve; const promise = new Promise(yes => { resolve = yes; }); return { promise, resolve }; };

async function setup(t) {
  document.body.innerHTML = ''; localStorage.clear();
  setAuth('picker-session', { id: 'u1', role: 'teacher', permissions: ['batch:run'] });
  t.mock.method(api, 'kpVersions', async () => ({ default_version_id: 'v1', versions: [{ id: 'v1', name: '2026大纲', point_count: 2, status: 'published' }, { id: 'v2', name: '2025大纲', point_count: 1, status: 'published' }] }));
  t.mock.method(api, 'kpTree', async () => ({ tree: [] }));
  t.mock.method(api, 'searchKPFiltered', async p => ({ points: p.version_id === 'v1' ? points : [point('旧', 'v2')], total: p.version_id === 'v1' ? 2 : 1, page: p.page || 1 }));
  const selected = ref([]);
  const Host = defineComponent({ setup: () => () => h(Picker, { modelValue: selected.value, 'onUpdate:modelValue': value => { selected.value = value; } }) });
  const w = mount(Host, { attachTo: document.body, global: { stubs: { SelectedKnowledgeTreeNode: true } } }); t.after(() => w.unmount()); await settle();
  return { w, selected };
}

test('late cross-page select-all cannot restore points from the previous version', async t => {
  const { w, selected } = await setup(t); const pending = deferred();
  t.mock.method(api, 'searchKPFiltered', async p => p.page_size === 200 ? pending.promise : ({ points: [point('旧', 'v2')], total: 1, page: 1 }));
  await btn(w, /全选匹配/).trigger('click');
  await btn(w, /切换版本/).trigger('click'); await settle();
  await w.get('input[value="v2"]').setValue(); await btn(w, /切换到此版本/).trigger('click'); await settle();
  pending.resolve({ points, total: 2, page: 1 }); await settle();
  assert.match(w.get('.kp-version-row strong').text(), /2025/); assert.deepEqual(selected.value, []);
});

test('reset cancels an in-flight all selection and a later manual choice is retained', async t => {
  const { w, selected } = await setup(t); const pending = deferred();
  t.mock.method(api, 'searchKPFiltered', async p => p.page_size === 200 ? pending.promise : ({ points, total: 2, page: 1 }));
  await btn(w, /全选匹配/).trigger('click'); await btn(w, /^重置$/).trigger('click'); await settle();
  await w.findAll('label.kp-item')[0].get('input').setValue();
  pending.resolve({ points, total: 2, page: 1 }); await settle();
  assert.deepEqual(selected.value.map(x => x.id), ['甲']);
});

test('select-all checks the response total even before search debounce catches up', async t => {
  const { w, selected } = await setup(t); let calls = 0;
  t.mock.method(api, 'searchKPFiltered', async p => { calls++; return { points: [point('page' + p.page)], total: 10001, page: p.page }; });
  await w.get('input[placeholder="搜索考点内容或大纲代码..."]').setValue('扩大范围');
  await btn(w, /全选匹配/).trigger('click'); await settle();
  assert.deepEqual(selected.value, []); assert.equal(calls, 1, '第一份响应超限后不继续下载全部页面');
});

test('identity changes reject late selection while a permission heartbeat preserves it', async t => {
  const { w, selected } = await setup(t); let pending = deferred();
  t.mock.method(api, 'searchKPFiltered', async () => pending.promise);
  await btn(w, /全选匹配/).trigger('click');
  setAuth('picker-session', { id: 'u1', role: 'teacher', permissions: ['batch:run'] });
  pending.resolve({ points, total: 2, page: 1 }); await settle();
  assert.deepEqual(selected.value.map(x => x.id), ['甲', '乙']);
  await btn(w, /清空已选/).trigger('click'); pending = deferred();
  await btn(w, /全选匹配/).trigger('click');
  setAuth('new-role-session', { id: 'u1', role: 'admin', permissions: ['batch:run'] });
  pending.resolve({ points, total: 2, page: 1 }); await settle();
  assert.deepEqual(selected.value, []);
});

test('normal select-all fetches every page and adds the frozen matching range once', async t => {
  const { w, selected } = await setup(t); const pagePoints = Array.from({ length: 201 }, (_, i) => point('p' + i)); const requests = [];
  t.mock.method(api, 'searchKPFiltered', async p => { requests.push({ ...p }); return { points: pagePoints.slice((p.page - 1) * 200, p.page * 200), total: 201, page: p.page }; });
  await btn(w, /全选匹配/).trigger('click'); await settle();
  assert.deepEqual(selected.value.map(x => x.id), pagePoints.map(x => x.id));
  assert.deepEqual(requests.map(p => p.page), [1, 2]);
  assert.ok(requests.every(p => p.version_id === 'v1' && p.q === '' && p.outline_code === ''));
});

test('changed page totals never produce a partial selection', async t => {
  const { w, selected } = await setup(t);
  t.mock.method(api, 'searchKPFiltered', async p => ({ points: p.page === 1 ? Array.from({ length: 200 }, (_, i) => point('p' + i)) : [point('last')], total: p.page === 1 ? 201 : 202, page: p.page }));
  await btn(w, /全选匹配/).trigger('click'); await settle();
  assert.deepEqual(selected.value, []); assert.match(document.body.textContent, /已更新/);
});

test('select-all caps the merged selection as well as the matching response total', async t => {
  const { w, selected } = await setup(t);
  selected.value = Array.from({ length: 9999 }, (_, i) => point('old' + i)); await settle();
  await btn(w, /全选匹配/).trigger('click'); await settle();
  assert.equal(selected.value.length, 9999); assert.match(document.body.textContent, /合计超过上限 10000/);
});

test('revoked permissions cancel an outstanding select-all without removing the saved selection', async t => {
  const { w, selected } = await setup(t); selected.value = [points[0]]; await settle(); const pending = deferred();
  t.mock.method(api, 'searchKPFiltered', async () => pending.promise);
  await btn(w, /全选匹配/).trigger('click');
  setAuth('picker-session', { id: 'u1', role: 'teacher', permissions: [] });
  pending.resolve({ points, total: 2, page: 1 }); await settle();
  assert.deepEqual(selected.value.map(x => x.id), ['甲']);
});

test('unmount cancels a pending filter debounce before it can query a later page', async t => {
  const { w } = await setup(t); let requests = 0;
  t.mock.method(api, 'searchKPFiltered', async () => { requests++; return { points, total: 2, page: 1 }; });
  await w.get('input[placeholder="搜索考点内容或大纲代码..."]').setValue('尚未执行的搜索');
  w.unmount();
  await new Promise(resolve => setTimeout(resolve, 350));
  assert.equal(requests, 0, '离开选择器后，旧搜索不能继续请求或污染下一页');
});

test('stable focus version refreshes preserve selected points and query while metadata overlaps',async t=>{
 const {w,selected}=await setup(t);
 await w.findAll('label.kp-item')[0].get('input').setValue();await settle();
 const filter=w.get('input[placeholder="大纲代码前缀"]');await filter.setValue('110.');
 const responses=[];t.mock.method(api,'kpVersions',()=>{const item=deferred();responses.push(item);return item.promise;});
 window.dispatchEvent(new Event('focus'));window.dispatchEvent(new Event('focus'));await settle();
 const value={default_version_id:'v1',versions:[{id:'v1',name:'2026大纲',point_count:2,status:'published'},{id:'v2',name:'2025大纲',point_count:1,status:'published'}]};
 responses[1].resolve(value);await settle();responses[0].resolve(value);await settle();
 assert.deepEqual(selected.value.map(x=>x.id),['甲']);assert.equal(filter.element.value,'110.');
});
