import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { registerHooks } from 'node:module';
import { parse, compileScript } from '@vue/compiler-sfc';
import { JSDOM } from 'jsdom';
registerHooks({ load(url, context, next) { if (!url.endsWith('.vue')) return next(url, context); const { descriptor } = parse(readFileSync(fileURLToPath(url), 'utf8'), { filename: fileURLToPath(url) }); return { format: 'module', source: compileScript(descriptor, { id: url, inlineTemplate: true }).content, shortCircuit: true }; } });
const dom = new JSDOM('<!doctype html><html><body></body></html>', { url: 'http://localhost/' });
for (const key of ['window', 'document', 'Element', 'HTMLElement', 'SVGElement', 'Node', 'Event', 'localStorage', 'sessionStorage']) globalThis[key] = dom.window[key];
dom.window.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
dom.window.HTMLDialogElement.prototype.close = function () { this.open = false; };
const { mount, flushPromises } = await import('@vue/test-utils'), { nextTick } = await import('vue');
const { api } = await import('./api.js'), { setAuth, updateUser, currentUser } = await import('./auth.js');
const { default: KnowledgePage } = await import('./pages/KnowledgePage.vue');
const settle = async () => { await flushPromises(); await nextTick(); };
const button = (w, name) => w.findAll('button').find(b => b.text().includes(name));
async function pickFile(w) {
  const input = w.find('input[type="file"]');
  const file = new dom.window.File(["分类,专业/系统\n临床综合,一、呼吸系统\n"], "大纲.csv", { type: "text/csv" });
  Object.defineProperty(input.element, "files", { value: [file], configurable: true });
  await input.trigger("change");
  await settle();
}

const version = { id: 'v2024', name: '2024 临床大纲', point_count: 2, status: 'published' };
const clinicalPoint = { id: 'kp-110', category: '临床综合', subject: '一、呼吸系统', unit: '肺部感染', sub_item: '肺炎', topic: '肺炎的诊断', outline_code: '110.1' };

async function setup(t) {
  document.body.innerHTML = '';
  setAuth('outline-token', { id: 'u', role: 'admin', permissions: ['knowledge:manage'] });
  t.mock.method(api, 'me', async () => currentUser.value);
  t.mock.method(api, 'kpVersions', async () => ({ versions: [version], default_version_id: 'v2024' }));
  t.mock.method(api, 'kpTree', async () => ({ tree: [{ id: 't1', label: '临床综合', field: 'category', value: '临床综合', count: 2, children: [] }], version }));
  t.mock.method(api, 'searchKPFiltered', async () => ({ points: [clinicalPoint], total: 1, page: 1, page_size: 30, has_more: false, version }));
  t.mock.method(api, 'kpMeta', async () => ({ categories: ['临床综合'], subjects: ['一、呼吸系统'], version }));
  const w = mount(KnowledgePage, { attachTo: document.body });
  t.after(() => w.unmount());
  await settle();
  return w;
}

test('outline import previews basic exclusion before confirming', async t => {
  const w = await setup(t);
  await button(w, '批量导入').trigger('click');
  await settle();
  const importCalls = [];
  t.mock.method(api, 'importKP', async (files, versionId, mode, opts = {}) => {
    importCalls.push({ mode, preview: !!opts.preview, token: opts.previewToken });
    if (opts.preview) {
      return { mode, accepted: 2, excluded_basic: 3, excluded_basic_codes: ['20.1', '20.2'], conflicted: [], invalid: [], total_current: 1, replace_would_remove: 0, preview_token: 'signed-preview' };
    }
    return { imported: 2, updated: 0, duplicated: 0, excluded_basic: 3, total: 3, version, files: ['a.csv'] };
  });
  // 没有文件时只能预览，不能直接导入。
  assert.equal(button(w, '预览导入内容').element.disabled, true);
  await pickFile(w);
  await button(w, '预览导入内容').trigger('click');
  await settle();
  assert.equal(importCalls.at(-1).preview, true);
  assert.match(w.text(), /可导入临床要点\s*2/);
  assert.match(w.text(), /剔除基础医学\s*3/);
  assert.match(w.text(), /20\.1、20\.2/);
  await button(w, '确认导入').trigger('click');
  await settle();
  assert.equal(importCalls.at(-1).preview, false);
  assert.equal(importCalls.at(-1).token, 'signed-preview');
  assert.match(w.text(), /导入完成/);
  assert.match(w.text(), /剔除基础医学\s*3/);
});

test('outline import blocks confirmation on conflicts, all-basic or invalid rows', async t => {
  const w = await setup(t);
  await button(w, '批量导入').trigger('click');
  await settle();
  t.mock.method(api, 'importKP', async (files, versionId, mode, opts = {}) => {
    if (opts.preview) {
      return { mode, accepted: 0, excluded_basic: 5, excluded_basic_codes: ['20.1'], conflicted: [{ file: 'a.csv', code: '21.1', reason: '分类矛盾' }], invalid: [{ file: 'a.csv', reason: '要点为空' }], total_current: 0, replace_would_remove: 0 };
    }
    throw new Error('不应执行导入');
  });
  await pickFile(w);
  await button(w, '预览导入内容').trigger('click');
  await settle();
  assert.match(w.text(), /没有可导入的临床要点/);
  assert.match(w.text(), /分类与专业矛盾/);
  const confirm = w.findAll('button').find(b => b.text().includes('确认导入'));
  assert.equal(confirm.element.disabled, true);
  await button(w, '返回修改').trigger('click');
  await settle();
  assert.doesNotMatch(w.text(), /预览结果/);
  assert.match(w.text(), /选择大纲文件/);
});

test('late outline preview is discarded after permission or identity changes', async t => {
  const w = await setup(t);
  let resolvePreview;
  t.mock.method(api, 'importKP', () => new Promise(resolve => { resolvePreview = resolve; }));
  await button(w, '批量导入').trigger('click');
  await pickFile(w);
  await button(w, '预览导入内容').trigger('click');
  setAuth('other-identity-token', { id: 'other-user', role: 'expert', permissions: [] });
  await settle();
  resolvePreview({ mode: 'merge', accepted: 999, excluded_basic: 555, conflicted: [], invalid: [] });
  await settle();
  assert.equal(w.find('.import-preview').exists(), false, 'old private preview must not reappear');
  assert.equal(w.find('dialog.knowledge-dialog').element.open, false);
});

test('normal permission heartbeat preserves selected files and an in-flight preview', async t => {
  const w = await setup(t);
  let resolvePreview;
  t.mock.method(api, 'importKP', () => new Promise(resolve => { resolvePreview = resolve; }));
  await button(w, '批量导入').trigger('click');
  await pickFile(w);
  await button(w, '预览导入内容').trigger('click');
  updateUser({ ...currentUser.value, permissions: [...currentUser.value.permissions] });
  await settle();
  assert.equal(w.find('dialog.knowledge-dialog').element.open, true);
  resolvePreview({ mode: 'merge', accepted: 2, excluded_basic: 1, conflicted: [], invalid: [], preview_token: 'heartbeat-preview' });
  await settle();
  assert.equal(w.find('.import-preview').exists(), true);
  assert.equal(button(w, '确认导入').element.disabled, false);
});
