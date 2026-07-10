import { ref, computed } from "vue";

const token = ref(localStorage.getItem("aigo_token") || "");
const user = ref(JSON.parse(localStorage.getItem("aigo_user") || "null"));

export const isLoggedIn = computed(() => !!token.value);
export const currentUser = computed(() => user.value);
export const isAdmin = computed(() => user.value?.role === "admin");

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
