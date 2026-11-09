import { api } from "./api.js";
import { currentUser, getToken, updateUser } from "./auth.js";
import { createSessionIdentityRefresher } from "./sessionAccess.js";
// 合并相同会话的权限刷新，迟到结果不能覆盖其他账号或工作身份。
export const refreshSessionIdentity = createSessionIdentityRefresher({ fetchMe: () => api.me(), getToken, getUser: () => currentUser.value, updateUser });
