// Navigation and route guards share permission semantics. Hidden groups never
// leave empty headings, and batch-only roles keep a valid generation entry.
export function permissionAllowed(permission, hasPermission) {
  if (!permission) return true;
  return (Array.isArray(permission) ? permission : [permission]).some(hasPermission);
}

export const navigationGroups = [
  { id: "data", label: "基础数据", icon: "M3 4h6l2 2h10v14H3Z M3 10h18", items: [
    { id: "knowledge", label: "知识点", path: "/knowledge" },
  ] },
  { id: "authoring", label: "命题管理", icon: "M14 4l6 6M4 20l4-1L20 7l-3-3L5 16Z M13 20h7", items: [
    { id: "generate", label: "试题生成", path: "/generate", activePaths: ["/generate", "/generate/batch", "/batch"], permission: ["question:generate", "batch:run"] },
    { id: "my-revisions", label: "待我修改", path: "/my-revisions", permission: "question:edit" },
    { id: "share-requests", label: "全局库分享", path: "/share-requests", permission: ["question:share", "question:share_review"] },
  ] },
  { id: "review", label: "审核管理", icon: "M8 4H5v17h14V4h-3 M8 3h8v4H8Z M8 14l3 3 5-6", items: [
    { id: "review", label: "待审任务", path: "/review", permission: "review:do" },
    { id: "review-decisions", label: "最终决断", path: "/review-decisions", permission: "review:final" },
    { id: "review-results", label: "审核记录", path: "/review-results", permission: "review:view_results" },
    { id: "review-flows", label: "审核流程", path: "/review-flows", permission: "flow:manage" },
  ] },
  { id: "bank", label: "题库与统计", icon: "M4 4h16v16H4Z M4 9h16 M9 9v11 M13 16v-3 M16 16v-5", items: [
    // 题库按生命周期分层（正式/过程/淘汰），任一分层的查看权限即可进入，页内按权限展示 tab
    { id: "bank", label: "题库", path: "/bank", permission: ["question:view", "question:view_formal", "question:view_eliminated"] },
    { id: "banks", label: "分类子题库", path: "/banks", permission: "bank:manage" },
    { id: "stats", label: "数据统计", path: "/stats", permission: "stats:view" },
  ] },
  { id: "system", label: "系统管理", icon: "M4 7h16M4 17h16 M8 4v6M16 14v6", items: [
    { id: "users", label: "用户管理", path: "/users", permission: "user:manage" },
    { id: "roles", label: "角色管理", path: "/roles", permission: "role:manage" },
    { id: "ai-providers", label: "AI 服务配置", path: "/system/ai-providers", permission: "role:manage" },
    { id: "audit", label: "操作日志", path: "/audit", permission: "audit:view" },
  ] },
];

export function visibleNavigation(hasPermission) {
  return navigationGroups.map(group => ({ ...group, items: group.items
    .filter(item => permissionAllowed(item.permission, hasPermission))
    .map(item => item.id === "generate" && !hasPermission("question:generate")
      ? { ...item, path: "/generate/batch" } : item),
  })).filter(group => group.items.length);
}

export function navigationItemActive(item, path) {
  return (item.activePaths || [item.path]).includes(path);
}
