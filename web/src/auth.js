import { ref, computed } from "vue";

const token = ref(localStorage.getItem("aigo_token") || "");
const user = ref(JSON.parse(localStorage.getItem("aigo_user") || "null"));

export const isLoggedIn = computed(() => !!token.value);
export const currentUser = computed(() => user.value);

// 角色显示名映射
export const roleNames = {
  admin: "管理员",
  expert: "审题专家",
  teacher: "命题教师",
};

export function roleName(role) {
  return roleNames[role] || (role ? role : "未分配角色");
}

// 当前用户是否拥有某权限（user.permissions 为后端计算的有效权限并集）
export function hasPerm(p) {
  return (user.value?.permissions || []).includes(p);
}

export function setAuth(tokenStr, userObj) {
  token.value = tokenStr;
  user.value = userObj;
  localStorage.setItem("aigo_token", tokenStr);
  localStorage.setItem("aigo_user", JSON.stringify(userObj));
}

export function clearAuth() {
  token.value = "";
  user.value = null;
  localStorage.removeItem("aigo_token");
  localStorage.removeItem("aigo_user");
}

export function getToken() {
  return token.value;
}
