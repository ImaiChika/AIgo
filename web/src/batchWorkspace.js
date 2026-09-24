const PREFIX = "aigo_batch_workspace_v1:";

export function batchWorkspaceKey(userId) {
  return `${PREFIX}${String(userId || "anonymous")}`;
}

export function loadBatchWorkspace(userId, storage = globalThis.localStorage) {
  if (!userId || !storage) return null;
  try {
    const value = JSON.parse(storage.getItem(batchWorkspaceKey(userId)) || "null");
    return value?.version === 1 && value.userId === userId && typeof value.jobId === "string" ? value : null;
  } catch { return null; }
}

export function saveBatchWorkspace(userId, jobId, storage = globalThis.localStorage) {
  if (!userId || !jobId || !storage) return false;
  try {
    storage.setItem(batchWorkspaceKey(userId), JSON.stringify({ version: 1, userId, jobId, savedAt: Date.now() }));
    return true;
  } catch { return false; }
}

export function clearBatchWorkspace(userId, storage = globalThis.localStorage) {
  if (!userId || !storage) return;
  try { storage.removeItem(batchWorkspaceKey(userId)); } catch { /* 隐私模式下不影响页面 */ }
}
