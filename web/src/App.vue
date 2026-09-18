<script setup>
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from "vue";
import { useRouter, useRoute, isNavigationFailure } from "vue-router";
import { currentUser, hasPerm, roleName, clearAuth, setAuth, updateUser, isLoggedIn, getToken } from "./auth.js";
import { api } from "./api.js";
import SidebarNavigation from "./components/SidebarNavigation.vue";
import { visibleNavigation, navigationItemActive, permissionAllowed } from "./navigation.js";
import "./navigation-shell.css";

const router = useRouter();
const route = useRoute();

function openProfile() {
  mobileNavigationOpen.value = false;
  router.push("/settings");
}

const mobileNavigationOpen = ref(false);
const sidebar = ref(null), menuButton = ref(null);
const identityRecoveredNotice = ref("");
const activeGroup = computed(() => visibleNavigation(hasPerm).find(g => g.items.some(item => navigationItemActive(item, route.path))));
const inGeneration = computed(() => route.matched.some(record => record.name === "generation"));
const batchMode = computed(() => route.name === "generation-batch");
const generationReturn = computed(() => hasPerm("question:generate") ? "/generate" : "/knowledge");
let previousOverflow = "";
watch(mobileNavigationOpen, async open => {
  if (open) { previousOverflow = document.body.style.overflow; document.body.style.overflow = "hidden"; await nextTick(); sidebar.value?.querySelector("button")?.focus(); }
  else { document.body.style.overflow = previousOverflow; menuButton.value?.focus(); }
});
watch(() => route.fullPath, () => {
  mobileNavigationOpen.value = false;
  closeRoleMenu();
  refreshCurrentUser();
});

// ===== 在线权限刷新：管理员调整授权后，无需重新登录即见菜单与守卫变化 =====
let meRefreshBusy = false;
let meRefreshedAt = 0;
const ME_REFRESH_MIN_INTERVAL = 15_000; // 聚焦/导航可能密集触发，按节流合并

async function refreshCurrentUser() {
  if (meRefreshBusy || isLoginPage.value || !isLoggedIn.value) return;
  if (Date.now() - meRefreshedAt < ME_REFRESH_MIN_INTERVAL) return;
  meRefreshBusy = true;
  const tokenBefore = getToken();
  try {
    const fresh = await api.me();
    // 请求在途期间若已切换身份/账号（token 变化），旧身份快照直接丢弃——
    // 同一账号的两个身份 userId 相同，不丢就会用旧身份权限覆盖新身份菜单，
    // 表现为“切到审题老师后侧栏仍显示命题工作页，点进去 403”。
    if (getToken() !== tokenBefore) return;
    if (fresh?.id && fresh.id === currentUser.value?.id) updateUser(fresh);
  } catch (e) {
    // 服务端不可达时降级沿用本地快照，不打扰当前操作。
  } finally {
    meRefreshBusy = false;
    meRefreshedAt = Date.now();
  }
}

function onWindowMeRefresh() {
  if (document.visibilityState === "visible") refreshCurrentUser();
}

// 权限变化后（在线刷新或跨页签同步），当前页若无权限则回到我的数据。
watch(() => currentUser.value?.permissions, async (next, prev) => {
  if (isLoginPage.value || !currentUser.value) return;
  if (JSON.stringify(next) === JSON.stringify(prev)) return;
  if (route.meta?.perm && !permissionAllowed(route.meta.perm, hasPerm)) {
    // 无权限页回退；若页面级离开守卫（未保存修改）拦截，则尊重用户选择留在原页。
    await router.push("/my");
  }
});
const desktopLayout = window.matchMedia("(min-width: 1041px)");
function closeDesktopDrawer() { if (desktopLayout.matches) mobileNavigationOpen.value = false; }
onMounted(() => {
  desktopLayout.addEventListener("change", closeDesktopDrawer);
  document.addEventListener("click", closeRoleMenuOnDocument);
  window.addEventListener("focus", onWindowMeRefresh);
  document.addEventListener("visibilitychange", onWindowMeRefresh);
  // 工作身份被撤销后由 api.js 自动切换到剩余身份并刷新，这里提示一次。
  const recoveredRole = sessionStorage.getItem("aigo_identity_recovered");
  if (recoveredRole) {
    sessionStorage.removeItem("aigo_identity_recovered");
    identityRecoveredNotice.value = `原工作身份已被管理员撤销，已自动切换为「${roleName(recoveredRole) || recoveredRole}」；可在左下角切换身份。`;
  }
  refreshCurrentUser();
});
onBeforeUnmount(() => {
  desktopLayout.removeEventListener("change", closeDesktopDrawer);
  document.removeEventListener("click", closeRoleMenuOnDocument);
  window.removeEventListener("focus", onWindowMeRefresh);
  document.removeEventListener("visibilitychange", onWindowMeRefresh);
  if (mobileNavigationOpen.value) document.body.style.overflow = previousOverflow;
});
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
const roleMenu = ref(null);
const roleMenuOpen = ref(false);
const availableRoles = computed(() => {
  const currentRole = currentUser.value?.role || "";
  const ids = [...new Set((currentUser.value?.roles || []).filter(Boolean))];
  ids.sort((left, right) => {
    if (left === currentRole) return -1;
    if (right === currentRole) return 1;
    return roleName(left).localeCompare(roleName(right), "zh-CN");
  });
  return ids.map((id) => ({ id, name: roleName(id) }));
});

function closeRoleMenu() {
  roleMenuOpen.value = false;
}

function toggleRoleMenu() {
  if (switchRoleBusy.value) return;
  roleMenuOpen.value = !roleMenuOpen.value;
}

function closeRoleMenuOnDocument(event) {
  if (!roleMenu.value?.contains(event.target)) closeRoleMenu();
}

function handleRoleMenuKeydown(event) {
  if (event.key === "Escape") {
    closeRoleMenu();
    event.currentTarget?.focus();
    return;
  }
  if (!roleMenuOpen.value || !["ArrowDown", "ArrowUp"].includes(event.key)) return;
  const options = [...(roleMenu.value?.querySelectorAll(".role-option") || [])];
  if (!options.length) return;
  event.preventDefault();
  const current = options.indexOf(document.activeElement);
  const offset = event.key === "ArrowDown" ? 1 : -1;
  options[(current + offset + options.length) % options.length].focus();
}

function chooseRole(role) {
  closeRoleMenu();
  switchRole(role);
}

async function switchRole(role) {
  if (!role || role === currentUser.value?.role || switchRoleBusy.value) return;
  closeRoleMenu();
  switchRoleBusy.value = true;
  switchRoleError.value = "";
  try {
	// 先离开当前业务页，让页面级“未保存修改”守卫有机会阻止切换；
	// 通过后再签发新身份，避免取消导航时留在无权限页面。
	if (route.path !== "/my") {
	  const failure = await router.push("/my");
	  if (failure && isNavigationFailure(failure)) return;
	}
    const data = await api.switchRole(role);
    setAuth(data.token, data.user);
    // 新身份的完整资料已随切换响应写入；解除 me 节流，让下次导航尽快
    // 用新 token 拉一次权威快照（同时清掉可能在途的旧身份刷新结果）。
    meRefreshedAt = 0;
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
    <div v-if="identityRecoveredNotice" class="identity-recovered-banner">
      <span>{{ identityRecoveredNotice }}</span>
      <button type="button" aria-label="关闭提示" @click="identityRecoveredNotice = ''">×</button>
    </div>
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
        <div v-if="availableRoles.length > 1" ref="roleMenu" class="role-switch" title="切换工作身份">
          <span class="sr-only">切换工作身份</span>
          <button
            class="role-switch-trigger"
            type="button"
            :disabled="switchRoleBusy"
            :aria-expanded="roleMenuOpen"
            aria-haspopup="listbox"
            @click.stop="toggleRoleMenu"
            @keydown="handleRoleMenuKeydown"
          >
            <span>{{ roleName(currentUser.role) }}</span>
            <svg viewBox="0 0 16 16" aria-hidden="true"><path d="m4 6 4 4 4-4" /></svg>
          </button>
          <div v-if="roleMenuOpen" class="role-menu" role="listbox" aria-label="选择工作身份">
            <button
              v-for="role in availableRoles"
              :key="role.id"
              class="role-option"
              :class="{ selected: role.id === currentUser.role }"
              type="button"
              role="option"
              :aria-selected="role.id === currentUser.role"
              @click="chooseRole(role.id)"
            >
              <span>{{ role.name }}</span>
              <span v-if="role.id === currentUser.role" class="role-option-current">当前</span>
            </button>
          </div>
        </div>
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
        <RouterLink v-else-if="batchMode" class="generation-mode-link" :to="generationReturn"><svg viewBox="0 0 20 20" aria-hidden="true"><path d="m8 4-6 6 6 6M2 10h15" /></svg>{{ hasPerm('question:generate') ? '返回单题出题' : '返回考试大纲' }}</RouterLink>
      </header>

      <router-view />
    </main>
  </div>

</template>

<style scoped>
.user-info {
  display: grid;
  grid-template-columns: 36px minmax(0, 1fr) auto;
  grid-template-rows: auto auto;
  align-items: center;
  column-gap: 10px;
  row-gap: 6px;
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
  grid-row: 1 / span 2;
}

.user-detail {
  flex: 1;
  min-width: 0;
  cursor: pointer;
  grid-column: 2;
  grid-row: 1;
}

.user-detail strong {
  display: block;
  font-size: 13px;
  color: #172033;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.user-detail span {
  display: block;
  font-size: 11px;
  color: #6e7b8f;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
  grid-column: 3;
  grid-row: 1;
}

.logout-btn:hover {
  background: #fff0f0;
}

.role-switch {
  grid-column: 2 / 4;
  grid-row: 2;
  min-width: 0;
  position: relative;
}

.role-switch-trigger {
  width: 100%;
  min-width: 0;
  height: 30px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 7px;
  padding: 0 8px;
  border: 1px solid #41617d;
  border-radius: 6px;
  background: #173b59;
  color: #e7f2fc;
  font-size: 11px;
  font-family: inherit;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
}

.role-switch-trigger > span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.role-switch-trigger svg {
  width: 14px;
  height: 14px;
  flex: 0 0 auto;
  fill: none;
  stroke: #a9cee9;
  stroke-width: 1.6;
  stroke-linecap: round;
  stroke-linejoin: round;
}

.role-switch-trigger:hover,
.role-switch-trigger[aria-expanded="true"] {
  background: #204967;
  border-color: #6eaed4;
}

.role-switch-trigger:focus-visible,
.role-option:focus-visible {
  outline: 2px solid #90cdff;
  outline-offset: 1px;
}

.role-switch-trigger:disabled {
  opacity: 0.7;
  cursor: wait;
}

.role-menu {
  position: absolute;
  z-index: 20;
  left: 0;
  right: 0;
  bottom: calc(100% + 6px);
  padding: 4px;
  border: 1px solid #41617d;
  border-radius: 7px;
  background: #102f4a;
  box-shadow: 0 10px 24px rgba(5, 24, 42, 0.35);
}

.role-option {
  width: 100%;
  min-height: 30px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 6px 8px;
  border: 0;
  border-radius: 5px;
  background: transparent;
  color: #d7eafa;
  font: inherit;
  font-size: 11px;
  text-align: left;
  cursor: pointer;
}

.role-option:hover {
  background: #2d617f;
  color: #fff;
}

.role-option.selected {
  background: #1385f8;
  color: #fff;
  font-weight: 700;
}

.role-option-current {
  color: #dff2ff;
  font-size: 10px;
  font-weight: 600;
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
