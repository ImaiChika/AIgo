import { createRouter, createWebHistory } from "vue-router";
import GeneratePage from "./pages/GeneratePage.vue";
import KnowledgePage from "./pages/KnowledgePage.vue";
import ReviewPage from "./pages/ReviewPage.vue";
import BankPage from "./pages/BankPage.vue";

const routes = [
  { path: "/", redirect: "/generate" },
  { path: "/generate", component: GeneratePage, meta: { title: "AI出题" } },
  { path: "/knowledge", component: KnowledgePage, meta: { title: "知识点" } },
  { path: "/review", component: ReviewPage, meta: { title: "多轮审核" } },
  { path: "/bank", component: BankPage, meta: { title: "题库" } },
];

export default createRouter({
  history: createWebHistory(),
  routes,
});
