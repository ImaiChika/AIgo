import test from 'node:test';
import assert from 'node:assert/strict';
import { sessionScope, createSessionRequestGate, createSessionIdentityRefresher } from './sessionAccess.js';
import { learningCapabilities, canAccessPersonalExperiences, manualKnowledgeRules, experienceSourceLabel } from './personalLearning.js';
import { visibleNavigation } from './navigation.js';

const codes = ['experience:view', 'experience:manage', 'question:view', 'learning:save', 'learning:analyze'];
const permissions = mask => codes.filter((_, i) => mask & (1 << i));
for (let mask = 0; mask < 32; mask++) test(`private knowledge and paid-learning permissions remain distinct ${mask}`, () => {
  const list = permissions(mask), cap = learningCapabilities(list), has = code => list.includes(code);
  const manualView = !!(mask & 1), manualManage = (mask & 3) === 3;
  const save = (mask & 12) === 12, analyze = (mask & 20) === 20;
  assert.equal(cap.libraryView, manualView || save || analyze);
  assert.equal(cap.manage, manualManage || save);
  assert.equal(cap.save, save); assert.equal(cap.analyze, analyze);
  assert.equal(cap.history, save || analyze);
  assert.equal(canAccessPersonalExperiences(has), cap.libraryView);
  const personal = visibleNavigation(has).flatMap(g => g.items).filter(i => i.path === '/my-experiences');
  assert.equal(personal.length, Number(cap.libraryView));
  if (personal.length) assert.equal(personal[0].label, '个人知识库');
});

test('manual-use grant retains its view dependency and does not grant AI job history', () => {
  for (const generate of ['question:generate', 'batch:run']) {
    const cap = learningCapabilities(['experience:view', 'experience:use', generate]);
    assert.equal(cap.use, true); assert.equal(cap.history, false); assert.equal(cap.save, false); assert.equal(cap.analyze, false);
    assert.equal(learningCapabilities(['experience:use', generate]).use, false);
    assert.equal(learningCapabilities(['learning:use', generate]).use, true);
  }
});

test('all permission transitions and account roles discard earlier request results', () => {
  for (let from = 0; from < 32; from++) for (let to = 0; to < 32; to++) {
    const gate = createSessionRequestGate();
    const ticket = gate.start(sessionScope({ id: 'u', role: 'teacher', permissions: permissions(from) }, 'old-token'));
    gate.invalidate();
    assert.equal(gate.accepts(ticket, sessionScope({ id: 'u', role: 'expert', permissions: permissions(to) }, 'new-token')), false);
  }
  const gate = createSessionRequestGate(), first = gate.start('same');
  const second = gate.start('same');
  assert.equal(gate.accepts(first, 'same'), false); assert.equal(gate.accepts(second, 'same'), true);
  assert.equal(gate.accepts(second, 'another-user'), false);
});

test('all-account permissions never authorize the active identity', () => {
  const account = { id: 'u', role: 'expert', permissions: [], account_permissions: codes };
  assert.equal(learningCapabilities(account.permissions).libraryView, false);
  assert.equal(learningCapabilities(account.permissions).manage, false);
});

test('concurrent identity refreshes merge only within the same session', async () => {
  let user = { id: 'a', permissions: codes }, token = 'a-token';
  const pending = [], applied = [];
  const refresh = createSessionIdentityRefresher({ getUser: () => user, getToken: () => token, updateUser: fresh => { user = fresh; applied.push(fresh); }, fetchMe: () => new Promise(resolve => pending.push(resolve)) });
  const first = refresh(), duplicate = refresh(); assert.equal(pending.length, 1);
  user = { id: 'b', permissions: [] }; token = 'b-token'; const next = refresh();
  pending[1]({ id: 'b', permissions: ['experience:view'] }); await next;
  pending[0]({ id: 'a', permissions: codes }); await Promise.all([first, duplicate]);
  assert.equal(applied.length, 1); assert.equal(user.id, 'b');
});

test('failed identity reads are retryable and a missing token never reads', async () => {
  let token = 'one', count = 0, user = { id: 'a', permissions: codes };
  const refresh = createSessionIdentityRefresher({ getToken: () => token, getUser: () => user, updateUser: fresh => { user = fresh; }, fetchMe: async () => { if (++count === 1) throw new Error('offline'); return { id: 'a', permissions: [] }; } });
  await assert.rejects(refresh(), /offline/); await refresh(); assert.equal(count, 2);
  token = ''; await refresh(); assert.equal(count, 2);
});

test('manual and migrated knowledge never invent an original question version', () => {
  assert.equal(experienceSourceLabel({ source: { kind: 'manual' } }), '手工维护');
  assert.equal(experienceSourceLabel({ source: { kind: 'legacy_personal_preference' } }), '原个人偏好迁入');
  assert.equal(experienceSourceLabel({ source: { job_id: 'j', original_version: 1, current_version: 3 } }), '原始 v1 → 保存 v3');
  assert.equal(experienceSourceLabel({}), '来源记录待核对');
});

test('manual 2000-character knowledge preserves Unicode when split into bounded entries', () => {
  const text = '医😀'.repeat(1000), parts = manualKnowledgeRules(text);
  assert.equal(parts.length, 4); assert.equal(parts.map(p => p.text).join(''), text);
  assert.ok(parts.every(p => Array.from(p.text).length <= 600 && !p.text.includes('\ufffd')));
  assert.deepEqual(manualKnowledgeRules(text + '多'), []);
});
