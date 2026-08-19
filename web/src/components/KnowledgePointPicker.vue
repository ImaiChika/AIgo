<script setup>
import { ref, onMounted } from "vue";
import { api } from "../api.js";

// 知识点选择器：模糊搜索（关键词）+ 精确筛选（分类/专业/大纲代码前缀）+ 分页 + 多选勾选
// 支持单选（multiple=false）与多选（multiple=true），已选项以标签展示可移除，
// 也可直接输入逗号分隔的大纲代码批量加入。
const props = defineProps({
  modelValue: { type: Array, default: () => [] }, // 已选知识点数组
  multiple: { type: Boolean, default: true },      // 是否多选
  placeholder: { type: String, default: "搜索知识点、大纲代码、专业..." },
});
const emit = defineEmits(["update:modelValue"]);

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
  loading.value = true;
  try {
    const data = await api.searchKPFiltered({
      q: keyword.value.trim(),
      subject: subject.value,
      category: category.value,
      outline_code: outlineCode.value.trim(),
      page: p,
      page_size: 50,
    });
    results.value = data.points || [];
    total.value = data.total || 0;
    page.value = p;
    pageCount.value = Math.max(1, Math.ceil(total.value / 50));
    showResults.value = true;
  } catch (e) {
    showToast("搜索失败: " + e.message);
  } finally {
    loading.value = false;
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

// 逗号分隔大纲代码批量加入（精确匹配，可包含名称片段？仅大纲代码精确）
async function addByCodes() {
  const codes = codeInput.value.split(/[,，\s]+/).map((s) => s.trim()).filter(Boolean);
  if (!codes.length) return;
  const list = [...props.modelValue];
  let added = 0;
  for (const code of codes) {
    try {
      const data = await api.searchKPFiltered({ outline_code: code, page: 1, page_size: 50 });
      // 取大纲代码完全匹配或前缀匹配的第一个
      const kp = (data.points || []).find((x) => x.outline_code === code);
      if (kp && !list.some((x) => x.id === kp.id)) {
        list.push(kp);
        added++;
      } else if (!kp) {
        // 未找到精确匹配，尝试列表里前缀匹配
        const pre = (data.points || []).find((x) => x.outline_code.startsWith(code));
        if (pre && !list.some((x) => x.id === pre.id)) {
          list.push(pre);
          added++;
        }
      }
    } catch (e) {
      // ignore
    }
  }
  if (added > 0) {
    emit("update:modelValue", list);
    showToast(`已加入 ${added} 个知识点（共 ${list.length} 个）`);
  } else {
    showToast("未找到匹配的大纲代码");
  }
  codeInput.value = "";
}

function reset() {
  keyword.value = "";
  subject.value = "";
  category.value = "";
  outlineCode.value = "";
  search(1);
}

onMounted(async () => {
  try {
    meta.value = await api.kpMeta();
  } catch (e) {
    console.error(e);
  }
  // 初始展示全部（第一页），便于浏览选择
  search(1);
});
</script>

<template>
  <div class="kp-picker">
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
      <button class="kp-btn" type="button" @click="search(1)" :disabled="loading">{{ loading ? "搜索中..." : "搜索" }}</button>
      <button class="kp-btn ghost" type="button" @click="reset">重置</button>
    </div>

    <!-- 结果区（分页 + 勾选） -->
    <div v-if="showResults" class="kp-results">
      <div class="kp-results-head">
        <span>共 {{ total }} 个知识点</span>
      </div>
      <div v-if="loading" class="kp-loading">搜索中...</div>
      <div v-else-if="!results.length" class="kp-empty">无匹配结果</div>
      <div v-else class="kp-list">
        <label v-for="kp in results" :key="kp.id" class="kp-item" :class="{ checked: isSelected(kp) }">
          <input
            v-if="multiple"
            type="checkbox"
            :checked="isSelected(kp)"
            @change="toggleSelect(kp)"
            class="kp-checkbox"
          />
          <span class="kp-radio" v-else :class="{ active: isSelected(kp) }" @click="toggleSelect(kp)"></span>
          <span class="kp-item-main" @click="toggleSelect(kp)">
            <span class="kp-topic">{{ kp.topic }}</span>
            <span class="kp-sub">{{ kp.subject }}</span>
            <code class="kp-code">{{ kp.outline_code }}</code>
          </span>
        </label>
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
      <div class="kp-selected-title">已选 {{ modelValue.length }} 个知识点：</div>
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
      <button class="kp-btn" type="button" @click="addByCodes">按代码加入</button>
    </div>
  </div>
</template>

<style scoped>
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
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  padding: 8px;
  max-height: 320px;
  overflow-y: auto;
  background: #fff;
}

.kp-results-head {
  font-size: 12px;
  color: #6e7b8f;
  padding: 0 4px 6px;
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
  display: flex;
  align-items: center;
  gap: 3px;
  margin-top: 8px;
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
