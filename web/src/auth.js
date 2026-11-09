import { ref, computed } from "vue";
import { clearGenerationWorkspace } from "./generationWorkspaceState.js";
import { clearPersonalKnowledgeDrafts } from "./personalKnowledgeDraft.js";

const emptySession = () => ({ token: '', user: null });
function tokenIdentity(value) {
  try {
    const parts = value.split('.');
    if (parts.length !== 3) return null;
    const payload = parts[1].replace(/-/g, '+').replace(/_/g, '/');
    const claim = JSON.parse(atob(payload.padEnd(Math.ceil(payload.length / 4) * 4, '=')));
    return typeof claim.user_id === 'string' && typeof claim.role === 'string' ? claim : null;
  } catch { return null; }
}
function sameIdentity(first, second) {
  return first?.id === second?.id && (first?.role || '') === (second?.role || '');
}
function coherentPair(token, user) {
  if (!token) return !user;
  if (!user || typeof user !== 'object') return false;
  // This only checks cache consistency. JWT authenticity remains a server check.
  const claim = tokenIdentity(token);
  return !claim || claim.user_id === user.id && claim.role === (user.role || '');
}
function readStoredSession(previous) {
  try {
    const token = localStorage.getItem('aigo_token') || '';
    if (!token) return emptySession();
    const user = JSON.parse(localStorage.getItem('aigo_user') || 'null');
    if (!coherentPair(token, user) || previous?.token === token && previous.user && !sameIdentity(previous.user, user)) return null;
    return { token, user };
  } catch { return null; }
}
// Publish the credential and its active identity as one reactive snapshot.
// Synchronous consumers must never combine a new JWT with old permissions.
const session = ref(readStoredSession() || emptySession());
export const isLoggedIn = computed(() => !!session.value.token);
export const currentUser = computed(() => session.value.user);

// 角色显示名映射（内置角色的本地兜底）
export const roleNames = {
  super_admin: "超级管理员",
  admin: "管理员",
  expert: "审题老师",
  teacher: "命题教师",
};

// 自定义角色（ID 形如 role-<时间戳>）的显示名由服务端随登录/me 响应下发，
// 本地映射只认识内置角色，必须优先查 currentUser.role_names，否则界面会把
// 原始 ID 当名字展示。
export function roleName(role, roleNamesFromServer) {
  if (role && roleNamesFromServer && roleNamesFromServer[role]) return roleNamesFromServer[role];
  if (role && session.value.user?.role_names && session.value.user.role_names[role]) return session.value.user.role_names[role];
  return roleNames[role] || (role ? role : "未分配角色");
}

const permissionNames = {
  "experience:view": "查看个人知识库",
  "experience:manage": "管理个人知识库",
  "experience:use": "生成时应用本人手工知识",
  "learning:analyze": "总结本人题目修改经验",
  "learning:save": "管理本人命题经验",
  "learning:use": "生成时应用本人经验",
  "question:generate": "单题出题",
  "batch:run": "批量推理",
  "stats:view": "数据统计",
  "flow:manage": "审核流程",
  "generation_quota:manage": "生成任务配额",
  "review:view_results": "审核记录",
  "review:restore": "审核还原",
  "question:view_formal": "查看正式题库",
  "question:delete_formal": "删除正式题库题目",
  "question:view_eliminated": "查看淘汰题库",
};

export function permissionName(permission) {
  const code = typeof permission === "string" ? permission : permission?.code;
  return permissionNames[code] || permission?.name || code || "";
}

// 当前用户是否拥有某权限（user.permissions 为后端计算的有效权限并集）
export function hasPerm(p) {
  // 配额管理是超级管理员专属能力；此处避免旧浏览器缓存短暂显示菜单，
  // 后端仍以实时有效权限和当前身份做最终校验，不能靠前端判断授权。
  if (p === "generation_quota:manage" && session.value.user?.role !== "super_admin") return false;
  return (session.value.user?.permissions || []).includes(p);
}

// 账号级权限判断：账号全部已挂身份的权限并集（登录/切换身份时后端随 user
// 下发 account_permissions）。只用于"跨身份待办提醒/引导切换身份"等展示判断；
// 当前身份的实际能力仍以 hasPerm（token 当前身份权限）为准，后端照常鉴权。
export function hasAccountPerm(p) {
  return (session.value.user?.account_permissions || []).includes(p);
}

// 具备指定权限的身份列表（[{id, name}]，按后端 role_permissions 逐身份判定）。
// 供跨身份提醒显示"需切换到 XX 身份"。完全基于权限点与身份有效权限计算，
// 不依赖内置角色 ID——角色模板可被删除重建，权限点才是稳定契约。
export function identitiesWithPerm(p) {
  const u = session.value.user || {};
  const result = [];
  for (const roleID of u.roles || []) {
    if ((u.role_permissions?.[roleID] || []).includes(p)) {
      result.push({ id: roleID, name: (u.role_names && u.role_names[roleID]) || roleNames[roleID] || roleID });
    }
  }
  return result;
}

function persistSession(next) {
  const before = { token: localStorage.getItem('aigo_token'), user: localStorage.getItem('aigo_user') };
  const write = (key, value) => value == null ? localStorage.removeItem(key) : localStorage.setItem(key, value);
  try {
    write('aigo_token', next.token || null);
    write('aigo_user', next.user ? JSON.stringify(next.user) : null);
  } catch (error) {
    // A storage failure must not publish or leave a half-written login behind.
    try { write('aigo_token', before.token); write('aigo_user', before.user); } catch { /* The in-memory session remains the previous coherent snapshot. */ }
    throw error;
  }
}

export function setAuth(tokenStr, userObj) {
  const previous = session.value;
  const next = tokenStr ? { token: tokenStr, user: userObj } : emptySession();
  if (!coherentPair(next.token, next.user)) throw new Error('登录身份数据不一致，请重新登录');
  persistSession(next);
  if (previous.token && previous.user?.id && next.user?.id && previous.user.id !== next.user.id) {
    clearGenerationWorkspace(previous.user.id);
    clearGenerationWorkspace(next.user.id);
  }
  session.value = next;
}

// A /me result may refresh this identity's permissions, never replace a later
// account/role chosen in this tab or in another tab whose storage event is queued.
export function updateUser(userObj) {
  const previous = session.value;
  if (!userObj?.id || !previous.token || !sameIdentity(previous.user, userObj) || !coherentPair(previous.token, userObj)) return;
  try { if ((localStorage.getItem('aigo_token') || '') !== previous.token) return; } catch { return; }
  const next = { token: previous.token, user: userObj };
  persistSession(next);
  session.value = next;
}

function syncFromStorage() {
  const next = readStoredSession(session.value);
  // A real JWT/user mismatch can occur between the two historical mirrors.
  // Ignore that partial write; the second storage event will supply the pair.
  if (!next || next.token === session.value.token && JSON.stringify(next.user) === JSON.stringify(session.value.user)) return;
  session.value = next;
}
let storageSyncQueued = false;
if (typeof window !== 'undefined') {
  window.addEventListener('storage', event => {
    if (!['aigo_token', 'aigo_user', null].includes(event.key) || storageSyncQueued) return;
    storageSyncQueued = true;
    queueMicrotask(() => { storageSyncQueued = false; syncFromStorage(); });
  });
}

export function clearAuth() {
  clearPersonalKnowledgeDrafts(session.value.user?.id);
  clearGenerationWorkspace(session.value.user?.id);
  try { persistSession(emptySession()); }
  finally { session.value = emptySession(); }
}

export function getToken() {
  return session.value.token;
}
