<script setup>
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from "vue";
import { useRouter, useRoute } from "vue-router";
import { currentUser, hasPerm, roleName, clearAuth, setAuth } from "./auth.js";
import { api } from "./api.js";
import SidebarNavigation from "./components/SidebarNavigation.vue";
import { visibleNavigation, navigationItemActive } from "./navigation.js";
import "./navigation-shell.css";

const router = useRouter();
const route = useRoute();

function openProfile() {
  mobileNavigationOpen.value = false;
  router.push("/settings");
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
const switchRoleBusy = ref(false);
const switchRoleError = ref("");
const availableRoles = computed(() => (currentUser.value?.roles || []).map((id) => ({ id, name: roleName(id) })));

async function switchRole(role) {
  if (!role || role === currentUser.value?.role || switchRoleBusy.value) return;
  switchRoleBusy.value = true;
  switchRoleError.value = "";
  try {
    const data = await api.switchRole(role);
    setAuth(data.token, data.user);
    await router.push("/my");
  } catch (error) {
    switchRoleError.value = error.message || "身份切换失败";
  } finally {
    switchRoleBusy.value = false;
  }
}
</script>

<template>
  <div v-if="isLoginPage">
    <router-view />
  </div>
  <div v-else class="app-shell" :class="{ 'mobile-navigation-open': mobileNavigationOpen }">
    <div v-if="mobileNavigationOpen" class="navigation-backdrop" aria-hidden="true" @click="mobileNavigationOpen = false"></div>
    <aside ref="sidebar" class="sidebar" id="application-navigation" :role="mobileNavigationOpen ? 'dialog' : undefined" :aria-modal="mobileNavigationOpen || undefined" aria-label="应用导航" @keydown="sidebarKeydown">
      <div class="brand">
        <img class="brand-logo" src="/zhique-logo.png" alt="治趣" />
        <strong class="brand-product-name">AI医学试题生成系统</strong>
        <button class="mobile-navigation-close" type="button" aria-label="关闭导航" @click="mobileNavigationOpen = false">×</button>
      </div>

      <SidebarNavigation @navigate="mobileNavigationOpen = false" />

      <div class="user-info" v-if="currentUser">
        <div class="user-avatar" role="button" tabindex="0" @click="openProfile" @keydown.enter="openProfile">{{ (currentUser.display_name || currentUser.username)[0] }}</div>
        <div class="user-detail" role="button" tabindex="0" @click="openProfile" @keydown.enter="openProfile">
          <strong>{{ currentUser.display_name || currentUser.username }}</strong>
          <span>{{ roleName(currentUser.role) }}</span>
        </div>
        <label v-if="availableRoles.length > 1" class="role-switch" title="切换工作身份">
          <span class="sr-only">切换工作身份</span>
          <select :value="currentUser.role" :disabled="switchRoleBusy" @change="switchRole($event.target.value)">
            <option v-for="role in availableRoles" :key="role.id" :value="role.id">{{ role.name }}</option>
          </select>
        </label>
        <button class="logout-btn" type="button" @click="logout" title="退出登录">退出</button>
      </div>
      <p v-if="switchRoleError" class="role-switch-error" role="alert">{{ switchRoleError }}</p>
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

</template>

<style scoped>
.user-info {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px;
  border-top: 1px solid #e5ebf3;
  margin-top: auto;
  position: relative;
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
  min-width: 44px;
  height: 32px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #c54858;
  padding: 0 10px;
  font-size: 12px;
  cursor: pointer;
  display: grid;
  place-items: center;
}

.logout-btn:hover {
  background: #fff0f0;
}

.role-switch select {
  width: 76px;
  height: 30px;
  border: 1px solid #d8e3f0;
  border-radius: 6px;
  background: #fff;
  color: #35506a;
  font-size: 11px;
}

.role-switch-error {
  position: absolute;
  left: 12px;
  right: 12px;
  bottom: 4px;
  margin: 0;
  color: #b53d52;
  font-size: 11px;
}

.sr-only {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
</style>
