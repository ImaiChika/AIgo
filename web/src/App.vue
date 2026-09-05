<script setup>
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from "vue";
import { useRouter, useRoute } from "vue-router";
import { currentUser, isLoggedIn, hasPerm, roleName, clearAuth, setAuth, getToken } from "./auth.js";
import { api } from "./api.js";
import SidebarNavigation from "./components/SidebarNavigation.vue";
import { visibleNavigation, navigationItemActive } from "./navigation.js";
import "./navigation-shell.css";

const router = useRouter();
const route = useRoute();

const showProfile = ref(false);
const editName = ref("");
const profileMsg = ref("");

function openProfile() {
  mobileNavigationOpen.value = false;
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

const mobileNavigationOpen = ref(false);
const sidebar = ref(null), menuButton = ref(null);
const activeGroup = computed(() => visibleNavigation(hasPerm).find(g => g.items.some(item => navigationItemActive(item, route.path))));
const inGeneration = computed(() => route.matched.some(record => record.name === "generation"));
const batchMode = computed(() => route.name === "generation-batch");
const generationReturn = computed(() => hasPerm("question:generate") ? "/generate" : "/knowledge");
let previousOverflow = "";
watch(mobileNavigationOpen, async open => {
  if (open) { previousOverflow = document.body.style.overflow; document.body.style.overflow = "hidden"; await nextTick(); sidebar.value?.querySelector("button")?.focus(); }
  else { document.body.style.overflow = previousOverflow; menuButton.value?.focus(); }
});
watch(() => route.fullPath, () => { mobileNavigationOpen.value = false; });
const desktopLayout = window.matchMedia("(min-width: 1041px)");
function closeDesktopDrawer() { if (desktopLayout.matches) mobileNavigationOpen.value = false; }
onMounted(() => desktopLayout.addEventListener("change", closeDesktopDrawer));
onBeforeUnmount(() => { desktopLayout.removeEventListener("change", closeDesktopDrawer); if (mobileNavigationOpen.value) document.body.style.overflow = previousOverflow; });
function sidebarKeydown(event) {
  if (!mobileNavigationOpen.value) return;
  if (event.key === "Escape") { event.preventDefault(); mobileNavigationOpen.value = false; return; }
  if (event.key !== "Tab") return;
  const focusable = [...sidebar.value.querySelectorAll("button, a[href], [tabindex='0']")].filter(el => el.getClientRects().length);
  const first = focusable[0], last = focusable.at(-1);
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
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
  <div v-else class="app-shell" :class="{ 'mobile-navigation-open': mobileNavigationOpen }">
    <div v-if="mobileNavigationOpen" class="navigation-backdrop" aria-hidden="true" @click="mobileNavigationOpen = false"></div>
    <aside ref="sidebar" class="sidebar" id="application-navigation" :role="mobileNavigationOpen ? 'dialog' : undefined" :aria-modal="mobileNavigationOpen || undefined" aria-label="应用导航" @keydown="sidebarKeydown">
      <div class="brand">
        <div class="brand-mark">A</div>
        <div>
          <strong>AIgo</strong>
          <span>命题审核平台</span>
        </div>
        <button class="mobile-navigation-close" type="button" aria-label="关闭导航" @click="mobileNavigationOpen = false">×</button>
      </div>

      <SidebarNavigation @navigate="mobileNavigationOpen = false" />

      <div class="user-info" v-if="currentUser">
        <div class="user-avatar" @click="openProfile">{{ (currentUser.display_name || currentUser.username)[0] }}</div>
        <div class="user-detail" @click="openProfile">
          <strong>{{ currentUser.display_name || currentUser.username }}</strong>
          <span>{{ roleName(currentUser.role) }}</span>
        </div>
        <button class="logout-btn" type="button" @click="logout" title="退出登录">退出</button>
      </div>
    </aside>

    <main class="workspace" :inert="mobileNavigationOpen">
      <header class="topbar">
        <div class="workspace-heading">
          <button ref="menuButton" class="mobile-navigation-button" type="button" aria-label="打开导航" aria-controls="application-navigation" :aria-expanded="mobileNavigationOpen" @click="mobileNavigationOpen = true"><svg viewBox="0 0 20 20" aria-hidden="true"><path d="M3 5h14M3 10h14M3 15h14" /></svg></button>
          <div><div class="workspace-location">{{ activeGroup?.label || '命题平台' }}<template v-if="batchMode"><span>/</span>试题生成</template></div><h1>{{ route.meta.title || "命题管理" }}</h1></div>
        </div>
        <RouterLink v-if="inGeneration && !batchMode && hasPerm('batch:run')" class="generation-mode-link" to="/generate/batch"><svg viewBox="0 0 20 20" aria-hidden="true"><path d="M3 3h10v10H3Z M7 16h9V7 M10 19h9V10" /></svg>批量推理</RouterLink>
        <RouterLink v-else-if="batchMode" class="generation-mode-link" :to="generationReturn"><svg viewBox="0 0 20 20" aria-hidden="true"><path d="m8 4-6 6 6 6M2 10h15" /></svg>{{ hasPerm('question:generate') ? '返回单题出题' : '返回知识点' }}</RouterLink>
      </header>

      <router-view />
    </main>
  </div>

  <!-- 昵称编辑弹窗 -->
  <div v-if="showProfile" class="modal-overlay" @click.self="showProfile = false">
    <div class="modal-card">
      <h3>修改昵称</h3>
      <div class="modal-field">
        <label>当前角色：{{ roleName(currentUser?.role) }}</label>
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
