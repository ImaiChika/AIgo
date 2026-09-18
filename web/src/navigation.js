// Navigation and route guards share permission semantics. Hidden groups never
// leave empty headings, and batch-only roles keep a valid generation entry.
export function permissionAllowed(permission, hasPermission) {
  if (!permission) return true;
  return (Array.isArray(permission) ? permission : [permission]).some(hasPermission);
}

// 命题工作区的两个子页面分别受各自权限保护：
// - 只有单题权限：进入单题页；误打开批量地址时回到单题页并提示原因。
// - 只有批量权限：保留命题工作区入口，直接进入批量页。
// - 两者都没有：回到登录后所有用户可访问的个人数据页。
export function generationRouteRedirect(routeName, hasPermission) {
  const canGenerate = hasPermission("question:generate");
  const canBatch = hasPermission("batch:run");
  if (routeName === "generation-single") {
    if (canGenerate) return null;
    return canBatch ? "/generate/batch" : "/my";
  }
  if (routeName === "generation-batch" && !canBatch) {
    return canGenerate
      ? { path: "/generate", query: { notice: "batch-permission" } }
      : "/my";
  }
  return null;
}

export const navigationGroups = [
  { id: "personal", label: "我的工作", icon: "M10 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8Z M3 21a7 7 0 0 1 14 0", items: [
    { id: "my-dashboard", label: "我的数据", path: "/my" },
    { id: "personal-settings", label: "个人设置", path: "/settings" },
  ] },
  { id: "authoring", label: "命题工作", icon: "M14 4l6 6M4 20l4-1L20 7l-3-3L5 16Z M13 20h7", items: [
    { id: "knowledge", label: "考试大纲", path: "/knowledge" },
    { id: "generate", label: "AI 出题", path: "/generate", activePaths: ["/generate", "/generate/batch", "/batch"], permission: ["question:generate", "batch:run"] },
    { id: "new-questions", label: "新题修改与送审", path: "/new-questions", permission: "review:submit", badgeKey: "newQuestions" },
    { id: "my-revisions", label: "待我修改", path: "/my-revisions", permission: "question:edit", badgeKey: "revisions" },
  ] },
  { id: "review", label: "审核工作", icon: "M8 4H5v17h14V4h-3 M8 3h8v4H8Z M8 14l3 3 5-6", items: [
    { id: "review", label: "待我审核", path: "/review", permission: "review:do", badgeKey: "review" },
    { id: "review-decisions", label: "待我决断", path: "/review-decisions", permission: "review:final", badgeKey: "decisions" },
    { id: "review-results", label: "审核记录", path: "/review-results", permission: "review:view_results" },
    { id: "review-flows", label: "流程配置", path: "/review-flows", permission: "flow:manage" },
  ] },
  { id: "bank", label: "题库与分享", icon: "M4 4h16v16H4Z M4 9h16 M9 9v11 M13 16v-3 M16 16v-5", items: [
    // 题库按生命周期分层（正式/过程/淘汰），任一分层的查看权限即可进入，页内按权限展示 tab
    { id: "bank", label: "题库", path: "/bank", permission: ["question:view", "question:view_formal", "question:view_eliminated"] },
    { id: "share-requests", label: "分享管理", path: "/share-requests", permission: ["question:share", "question:share_review"], badgeKey: "shares" },
    { id: "stats", label: "数据统计", path: "/stats", permission: "stats:view" },
  ] },
  { id: "system", label: "系统管理", icon: "M4 7h16M4 17h16 M8 4v6M16 14v6", items: [
    { id: "users", label: "用户管理", path: "/users", permission: "user:manage" },
    { id: "roles", label: "角色模板", path: "/roles", permission: "role:manage" },
    { id: "ai-providers", label: "AI 服务配置", path: "/system/ai-providers", permission: "role:manage" },
    { id: "audit", label: "操作日志", path: "/audit", permission: "audit:view" },
  ] },
];

export function visibleNavigation(hasPermission) {
  return navigationGroups.map(group => ({ ...group, items: group.items
    .filter(item => permissionAllowed(item.permission, hasPermission))
    .map(item => {
      const next = item.id === "generate" && !hasPermission("question:generate")
        ? { ...item, path: "/generate/batch" }
        : { ...item };
      if (item.id === "share-requests") {
        if (hasPermission("question:share_review") && !hasPermission("question:share")) next.label = "分享审批";
        else if (hasPermission("question:share") && !hasPermission("question:share_review")) next.label = "我的分享";
      }
      return next;
    }),
  })).filter(group => group.items.length);
}

export function navigationItemActive(item, path) {
  return (item.activePaths || [item.path]).includes(path);
}

// notifyNavigationWorkChanged 通知侧栏立即重算待办角标。提交送审、审核投票、
// 最终决断、保存退修、重新送审、分享审批等动作会改变待办数量，而动作页
// 通常不发生路由跳转，侧栏无法自行感知；相关页面在动作成功后调用本函数。
export function notifyNavigationWorkChanged() {
  if (typeof window !== "undefined") window.dispatchEvent(new Event("aigo:refresh-navigation-badges"));
}
