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
const { default: Page } = await import('./pages/AIProviderPage.vue');

const settle=async()=>{await flushPromises();await nextTick();};
test('new provider requires explicit activation while editing preserves its current state',async t=>{
 t.mock.method(api,'listAIProviders',async()=>({providers:[{id:'live',name:'现用模型',deployment:'cloud',base_url:'https://example.invalid/v1',generation_model:'model',check_model:'model',active:true}]}));
 const w=mount(Page,{attachTo:document.body});t.after(()=>w.unmount());await settle();await w.findAll('button').find(b=>b.text()==='＋ 新增配置').trigger('click');await settle();
 const active=w.findAll('input[type="checkbox"]').find(x=>x.element.parentElement.textContent.includes('保存后立即启用'));assert.ok(active);assert.equal(active.element.checked,false);
 await w.findAll('button').find(b=>b.text()==='取消').trigger('click');await w.findAll('button').find(b=>b.text()==='编辑').trigger('click');await settle();
 const editing=w.findAll('input[type="checkbox"]').find(x=>x.element.parentElement.textContent.includes('保存后立即启用'));assert.equal(editing.element.checked,true);

});

test('summary configuration has its own service and defaults to 0.6 without changing generation',async t=>{
 let saved; let generationWrites=0;
 t.mock.method(api,'listAIProviders',async()=>({providers:[{id:'live',name:'现用出题',generation_model:'generation-model',check_model:'check-model',active:true}]}));
 t.mock.method(api,'getLearningProvider',async()=>({independent:false,deployment:'cloud',base_url:'',model:'qwen3.5-flash',temperature:0.6,api_key_configured:false}));
 t.mock.method(api,'saveLearningProvider',async payload=>{saved=payload;return {...payload,api_key:undefined,api_key_configured:true,api_key_masked:'***saved'};});
 t.mock.method(api,'updateAIProvider',async()=>{generationWrites++;});
 const w=mount(Page,{attachTo:document.body});t.after(()=>w.unmount());await settle();
 const tab=w.findAll('button').find(b=>b.text()==='AI总结');assert.ok(tab,'summary tab missing');await tab.trigger('click');await settle();
 assert.equal(w.find('input[type="number"]').element.value,'0.6');
 await w.find('input[value="independent"]').setValue();await settle();
 await w.find('input[aria-label="总结 API 地址"]').setValue('https://summary.example/v1');
 await w.find('input[aria-label="总结模型"]').setValue('qwen3.8-flash');
 await w.find('input[type="password"]').setValue('new-summary-secret');
 await w.find('form').trigger('submit');await settle();
 assert.equal(saved.model,'qwen3.8-flash');assert.equal(saved.temperature,0.6);assert.equal(saved.independent,true);assert.equal(generationWrites,0);
 assert.equal(w.find('input[type="password"]').element.value,'');assert.equal(w.text().includes('new-summary-secret'),false);
});

test('failed summary settings read cannot overwrite saved settings with defaults',async t=>{
 let saves=0;t.mock.method(api,'listAIProviders',async()=>({providers:[]}));t.mock.method(api,'getLearningProvider',async()=>{throw new Error('offline');});t.mock.method(api,'saveLearningProvider',async()=>{saves++;});
 const w=mount(Page,{attachTo:document.body});t.after(()=>w.unmount());await settle();await w.findAll('button').find(b=>b.text()==='AI总结').trigger('click');await settle();
 assert.ok(w.text().includes('读取总结配置失败'));assert.equal(w.findAll('button').some(b=>b.text()==='保存总结配置'),false);await w.find('form').trigger('submit');assert.equal(saves,0);
});

test('generation and first-check sampling preserve defaults and save independently',async t=>{
 let saved,providerWrites=0,summaryWrites=0;
 t.mock.method(api,'listAIProviders',async()=>({providers:[]}));
 t.mock.method(api,'getModelSampling',async()=>({generation_temperature:0.4,check_temperature:0.2,revision:3}));
 t.mock.method(api,'saveModelSampling',async payload=>{saved=payload;return {...payload,revision:4};});
 t.mock.method(api,'updateAIProvider',async()=>{providerWrites++;});
 t.mock.method(api,'saveLearningProvider',async()=>{summaryWrites++;});
 const w=mount(Page,{attachTo:document.body});t.after(()=>w.unmount());await settle();
 await w.findAll('button').find(b=>b.text()==='调用参数').trigger('click');await settle();
 assert.equal(w.find('input[aria-label="出题温度"]').element.value,'0.4');
 assert.equal(w.find('input[aria-label="首检温度"]').element.value,'0.2');
 await w.find('input[aria-label="出题温度"]').setValue('0');
 await w.find('input[aria-label="首检温度"]').setValue('0.6');
 await w.find('form').trigger('submit');await settle();
 assert.deepEqual(saved,{generation_temperature:0,check_temperature:0.6,revision:3});
 assert.equal(providerWrites+summaryWrites,0);assert.ok(w.text().includes('调用参数已保存'));
});

test('sampling read failures and concurrent-save conflicts preserve safe edits',async t=>{
 let saves=0;
 t.mock.method(api,'listAIProviders',async()=>({providers:[]}));
 const reader=t.mock.method(api,'getModelSampling',async()=>{throw new Error('offline');});
 t.mock.method(api,'saveModelSampling',async()=>{saves++;throw new Error('调用参数已被其他窗口修改');});
 const w=mount(Page,{attachTo:document.body});t.after(()=>w.unmount());await settle();
 await w.findAll('button').find(b=>b.text()==='调用参数').trigger('click');await settle();
 assert.ok(w.text().includes('读取调用参数失败'));await w.find('form').trigger('submit');assert.equal(saves,0);
 reader.mock.mockImplementation(async()=>({generation_temperature:0.4,check_temperature:0.2,revision:2}));
 await w.findAll('button').find(b=>b.text()==='重新读取').trigger('click');await settle();
 await w.find('input[aria-label="出题温度"]').setValue('0.6');
 await w.find('form').trigger('submit');await settle();
 assert.equal(saves,1);assert.ok(w.text().includes('其他窗口修改'));assert.equal(w.find('input[aria-label="出题温度"]').element.value,'0.6');
});
