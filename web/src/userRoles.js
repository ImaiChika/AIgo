// 用户身份集合的纯函数规则，供用户管理页与测试共用。

// 归一化身份集合：兼容历史单角色字段，去重去空。roles 为空时回退 role。
export function normalizeRoleIDs(role, roles) {
  const ids = [...new Set((roles || []).filter(Boolean))];
  if (!ids.length && role) ids.push(role);
  return ids;
}

// 主身份切换：只决定“哪个身份为主”，不改变身份集合的成员资格；
// 身份的增删走显式勾选，避免下拉切换把原有身份静默清掉。
export function rolesAfterPrimarySwitch(assigned, role) {
  const ids = normalizeRoleIDs("", assigned);
  if (role && !ids.includes(role)) ids.push(role);
  return ids;
}
