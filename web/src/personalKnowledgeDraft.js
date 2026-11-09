const PREFIX = 'aigo_personal_knowledge_draft_v1:';
export function personalKnowledgeDraftKey(user) {
  return `${PREFIX}${encodeURIComponent(user?.id || '')}:${encodeURIComponent(user?.role || '')}`;
}
export function readPersonalKnowledgeDraft(user, storage = globalThis.sessionStorage) {
  if (!user?.id || !storage) return null;
  try {
    const value = JSON.parse(storage.getItem(personalKnowledgeDraftKey(user)) || 'null');
    const draft = value?.draft;
    if (value?.version !== 1 || value.owner !== user.id || value.role !== (user.role || '') ||
      typeof draft?.createKey !== 'string' || !draft.createKey || typeof draft.title !== 'string' || typeof draft.content !== 'string' ||
      !['all', 'point', 'profession'].includes(draft.scope?.kind)) return null;
    return { ...draft, medical: Boolean(draft.medical), confirmed: Boolean(draft.confirmed), pending: draft.pending || null };
  } catch { return null; }
}
export function savePersonalKnowledgeDraft(user, draft, storage = globalThis.sessionStorage) {
  if (!user?.id || !storage) return;
  try {
    if (!draft) storage.removeItem(personalKnowledgeDraftKey(user));
    else storage.setItem(personalKnowledgeDraftKey(user), JSON.stringify({ version: 1, owner: user.id, role: user.role || '', draft }));
  } catch { /* The in-memory draft and beforeunload guard remain available. */ }
}
export function clearPersonalKnowledgeDrafts(userId, storage = globalThis.sessionStorage) {
  if (!userId || !storage) return;
  try {
    const prefix = `${PREFIX}${encodeURIComponent(userId)}:`;
    const keys = Array.from({ length: storage.length }, (_, i) => storage.key(i));
    for (const key of keys) if (key?.startsWith(prefix)) storage.removeItem(key);
  } catch { /* Signing out must remain possible in private browsing. */ }
}
