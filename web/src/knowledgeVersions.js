// Notify other open workspaces when their selected syllabus may have changed.
const CHANGE_KEY = "aigo_knowledge_versions_changed";
export function notifyKnowledgeVersionChange() {
  window.dispatchEvent(new Event(CHANGE_KEY));
  try { localStorage.setItem(CHANGE_KEY, `${Date.now()}-${Math.random()}`); } catch { /* storage may be disabled */ }
}
export function subscribeKnowledgeVersions(refresh) {
  const onStorage = event => { if (event.key === CHANGE_KEY) refresh(); };
  const onFocus = () => { if (document.visibilityState !== "hidden") refresh(); };
  window.addEventListener(CHANGE_KEY, refresh);
  window.addEventListener("storage", onStorage);
  window.addEventListener("focus", onFocus);
  return () => {
    window.removeEventListener(CHANGE_KEY, refresh);
    window.removeEventListener("storage", onStorage);
    window.removeEventListener("focus", onFocus);
  };
}
