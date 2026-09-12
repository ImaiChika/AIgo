const PREFIX = "aigo_generation_workspace_v1:";

export function generationWorkspaceKey(userId) {
  return `${PREFIX}${String(userId || "anonymous")}`;
}

export function loadGenerationWorkspace(userId, storage = globalThis.localStorage) {
  if (!userId || !storage) return null;
  try {
    const value = JSON.parse(storage.getItem(generationWorkspaceKey(userId)) || "null");
    return value && value.version === 1 && value.userId === userId ? value : null;
  } catch {
    return null;
  }
}

export function saveGenerationWorkspace(userId, state, storage = globalThis.localStorage) {
  if (!userId || !storage) return false;
  try {
    storage.setItem(generationWorkspaceKey(userId), JSON.stringify({
      ...state,
      version: 1,
      userId,
      savedAt: Date.now(),
    }));
    return true;
  } catch {
    return false;
  }
}

export function clearGenerationWorkspace(userId, storage = globalThis.localStorage) {
  if (!userId || !storage) return;
  try {
    storage.removeItem(generationWorkspaceKey(userId));
  } catch {
    // 隐私模式或禁用本地存储时，清理失败不应阻断退出登录。
  }
}
