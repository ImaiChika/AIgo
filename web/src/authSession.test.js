import test from 'node:test';
import assert from 'node:assert/strict';
import { JSDOM } from 'jsdom';
const dom = new JSDOM('<!doctype html>', { url: 'http://localhost/' });
globalThis.window = dom.window;
globalThis.localStorage = dom.window.localStorage;
globalThis.sessionStorage = dom.window.sessionStorage;
const { watch } = await import('vue');
const { setAuth, updateUser, clearAuth, getToken, currentUser, hasPerm } = await import('./auth.js');
const jwt = (id, role) => `header.${Buffer.from(JSON.stringify({ user_id: id, role })).toString('base64url')}.signature`;
const teacher = () => ({ id: 'dual', role: 'teacher', permissions: ['question:share', 'question:view'] });
const expert = () => ({ id: 'dual', role: 'expert', permissions: ['review:do'] });
function reset() { clearAuth(); localStorage.clear(); sessionStorage.clear(); }
function storageEvent(key) { window.dispatchEvent(new window.StorageEvent('storage', { key })); }
const settled = async () => { await Promise.resolve(); await Promise.resolve(); };

test('flush-sync consumers never observe a new expert token with old teacher share permission', t => {
  reset(); setAuth(jwt('dual', 'teacher'), teacher());
  const observed = [], requests = [];
  const stop = watch(() => [getToken(), currentUser.value?.id, currentUser.value?.role, hasPerm('question:share')], pair => {
    observed.push(pair);
    if (getToken() && hasPerm('question:share')) requests.push(getToken());
  }, { flush: 'sync' }); t.after(stop);
  setAuth(jwt('dual', 'expert'), expert());
  assert.ok(observed.length > 0);
  assert.ok(observed.every(([token, id, role, share]) => token === jwt('dual', 'expert') && id === 'dual' && role === 'expert' && share === false), observed);
  assert.deepEqual(requests, []);
});

test('storage mirrors are coherent before any reactive session notification', t => {
  reset(); setAuth(jwt('dual', 'teacher'), teacher()); const observed = [];
  const stop = watch(() => [getToken(), currentUser.value, hasPerm('question:share')], ([token, user, share]) => {
    observed.push({ token, user, share, storedToken: localStorage.getItem('aigo_token') || '', storedUser: JSON.parse(localStorage.getItem('aigo_user') || 'null') });
  }, { flush: 'sync' }); t.after(stop);
  setAuth(jwt('dual', 'expert'), expert());
  updateUser({ ...expert(), display_name: '审题身份资料刷新' });
  clearAuth();
  assert.ok(observed.length >= 3);
  for (const item of observed) {
    assert.equal(item.token, item.storedToken);
    assert.deepEqual(item.user, item.storedUser);
    if (!item.token) { assert.equal(item.user, null); assert.equal(item.share, false); }
  }
});

test('an account change publishes one consistent new account and clears prior workspaces', t => {
  reset(); setAuth(jwt('dual', 'teacher'), teacher());
  localStorage.setItem('aigo_generation_workspace_v1:dual', JSON.stringify({ runId: 'private-A' }));
  localStorage.setItem('aigo_generation_workspace_v1:other', JSON.stringify({ runId: 'old-B' }));
  const observed = [], stop = watch(() => [getToken(), currentUser.value?.id], pair => observed.push(pair), { flush: 'sync' }); t.after(stop);
  setAuth(jwt('other', 'teacher'), { ...teacher(), id: 'other' });
  assert.ok(observed.every(([token, id]) => token === jwt('other', 'teacher') && id === 'other'));
  assert.equal(localStorage.getItem('aigo_generation_workspace_v1:dual'), null);
  assert.equal(localStorage.getItem('aigo_generation_workspace_v1:other'), null);
});

test('half-written real JWT and user storage pairs are ignored until both identities match', async () => {
  reset(); setAuth(jwt('dual', 'teacher'), teacher());
  localStorage.setItem('aigo_token', jwt('dual', 'expert')); storageEvent('aigo_token'); await settled();
  assert.equal(getToken(), jwt('dual', 'teacher')); assert.equal(currentUser.value.role, 'teacher');
  localStorage.setItem('aigo_user', JSON.stringify(expert())); storageEvent('aigo_user'); await settled();
  assert.equal(getToken(), jwt('dual', 'expert')); assert.equal(currentUser.value.role, 'expert'); assert.equal(hasPerm('question:share'), false);
  const other = { ...teacher(), id: 'other' };
  localStorage.setItem('aigo_user', JSON.stringify(other)); storageEvent('aigo_user'); await settled();
  assert.equal(currentUser.value.id, 'dual');
  localStorage.setItem('aigo_token', jwt('other', 'teacher')); storageEvent('aigo_token'); await settled();
  assert.equal(currentUser.value.id, 'other'); assert.equal(getToken(), jwt('other', 'teacher'));
});

test('legacy opaque token caches still synchronize when their two mirrors are written together', async () => {
  reset(); setAuth('old-opaque-token', teacher());
  localStorage.setItem('aigo_token', 'new-opaque-token'); storageEvent('aigo_token');
  localStorage.setItem('aigo_user', JSON.stringify(expert())); storageEvent('aigo_user');
  await settled(); assert.equal(getToken(), 'new-opaque-token'); assert.equal(currentUser.value.role, 'expert');
});

test('a stale me response cannot move current identity or overwrite a newer storage session', async () => {
  reset(); setAuth(jwt('dual', 'expert'), expert());
  updateUser(teacher()); assert.equal(currentUser.value.role, 'expert'); assert.equal(hasPerm('question:share'), false);
  updateUser({ ...expert(), id: 'foreign' }); assert.equal(currentUser.value.id, 'dual');
  localStorage.setItem('aigo_token', jwt('other', 'teacher'));
  localStorage.setItem('aigo_user', JSON.stringify({ ...teacher(), id: 'other' }));
  updateUser({ ...expert(), display_name: 'late-me' });
  assert.equal(JSON.parse(localStorage.getItem('aigo_user')).id, 'other');
  storageEvent('aigo_user'); await settled(); assert.equal(currentUser.value.id, 'other');
});

test('corrupt cross-tab user JSON cannot produce a token with stale permissions', async () => {
  reset(); setAuth(jwt('dual', 'teacher'), teacher());
  localStorage.setItem('aigo_token', jwt('dual', 'expert')); localStorage.setItem('aigo_user', '{bad');
  storageEvent('aigo_user'); await settled(); assert.equal(getToken(), jwt('dual', 'teacher'));
  localStorage.setItem('aigo_user', JSON.stringify(expert())); storageEvent('aigo_user'); await settled();
  assert.equal(currentUser.value.role, 'expert');
  localStorage.removeItem('aigo_token'); storageEvent('aigo_token'); await settled();
  assert.equal(getToken(), ''); assert.equal(currentUser.value, null); assert.equal(hasPerm('question:share'), false);
});

test('failed mirror persistence never publishes a partly stored new session', t => {
  reset(); setAuth(jwt('dual', 'teacher'), teacher());
  const realStorage = localStorage; let fail = true;
  globalThis.localStorage = { getItem: k => realStorage.getItem(k), removeItem: k => realStorage.removeItem(k), setItem(k, value) { if (k === 'aigo_user' && fail) { fail = false; throw new Error('storage unavailable'); } realStorage.setItem(k, value); } };
  t.after(() => { globalThis.localStorage = realStorage; });
  assert.throws(() => setAuth(jwt('dual', 'expert'), expert()), /storage unavailable/);
  assert.equal(getToken(), jwt('dual', 'teacher')); assert.equal(currentUser.value.role, 'teacher');
  assert.equal(localStorage.getItem('aigo_token'), jwt('dual', 'teacher'));
});
