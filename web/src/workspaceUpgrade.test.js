import test from 'node:test';
import assert from 'node:assert/strict';
import { upgradeBatchWorkspace } from './workspaceUpgrade.js';
import { loadBatchWorkspace, saveBatchWorkspace, batchWorkspaceKey } from './batchWorkspace.js';
import { loadGenerationWorkspace, generationWorkspaceKey } from './generationWorkspaceState.js';
const canonical = value => Array.isArray(value) ? `[${value.map(canonical).join(',')}]` : value && typeof value === 'object' ? `{${Object.keys(value).sort().map(k => `${JSON.stringify(k)}:${canonical(value[k])}`).join(',')}}` : JSON.stringify(value);
const memory = () => { const map = new Map(); return { getItem: k => map.get(k) || null, setItem: (k, v) => map.set(k, v), removeItem: k => map.delete(k) }; };

test('retiring the old choice preserves an ambiguous accepted batch key and every other input', () => {
  const payload = { mode: 'random_total', knowledge_point_ids: ['a', 'b'], total_count: 3, version_id: '2026', skip_existing: true, job_name: '原任务', apply_personal_rules: false, apply_personal_experiences: false, workflow_mode: 'knowledge', presentation: { section_order: ['a', 'b'] }, future_scope_field: 'must-preserve' };
  const intent = { owner_id: 'owner', role: 'teacher', payload };
  const current = upgradeBatchWorkspace({ applyPersonalRules: false, applyPersonalExperiences: false, pendingBatchSubmissions: [{ intent: canonical(intent), requestKey: 'lost-response-receipt' }] });
  const { apply_personal_rules, ...expected } = payload;
  assert.equal(current.pendingBatchSubmissions[0].intent, canonical({ ...intent, payload: expected }));
  assert.equal(current.pendingBatchSubmissions[0].requestKey, 'lost-response-receipt');
  assert.equal(current.applyPersonalExperiences, false); assert.equal(Object.hasOwn(current, 'applyPersonalRules'), false);
});

test('loading and saving old batch browser state repairs it without replacing its job or receipt', () => {
  const storage = memory(), original = { version: 1, userId: 'a', jobId: 'already-submitted', applyPersonalRules: true, selectedKPs: [{ id: 'p', version_id: 'v' }], pendingBatchSubmissions: [{ requestKey: 'original-key', intent: canonical({ owner_id: 'a', role: '', payload: { mode: 'per_point', count: 1, apply_personal_rules: true, apply_personal_experiences: true } }) }] };
  storage.setItem(batchWorkspaceKey('a'), JSON.stringify(original));
  const loaded = loadBatchWorkspace('a', storage);
  assert.equal(loaded.jobId, original.jobId); assert.equal(loaded.pendingBatchSubmissions[0].requestKey, 'original-key');
  assert.equal(Object.hasOwn(JSON.parse(loaded.pendingBatchSubmissions[0].intent).payload, 'apply_personal_rules'), false);
  assert.equal(storage.getItem(batchWorkspaceKey('a')).includes('applyPersonalRules'), false);
  saveBatchWorkspace('a', original.jobId, storage, { batchConfig: { count: 2 } });
  assert.equal(loadBatchWorkspace('a', storage).pendingBatchSubmissions[0].requestKey, 'original-key');
});

test('a single-generation upgrade keeps its durable run and frozen progress data', () => {
  const storage = memory(), original = { version: 1, userId: 'a', runId: 'unfinished-run', workspaceStatus: 'recovering', applyPersonalRules: false, questionIds: ['passed', 'waiting'], generatedQuestions: [{ id: 'passed' }], runKnowledgePoints: [{ id: 'frozen-point' }], applyPersonalExperiences: false };
  storage.setItem(generationWorkspaceKey('a'), JSON.stringify(original));
  const loaded = loadGenerationWorkspace('a', storage), { applyPersonalRules, ...expected } = original;
  assert.deepEqual(loaded, expected); assert.equal(storage.getItem(generationWorkspaceKey('a')).includes('applyPersonalRules'), false);
});

test('a cache without the retired field preserves its receipt bytes and a read-only cache remains readable', () => {
  const entry = { requestKey: 'same', intent: '{"owner_id":"a", "payload":{"apply_personal_experiences":false}}' };
  assert.equal(upgradeBatchWorkspace({ pendingBatchSubmissions: [entry] }).pendingBatchSubmissions[0], entry);
  const value = JSON.stringify({ version: 1, userId: 'a', runId: 'keep-me', applyPersonalRules: true });
  const loaded = loadGenerationWorkspace('a', { getItem: () => value, setItem: () => { throw new Error('read only'); } });
  assert.equal(loaded.runId, 'keep-me'); assert.equal(Object.hasOwn(loaded, 'applyPersonalRules'), false);
});
