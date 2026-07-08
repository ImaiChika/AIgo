<script setup>
import { ref, onMounted } from "vue";
import { api } from "../api.js";

const toast = ref("");
const points = ref([]);
const total = ref(0);
const page = ref(1);
const searchQuery = ref("");
const loading = ref(false);
const importResult = ref(null);

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadPoints() {
  loading.value = true;
  try {
    const data = await api.listKP({ page: page.value, page_size: 20 });
    points.value = data.points || [];
    total.value = data.total || 0;
  } catch (e) {
    showToast("加载失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

async function search() {
  if (!searchQuery.value.trim()) {
    loadPoints();
    return;
  }
  loading.value = true;
  try {
    const data = await api.searchKP(searchQuery.value.trim());
    points.value = data.points || [];
    total.value = data.total || 0;
  } catch (e) {
    showToast("搜索失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

async function handleImport(event) {
  const file = event.target.files[0];
  if (!file) return;
  try {
    const data = await api.importKP(file);
    importResult.value = data;
    showToast(`导入成功: ${data.imported} 个知识点`);
    loadPoints();
  } catch (e) {
    showToast("导入失败: " + e.message);
  }
  event.target.value = "";
}

function nextPage() {
  if (page.value * 20 < total.value) {
    page.value++;
    loadPoints();
  }
}

function prevPage() {
  if (page.value > 1) {
    page.value--;
    loadPoints();
  }
}

onMounted(loadPoints);
</script>

<template>
  <div class="kp-layout">
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>知识点管理</h2>
        <small>共 {{ total }} 个知识点</small>
      </div>

      <!-- 搜索 + 导入 -->
      <div class="toolbar">
        <div class="search-row">
          <input
            v-model="searchQuery"
            placeholder="搜索知识点名称或关键词..."
            @keyup.enter="search"
          />
          <button class="primary-button" type="button" @click="search">搜索</button>
          <button class="ghost-button" type="button" @click="searchQuery = ''; loadPoints()">重置</button>
        </div>
        <label class="ghost-button import-btn">
          导入 Excel
          <input type="file" accept=".xlsx,.xls" hidden @change="handleImport" />
        </label>
      </div>

      <!-- 导入结果 -->
      <div v-if="importResult" class="import-result">
        导入 {{ importResult.imported }} 个知识点，当前总量 {{ importResult.total }}
      </div>

      <!-- 列表 -->
      <div v-if="loading" class="loading">加载中...</div>
      <table v-else class="kp-table">
        <thead>
          <tr>
            <th>ID</th>
            <th>系统</th>
            <th>知识点名称</th>
            <th>关键词</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in points" :key="p.id">
            <td class="id-cell">{{ p.id }}</td>
            <td><span class="system-tag">{{ p.system || "未分类" }}</span></td>
            <td>{{ p.topic }}</td>
            <td class="kw-cell">{{ (p.keywords || []).slice(0, 3).join("、") }}</td>
          </tr>
          <tr v-if="!points.length">
            <td colspan="4" class="empty">暂无数据</td>
          </tr>
        </tbody>
      </table>

      <!-- 分页 -->
      <div class="pagination">
        <button class="ghost-button" :disabled="page <= 1" @click="prevPage">上一页</button>
        <span>第 {{ page }} 页</span>
        <button class="ghost-button" :disabled="page * 20 >= total" @click="nextPage">下一页</button>
      </div>
    </section>
  </div>
</template>

<style scoped>
.kp-layout {
  max-width: 100%;
}

.toolbar {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;
  flex-wrap: wrap;
}

.search-row {
  display: flex;
  gap: 8px;
  flex: 1;
}

.search-row input {
  flex: 1;
  min-width: 200px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 8px 12px;
  font-size: 14px;
}

.import-btn {
  cursor: pointer;
  display: inline-flex;
  align-items: center;
}

.import-result {
  padding: 10px 14px;
  background: #f0fff8;
  border: 1px solid #bdecd9;
  border-radius: 8px;
  color: #087c55;
  font-size: 13px;
  margin-bottom: 14px;
}

.kp-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 14px;
}

.kp-table th {
  text-align: left;
  padding: 10px 12px;
  background: #f8fbff;
  color: #6e7b8f;
  font-weight: 600;
  border-bottom: 1px solid #e5ebf3;
}

.kp-table td {
  padding: 10px 12px;
  border-bottom: 1px solid #f0f3f7;
}

.kp-table tr:hover {
  background: #f8fbff;
}

.id-cell {
  color: #6e7b8f;
  font-size: 12px;
  font-family: monospace;
}

.system-tag {
  display: inline-block;
  padding: 2px 8px;
  background: #eff8ff;
  color: #0571dc;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 600;
}

.kw-cell {
  color: #6e7b8f;
  font-size: 12px;
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.empty {
  text-align: center;
  color: #6e7b8f;
  padding: 30px !important;
}

.loading {
  text-align: center;
  color: #6e7b8f;
  padding: 30px;
}

.pagination {
  display: flex;
  justify-content: center;
  align-items: center;
  gap: 16px;
  margin-top: 16px;
  padding-top: 14px;
  border-top: 1px solid #e5ebf3;
}

.pagination span {
  color: #6e7b8f;
  font-size: 13px;
}
</style>
