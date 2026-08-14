<script setup>
import { ref, onMounted } from "vue";
import { api } from "../api.js";
import { isAdmin } from "../auth.js";

const toast = ref("");
const questions = ref([]);
const loading = ref(false);
const filterStatus = ref("");
const searchQuery = ref("");
const selectedQuestion = ref(null);
const selectedImages = ref([]);
const lightboxImage = ref("");
const editing = ref(false);
const editForm = ref({ clinical_stem: "", options: [], answer: "", explanation: "" });

// 批量选择
const selectedIds = ref(new Set());
const selectAll = ref(false);
const exporting = ref(false);

function toggleSelect(id) {
  if (selectedIds.value.has(id)) {
    selectedIds.value.delete(id);
  } else {
    selectedIds.value.add(id);
  }
}

function toggleSelectAll() {
  if (selectAll.value) {
    selectedIds.value.clear();
    selectAll.value = false;
  } else {
    questions.value.forEach(q => selectedIds.value.add(q.id));
    selectAll.value = true;
  }
}

function downloadFile(filename) {
  const a = document.createElement('a');
  a.href = `http://127.0.0.1:8080/api/export/download/${filename}`;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
}

async function exportXlsx() {
  const ids = Array.from(selectedIds.value);
  if (ids.length === 0) {
    showToast("请先选择要导出的题目");
    return;
  }
  exporting.value = true;
  try {
    const data = await api.exportXlsx({ question_ids: ids });
    showToast(`已导出 ${data.count} 道题`);
    downloadFile(data.filename);
  } catch (e) {
    showToast("导出失败: " + e.message);
  } finally {
    exporting.value = false;
  }
}

async function exportDocx() {
  const ids = Array.from(selectedIds.value);
  if (ids.length === 0) {
    showToast("请先选择要导出的题目");
    return;
  }
  exporting.value = true;
  try {
    const data = await api.exportDocx({ question_ids: ids });
    showToast(`已导出 ${data.count} 道题`);
    downloadFile(data.filename);
  } catch (e) {
    showToast("导出失败: " + e.message);
  } finally {
    exporting.value = false;
  }
}

async function exportAllXlsx() {
  exporting.value = true;
  try {
    const data = await api.exportXlsx({ export_all: true });
    showToast(`已导出全部 ${data.count} 道题`);
    downloadFile(data.filename);
  } catch (e) {
    showToast("导出失败: " + e.message);
  } finally {
    exporting.value = false;
  }
}

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  toast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadQuestions() {
  loading.value = true;
  try {
    let data;
    if (searchQuery.value || filterStatus.value) {
      data = await api.searchQuestions(searchQuery.value, filterStatus.value);
    } else {
      data = await api.listQuestions();
    }
    questions.value = data.questions || [];
  } catch (e) {
    showToast("加载失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

async function selectQuestion(q) {
  selectedQuestion.value = q;
  selectedImages.value = [];
  try {
    const data = await api.listImages(q.id);
    selectedImages.value = data.images || [];
  } catch (e) {
    selectedImages.value = [];
  }
}

function imageSrc(path) {
  if (!path) return "";
  const filename = path.split("/").pop();
  return `http://127.0.0.1:8080/images/${filename}`;
}

function openImage(src) { lightboxImage.value = src; }
function closeLightbox() { lightboxImage.value = ""; }

async function deleteQuestion(q) {
  if (!confirm(`确定删除题目？\n${(q.clinical_stem || "").slice(0, 50)}...`)) return;
  try {
    await api.deleteQuestion(q.id);
    showToast("已删除");
    questions.value = questions.value.filter((item) => item.id !== q.id);
    if (selectedQuestion.value?.id === q.id) selectedQuestion.value = null;
  } catch (e) {
    showToast("删除失败: " + e.message);
  }
}

async function publishQuestion(q) {
  if (!confirm(`确定将题目发布到正式题库？\n${(q.clinical_stem || "").slice(0, 50)}...`)) return;
  try {
    await api.publishQuestion(q.id);
    q.status = "published";
    showToast("已发布到正式题库");
  } catch (e) {
    showToast("发布失败: " + e.message);
  }
}

function startEdit() {
  editForm.value = {
    clinical_stem: selectedQuestion.value.clinical_stem || "",
    options: (selectedQuestion.value.options || []).map(o => ({ ...o })),
    answer: selectedQuestion.value.answer || "",
    explanation: selectedQuestion.value.explanation || "",
  };
  editing.value = true;
}

function cancelEdit() { editing.value = false; }

function removeEditOption(index) {
  if (editForm.value.options.length <= 4) {
    showToast("至少保留4个选项");
    return;
  }
  editForm.value.options.splice(index, 1);
}

function addEditOption() {
  if (editForm.value.options.length >= 5) {
    showToast("最多5个选项");
    return;
  }
  const label = String.fromCharCode(65 + editForm.value.options.length);
  editForm.value.options.push({ label, text: "" });
}

async function saveEdit() {
  try {
    const data = await api.updateQuestion(selectedQuestion.value.id, {
      clinical_stem: editForm.value.clinical_stem,
      options: editForm.value.options,
      answer: editForm.value.answer,
      explanation: editForm.value.explanation,
    });
    selectedQuestion.value = data;
    const idx = questions.value.findIndex(q => q.id === data.id);
    if (idx >= 0) questions.value[idx] = data;
    editing.value = false;
    showToast("已保存修改");
  } catch (e) {
    showToast("保存失败: " + e.message);
  }
}

function doSearch() { loadQuestions(); }

function clearSearch() {
  searchQuery.value = "";
  filterStatus.value = "";
  loadQuestions();
}

function statusText(status) {
  const map = {
    ai_draft: "AI草稿", auto_checked: "已初评", ai_reviewed: "AI已检查",
    reviewing: "审核中", approved: "已通过", rejected: "已驳回",
    revision_required: "需修改", published: "已入库", archived: "已归档",
  };
  return map[status] || status;
}

function statusClass(status) {
  if (status === "approved" || status === "published" || status === "ai_reviewed") return "status-good";
  if (status === "rejected") return "status-bad";
  if (status === "reviewing") return "status-active";
  return "";
}

onMounted(loadQuestions);
</script>

<template>
  <div class="bank-layout">
    <!-- 左侧列表 -->
    <section class="panel bank-list-panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>题库</h2>
        <small>{{ questions.length }} 道</small>
      </div>

      <div class="filter-row">
        <input v-model="searchQuery" placeholder="搜索ID、题干或答案..." @keyup.enter="doSearch" class="search-input" />
        <select v-model="filterStatus" @change="doSearch">
          <option value="">全部状态</option>
          <option value="ai_draft">AI草稿</option>
          <option value="auto_checked">已初评</option>
          <option value="ai_reviewed">AI已检查</option>
          <option value="reviewing">审核中</option>
          <option value="approved">已通过</option>
          <option value="rejected">已驳回</option>
          <option value="published">已入库</option>
        </select>
        <button class="ghost-button" type="button" @click="doSearch">搜索</button>
        <button class="ghost-button" type="button" @click="clearSearch">重置</button>
      </div>

      <!-- 批量操作栏 -->
      <div class="batch-bar">
        <label class="select-all">
          <input type="checkbox" :checked="selectAll" @change="toggleSelectAll" />
          <span>全选</span>
        </label>
        <span class="selected-count" v-if="selectedIds.size > 0">已选 {{ selectedIds.size }} 道</span>
        <div class="export-btns">
          <button class="ghost-button" type="button" @click="exportXlsx" :disabled="exporting || selectedIds.size === 0">
            {{ exporting ? "导出中..." : "导出 Excel" }}
          </button>
          <button class="ghost-button" type="button" @click="exportDocx" :disabled="exporting || selectedIds.size === 0">
            {{ exporting ? "导出中..." : "导出 Word" }}
          </button>
          <button class="ghost-button" type="button" @click="exportAllXlsx" :disabled="exporting">
            导出全部 Excel
          </button>
        </div>
      </div>

      <div v-if="loading" class="loading">加载中...</div>
      <div v-else class="question-list">
        <div
          v-for="q in questions"
          :key="q.id"
          class="question-item"
          :class="{ active: selectedQuestion?.id === q.id, selected: selectedIds.has(q.id) }"
        >
          <input
            type="checkbox"
            :checked="selectedIds.has(q.id)"
            @change="toggleSelect(q.id)"
            @click.stop
            class="q-checkbox"
          />
          <button
            class="q-content"
            type="button"
            @click="selectQuestion(q)"
          >
            <div class="q-info">
              <span class="q-stem">{{ (q.clinical_stem || "").slice(0, 60) }}...</span>
              <span class="q-meta">
                答案: {{ q.answer }}
                <span v-if="q.outline_code"> | 大纲: {{ q.outline_code }}</span>
                <span v-if="q.profession"> | 专业: {{ q.profession }}</span>
              </span>
            </div>
            <div class="q-actions">
              <span class="q-status" :class="statusClass(q.status)">{{ statusText(q.status) }}</span>
              <button v-if="q.status === 'approved' && isAdmin" class="publish-btn" type="button" @click.stop="publishQuestion(q)" title="发布">发布</button>
              <button class="delete-btn" type="button" @click.stop="deleteQuestion(q)" title="删除">×</button>
            </div>
          </button>
        </div>
        <div v-if="!questions.length" class="empty">暂无题目</div>
      </div>
    </section>

    <!-- 右侧详情 -->
    <section class="panel" v-if="selectedQuestion">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>{{ editing ? "编辑题目" : "题目详情" }}</h2>
        <span class="q-status" :class="statusClass(selectedQuestion.status)">
          {{ statusText(selectedQuestion.status) }}
        </span>
        <div class="edit-actions" v-if="!editing">
          <button class="ghost-button" type="button" @click="startEdit">编辑</button>
        </div>
      </div>

      <!-- 查看模式 -->
      <template v-if="!editing">
        <!-- 试题参数 -->
        <div class="params-bar">
          <span v-if="selectedQuestion.outline_code" class="param-item">
            <span class="param-label">大纲代码</span>
            <span class="param-value">{{ selectedQuestion.outline_code }}</span>
          </span>
          <span v-if="selectedQuestion.difficulty" class="param-item">
            <span class="param-label">预估难度</span>
            <span class="param-value">{{ selectedQuestion.difficulty }}</span>
          </span>
          <span v-if="selectedQuestion.cognitive_level" class="param-item">
            <span class="param-label">认知层次</span>
            <span class="param-value">{{ selectedQuestion.cognitive_level }}</span>
          </span>
          <span v-if="selectedQuestion.profession" class="param-item">
            <span class="param-label">专业</span>
            <span class="param-value">{{ selectedQuestion.profession }}</span>
          </span>
          <span v-if="selectedQuestion.system" class="param-item">
            <span class="param-label">系统</span>
            <span class="param-value">{{ selectedQuestion.system }}</span>
          </span>
        </div>

        <div v-if="selectedQuestion.exam_points" class="detail-field">
          <label>考核要点</label>
          <p>{{ selectedQuestion.exam_points }}</p>
        </div>

        <div class="detail-field">
          <label>题干</label>
          <p class="stem-text">{{ selectedQuestion.clinical_stem }}</p>
        </div>

        <div class="detail-field">
          <label>选项</label>
          <div v-for="(opt, i) in (selectedQuestion.options || [])" :key="i" class="option-row-detail">
            <span class="opt-label">{{ opt.label }}</span>
            <span>{{ opt.text }}</span>
            <span v-if="opt.label === selectedQuestion.answer" class="correct">✓ 正确答案</span>
          </div>
        </div>

        <div v-if="selectedQuestion.explanation" class="detail-field">
          <label>解析</label>
          <p class="explanation-text">{{ selectedQuestion.explanation }}</p>
        </div>

        <div class="detail-field">
          <label>知识点</label>
          <div v-for="kp in (selectedQuestion.knowledge_points || [])" :key="kp.id" class="kp-tag">
            {{ kp.topic }}
          </div>
        </div>

        <!-- 配图 -->
        <div v-if="selectedImages.length" class="detail-field">
          <label>配图（{{ selectedImages.length }} 张）</label>
          <div class="image-grid">
            <div v-for="(img, i) in selectedImages" :key="img.id" class="image-thumb">
              <img :src="imageSrc(img.image_path)" :alt="`配图${i+1}`" @click="openImage(imageSrc(img.image_path))" />
              <span class="image-status" :class="img.status">{{ img.status === "approved" ? "已通过" : img.status === "rejected" ? "已驳回" : "待审核" }}</span>
            </div>
          </div>
        </div>

        <div class="detail-field">
          <label>元信息</label>
          <p class="meta-text">ID: {{ selectedQuestion.id }}</p>
          <p class="meta-text">版本: {{ selectedQuestion.version }}</p>
          <p class="meta-text">创建: {{ selectedQuestion.created_at }}</p>
        </div>

        <div v-if="selectedQuestion.status === 'approved' && isAdmin" class="publish-section">
          <button class="primary-button" type="button" @click="publishQuestion(selectedQuestion)">发布到正式题库</button>
        </div>
      </template>

      <!-- 编辑模式 -->
      <template v-else>
        <div class="detail-field">
          <label>题干</label>
          <textarea v-model="editForm.clinical_stem" class="edit-textarea"></textarea>
        </div>

        <div class="detail-field">
          <label>选项</label>
          <div v-for="(opt, i) in editForm.options" :key="i" class="edit-option-row">
            <span class="opt-label">{{ String.fromCharCode(65 + i) }}</span>
            <input v-model="opt.text" class="edit-input" />
            <button class="remove-btn" type="button" @click="removeEditOption(i)" :disabled="editForm.options.length <= 4">×</button>
          </div>
          <button class="text-button" type="button" @click="addEditOption">+ 添加选项</button>
        </div>

        <div class="detail-field">
          <label>正确答案</label>
          <select v-model="editForm.answer" class="edit-select">
            <option v-for="(opt, i) in editForm.options" :key="i" :value="opt.label">{{ opt.label }}</option>
          </select>
        </div>

        <div class="detail-field">
          <label>解析</label>
          <textarea v-model="editForm.explanation" class="edit-textarea"></textarea>
        </div>

        <div class="edit-buttons">
          <button class="primary-button" type="button" @click="saveEdit">保存修改</button>
          <button class="ghost-button" type="button" @click="cancelEdit">取消</button>
        </div>
      </template>
    </section>

    <section v-else class="panel empty-panel">
      <p>← 请从左侧选择一道题目查看详情</p>
    </section>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>

  <div v-if="lightboxImage" class="lightbox" @click="closeLightbox">
    <img :src="lightboxImage" @click.stop />
  </div>
</template>

<style scoped>
.bank-layout {
  display: grid;
  grid-template-columns: 420px minmax(0, 1fr);
  gap: 16px;
}

.bank-list-panel {
  max-height: calc(100vh - 180px);
  overflow-y: auto;
}

.filter-row {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
}

.search-input {
  flex: 1;
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 10px;
  font-size: 13px;
}

.filter-row select {
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 8px;
  font-size: 13px;
}

/* 批量操作栏 */
.batch-bar {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 0;
  border-bottom: 1px solid #e5ebf3;
  margin-bottom: 8px;
}

.select-all {
  display: flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  font-size: 13px;
  color: #6e7b8f;
}

.select-all input { width: 16px; height: 16px; }

.selected-count {
  font-size: 12px;
  color: #0571dc;
  font-weight: 600;
}

.export-btns {
  display: flex;
  gap: 8px;
  margin-left: auto;
}

.question-list { display: grid; gap: 6px; }

.question-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  background: #fff;
}

.question-item:hover { background: #f8fbff; }
.question-item.active { border-color: #1385f8; background: #eff8ff; }
.question-item.selected { border-color: #0571dc; background: #eff8ff; }

.q-checkbox {
  width: 16px;
  height: 16px;
  flex-shrink: 0;
  cursor: pointer;
}

.q-content {
  flex: 1;
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 10px;
  background: transparent;
  border: 0;
  padding: 0;
  text-align: left;
  cursor: pointer;
  min-width: 0;
}

.q-info { flex: 1; min-width: 0; }

.q-stem {
  display: block;
  font-size: 13px;
  color: #172033;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.q-meta { font-size: 11px; color: #6e7b8f; }

.q-actions { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }

.q-status {
  font-size: 11px;
  font-weight: 700;
  padding: 2px 6px;
  border-radius: 4px;
  background: #f0f3f7;
  color: #6e7b8f;
  white-space: nowrap;
}

.status-good { background: #f0fff8; color: #087c55; }
.status-bad { background: #fff0f0; color: #c54858; }
.status-active { background: #eff8ff; color: #0571dc; }

.delete-btn {
  width: 28px; height: 28px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #c54858;
  font-size: 18px;
  cursor: pointer;
  display: grid;
  place-items: center;
}

.delete-btn:hover { background: #fff0f0; border-color: #c54858; }

.publish-btn {
  padding: 2px 8px;
  border: 1px solid #087c55;
  border-radius: 6px;
  background: #f0fff8;
  color: #087c55;
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
  white-space: nowrap;
}

.publish-btn:hover { background: #087c55; color: #fff; }

.publish-section {
  border-top: 1px solid #e5ebf3;
  padding-top: 16px;
  margin-top: 16px;
}

/* 参数栏 */
.params-bar {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  padding: 12px 16px;
  background: #f8fbff;
  border: 1px solid #dce8f7;
  border-radius: 8px;
  margin-bottom: 16px;
}

.param-item {
  display: flex;
  align-items: center;
  gap: 6px;
}

.param-label {
  font-size: 11px;
  color: #6e7b8f;
  font-weight: 600;
}

.param-value {
  font-size: 12px;
  color: #172033;
  padding: 2px 8px;
  background: #fff;
  border: 1px solid #e5ebf3;
  border-radius: 4px;
  font-family: monospace;
}

.detail-field { margin-bottom: 16px; }

.detail-field label {
  display: block;
  font-size: 12px;
  font-weight: 700;
  color: #6e7b8f;
  margin-bottom: 6px;
}

.detail-field p {
  font-size: 14px;
  line-height: 1.7;
  margin: 0;
  color: #172033;
}

.stem-text {
  white-space: pre-wrap;
  word-break: break-all;
}

.explanation-text {
  white-space: pre-wrap;
  word-break: break-all;
  color: #444 !important;
}

.option-row-detail {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 0;
  font-size: 14px;
}

.opt-label {
  width: 22px; height: 22px;
  display: grid;
  place-items: center;
  border-radius: 50%;
  background: #1385f8;
  color: #fff;
  font-size: 11px;
  font-weight: 700;
}

.correct { color: #087c55; font-weight: 700; }

.kp-tag {
  display: inline-block;
  padding: 3px 10px;
  background: #eff8ff;
  color: #0571dc;
  border-radius: 5px;
  font-size: 12px;
  font-weight: 600;
  margin: 2px 4px 2px 0;
}

.meta-text {
  font-size: 12px !important;
  color: #6e7b8f !important;
  font-family: monospace;
}

.empty-panel { display: grid; place-items: center; color: #6e7b8f; }
.empty, .loading { text-align: center; color: #6e7b8f; padding: 30px; }
.edit-actions { margin-left: auto; }

.edit-textarea {
  width: 100%;
  min-height: 100px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 10px;
  font-size: 14px;
  line-height: 1.6;
  resize: vertical;
}

.edit-option-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.edit-input {
  flex: 1;
  height: 34px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 10px;
  font-size: 14px;
}

.edit-select {
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 10px;
  font-size: 14px;
}

.edit-buttons {
  display: flex;
  gap: 10px;
  margin-top: 16px;
  padding-top: 16px;
  border-top: 1px solid #e5ebf3;
}

.text-button {
  border: 0;
  background: transparent;
  color: #0571dc;
  cursor: pointer;
  font-size: 13px;
  padding: 4px 0;
}

.remove-btn {
  width: 28px; height: 28px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #c54858;
  font-size: 18px;
  cursor: pointer;
}

.remove-btn:hover:not(:disabled) { background: #fff0f0; }
.remove-btn:disabled { opacity: 0.3; cursor: not-allowed; }

.image-grid { display: flex; gap: 10px; flex-wrap: wrap; }

.image-thumb {
  display: flex;
  flex-direction: column;
  gap: 4px;
  align-items: center;
}

.image-thumb img {
  width: 150px; height: 120px;
  object-fit: cover;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  cursor: pointer;
}

.image-thumb img:hover { border-color: #1385f8; }

.image-status {
  font-size: 11px;
  font-weight: 600;
  padding: 2px 6px;
  border-radius: 4px;
  background: #f0f3f7;
  color: #6e7b8f;
}

.image-status.approved { background: #f0fff8; color: #087c55; }
.image-status.rejected { background: #fff0f0; color: #c54858; }

.lightbox {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.85);
  display: grid;
  place-items: center;
  z-index: 200;
  cursor: pointer;
}

.lightbox img {
  max-width: 90vw;
  max-height: 90vh;
  border-radius: 8px;
  cursor: default;
}
</style>
