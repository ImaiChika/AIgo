import test from 'node:test';
import assert from 'node:assert/strict';
import { reactive } from 'vue';
import { knowledgeCapabilities, knowledgeScope, createKnowledgeRequestGate, toKnowledgeEditor, fromKnowledgeEditor } from './knowledgeBaseAccess.js';
test('KB management and use require current identity view permission',()=>{ for(let mask=0;mask<8;mask++){const permissions=['kb:view','kb:manage','kb:use'].filter((_,i)=>mask&(1<<i));assert.deepEqual(knowledgeCapabilities(permissions),{view:!!(mask&1),manage:(mask&3)===3,use:(mask&5)===5});}});
test('stale KB reads cannot repopulate data after identity or permission change',()=>{const gate=createKnowledgeRequestGate();const user={id:'a',role:'teacher',permissions:['kb:view','kb:manage']};const scope=knowledgeScope(user,'one');const ticket=gate.start(scope);assert.ok(gate.accepts(ticket,scope));assert.equal(gate.accepts(ticket,knowledgeScope({...user,role:'expert'},'one')),false);assert.equal(gate.accepts(ticket,knowledgeScope({...user,permissions:[]},'one')),false);gate.invalidate();assert.equal(gate.accepts(ticket,scope),false);});
test('editor round trips reactive cards, preserving fact IDs and filtering blank options',()=>{const editor=reactive(toKnowledgeEditor());editor.lines.features='一\n\n二';editor.content.facts.push({id:'f1',text:'事实',applies_when:'前提',source:'出处'});const saved=fromKnowledgeEditor(editor);assert.deepEqual(saved.content.features,['一','二']);assert.equal(saved.content.reference,undefined);assert.equal(saved.content.facts[0].id,'f1');editor.kind='exam_reference';editor.content.reference.options[0].text='选项';assert.deepEqual(fromKnowledgeEditor(editor).content.reference.options,[{label:'A',text:'选项'}]);});

test('global method editor saves only method text and clears point-specific medical data',()=>{
 const editor=toKnowledgeEditor();editor.kind='authoring_guideline';editor.knowledge_points=[{id:'p',version_id:'v'}];editor.lines.exam_targets='旧目标';editor.content.facts=[{id:'f',text:'旧医学事实'}];editor.content.custom_text='同一维度的互斥选项';
 const saved=fromKnowledgeEditor(editor);assert.deepEqual(saved.knowledge_points,[]);assert.deepEqual(saved.content.facts,[]);assert.deepEqual(saved.content.exam_targets,[]);assert.equal(saved.content.custom_text,'同一维度的互斥选项');assert.equal(saved.content.reference,undefined);
});

test('expression intent is opt-in, survives custom-card editing and is cleared for other kinds',()=>{
 const old=toKnowledgeEditor();assert.equal(old.content.expression,undefined);
 old.kind='custom';old.content.expression={instructions:'各理由先说明依据',explanation_note:'教学提示。'};
 const saved=fromKnowledgeEditor(old);assert.deepEqual(toKnowledgeEditor(saved).content.expression,old.content.expression);
 old.kind='medical';assert.equal(fromKnowledgeEditor(old).content.expression,undefined);
});
