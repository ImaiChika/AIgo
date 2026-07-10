import { createRouter, createWebHistory } from "vue-router";
import { isLoggedIn } from "./auth.js";
import LoginPage from "./pages/LoginPage.vue";
import GeneratePage from "./pages/GeneratePage.vue";
import KnowledgePage from "./pages/KnowledgePage.vue";
import ReviewPage from "./pages/ReviewPage.vue";
import BankPage from "./pages/BankPage.vue";
import ExpertsPage from "./pages/ExpertsPage.vue";
import UsersPage from "./pages/UsersPage.vue";

const routes = [
  { path: "/login", component: LoginPage, meta: { public: true } },
  { path: "/", redirect: "/generate" },
  { path: "/generate", component: GeneratePage, meta: { title: "AI出题" } },
  { path: "/knowledge", component: KnowledgePage, meta: { title: "知识点" } },
  { path: "/review", component: ReviewPage, meta: { title: "多轮审核" } },
  { path: "/bank", component: BankPage, meta: { title: "题库" } },
  { path: "/experts", component: ExpertsPage, meta: { title: "专家库", admin: true } },
  { path: "/users", component: UsersPage, meta: { title: "用户管理", admin: true } },
];

const router = createRouter({
  history: createWebHistory(),
  routes,
});

// 路由守卫
router.beforeEach((to, from, next) => {
  if (!to.meta.public && !isLoggedIn.value) {
    next("/login");
  } else {
    next();
  }
});

export default router;
