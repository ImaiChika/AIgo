import {questionReviewPathVisible} from "./questionReviewUi.js";
import { createRouter, createWebHistory } from "vue-router";
import { isLoggedIn, hasPerm } from "./auth.js";
import LoginPage from "./pages/LoginPage.vue";
import GenerationWorkspace from "./pages/GenerationWorkspace.vue";
import { canAccessBank, canAccessStats, canAccessQuestionArrangement, canAccessPersonalExperiences, generationRouteRedirect, initialBankScope, permissionAllowed } from "./navigation.js";
import GeneratePage from "./pages/GeneratePage.vue";
import KnowledgePage from "./pages/KnowledgePage.vue";
import ReviewPage from "./pages/ReviewPage.vue";
import ReviewRestorePage from "./pages/ReviewRestorePage.vue";
import MyRevisionsPage from "./pages/MyRevisionsPage.vue";
import BankPage from "./pages/BankPage.vue";
import ReviewFlowPage from "./pages/ReviewFlowPage.vue";
import AuditPage from "./pages/AuditPage.vue";
import UsersPage from "./pages/UsersPage.vue";
import BatchPage from "./pages/BatchPage.vue";
import BatchHistoryPage from "./pages/BatchHistoryPage.vue";
import StatsPage from "./pages/StatsPage.vue";
import RolesPage from "./pages/RolesPage.vue";
import ReviewResultsPage from "./pages/ReviewResultsPage.vue";
import ReviewDecisionsPage from "./pages/ReviewDecisionsPage.vue";
import ShareRequestsPage from "./pages/ShareRequestsPage.vue";
import AIProviderPage from "./pages/AIProviderPage.vue";
import GenerationQuotaPage from "./pages/GenerationQuotaPage.vue";
import MyDashboardPage from "./pages/MyDashboardPage.vue";
import PersonalSettingsPage from "./pages/PersonalSettingsPage.vue";
import NewQuestionsPage from "./pages/NewQuestionsPage.vue";

const routes = [
  { path: "/:pathMatch(.*)*", component: () => import("./pages/NotFoundPage.vue"), meta: { title: "页面不存在" } },
 { path: "/knowledge-base", component: () => import("./pages/KnowledgeBasePage.vue"), meta: { title: "知识库", perm: "kb:view" } },
  { path: "/login", component: LoginPage, meta: { public: true } },
  { path: "/", redirect: "/my" },
  { path: "/my", component: MyDashboardPage, meta: { title: "我的数据" } },
  { path: "/settings", component: PersonalSettingsPage, meta: { title: "个人设置" } },
  { path: "/generate", name: "generation", component: GenerationWorkspace, children: [
    { path: "", name: "generation-single", component: GeneratePage, meta: { title: "试题生成", perm: "question:generate" } },
    { path: "batch", name: "generation-batch", component: BatchPage, meta: { title: "批量推理", perm: "batch:run" } },
  ] },
  { path: "/my-experiences", component: () => import('./pages/PersonalExperiencesPage.vue'), meta: { title: '个人知识库', perm: canAccessPersonalExperiences } },
  { path: "/new-questions", component: NewQuestionsPage, meta: { title: "题目整理", perm: canAccessQuestionArrangement } },
  { path: "/batch-history", component: BatchHistoryPage, meta: { title: "历史出题记录", perm: ["batch:run", "question:view_global"] } },
  { path: "/knowledge", component: KnowledgePage, meta: { title: "考试大纲" } },
  { path: "/review", component: ReviewPage, meta: { title: "待我审核", perm: "review:do" } },
  { path: "/review-restore", component: ReviewRestorePage, meta: { title: "审核还原", perm: "review:restore" } },
  { path: "/my-revisions", component: MyRevisionsPage, meta: { title: "待我修改", perm: "question:edit" } },
  { path: "/share-requests", component: ShareRequestsPage, meta: { title: "全局库分享审核", perm: ["question:share", "question:share_review"] } },
  { path: "/review-decisions", component: ReviewDecisionsPage, meta: { title: "最终决断", perm: "review:final" } },
  { path: "/review-results", component: ReviewResultsPage, meta: { title: "审核记录", perm: "review:view_results" } },
  { path: "/bank", component: BankPage, meta: { title: "题库", perm: canAccessBank } },
  // 旧分类子题库地址只做兼容跳转，当前不再提供分类子题库页面。
  { path: "/banks", redirect: "/bank" },
  { path: "/batch", redirect: to => ({ path: "/generate/batch", query: to.query, hash: to.hash }) },
  { path: "/stats", component: StatsPage, meta: { title: "数据统计", perm: canAccessStats } },
  { path: "/review-flows", component: ReviewFlowPage, meta: { title: "审核流程", perm: "flow:manage" } },
  { path: "/audit", component: AuditPage, meta: { title: "操作日志", perm: "audit:view" } },
  { path: "/users", component: UsersPage, meta: { title: "用户管理", perm: "user:manage" } },
  { path: "/roles", component: RolesPage, meta: { title: "角色管理", perm: "role:manage" } },
  { path: "/system/ai-providers", component: AIProviderPage, meta: { title: "AI 服务配置", perm: "role:manage" } },
  { path: "/system/generation-quota", component: GenerationQuotaPage, meta: { title: "生成任务配额", perm: "generation_quota:manage" } },
];

const router = createRouter({
  history: createWebHistory(),
  routes,
 scrollBehavior(to,from,saved){if(to.hash)return {el:to.hash,top:16,behavior:"smooth"};return saved||{top:0};}
});

// 路由守卫：登录检查 + 权限点检查（perm 可为数组，任一满足即可）
router.beforeEach((to, from, next) => {
  if(!questionReviewPathVisible(to.path)){next({path:"/my",replace:true});return;}
  if (!to.meta.public && !isLoggedIn.value) {
    next("/login");
  } else {
    const generationRedirect = generationRouteRedirect(to.name, hasPerm);
    if (generationRedirect) {
      next(generationRedirect);
      return;
    }
    if (to.path === "/bank" && canAccessBank(hasPerm) && ["personal", "global"].includes(to.query.scope)) {
      const availableScope = initialBankScope(to.query.scope, hasPerm);
      if (availableScope !== to.query.scope) {
        next({ path: to.path, query: { ...to.query, scope: availableScope }, hash: to.hash, replace: true });
        return;
      }
    }
    if (!permissionAllowed(to.meta.perm, hasPerm)) {
      next("/my"); // 无权限重定向到所有登录用户都可访问的个人数据页
      return;
    }
    next();
  }
});

export default router;
