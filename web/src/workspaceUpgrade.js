// Historical browser-cache adapter only. No retired field is sent to a new API
// request; an accepted request's opaque receipt must survive a client upgrade.
function canonical(value) {
  if (Array.isArray(value)) return `[${value.map(canonical).join(',')}]`;
  if (value && typeof value === 'object') return `{${Object.keys(value).sort().map(k => `${JSON.stringify(k)}:${canonical(value[k])}`).join(',')}}`;
  return JSON.stringify(value);
}
export function upgradeGenerationWorkspace(value) {
  const { applyPersonalRules: retiredChoice, ...current } = value;
  return current;
}
export function upgradeBatchWorkspace(value) {
  const current = upgradeGenerationWorkspace(value);
  if (!Array.isArray(current.pendingBatchSubmissions)) return current;
  current.pendingBatchSubmissions = current.pendingBatchSubmissions.map(item => {
    try {
      const intent = JSON.parse(item.intent);
      if (!intent?.payload || !Object.hasOwn(intent.payload, 'apply_personal_rules')) return item;
      const { apply_personal_rules: retiredChoice, ...payload } = intent.payload;
      return { ...item, intent: canonical({ ...intent, payload }) };
    } catch { return item; }
  });
  return current;
}
export function repairWorkspaceCache(storage, key, before, after) {
  try {
    const text = JSON.stringify(after);
    if (text !== JSON.stringify(before)) storage.setItem(key, text);
  } catch { /* A read-only browser cache still returns the upgraded in-memory value. */ }
}
