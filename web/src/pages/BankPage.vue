<script setup>
import { ref, onMounted } from "vue";
import { api } from "../api.js";

const toast = ref("");
const questions = ref([]);
const loading = ref(false);
const filterStatus = ref("");
const selectedQuestion = ref(null);

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadQuestions() {
  loading.value = true;
  try {
    const data = await api.listQuestions();
    let list = data.questions || [];
    if (filterStatus.value) {
      list = list.filter((q) => q.status === filterStatus.value);
    }
    questions.value = list;
  } catch (e) {
    showToast("加载失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

function selectQuestion(q) {
  selectedQuestion.value = q;
}

function statusText(status) {
  const map = {
    ai_draft: "AI草稿",
    auto_checked: "已初评",
    reviewing: "审核中",
    approved: "已通过",
    rejected: "已驳回",
    revision_required: "需修改",
    published: "已入库",
    archived: "已归档",
  };
  return map[status] || status;
}

function statusClass(status) {
  if (status === "approved" || status === "published") return "status-good";
  if (status === "rejected") return "status-bad";
  if (status === "reviewing") return "status-active";
  return "";
}

function exportJSON() {
  const data = questions.value.filter((q) => q.status === "approved" || q.status === "published");
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = "题库导出.json";
  a.click();
  URL.revokeObjectURL(url);
  showToast(`已导出 ${data.length} 道审核通过的题目`);
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
        <select v-model="filterStatus" @change="loadQuestions">
          <option value="">全部状态</option>
          <option value="ai_draft">AI草稿</option>
          <option value="auto_checked">已初评</option>
          <option value="reviewing">审核中</option>
          <option value="approved">已通过</option>
          <option value="rejected">已驳回</option>
          <option value="published">已入库</option>
        </select>
        <button class="ghost-button" type="button" @click="exportJSON">导出 JSON</button>
      </div>

      <div v-if="loading" class="loading">加载中...</div>
      <div v-else class="question-list">
        <button
          v-for="q in questions"
          :key="q.id"
          class="question-item"
          :class="{ active: selectedQuestion?.id === q.id }"
          type="button"
          @click="selectQuestion(q)"
        >
          <div class="q-info">
            <span class="q-stem">{{ (q.clinical_stem || "").slice(0, 50) }}...</span>
            <span class="q-meta">答案: {{ q.answer }} | 选项: {{ (q.options || []).length }}个</span>
          </div>
          <span class="q-status" :class="statusClass(q.status)">{{ statusText(q.status) }}</span>
        </button>
        <div v-if="!questions.length" class="empty">暂无题目</div>
      </div>
    </section>

    <!-- 右侧详情 -->
    <section class="panel" v-if="selectedQuestion">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>题目详情</h2>
        <span class="q-status" :class="statusClass(selectedQuestion.status)">
          {{ statusText(selectedQuestion.status) }}
        </span>
      </div>

      <div class="detail-field">
        <label>题干</label>
        <p>{{ selectedQuestion.clinical_stem }}</p>
      </div>

      <div class="detail-field">
        <label>选项</label>
        <div v-for="(opt, i) in (selectedQuestion.options || [])" :key="i" class="option-row-detail">
          <span class="opt-label">{{ String.fromCharCode(65 + i) }}</span>
          <span>{{ opt.text }}</span>
          <span v-if="opt.label === selectedQuestion.answer" class="correct">✓</span>
        </div>
      </div>

      <div v-if="selectedQuestion.explanation" class="detail-field">
        <label>解析</label>
        <p>{{ selectedQuestion.explanation }}</p>
      </div>

      <div class="detail-field">
        <label>知识点</label>
        <div v-for="kp in (selectedQuestion.knowledge_points || [])" :key="kp.id" class="kp-tag">
          {{ kp.topic }}
        </div>
      </div>

      <div class="detail-field">
        <label>元信息</label>
        <p class="meta-text">ID: {{ selectedQuestion.id }}</p>
        <p class="meta-text">版本: {{ selectedQuestion.version }}</p>
        <p class="meta-text">创建: {{ selectedQuestion.created_at }}</p>
      </div>
    </section>

    <section v-else class="panel empty-panel">
      <p>← 请从左侧选择一道题目查看详情</p>
    </section>
  </div>
</template>

<style scoped>
.bank-layout {
  display: grid;
  grid-template-columns: 360px minmax(0, 1fr);
  gap: 16px;
}

.bank-list-panel {
  max-height: calc(100vh - 180px);
  overflow-y: auto;
}

.filter-row {
  display: flex;
  gap: 8px;
  margin-bottom: 14px;
}

.filter-row select {
  flex: 1;
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 10px;
}

.question-list {
  display: grid;
  gap: 6px;
}

.question-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  background: #fff;
  text-align: left;
  cursor: pointer;
}

.question-item:hover {
  background: #f8fbff;
}

.question-item.active {
  border-color: #1385f8;
  background: #eff8ff;
}

.q-info {
  flex: 1;
  min-width: 0;
}

.q-stem {
  display: block;
  font-size: 13px;
  color: #172033;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.q-meta {
  font-size: 11px;
  color: #6e7b8f;
}

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

.detail-field {
  margin-bottom: 16px;
}

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

.option-row-detail {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 0;
  font-size: 14px;
}

.opt-label {
  width: 22px;
  height: 22px;
  display: grid;
  place-items: center;
  border-radius: 50%;
  background: #1385f8;
  color: #fff;
  font-size: 11px;
  font-weight: 700;
}

.correct {
  color: #087c55;
  font-weight: 700;
}

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

.empty-panel {
  display: grid;
  place-items: center;
  color: #6e7b8f;
}

.empty, .loading {
  text-align: center;
  color: #6e7b8f;
  padding: 30px;
}
</style>
