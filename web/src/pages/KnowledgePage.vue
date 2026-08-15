<script setup>
import { ref, onMounted, computed } from "vue";
import { currentUser } from "../auth.js";
import { api } from "../api.js";

const toast = ref("");
const points = ref([]);
const total = ref(0);
const page = ref(1);
const searchQuery = ref("");
const loading = ref(false);

// 导入/添加/删除权限：仅 admin/expert（teacher 只有查看权限）
const canManage = computed(() => {
  const role = currentUser.value?.role;
  return role === "admin" || role === "expert";
});

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  toast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadPoints() {
  loading.value = true;
  try {
    const data = await api.listKP({ page: page.value, page_size: 50 });
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
    page.value = 1;
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

async function deleteKP(p) {
  if (!confirm(`确定删除知识点：${p.topic}？`)) return;
  try {
    await api.deleteKP(p.id);
    showToast("已删除");
    points.value = points.value.filter((item) => item.id !== p.id);
    total.value--;
  } catch (e) {
    showToast("删除失败: " + e.message);
  }
}

async function handleImport(event) {
  const file = event.target.files[0];
  if (!file) return;
  try {
    const data = await api.importKP(file);
    showToast(`导入成功: ${data.imported} 个知识点`);
    loadPoints();
  } catch (e) {
    showToast("导入失败: " + e.message);
  }
  event.target.value = "";
}

function doSearch() {
  page.value = 1;
  search();
}

function clearSearch() {
  searchQuery.value = "";
  page.value = 1;
  loadPoints();
}

function nextPage() {
  if (page.value * 50 < total.value) {
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
        <h2>考试大纲知识点</h2>
        <small>共 {{ total }} 个考点</small>
      </div>

      <!-- 操作栏 -->
      <div class="toolbar">
        <div class="search-row">
          <input
            v-model="searchQuery"
            placeholder="搜索知识点、大纲代码、专业..."
            @keyup.enter="doSearch"
          />
          <button class="primary-button" type="button" @click="doSearch">搜索</button>
          <button class="ghost-button" type="button" @click="clearSearch">重置</button>
        </div>
        <div class="action-btns" v-if="canManage">
          <label class="ghost-button import-btn">
            导入考试大纲
            <input type="file" accept=".xlsx,.xls" hidden @change="handleImport" />
          </label>
        </div>
      </div>

      <!-- 列表 -->
      <div v-if="loading" class="loading">加载中...</div>
      <table v-else class="kp-table">
        <thead>
          <tr>
            <th>大纲代码</th>
            <th>分类</th>
            <th>专业/系统</th>
            <th>单元</th>
            <th>细目</th>
            <th>要点</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="p in points" :key="p.id">
            <td class="code-cell">{{ p.outline_code }}</td>
            <td><span class="category-tag" :class="p.category === '基础医学' ? 'basic' : 'clinical'">{{ p.category }}</span></td>
            <td>{{ p.subject }}</td>
            <td class="unit-cell">{{ p.unit }}</td>
            <td>{{ p.sub_item }}</td>
            <td>{{ p.topic }}</td>
            <td>
              <button v-if="canManage" class="delete-btn" type="button" @click="deleteKP(p)" title="删除">×</button>
            </td>
          </tr>
          <tr v-if="!points.length">
            <td colspan="7" class="empty">暂无数据，请导入考试大纲</td>
          </tr>
        </tbody>
      </table>

      <!-- 分页 -->
      <div class="pagination">
        <button class="ghost-button" :disabled="page <= 1" @click="prevPage">上一页</button>
        <span>第 {{ page }} 页</span>
        <button class="ghost-button" :disabled="page * 50 >= total" @click="nextPage">下一页</button>
      </div>
    </section>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
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

.action-btns {
  display: flex;
  gap: 8px;
}

.import-btn {
  cursor: pointer;
  display: inline-flex;
  align-items: center;
}

.kp-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
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
  padding: 8px 12px;
  border-bottom: 1px solid #f0f3f7;
}

.kp-table tr:hover {
  background: #f8fbff;
}

.code-cell {
  color: #6e7b8f;
  font-size: 11px;
  font-family: monospace;
}

.category-tag {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
}

.category-tag.basic {
  background: #fff3e0;
  color: #e65100;
}

.category-tag.clinical {
  background: #e8f5e9;
  color: #2e7d32;
}

.unit-cell {
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.delete-btn {
  width: 28px;
  height: 28px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #c54858;
  font-size: 18px;
  cursor: pointer;
  display: grid;
  place-items: center;
}

.delete-btn:hover {
  background: #fff0f0;
  border-color: #c54858;
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
