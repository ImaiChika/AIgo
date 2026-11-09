// 一个可见标签共用一个定时器；同一订阅在途时合并重复通知。
export function createLiveRefresh({ document, window, intervalMs = 5000, setTimer = setInterval, clearTimer = clearInterval }) {
  const subscribers = new Set();
  let timer = null, attached = false;
  const visible = () => document.visibilityState === 'visible' && window.navigator?.onLine !== false;
  function refresh() {
    if (!visible()) return;
    for (const entry of subscribers) {
      if (entry.running) continue;
      entry.running = true;
      Promise.resolve().then(() => subscribers.has(entry) && entry.callback()).catch(() => {}).finally(() => { entry.running = false; });
    }
  }
  function visibilityChanged() {
    if (timer !== null) { clearTimer(timer); timer = null; }
    if (visible() && subscribers.size) { refresh(); timer = setTimer(refresh, intervalMs); }
  }
  const onStorage = event => { if (event.key === 'aigo:work-change') refresh(); };
  function attach() {
    if (attached) return;
    attached = true;
    document.addEventListener('visibilitychange', visibilityChanged);
    for (const name of ['focus', 'aigo:refresh-navigation-badges']) window.addEventListener(name, refresh);
    window.addEventListener('online', visibilityChanged);
    window.addEventListener('storage', onStorage);
    if (visible()) timer = setTimer(refresh, intervalMs);
  }
  function detach() {
    if (timer !== null) { clearTimer(timer); timer = null; }
    document.removeEventListener('visibilitychange', visibilityChanged);
    for (const name of ['focus', 'aigo:refresh-navigation-badges']) window.removeEventListener(name, refresh);
    window.removeEventListener('online', visibilityChanged);
    window.removeEventListener('storage', onStorage);
    attached = false;
  }
  return {
    refresh,
    subscribe(callback) { const entry = { callback, running: false }; subscribers.add(entry); attach(); return () => { subscribers.delete(entry); if (!subscribers.size) detach(); }; },
    destroy() { subscribers.clear(); detach(); },
  };
}
