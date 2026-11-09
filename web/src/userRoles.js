// 用户身份集合的纯函数规则，供用户管理页与测试共用。

// 归一化身份集合：兼容历史单角色字段，去重去空。roles 为空时回退 role。
export function normalizeRoleIDs(role, roles) {
  const ids = [...new Set((roles || []).filter(Boolean))];
  if (!ids.length && role) ids.push(role);
  return ids;
}

// 角色下拉切换主身份：
//   - 新主身份加入集合；
//   - 旧主身份让位（自动取消附加勾选），避免换走后旧角色残留——
//     对任何角色模板一视同仁，不依赖内置角色 ID；
//   - 唯一例外：账号原本只有一个身份时，原岗位保留为附加身份，
//     保证升级（如审题老师→管理员）后随时可以切回原岗位。
// 附加身份的手工增删仍走身份勾选（toggleRoleAssignment）。
export function rolesAfterRoleChange(assigned, previousPrimary, newRole) {
  const ids = normalizeRoleIDs("", assigned);
  // 账号原本只有这一个身份：视为其原岗位，升级后保留为附加身份。
  const keepOriginalJob = ids.length <= 1;
  let next = ids.filter(id => id !== previousPrimary);
  if (newRole && !next.includes(newRole)) next.push(newRole);
  if (keepOriginalJob && previousPrimary && !next.includes(previousPrimary)) {
    next.push(previousPrimary);
  }
  return next;
}
