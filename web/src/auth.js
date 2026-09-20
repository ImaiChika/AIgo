import { ref, computed } from "vue";
import { clearGenerationWorkspace } from "./generationWorkspaceState.js";

const token = ref(localStorage.getItem("aigo_token") || "");
const user = ref(JSON.parse(localStorage.getItem("aigo_user") || "null"));

export const isLoggedIn = computed(() => !!token.value);
export const currentUser = computed(() => user.value);

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
  if (role && user.value?.role_names && user.value.role_names[role]) return user.value.role_names[role];
  return roleNames[role] || (role ? role : "未分配角色");
}

const permissionNames = {
  "question:generate": "单题出题",
  "batch:run": "批量推理",
  "stats:view": "数据统计",
  "flow:manage": "审核流程",
  "review:view_results": "审核记录",
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
  return (user.value?.permissions || []).includes(p);
}

export function setAuth(tokenStr, userObj) {
	// 切换账号时清除前一账号的命题工作台；修改昵称沿用同一 token，不触发清理。
	if (token.value && user.value?.id && userObj?.id && user.value.id !== userObj.id) {
		clearGenerationWorkspace(user.value?.id);
		clearGenerationWorkspace(userObj?.id);
	}
  token.value = tokenStr;
  user.value = userObj;
  localStorage.setItem("aigo_token", tokenStr);
  localStorage.setItem("aigo_user", JSON.stringify(userObj));
}

// updateUser 只刷新当前账号的用户信息（角色/权限），不动 token 与工作区。
// 供 /api/auth/me 在线刷新使用：管理员调整授权后，菜单与路由守卫即时生效。
export function updateUser(userObj) {
  if (!userObj || !userObj.id) return;
  user.value = userObj;
  localStorage.setItem("aigo_user", JSON.stringify(userObj));
}

// syncFromStorage 把 localStorage 的最新登录态同步进内存响应式状态。
// 供跨标签页 storage 事件使用：其他页签登录/登出/切换身份后，本页签
// 不刷新页面也能跟上（storage 事件只在别的页签触发，天然无回环）。
function syncFromStorage() {
  const storedToken = localStorage.getItem("aigo_token") || "";
  let storedUser = null;
  try {
    storedUser = JSON.parse(localStorage.getItem("aigo_user") || "null");
  } catch (e) {
    storedUser = null;
  }
  if (storedToken === token.value && JSON.stringify(storedUser) === JSON.stringify(user.value)) return;
  token.value = storedToken;
  user.value = storedUser;
}

if (typeof window !== "undefined") {
  window.addEventListener("storage", (event) => {
    if (event.key === "aigo_token" || event.key === "aigo_user" || event.key === null) {
      syncFromStorage();
    }
  });
}

export function clearAuth() {
  clearGenerationWorkspace(user.value?.id);
  token.value = "";
  user.value = null;
  localStorage.removeItem("aigo_token");
  localStorage.removeItem("aigo_user");
}

export function getToken() {
  return token.value;
}
