import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { registerHooks } from "node:module";
import { parse, compileScript } from "@vue/compiler-sfc";
import { JSDOM } from "jsdom";
registerHooks({load(url,context,next){if(!url.endsWith(".vue"))return next(url,context);const {descriptor}=parse(readFileSync(fileURLToPath(url),"utf8"));return {format:"module",source:compileScript(descriptor,{id:url,inlineTemplate:true}).content,shortCircuit:true};}});
const dom=new JSDOM("<!doctype html><html><body></body></html>",{url:"http://localhost/"});
for(const key of ["window","document","Element","HTMLElement","SVGElement","Node","Event","localStorage","sessionStorage"])globalThis[key]=dom.window[key];
const {mount,flushPromises}=await import("@vue/test-utils");
const {nextTick}=await import("vue");
const {api}=await import("./api.js");
const {default:Page}=await import("./pages/GenerationQuotaPage.vue");
const settle=async()=>{await flushPromises();await nextTick()};
const search=w=>w.get('input[aria-label="允许联网搜索"]');
const extractor=w=>w.get('input[aria-label="允许网页抓取"]');
const button=(w,text)=>w.findAll("button").find(x=>x.text().includes(text));
function fixture(t){
  const f={quota:{revision:1,single_max_questions:20,batch_max_questions:100,single_active_per_user:2,batch_active_per_user:1,global_pending_questions:500,generation_concurrency:2,batch_concurrency:2,ai_check_concurrency:2,model_tools:{web_search_enabled:false,web_extractor_enabled:false}},saves:[]};
  const response=()=>({quota:structuredClone(f.quota),usage:{},runtime_available:true,runtime:{}});
  t.mock.method(api,"generationQuota",async()=>response());
  t.mock.method(api,"saveGenerationQuota",async next=>{f.saves.push(structuredClone(next));f.quota={...next,revision:next.revision+1};return response()});
  return f;
}
async function page(t){const w=mount(Page,{attachTo:document.body});t.after(()=>w.unmount());await settle();return w}

test("network controls persist with quota and disabling search clears the dependent extractor",async t=>{
  const f=fixture(t),w=await page(t);
  assert.equal(w.find('[aria-label="AI总结并发"]').exists(),false);
  assert.equal(search(w).element.checked,false);assert.equal(extractor(w).element.disabled,true);
  await search(w).setValue(true);await extractor(w).setValue(true);
  assert.equal(f.quota.model_tools.web_search_enabled,false);
  await button(w,"保存并立即生效").trigger("click");await settle();
  assert.deepEqual(f.saves[0].model_tools,{web_search_enabled:true,web_extractor_enabled:true});
  assert.equal(f.saves[0].batch_max_questions,100);assert.equal(f.saves[0].revision,1);
  assert.match(w.text(),/配置已保存/);
  await search(w).setValue(false);
  assert.equal(extractor(w).element.checked,false);assert.equal(extractor(w).element.disabled,true);
  await button(w,"保存并立即生效").trigger("click");await settle();
  assert.deepEqual(f.saves[1].model_tools,{web_search_enabled:false,web_extractor_enabled:false});
});

test("another editor cannot overwrite a dirty tool draft through polling or a stale save",async t=>{
  const f=fixture(t),w=await page(t);await search(w).setValue(true);
  f.quota.revision=2;await button(w,"立即刷新").trigger("click");await settle();
  assert.equal(search(w).element.checked,true);assert.match(w.text(),/其他窗口已修改配置/);
  t.mock.method(api,"saveGenerationQuota",async p=>{assert.equal(p.revision,1);const e=new Error("配置已变化");e.status=409;throw e});
  await button(w,"保存并立即生效").trigger("click");await settle();
  assert.equal(search(w).element.checked,true);assert.match(w.text(),/配置已变化/);
  await button(w,"载入最新配置").trigger("click");await settle();
  assert.equal(search(w).element.checked,false);assert.equal(extractor(w).element.disabled,true);
  assert.equal(button(w,"保存并立即生效").element.disabled,true);
});

test("failed initial read keeps controls hidden and can recover with refresh",async t=>{
  fixture(t);t.mock.method(api,"generationQuota",async()=>{throw new Error("服务不可用")});
  const w=await page(t);assert.match(w.text(),/读取配额失败/);assert.equal(w.find('input[aria-label="允许联网搜索"]').exists(),false);
  const f=fixture(t);await button(w,"立即刷新").trigger("click");await settle();assert.equal(search(w).element.checked,false);assert.doesNotMatch(w.text(),/读取配额失败/);
});
test("a failed save preserves the unsaved tool draft and saved indicator",async t=>{
  fixture(t);const w=await page(t);await search(w).setValue(true);
  t.mock.method(api,"saveGenerationQuota",async()=>{throw new Error("保存失败")});
  await button(w,"保存并立即生效").trigger("click");await settle();
  assert.equal(search(w).element.checked,true);assert.match(w.text(),/保存失败/);assert.doesNotMatch(w.text(),/配置已保存/);assert.match(w.get('.quota-tools').text(),/已保存：关闭/);
});

test('new backend exposes bounded learning concurrency while old backend hides it',async t=>{
 const f=fixture(t);f.quota.learning_concurrency=1;t.mock.method(api,'generationQuota',async()=>({quota:structuredClone(f.quota),usage:{},runtime_available:true,learning_runtime_available:true,runtime:{learning_analysis:{active:0,limit:1,waiting:0}}}));const w=await page(t);await w.get('[aria-label="AI总结并发"]').setValue(2);await button(w,'保存并立即生效').trigger('click');await settle();assert.equal(f.saves[0].learning_concurrency,2);
});
test('missing learning executor disables its control',async t=>{
 const f=fixture(t);f.quota.learning_concurrency=1;const response=()=>({quota:structuredClone(f.quota),runtime_available:true,learning_runtime_available:false});t.mock.method(api,'generationQuota',async()=>response());const w=await page(t);assert.equal(w.get('[aria-label="AI总结并发"]').element.disabled,true);
});

test('a backend dropping learning settings cannot falsely acknowledge the draft',async t=>{
 const f=fixture(t);f.quota.learning_concurrency=1;t.mock.method(api,'generationQuota',async()=>({quota:structuredClone(f.quota),runtime_available:true,learning_runtime_available:true}));const w=await page(t);await w.get('[aria-label="AI总结并发"]').setValue(2);
 t.mock.method(api,'saveGenerationQuota',async()=>({quota:{...structuredClone(f.quota),learning_concurrency:undefined,revision:2},runtime_available:true,learning_runtime_available:true}));await button(w,'保存并立即生效').trigger('click');await settle();assert.equal(w.get('[aria-label="AI总结并发"]').element.value,'2');assert.doesNotMatch(w.text(),/配置已保存/);assert.match(w.text(),/总结并发/);
});
