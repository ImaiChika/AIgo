import { sessionScope, createSessionRequestGate } from './sessionAccess.js';
export { createSessionRequestGate as createKnowledgeRequestGate, sessionScope as knowledgeScope };
export function knowledgeCapabilities(permissions = []) {
  const have = new Set(permissions), view = have.has('kb:view');
  return { view, manage: view && have.has('kb:manage'), use: view && have.has('kb:use') };
}
export const cardKinds = { medical: '考点知识', exam_reference: '真题参考', custom: '自定义资料', authoring_guideline: '通用命题规范（全局）' };
export const cardStates = { draft: '草稿', active: '已启用', archived: '已归档' };
export function cardKindLabel(card){return card?.has_expression||card?.content?.expression?'表达要求':cardKinds[card?.kind];}
export function newKnowledgeCard() {
  return { title: '', kind: 'medical', revision: 0, source: '', note: '', knowledge_points: [], content: { exam_targets: [], features: [], preconditions: [], facts: [], pitfalls: [], custom_text: '' } };
}
export const dimensionFields = [ { key: 'exam_targets', name: '关键考点与目标', help: '每行一个具体目标，如诊断、鉴别或处理决策' }, { key: 'features', name: '临床与鉴别特征', help: '每行一项特征，注明适用情境' }, { key: 'preconditions', name: '适用条件与边界', help: '年龄、病程、分期、禁忌或其他前提，每行一项' }, { key: 'pitfalls', name: '易错点与反例', help: '每行一项，避免无条件或绝对化结论' } ];
export function toKnowledgeEditor(card) {
 if(card?.is_summary)throw new Error("请先读取完整知识卡再编辑。");
  const value = JSON.parse(JSON.stringify(card || newKnowledgeCard()));
  value.content = { ...newKnowledgeCard().content, ...value.content };
  value.lines = Object.fromEntries(dimensionFields.map(f => [f.key, (value.content[f.key] || []).join('\n')]));
  value.content.facts ||= []; value.knowledge_points ||= [];
  value.content.reference ||= { year: '', stem: '', options: ['A','B','C','D','E'].map(label => ({label, text:''})), answer: '', explanation: '' };
  return value;
}
export function fromKnowledgeEditor(editor) {
 if(editor?.is_summary)throw new Error("摘要不能作为知识卡正文保存。");
  const value = JSON.parse(JSON.stringify(editor)); delete value.lines;
  for (const f of dimensionFields) value.content[f.key] = editor.lines[f.key].split('\n').map(x => x.trim()).filter(Boolean);
  if (value.kind === 'authoring_guideline') {
    value.knowledge_points = [];
    value.content = { ...newKnowledgeCard().content, custom_text: value.content.custom_text };
  }
  if (value.kind !== 'exam_reference') delete value.content.reference;
  else value.content.reference.options = value.content.reference.options.filter(o => o.text.trim());
  if(value.kind!=='custom')delete value.content.expression;
  return value;
}
