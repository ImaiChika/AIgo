<script setup>
import { ref, computed } from "vue";
import { useRouter, useRoute } from "vue-router";
import { currentUser, isLoggedIn, isAdmin, clearAuth, setAuth, getToken } from "./auth.js";
import { api } from "./api.js";

const router = useRouter();
const route = useRoute();

const showProfile = ref(false);
const editName = ref("");
const profileMsg = ref("");

function openProfile() {
  editName.value = currentUser.value?.display_name || "";
  profileMsg.value = "";
  showProfile.value = true;
}

async function saveProfile() {
  if (!editName.value.trim()) {
    profileMsg.value = "昵称不能为空";
    return;
  }
  try {
    const user = await api.updateProfile(editName.value.trim());
    setAuth(getToken(), user);
    profileMsg.value = "已保存";
    setTimeout(() => { showProfile.value = false; }, 800);
  } catch (e) {
    profileMsg.value = e.message;
  }
}

const navItems = computed(() => {
  const items = [
    { key: "generate", icon: "✦", label: "AI出题", path: "/generate" },
    { key: "knowledge", icon: "⌁", label: "知识点", path: "/knowledge" },
    { key: "ai-check", icon: "🔍", label: "AI检查", path: "/ai-check" },
    { key: "review", icon: "✓", label: "多轮审核", path: "/review" },
    { key: "bank", icon: "□", label: "题库", path: "/bank" },
    { key: "batch", icon: "⚡", label: "批量推理", path: "/batch" },
  ];
  if (isAdmin.value) {
    items.push({ key: "experts", icon: "👤", label: "专家库", path: "/experts" });
    items.push({ key: "review-flows", icon: "⚙", label: "审核流程配置", path: "/review-flows" });
    items.push({ key: "audit", icon: "📋", label: "操作日志", path: "/audit" });
    items.push({ key: "users", icon: "⚙", label: "用户管理", path: "/users" });
  }
  return items;
});

const activeNav = computed(() => {
  const item = navItems.value.find((n) => n.path === route.path);
  return item ? item.key : "generate";
});

function navigate(path) {
  router.push(path);
}

function logout() {
  clearAuth();
  router.push("/login");
}

const isLoginPage = computed(() => route.path === "/login");
</script>

<template>
  <div v-if="isLoginPage">
    <router-view />
  </div>
  <div v-else class="app-shell">
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

      <div class="user-info" v-if="currentUser">
        <div class="user-avatar" @click="openProfile">{{ (currentUser.display_name || currentUser.username)[0] }}</div>
        <div class="user-detail" @click="openProfile">
          <strong>{{ currentUser.display_name || currentUser.username }}</strong>
          <span>{{ currentUser.role === "admin" ? "管理员" : currentUser.role === "expert" ? "专家" : "命题教师" }}</span>
        </div>
        <button class="logout-btn" type="button" @click="logout" title="退出登录">⏻</button>
      </div>

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

  <!-- 昵称编辑弹窗 -->
  <div v-if="showProfile" class="modal-overlay" @click.self="showProfile = false">
    <div class="modal-card">
      <h3>修改昵称</h3>
      <div class="modal-field">
        <label>当前角色：{{ currentUser?.role === "admin" ? "管理员" : currentUser?.role === "expert" ? "专家" : "命题教师" }}</label>
      </div>
      <div class="modal-field">
        <label>昵称</label>
        <input v-model="editName" placeholder="输入新昵称" @keyup.enter="saveProfile" />
      </div>
      <div v-if="profileMsg" class="modal-msg">{{ profileMsg }}</div>
      <div class="modal-actions">
        <button class="primary-button" type="button" @click="saveProfile">保存</button>
        <button class="ghost-button" type="button" @click="showProfile = false">取消</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.user-info {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px;
  border-top: 1px solid #e5ebf3;
  margin-top: auto;
}

.user-avatar {
  width: 36px;
  height: 36px;
  border-radius: 50%;
  background: #1385f8;
  color: #fff;
  display: grid;
  place-items: center;
  font-size: 16px;
  font-weight: 700;
  cursor: pointer;
}

.user-detail {
  flex: 1;
  min-width: 0;
  cursor: pointer;
}

.user-detail strong {
  display: block;
  font-size: 13px;
  color: #172033;
}

.user-detail span {
  font-size: 11px;
  color: #6e7b8f;
}

.logout-btn {
  width: 32px;
  height: 32px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #c54858;
  font-size: 16px;
  cursor: pointer;
  display: grid;
  place-items: center;
}

.logout-btn:hover {
  background: #fff0f0;
}

.modal-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.4);
  display: grid;
  place-items: center;
  z-index: 100;
}

.modal-card {
  width: 360px;
  padding: 28px;
  background: #fff;
  border-radius: 12px;
  box-shadow: 0 20px 40px rgba(0, 0, 0, 0.15);
}

.modal-card h3 {
  margin: 0 0 20px;
  font-size: 18px;
  color: #172033;
}

.modal-field {
  margin-bottom: 14px;
}

.modal-field label {
  display: block;
  font-size: 13px;
  color: #6e7b8f;
  margin-bottom: 6px;
}

.modal-field input {
  width: 100%;
  height: 38px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 12px;
  font-size: 14px;
}

.modal-field input:focus {
  outline: none;
  border-color: #1385f8;
}

.modal-msg {
  padding: 8px 12px;
  margin-bottom: 14px;
  border-radius: 6px;
  font-size: 13px;
  background: #f0fff8;
  color: #087c55;
}

.modal-actions {
  display: flex;
  gap: 10px;
}
</style>
