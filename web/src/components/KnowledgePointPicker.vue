<script setup>
import { ref, onMounted, onBeforeUnmount, nextTick, useId, computed } from "vue";
import { api } from "../api.js";
import KnowledgeVersionDialog from "./KnowledgeVersionDialog.vue";
import { subscribeKnowledgeVersions } from "../knowledgeVersions.js";

// 大纲要点选择器：模糊搜索（关键词）+ 精确筛选（分类/专业/大纲代码前缀）+ 分页 + 多选勾选
// 支持单选（multiple=false）与多选（multiple=true），已选项以标签展示可移除，
// 也可直接输入逗号分隔的大纲代码批量加入。
const props = defineProps({
  modelValue: { type: Array, default: () => [] }, // 已选知识点数组
  multiple: { type: Boolean, default: true },      // 是否多选
  placeholder: { type: String, default: "搜索大纲要点、大纲代码、专业..." },
});
const emit = defineEmits(["update:modelValue", "version-change"]);

const versions = ref([]);
const versionDialog = ref(null), versionsLoading = ref(false), versionsError = ref("");
const currentVersion = computed(() => versions.value.find(v => v.id === versionId.value));
let versionsTicket = 0, unsubscribeVersions, disposed = false;
const versionId = ref("");
const defaultVersionId = ref("");
const addingCodes = ref(false);
const pickerId = useId();
let searchTicket = 0, metaTicket = 0;
const keyword = ref("");
const subject = ref("");
const category = ref("");
const outlineCode = ref("");
const meta = ref({ categories: [], subjects: [] });

const results = ref([]);
const total = ref(0);
const page = ref(1);
const pageCount = ref(1);
const loading = ref(false);
const showResults = ref(false);
const resultsScroll = ref(null);
let debounceTimer = null;

// 已选大纲代码输入（逗号分隔）
const codeInput = ref("");

function showToast(msg) {
  // 轻提示（不依赖页面 toast）
  const el = document.createElement("div");
  el.className = "kp-toast";
  el.textContent = msg;
  document.body.appendChild(el);
  setTimeout(() => el.remove(), 2500);
}

function isSelected(kp) {
  return props.modelValue.some((x) => x.id === kp.id);
}

function selectedIds() {
  return props.modelValue.map((x) => x.id);
}

// 搜索（防抖 300ms，避免卡顿）
function onKeywordInput() {
  window.clearTimeout(debounceTimer);
  debounceTimer = window.setTimeout(() => search(1), 300);
}

function onFilterChange() {
  search(1);
}

async function search(p = 1) {
  if (!versionId.value) return;
  const ticket = ++searchTicket;
  loading.value = true;
  try {
    const data = await api.searchKPFiltered({
      version_id: versionId.value,
      q: keyword.value.trim(),
      subject: subject.value,
      category: category.value,
      outline_code: outlineCode.value.trim(),
      page: p,
      page_size: 50,
    });
    if (ticket !== searchTicket) return;
    results.value = data.points || [];
    total.value = data.total || 0;
    page.value = data.page;
    pageCount.value = Math.max(1, Math.ceil(total.value / 50));
    showResults.value = true;
    await nextTick();
    resultsScroll.value?.scrollTo({ top: 0 });
  } catch (e) {
    if (ticket === searchTicket) {
      results.value = []; total.value = 0;
      if (e.status === 404) { emit("update:modelValue", []); await loadVersions(); }
      else showToast("搜索失败: " + e.message);
    }
  } finally {
    if (ticket === searchTicket) loading.value = false;
  }
}

function goPage(p) {
  if (p < 1 || p > pageCount.value) return;
  search(p);
}

// 页面可用页码窗口
const pageWindow = [];
function pageWindowArr() {
  const totalPages = pageCount.value;
  const cur = page.value;
  const start = Math.max(1, Math.min(cur - 4, totalPages - 9));
  const end = Math.min(totalPages, start + 9);
  const arr = [];
  for (let i = start; i <= end; i++) arr.push(i);
  return arr;
}

// 勾选/单选选择
function toggleSelect(kp) {
  const list = [...props.modelValue];
  if (isSelected(kp)) {
    emit("update:modelValue", list.filter((x) => x.id !== kp.id));
  } else {
    if (props.multiple) {
      list.push(kp);
    } else {
      list.splice(0, list.length, kp);
    }
    emit("update:modelValue", list);
  }
}

function removeSelected(id) {
  emit("update:modelValue", props.modelValue.filter((x) => x.id !== id));
}

function notifyVersionChange() {
  emit("version-change", {
    versionId: versionId.value,
    version: currentVersion.value ? { ...currentVersion.value } : null,
  });
}

// 逗号分隔大纲代码批量加入（精确匹配，可包含名称片段？仅大纲代码精确）
async function addByCodes() {
  const codes = [...new Set(codeInput.value.split(/[,，\s]+/).map(s => s.trim()).filter(Boolean))];
  if (!codes.length || !versionId.value || addingCodes.value) return;
  const selectedVersion = versionId.value;
  const list = [...props.modelValue]; const missing = []; let added = 0;
  addingCodes.value = true;
  try {
    for (const code of codes) {
      const data = await api.searchKPFiltered({ version_id: selectedVersion, outline_code: code, page: 1, page_size: 200 });
      if (selectedVersion !== versionId.value) return;
      const kp = (data.points || []).find(x => x.outline_code === code);
      if (!kp) { missing.push(code); continue; }
      if (!list.some(x => x.id === kp.id)) { if (!props.multiple) list.splice(0); list.push(kp); added++; }
      if (!props.multiple) break;
    }
    emit("update:modelValue", list);
    showToast(missing.length ? `已加入 ${added} 个；当前版本未找到：${missing.join("、")}` : `已加入 ${added} 个大纲要点`);
    codeInput.value = "";
  } catch (e) { showToast("代码查找失败：" + e.message); }
  finally { addingCodes.value = false; }
}

async function changeVersion() {
  const ticket = ++metaTicket;
  ++searchTicket;
  results.value = []; total.value = 0; page.value = 1;
  keyword.value = ""; subject.value = ""; category.value = ""; outlineCode.value = ""; codeInput.value = "";
  emit("update:modelValue", []);
  meta.value = { categories: [], subjects: [] };
  pageCount.value = 1; showResults.value = true; loading.value = false;
  notifyVersionChange();
  if (!versionId.value) return;
  try { const data = await api.kpMeta(versionId.value); if (ticket !== metaTicket) return; meta.value = data; }
  catch (e) { if (ticket === metaTicket) { if (e.status === 404) { await loadVersions(); return; } showToast(e.message); } }
  if (ticket === metaTicket) search(1);
}

function reset() {
  keyword.value = "";
  subject.value = "";
  category.value = "";
  outlineCode.value = "";
  search(1);
}

async function loadVersions() {
  if (disposed) return;
  const ticket = ++versionsTicket; versionsLoading.value = true; versionsError.value = "";
  try {
    const data = await api.kpVersions(); if (ticket !== versionsTicket) return;
    versions.value = (data.versions || []).filter(v => v.status === "published");
    defaultVersionId.value = data.default_version_id || "";
    const previous = versionId.value;
    if (!versions.value.some(v => v.id === previous)) versionId.value = defaultVersionId.value;
    if (previous !== versionId.value || !showResults.value) await changeVersion();
  } catch (e) { if (ticket === versionsTicket) versionsError.value = "大纲版本加载失败：" + e.message; }
  finally { if (ticket === versionsTicket) versionsLoading.value = false; }
}
async function chooseVersion(id) {
  if (id === versionId.value || !versions.value.some(v => v.id === id)) return;
  versionId.value = id; await changeVersion();
}
onMounted(async () => { await loadVersions(); if (!disposed) unsubscribeVersions = subscribeKnowledgeVersions(loadVersions); });
onBeforeUnmount(() => { disposed = true; clearTimeout(debounceTimer); searchTicket++; metaTicket++; versionsTicket++; unsubscribeVersions?.(); });
</script>

<template>
  <div class="kp-picker">
    <div class="kp-version-row"><div><span>出题大纲</span><strong>{{ currentVersion?.name || '暂无已启用大纲' }}</strong><small v-if="currentVersion?.id === defaultVersionId && currentVersion">默认</small></div><button class="kp-btn ghost" type="button" @click="versionDialog.open()">切换版本</button></div>
    <p v-if="!currentVersion && !versionsLoading" class="kp-version-empty">{{ versionsError || '请先在考试大纲管理中创建并启用大纲版本，再选择大纲要点出题。' }}</p>
    <KnowledgeVersionDialog ref="versionDialog" :versions="versions" :current-id="versionId" :default-id="defaultVersionId" :loading="versionsLoading" :error="versionsError" @refresh="loadVersions" @select="chooseVersion" />
    <!-- 搜索区：模糊 + 精确 -->
    <div class="kp-search-row">
      <input v-model="keyword" :placeholder="props.placeholder" @input="onKeywordInput" class="kp-input" />
      <select v-model="category" @change="onFilterChange" class="kp-select" title="按分类精确筛选">
        <option value="">全部分类</option>
        <option v-for="c in meta.categories" :key="c" :value="c">{{ c }}</option>
      </select>
      <select v-model="subject" @change="onFilterChange" class="kp-select" title="按专业精确筛选">
        <option value="">全部专业</option>
        <option v-for="s in meta.subjects" :key="s" :value="s">{{ s }}</option>
      </select>
      <input v-model="outlineCode" placeholder="大纲代码前缀" @input="onKeywordInput" class="kp-input kp-code-input" />
      <button class="kp-btn" type="button" @click="search(1)" :disabled="loading || !versionId">{{ loading ? "搜索中..." : "搜索" }}</button>
      <button class="kp-btn ghost" type="button" @click="reset">重置</button>
    </div>

    <!-- 结果区（分页 + 勾选） -->
    <div v-if="showResults" class="kp-results">
      <div class="kp-results-head">
        <span>共 {{ total }} 个大纲要点</span>
      </div>
      <div ref="resultsScroll" class="kp-results-scroll">
        <div v-if="loading" class="kp-loading">搜索中...</div>
        <div v-else-if="!results.length" class="kp-empty">无匹配结果</div>
        <div v-else class="kp-list">
          <label v-for="kp in results" :key="kp.id" class="kp-item" :class="{ checked: isSelected(kp) }">
            <input :type="multiple ? 'checkbox' : 'radio'" :name="`${pickerId}-point`" :checked="isSelected(kp)" @change="toggleSelect(kp)" class="kp-checkbox" />
            <span class="kp-item-main">
              <span class="kp-topic">{{ kp.topic }}</span>
              <span class="kp-sub">{{ kp.subject }}</span>
              <code class="kp-code">{{ kp.outline_code }}</code>
            </span>
          </label>
        </div>
      </div>
      <!-- 分页 -->
      <div v-if="pageCount > 1" class="kp-pagination">
        <button class="kp-page-btn" type="button" :disabled="page <= 1 || loading" @click="goPage(page - 1)">‹</button>
        <button
          v-for="p in pageWindowArr()"
          :key="p"
          type="button"
          class="kp-page-btn"
          :class="{ active: p === page }"
          :disabled="loading"
          @click="goPage(p)"
        >{{ p }}</button>
        <button class="kp-page-btn" type="button" :disabled="page >= pageCount || loading" @click="goPage(page + 1)">›</button>
        <span class="kp-page-info">第 {{ page }} / {{ pageCount }} 页</span>
      </div>
    </div>

    <!-- 已选区 -->
    <div v-if="modelValue.length" class="kp-selected">
      <div class="kp-selected-title">已选 {{ modelValue.length }} 个大纲要点：</div>
      <div class="kp-tags">
        <span v-for="kp in modelValue" :key="kp.id" class="kp-tag">
          {{ kp.topic }}
          <code>{{ kp.outline_code }}</code>
          <button type="button" class="kp-tag-remove" @click="removeSelected(kp.id)" title="移除">×</button>
        </span>
      </div>
    </div>

    <!-- 逗号分隔大纲代码加入 -->
    <div class="kp-code-add">
      <input v-model="codeInput" placeholder="按大纲代码加入：110.2.1.1, 110.2.1.2, ...（逗号分隔）" class="kp-input" @keyup.enter="addByCodes" />
      <button class="kp-btn" type="button" :disabled="addingCodes || !versionId" @click="addByCodes">{{ addingCodes ? "查找中…" : "按代码加入" }}</button>
    </div>
  </div>
</template>

<style scoped>
.kp-version-row { display: flex; align-items: center; justify-content: space-between; gap: 14px; padding: 11px 0 15px; border-bottom: 1px solid #e7eee9; margin-bottom: 7px; font-size: 12px; color: #526773; }
.kp-version-row > div { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.kp-version-row span { color: #94a1a9; font-size: 11px; }
.kp-version-row strong { color: #3d5c4b; font-weight: 500; font-size: 13px; }
.kp-version-row small { color: #5a896c; background: #edf5ec; font-size: 10px; padding: 2px 5px; border-radius: 3px; }
.kp-version-empty { padding: 12px; background: #f6f8f3; color: #899879; font-size: 12px; line-height: 1.8; }

.kp-picker {
  display: grid;
  gap: 8px;
}

.kp-search-row {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  align-items: center;
}

.kp-input {
  flex: 1;
  min-width: 180px;
  height: 32px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 10px;
  font-size: 13px;
}

.kp-code-input {
  flex: 0.8;
  min-width: 130px;
}

.kp-select {
  height: 32px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 8px;
  font-size: 12px;
  max-width: 160px;
}

.kp-btn {
  height: 32px;
  padding: 0 12px;
  border: 0;
  border-radius: 6px;
  background: #1385f8;
  color: #fff;
  font-size: 13px;
  cursor: pointer;
  white-space: nowrap;
}

.kp-btn.ghost {
  background: #fff;
  border: 1px solid #e5ebf3;
  color: #6e7b8f;
}

.kp-btn:disabled {
  opacity: 0.5;
}

.kp-results {
  display: flex;
  flex-direction: column;
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  padding: 8px;
  max-height: 320px;
  overflow: hidden;
  background: #fff;
}

.kp-results-head {
  flex: 0 0 auto;
  font-size: 12px;
  color: #6e7b8f;
  padding: 0 4px 6px;
}

.kp-results-scroll {
  flex: 1 1 auto;
  min-height: 0;
  overflow-x: hidden;
  overflow-y: auto;
  overscroll-behavior: contain;
  padding-right: 3px;
  scrollbar-gutter: stable;
}

.kp-list {
  display: grid;
  gap: 4px;
}

.kp-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  border-radius: 6px;
  cursor: pointer;
  border: 1px solid transparent;
}

.kp-item:hover {
  background: #f8fbff;
}

.kp-item.checked {
  background: #eff8ff;
  border-color: #dce8f7;
}

.kp-checkbox {
  cursor: pointer;
}

.kp-radio {
  width: 14px;
  height: 14px;
  border: 2px solid #d5deeb;
  border-radius: 50%;
  flex-shrink: 0;
}

.kp-radio.active {
  border-color: #1385f8;
  background: #1385f8;
  box-shadow: inset 0 0 0 2px #fff;
}

.kp-item-main {
  flex: 1;
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.kp-topic {
  font-size: 13px;
  color: #172033;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kp-sub {
  font-size: 11px;
  color: #6e7b8f;
  white-space: nowrap;
}

.kp-code {
  font-size: 11px;
  color: #0571dc;
  background: #eff8ff;
  padding: 1px 5px;
  border-radius: 3px;
  white-space: nowrap;
}

.kp-loading, .kp-empty {
  text-align: center;
  color: #9aa5b4;
  font-size: 13px;
  padding: 14px;
}

.kp-pagination {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  gap: 3px;
  margin-top: 8px;
  padding-top: 8px;
  border-top: 1px solid #edf1f6;
  flex-wrap: wrap;
}

.kp-page-btn {
  min-width: 26px;
  height: 24px;
  border: 1px solid #e5ebf3;
  border-radius: 4px;
  background: #fff;
  color: #3a4658;
  font-size: 12px;
  cursor: pointer;
}

.kp-page-btn.active {
  background: #1385f8;
  color: #fff;
  border-color: #1385f8;
}

.kp-page-btn:disabled {
  opacity: 0.4;
}

.kp-page-info {
  font-size: 11px;
  color: #6e7b8f;
  margin-left: 4px;
}

.kp-selected {
  border: 1px solid #dce8f7;
  background: #f8fbff;
  border-radius: 8px;
  padding: 8px;
}

.kp-selected-title {
  font-size: 12px;
  color: #6e7b8f;
  margin-bottom: 6px;
}

.kp-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.kp-tag {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 3px 8px;
  background: #eff8ff;
  border: 1px solid #dce8f7;
  border-radius: 14px;
  font-size: 12px;
  color: #172033;
}

.kp-tag code {
  font-size: 10px;
  color: #0571dc;
}

.kp-tag-remove {
  border: 0;
  background: transparent;
  color: #c54858;
  cursor: pointer;
  font-size: 14px;
  line-height: 1;
  padding: 0 2px;
}

.kp-code-add {
  display: flex;
  gap: 6px;
}
</style>

<style>
/* 组件内 toast（全局样式，避免 scoped 不生效） */
.kp-toast {
  position: fixed;
  top: 20px;
  left: 50%;
  transform: translateX(-50%);
  background: #172033;
  color: #fff;
  padding: 10px 18px;
  border-radius: 8px;
  font-size: 13px;
  z-index: 9999;
  box-shadow: 0 6px 18px rgba(0,0,0,0.2);
}
</style>
