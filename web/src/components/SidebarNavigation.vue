<script setup>
import { ref, computed, watch } from "vue";
import { useRoute } from "vue-router";
import { currentUser, hasPerm } from "../auth.js";
import { visibleNavigation, navigationItemActive } from "../navigation.js";

const emit = defineEmits(["navigate"]);
const route = useRoute();
const groups = computed(() => visibleNavigation(hasPerm));
const activeGroup = computed(() => groups.value.find(g => g.items.some(item => navigationItemActive(item, route.path)))?.id);
const expanded = ref(new Set());
const storageKey = computed(() => `aigo_navigation:${currentUser.value?.id || currentUser.value?.username || "anonymous"}`);
function persist() { try { sessionStorage.setItem(storageKey.value, JSON.stringify([...expanded.value])); } catch { /* Navigation also works without browser storage. */ } }
function revealActive() {
  if (activeGroup.value) expanded.value = new Set([...expanded.value, activeGroup.value]);
  persist();
}
watch(storageKey, () => {
  let ids = [];
  try { const saved = JSON.parse(sessionStorage.getItem(storageKey.value) || "[]"); if (Array.isArray(saved)) ids = saved; } catch { /* Ignore invalid preferences. */ }
  expanded.value = new Set(ids.filter(id => groups.value.some(g => g.id === id)));
  revealActive();
}, { immediate: true });
watch(() => route.path, revealActive);
watch(activeGroup, revealActive);
function toggle(id) {
  const set = new Set(expanded.value);
  set.has(id) ? set.delete(id) : set.add(id);
  expanded.value = set; persist();
}
</script>

<template>
  <nav class="business-navigation" aria-label="业务导航">
    <span class="navigation-caption">工作导航</span>
    <section v-for="group in groups" :key="group.id" class="navigation-group" :class="{ 'contains-active': activeGroup === group.id }">
      <button class="navigation-group-button" type="button" :aria-expanded="expanded.has(group.id)" :aria-controls="`nav-${group.id}`" @click="toggle(group.id)">
        <svg class="group-icon" viewBox="0 0 24 24" aria-hidden="true"><path :d="group.icon" /></svg><span>{{ group.label }}</span>
        <span v-if="activeGroup === group.id && !expanded.has(group.id)" class="active-marker" aria-label="包含当前页面"></span>
        <svg class="group-chevron" :class="{ expanded: expanded.has(group.id) }" viewBox="0 0 16 16" aria-hidden="true"><path d="m6 4 4 4-4 4" /></svg>
      </button>
      <ul :id="`nav-${group.id}`" v-show="expanded.has(group.id)" class="navigation-children">
        <li v-for="item in group.items" :key="item.id"><RouterLink :to="item.path" :class="{ selected: navigationItemActive(item, route.path) }" :aria-current="navigationItemActive(item, route.path) ? 'page' : undefined" @click="emit('navigate')"><span class="child-marker" aria-hidden="true"></span>{{ item.label }}</RouterLink></li>
      </ul>
    </section>
  </nav>
</template>

<style scoped>
.business-navigation { min-height: 0; overflow-y: auto; scrollbar-width: thin; scrollbar-color: #355474 transparent; padding: 6px 0 20px; flex: 1; }
.navigation-caption { display: block; padding: 2px 14px 13px; color: #8aa2bc; font-size: 10px; letter-spacing: .14em; }
.navigation-group { border-top: 1px solid #36506b; padding: 9px 0; }
.navigation-group:last-child { border-bottom: 1px solid #36506b; }
.navigation-group-button { display: flex; align-items: center; width: 100%; gap: 11px; border: 0; background: transparent; padding: 13px 12px; color: #d8e4f0; font-family: "PingFang SC", "Microsoft YaHei", sans-serif; font-size: 13px; font-weight: 600; text-align: left; border-radius: 5px; }
.navigation-group-button:hover { background: #203f5e; color: #fff; }
.contains-active > .navigation-group-button { color: #f4f8ff; }
.navigation-group-button > span:first-of-type { flex: 1; }
.group-icon { width: 18px; height: 18px; fill: none; stroke: #91aecb; stroke-width: 1.5; stroke-linecap: round; stroke-linejoin: round; }
.contains-active .group-icon { stroke: #70b8ff; }
.group-chevron { width: 14px; height: 14px; fill: none; stroke: #8ea4bb; stroke-width: 1.5; transition: transform .16s ease; }
.group-chevron.expanded { transform: rotate(90deg); }
.active-marker { width: 5px; height: 5px; background: #68b5ff; border-radius: 50%; }
.navigation-children { list-style: none; padding: 0 0 3px; margin: 0; }
.navigation-children a { position: relative; display: flex; align-items: center; gap: 12px; text-decoration: none; min-height: 40px; margin: 3px 0; padding: 10px 15px 10px 20px; border-radius: 5px; color: #b3c5d9; font-family: "PingFang SC", "Microsoft YaHei", sans-serif; font-size: 12px; transition: background .12s; }
.child-marker { width: 5px; height: 5px; margin-left: 9px; flex-shrink: 0; border-radius: 50%; background: #63819e; }
.navigation-children a:hover { background: #203f5e; color: #fff; }
.navigation-children a.selected { color: #fff; background: #146ec0; }
.navigation-children a.selected::before { content: ""; position: absolute; left: 0; top: 9px; bottom: 9px; width: 3px; background: #9bd4ff; border-radius: 2px; }
.navigation-children a.selected .child-marker { background: #b9e0ff; }
button:focus-visible, a:focus-visible { outline: 2px solid #90cdff; outline-offset: -2px; }
@media(prefers-reduced-motion: reduce) { .group-chevron, .navigation-children a { transition: none; } }
</style>
