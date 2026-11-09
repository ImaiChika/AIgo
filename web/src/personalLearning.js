import { sessionScope, createSessionRequestGate } from './sessionAccess.js';
export { sessionScope as learningScope, createSessionRequestGate as createLearningRequestGate };

export function learningCapabilities(permissions = []) {
  const p = new Set(permissions), view = p.has('question:view');
  const canGenerate = p.has('question:generate') || p.has('batch:run');
  const analyze = view && p.has('learning:analyze'), save = view && p.has('learning:save');
  const learnedUse = p.has('learning:use') && canGenerate;
  const history = view && (analyze || save || learnedUse);
  const libraryView = p.has('experience:view') || history;
  return { view, analyze, save, history, libraryView,
    manage: save || p.has('experience:view') && p.has('experience:manage'),
    use: learnedUse || p.has('experience:view') && p.has('experience:use') && canGenerate };
}
export function canAccessPersonalExperiences(has) {
  return has('experience:view') || has('question:view') && (has('learning:analyze') || has('learning:save') || has('learning:use') && (has('question:generate') || has('batch:run')));
}
export const experienceStates = { draft: '草稿 / 已停用', active: '已启用', archived: '已归档' };
export const candidateStyles = { concise: '简洁命题经验', detailed: '详细修改经验', checklist: '检查清单' };
export const learningFields = { clinical_stem: '题干', options: '选项', answer: '正确答案', explanation: '解析', difficulty: '难度', knowledge_points: '知识点', stem: '题干' };
export function displayLearningValue(value) {
  if (value == null || value === '') return '（空）';
  if (typeof value === 'string') return value;
  if (Array.isArray(value)) return value.map(v => typeof v === 'string' ? v : v?.label ? `${v.label}. ${v.text || ''}` : v?.topic || JSON.stringify(v)).join('\n');
  return JSON.stringify(value, null, 2);
}
export function scopeLabel(scope) {
  if (scope?.kind === 'all') return '本人全部临床题';
  if (scope?.kind === 'profession') return `专业：${scope.profession || '未选择'}`;
  if (scope?.kind === 'point') return `${scope.point_ids?.length || 0} 个指定知识点`;
  return '适用范围待核对';
}
export function validExperienceScope(scope) {
  return scope?.kind === 'all' || scope?.kind === 'profession' && Boolean(scope.profession?.trim()) || scope?.kind === 'point' && Array.isArray(scope.point_ids) && scope.point_ids.length > 0;
}
export function learningRequestKey() { return globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(36).slice(2)}`; }
// Persist only an opaque receipt key and a non-authoritative input fingerprint.
// The server still checks the complete intent before replaying an operation.
export function learningIntentKey(owner, role, operation, intent) {
  let hash = 2166136261;
  for (const char of JSON.stringify(intent)) { hash ^= char.charCodeAt(0); hash = Math.imul(hash, 16777619); }
  const name = `aigo_learning_receipt:${owner}:${role}:${operation}:${hash >>> 0}`;
  try { const found = sessionStorage.getItem(name); if (found) return found; const value = learningRequestKey(); sessionStorage.setItem(name, value); return value; }
  catch { return learningRequestKey(); }
}
export function sourceExpectation(q) {
  return { expected_version: q?.version, expected_library_revision: q?.library_revision || 0, expected_review_revision: q?.review_revision || 0 };
}
export function selectedExperienceWarnings(items) {
  const seen = new Set(), edits = new Map(), warnings = new Set();
  for (const item of items) for (const rule of item.rules || []) {
    const text = (rule.text || '').trim().replace(/\s+/g, '');
    if (text && seen.has(text)) warnings.add('所选规则有相同文本，建议合并或取消重复条目。');
    if (text) seen.add(text);
    if (rule.field && rule.before?.trim() && rule.after?.trim()) {
      const key = `${rule.field}:${rule.before.trim()}`, after = rule.after.trim();
      if (edits.has(key) && edits.get(key) !== after) warnings.add('同一原字段出现不同改法，请核对是否矛盾及其适用前提。');
      edits.set(key, after);
    }
  }
  return [...warnings];
}

export function experienceSourceLabel(card) {
  const source = card?.source || {};
  if (source.kind === 'manual') return '手工维护';
  if (source.kind === 'legacy_personal_preference') return '原个人偏好迁入';
  if (source.job_id && source.original_version > 0 && source.current_version > 0) return `原始 v${source.original_version} → 保存 v${source.current_version}`;
  return '来源记录待核对';
}
export function manualKnowledgeRules(text, medicalChange = false) {
  const chars = Array.from(text.trim());
  if (!chars.length || chars.length > 2000) return [];
  const rules = [];
  for (let i = 0; i < chars.length; i += 600) rules.push({ id: `manual-${rules.length + 1}`, text: chars.slice(i, i + 600).join(''), field: 'clinical_stem', reason: '', before: '', after: '', medical_change: medicalChange });
  return rules;
}
