import { createRouter, createWebHistory } from "vue-router";
import { isLoggedIn, hasPerm } from "./auth.js";
import LoginPage from "./pages/LoginPage.vue";
import GenerationWorkspace from "./pages/GenerationWorkspace.vue";
import { generationRouteRedirect, permissionAllowed } from "./navigation.js";
import GeneratePage from "./pages/GeneratePage.vue";
import KnowledgePage from "./pages/KnowledgePage.vue";
import ReviewPage from "./pages/ReviewPage.vue";
import MyRevisionsPage from "./pages/MyRevisionsPage.vue";
import BankPage from "./pages/BankPage.vue";
import ReviewFlowPage from "./pages/ReviewFlowPage.vue";
import AuditPage from "./pages/AuditPage.vue";
import UsersPage from "./pages/UsersPage.vue";
import BatchPage from "./pages/BatchPage.vue";
import StatsPage from "./pages/StatsPage.vue";
import RolesPage from "./pages/RolesPage.vue";
import ReviewResultsPage from "./pages/ReviewResultsPage.vue";
import ReviewDecisionsPage from "./pages/ReviewDecisionsPage.vue";
import ShareRequestsPage from "./pages/ShareRequestsPage.vue";
import AIProviderPage from "./pages/AIProviderPage.vue";
import MyDashboardPage from "./pages/MyDashboardPage.vue";
import PersonalSettingsPage from "./pages/PersonalSettingsPage.vue";
import NewQuestionsPage from "./pages/NewQuestionsPage.vue";

const routes = [
  { path: "/login", component: LoginPage, meta: { public: true } },
  { path: "/", redirect: "/my" },
  { path: "/my", component: MyDashboardPage, meta: { title: "我的数据" } },
  { path: "/settings", component: PersonalSettingsPage, meta: { title: "个人设置" } },
  { path: "/generate", name: "generation", component: GenerationWorkspace, children: [
    { path: "", name: "generation-single", component: GeneratePage, meta: { title: "试题生成", perm: "question:generate" } },
    // 批量页按过程库查看权放行：无 batch:run 的原提交人仍可进入只读回看
    // 自己的历史任务；页面内部再按 batch:run 隐藏提交配置。
    { path: "batch", name: "generation-batch", component: BatchPage, meta: { title: "批量推理", perm: "question:view" } },
  ] },
  { path: "/new-questions", component: NewQuestionsPage, meta: { title: "新题修改与提交审核", perm: "review:submit" } },
  { path: "/knowledge", component: KnowledgePage, meta: { title: "考试大纲" } },
  { path: "/review", component: ReviewPage, meta: { title: "待我审核", perm: "review:do" } },
  { path: "/my-revisions", component: MyRevisionsPage, meta: { title: "待我修改", perm: "question:edit" } },
  { path: "/share-requests", component: ShareRequestsPage, meta: { title: "全局库分享审核", perm: ["question:share", "question:share_review"] } },
  { path: "/review-decisions", component: ReviewDecisionsPage, meta: { title: "最终决断", perm: "review:final" } },
  { path: "/review-results", component: ReviewResultsPage, meta: { title: "审核记录", perm: "review:view_results" } },
  { path: "/bank", component: BankPage, meta: { title: "题库", perm: ["question:view", "question:view_formal", "question:view_eliminated"] } },
  // 旧分类子题库地址只做兼容跳转，当前不再提供分类子题库页面。
  { path: "/banks", redirect: "/bank" },
  { path: "/batch", redirect: to => ({ path: "/generate/batch", query: to.query, hash: to.hash }) },
  { path: "/stats", component: StatsPage, meta: { title: "数据统计", perm: "stats:view" } },
  { path: "/review-flows", component: ReviewFlowPage, meta: { title: "审核流程", perm: "flow:manage" } },
  { path: "/audit", component: AuditPage, meta: { title: "操作日志", perm: "audit:view" } },
  { path: "/users", component: UsersPage, meta: { title: "用户管理", perm: "user:manage" } },
  { path: "/roles", component: RolesPage, meta: { title: "角色管理", perm: "role:manage" } },
  { path: "/system/ai-providers", component: AIProviderPage, meta: { title: "AI 服务配置", perm: "role:manage" } },
];

const router = createRouter({
  history: createWebHistory(),
  routes,
});

// 路由守卫：登录检查 + 权限点检查（perm 可为数组，任一满足即可）
router.beforeEach((to, from, next) => {
  if (!to.meta.public && !isLoggedIn.value) {
    next("/login");
  } else {
    const generationRedirect = generationRouteRedirect(to.name, hasPerm);
    if (generationRedirect) {
      next(generationRedirect);
      return;
    }
    if (!permissionAllowed(to.meta.perm, hasPerm)) {
      next("/my"); // 无权限重定向到所有登录用户都可访问的个人数据页
      return;
    }
    next();
  }
});

export default router;
