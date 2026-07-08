<script setup>
import { ref, computed } from "vue";
import { useRouter, useRoute } from "vue-router";

const router = useRouter();
const route = useRoute();

const navItems = [
  { key: "generate", icon: "✦", label: "AI出题", path: "/generate" },
  { key: "knowledge", icon: "⌁", label: "知识点", path: "/knowledge" },
  { key: "review", icon: "✓", label: "多轮审核", path: "/review" },
  { key: "bank", icon: "□", label: "题库", path: "/bank" },
];

const activeNav = computed(() => {
  const path = route.path;
  const item = navItems.find((n) => n.path === path);
  return item ? item.key : "generate";
});

function navigate(path) {
  router.push(path);
}

const toast = ref("");
const toastTimer = ref(null);

function showToast(msg) {
  toast.value = msg;
  clearTimeout(toastTimer.value);
  toastTimer.value = setTimeout(() => { toast.value = ""; }, 3000);
}

// 暴露给子页面
window.$toast = showToast;
</script>

<template>
  <div class="app-shell">
    <aside class="sidebar">
      <div class="brand">
        <div class="brand-mark">A</div>
        <div>
          <strong>AIgo</strong>
          <span>智能命题</span>
        </div>
      </div>

      <nav class="nav-list">
        <button
          v-for="item in navItems"
          :key="item.key"
          class="nav-item"
          :class="{ active: activeNav === item.key }"
          type="button"
          @click="navigate(item.path)"
        >
          <span class="nav-icon">{{ item.icon }}</span>
          {{ item.label }}
        </button>
      </nav>

      <div class="sidebar-note">
        <span>当前版本</span>
        <strong>千问出题 + AI配图 + 专家审核</strong>
      </div>
    </aside>

    <main class="workspace">
      <header class="topbar">
        <div>
          <h1>{{ route.meta.title || "AI智能出题工作台" }}</h1>
        </div>
      </header>

      <router-view />
    </main>
  </div>

  <div class="toast" :class="{ show: toast }">{{ toast }}</div>
</template>
