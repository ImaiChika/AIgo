import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { registerHooks } from 'node:module';
import { parse,compileScript } from '@vue/compiler-sfc';
import { JSDOM } from 'jsdom';
registerHooks({load(url,context,next){if(!url.endsWith('.vue'))return next(url,context);const{descriptor}=parse(readFileSync(fileURLToPath(url),'utf8'));return{format:'module',source:compileScript(descriptor,{id:url,inlineTemplate:true}).content,shortCircuit:true};}});
const dom=new JSDOM('<!doctype html><html><body></body></html>',{url:'http://localhost/'});
for(const key of ['window','document','Element','HTMLElement','SVGElement','Node','Event','localStorage','sessionStorage'])globalThis[key]=dom.window[key];
const {mount,flushPromises}=await import('@vue/test-utils');const {nextTick,defineComponent,h,ref}=await import('vue');
const {api}=await import('./api.js');const {setAuth,updateUser,currentUser}=await import('./auth.js');
const {default:Policy}=await import('./components/GenerationPolicyNotice.vue');
const {default:Selector}=await import('./components/KnowledgeWorkflowSelector.vue');
const {default:Provenance}=await import('./components/KnowledgeProvenance.vue');
const {standardSectionOrder,imagingFirstOrder}=await import('./presentation.js');
const settle=async()=>{await flushPromises();await nextTick()};

async function helpText(w,label){const button=w.findAll('button').find(b=>b.attributes('aria-label')===label);assert.ok(button,`missing help ${label}`);await button.trigger('mouseenter');await flushPromises();await nextTick();return document.querySelector('[role="tooltip"]').textContent;}
function setup(t){document.body.innerHTML='';setAuth('kb-session',{id:'kb-user',role:'teacher',permissions:['kb:view','kb:use']});t.mock.method(api,'me',async()=>currentUser.value);}
const button=(w,name)=>w.findAll('button').find(x=>x.text().includes(name));
test('knowledge checkbox needs no preview and preserves explicit choice after revoke',async t=>{
 setup(t);let calls=0;t.mock.method(api,'prepareKnowledgeWorkflow',async()=>{calls++;throw new Error('must not preview')});
 const w=mount(Selector,{props:{modelValue:'knowledge',knowledgePoints:[{id:'no-card',version_id:'v'}]}});t.after(()=>w.unmount());await settle();
 assert.equal(w.get('input[type="checkbox"]').element.checked,true);assert.equal(w.find('select').exists(),false);assert.doesNotMatch(w.text(),/查看将用|缺少|预览/);assert.equal(calls,0);
 updateUser({...currentUser.value,permissions:['kb:view']});await settle();assert.match(w.text(),/已无知识库使用权限/);assert.equal(w.get('input').element.checked,true);assert.equal(w.emitted('update:modelValue'),undefined);
 await w.get('input').setValue(false);assert.deepEqual(w.emitted('update:modelValue').at(-1),['direct']);
});
test('checking knowledge lookup works before choosing any point without a request',async t=>{
 setup(t);let calls=0;t.mock.method(api,'prepareKnowledgeWorkflow',async()=>{calls++;return {bundles:[]}});
 const w=mount(Selector,{props:{modelValue:'direct'}});t.after(()=>w.unmount());await w.get('input').setValue(true);assert.deepEqual(w.emitted('update:modelValue').at(-1),['knowledge']);await w.setProps({modelValue:'knowledge',knowledgePoints:[{id:'other'}]});await settle();assert.equal(calls,0);
});
test('question provenance never repopulates after losing the view grant',async t=>{
 setup(t);let resolve;t.mock.method(api,'questionGenerationContext',()=>new Promise(r=>{resolve=r}));const w=mount(Provenance,{props:{question:{id:'q1',workflow_version:'knowledge-v1'}}});t.after(()=>w.unmount());await button(w,'生成依据').trigger('click');await settle();updateUser({...currentUser.value,permissions:[]});await settle();resolve({target:{target:'旧目标'},basis:{fact_refs:[]},bundle:{cards:[]}});await settle();assert.equal(w.text(),'');
});
test('direct generation performs no reference request',async t=>{
 setup(t);let calls=0;t.mock.method(api,'prepareKnowledgeWorkflow',async()=>{calls++;return {bundles:[]}});const w=mount(Selector,{props:{modelValue:'direct',knowledgePoints:[{id:'p',version_id:'v'}]}});t.after(()=>w.unmount());await settle();assert.equal(calls,0);assert.doesNotMatch(w.text(),/查看将用的资料/);
});

test('v2 provenance shows original case and option evidence then clears on revoke',async t=>{
 setup(t);
 t.mock.method(api,'questionGenerationContext',async()=>({workflow_version:'knowledge-v2',target:{target:'按病例证据判断'},basis:{condition_notes:'本例前提',answer_support:'本例支持',fact_refs:['card@2:f1'],case_plan:{person:'患者，女，41岁。',facts:[{id:'c1',section:'chief_complaint',text:'发热咳嗽4日。'}],lead_in:'该患者最可能的诊断是',answer:'A',option_evidence:[{label:'A',case_refs:['c1'],fact_refs:['card@2:f1'],reason:'使用已给出的病例证据。',limitations:'不能推断具体病原。'}],preference_notes:['本次采用主诉在前。']}},bundle:{cards:[{id:'card',revision:2,title:'测试知识卡',source:'测试来源',content:{facts:[{id:'f1',text:'仅作映射测试的事实',applies_when:'测试前提'}]}}]}}));
 const w=mount(Provenance,{props:{question:{id:'q-v2',workflow_version:'knowledge-v2',answer:'B'}}});t.after(()=>w.unmount());
 await button(w,'生成依据').trigger('click');await settle();
 assert.match(w.text(),/首次生成的病例与选项依据/);assert.match(w.text(),/A 项 · 首次生成答案/);assert.match(w.text(),/引用病例：发热咳嗽4日/);assert.match(w.text(),/引用知识：仅作映射测试的事实/);assert.match(w.text(),/不能推断具体病原/);assert.match(w.text(),/本次偏好说明（生成器自述）/);assert.match(await helpText(w,'首次生成依据说明'),/不能当作对当前版本的重新检查/);
 updateUser({...currentUser.value,permissions:[]});await settle();assert.equal(w.text(),'');
});

test('legacy provenance stays readable without a clinical plan',async t=>{
 setup(t);t.mock.method(api,'questionGenerationContext',async()=>({workflow_version:'knowledge-v1',target:{target:'旧目标'},basis:{condition_notes:'旧条件',answer_support:'旧依据',fact_refs:[]},bundle:{cards:[]}}));
 const w=mount(Provenance,{props:{question:{id:'old-q',workflow_version:'knowledge-v1'}}});t.after(()=>w.unmount());await button(w,'生成依据').trigger('click');await settle();assert.match(w.text(),/旧目标/);assert.match(w.text(),/旧依据/);assert.doesNotMatch(w.text(),/首次生成的病例与选项依据/);
});

function editableSelector(t){
 const mode=ref('knowledge'),presentation=ref(null);
 const Host=defineComponent({setup:()=>()=>h(Selector,{modelValue:mode.value,presentation:presentation.value,knowledgePoints:[{id:'p',version_id:'v'}],'onUpdate:modelValue':v=>mode.value=v,'onUpdate:presentation':v=>presentation.value=v})});
 const w=mount(Host);t.after(()=>w.unmount());return {w,mode,presentation};
}
test('new tasks show mandatory order while preserving old-task messaging',async t=>{
 setup(t);const {w,presentation,mode}=editableSelector(t);
 assert.equal(w.find('[aria-label="本次病例分组顺序"]').exists(),false);const notice=mount(Policy,{attachTo:document.body});t.after(()=>notice.unmount());const explanation=await helpText(notice,'命题要求说明');assert.match(explanation,/新题依次叙述信息、主诉、现病史/);assert.match(explanation,/省略无关模块/);assert.match(explanation,/已提交任务沿用原设置/);assert.equal(presentation.value,null);
 presentation.value={section_order:standardSectionOrder()};mode.value='direct';await settle();assert.equal(presentation.value,null);
 mode.value='knowledge';presentation.value={section_order:standardSectionOrder()};setAuth('other-user-session',{...currentUser.value,id:'other-user'});await settle();assert.equal(presentation.value,null);
});
for(const [state,label] of [['applied','已按本次设置呈现'],['not_applicable','无需比较顺序'],['unverified','待核对']])test(`v3 feedback ${state} stays scoped to the initial artifact`,async t=>{
 setup(t);t.mock.method(api,'questionGenerationContext',async()=>({workflow_version:'knowledge-v3',presentation:{section_order:standardSectionOrder()},presentation_feedback:{state,section_order:standardSectionOrder(),unused_sections:['vitals']},target:{target:'隔离目标'},basis:{fact_refs:[],condition_notes:'隔离条件',answer_support:'隔离依据',case_plan:{person:'患者，男，45岁。',facts:[],option_evidence:[]}},bundle:{cards:[]}}));
 const w=mount(Provenance,{props:{question:{id:'v3-question',version:2,workflow_version:'knowledge-v3'}}});t.after(()=>w.unmount());await button(w,'生成依据').trigger('click');await settle();
 assert.match(w.text(),new RegExp(label));assert.match(w.text(),/首次生成/);assert.match(await helpText(w,'病例分组说明'),/其他文字要求仍待核对/);assert.match(w.text(),/本题未使用：生命征/);
 updateUser({...currentUser.value,permissions:[]});await settle();assert.equal(w.text(),'');
});
