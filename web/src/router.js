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
import BanksPage from "./pages/BanksPage.vue";
import ReviewResultsPage from "./pages/ReviewResultsPage.vue";
import ReviewDecisionsPage from "./pages/ReviewDecisionsPage.vue";
import ShareRequestsPage from "./pages/ShareRequestsPage.vue";
import AIProviderPage from "./pages/AIProviderPage.vue";

const routes = [
  { path: "/login", component: LoginPage, meta: { public: true } },
  { path: "/", redirect: "/generate" },
  { path: "/generate", name: "generation", component: GenerationWorkspace, children: [
    { path: "", name: "generation-single", component: GeneratePage, meta: { title: "试题生成", perm: "question:generate" } },
    { path: "batch", name: "generation-batch", component: BatchPage, meta: { title: "批量推理", perm: "batch:run" } },
  ] },
  { path: "/knowledge", component: KnowledgePage, meta: { title: "知识点" } },
  { path: "/review", component: ReviewPage, meta: { title: "待审任务", perm: "review:do" } },
  { path: "/my-revisions", component: MyRevisionsPage, meta: { title: "待我修改", perm: "question:edit" } },
  { path: "/share-requests", component: ShareRequestsPage, meta: { title: "全局库分享", perm: ["question:share", "question:share_review"] } },
  { path: "/review-decisions", component: ReviewDecisionsPage, meta: { title: "最终决断", perm: "review:final" } },
  { path: "/review-results", component: ReviewResultsPage, meta: { title: "审核记录", perm: "review:view_results" } },
  { path: "/bank", component: BankPage, meta: { title: "题库", perm: ["question:view", "question:view_formal", "question:view_eliminated"] } },
  { path: "/batch", redirect: to => ({ path: "/generate/batch", query: to.query, hash: to.hash }) },
  { path: "/stats", component: StatsPage, meta: { title: "数据统计", perm: "stats:view" } },
  { path: "/review-flows", component: ReviewFlowPage, meta: { title: "审核流程", perm: "flow:manage" } },
  { path: "/audit", component: AuditPage, meta: { title: "操作日志", perm: "audit:view" } },
  { path: "/users", component: UsersPage, meta: { title: "用户管理", perm: "user:manage" } },
  { path: "/roles", component: RolesPage, meta: { title: "角色管理", perm: "role:manage" } },
  { path: "/system/ai-providers", component: AIProviderPage, meta: { title: "AI 服务配置", perm: "role:manage" } },
  { path: "/banks", component: BanksPage, meta: { title: "题库管理", perm: "bank:manage" } },
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
      next("/knowledge"); // 无权限重定向到所有登录用户都可访问的知识点页
      return;
    }
    next();
  }
});

export default router;
