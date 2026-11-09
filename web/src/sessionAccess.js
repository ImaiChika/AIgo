export function sessionScope(user, token = "") {
  return JSON.stringify([user?.id || "", user?.role || "", token, [...(user?.permissions || [])].sort()]);
}
// 旧账号/旧身份/旧授权/旧选中人以及先发请求的响应不能恢复已清理的数据。
export function createSessionRequestGate() {
  let epoch = 0;
  return { invalidate() { epoch += 1; }, start(scope) { return { epoch: ++epoch, scope }; }, accepts(ticket, scope) { return ticket.epoch === epoch && ticket.scope === scope; } };
}

export function createSessionIdentityRefresher({ fetchMe, getToken, getUser, updateUser }) {
  let inFlight = null, key = "";
  return async function refresh() {
    const token = getToken(), id = getUser()?.id;
    if (!token || !id) return;
    const nextKey = `${id}:${token}`;
    if (inFlight && key === nextKey) return inFlight;
    key = nextKey;
    const request = fetchMe().then(fresh => {
      if (getToken() === token && getUser()?.id === id && fresh?.id === id) updateUser(fresh);
    });
    inFlight = request;
    try { await request; } finally { if (inFlight === request) { inFlight = null; key = ""; } }
  };
}
