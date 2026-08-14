<script setup>
import { ref, computed, onMounted } from "vue";
import { api } from "../api.js";

const toast = ref("");
const questions = ref([]);
const selectedIds = ref(new Set());
const results = ref({}); // questionId -> AIReviewResult
const checking = ref(false);
const filterStatus = ref("");
const searchQuery = ref("");
const selectedQuestion = ref(null);
const questionPage = ref(1);
const questionTotal = ref(0);
const questionHasMore = ref(false);

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

// 加载题目列表（分页追加）
async function loadQuestions() {
  try {
    const data = await api.listQuestions(questionPage.value, 200);
    const list = data.questions || data || [];
    questions.value = questionPage.value === 1 ? list : questions.value.concat(list);
    questionTotal.value = data.total || 0;
    questionHasMore.value = !!data.has_more;
  } catch (e) {
    console.error(e);
    showToast("加载题目失败: " + e.message);
  }
}

async function loadMoreQuestions() {
  questionPage.value += 1;
  await loadQuestions();
}

// 加载已有检查结果
async function loadResults() {
  try {
    const data = await api.aiCheckResults(1000);
    const map = {};
    for (const r of data.results || []) {
      map[r.question_id] = r;
    }
    results.value = map;
  } catch (e) {
    console.error(e);
    showToast("加载检查结果失败: " + e.message);
  }
}

// 筛选后的题目列表
const filteredQuestions = computed(() => {
  let list = questions.value;
  if (filterStatus.value) {
    list = list.filter(q => q.status === filterStatus.value);
  }
  if (searchQuery.value) {
    const kw = searchQuery.value.toLowerCase();
    list = list.filter(q =>
      (q.clinical_stem || "").toLowerCase().includes(kw) ||
      (q.outline_code || "").toLowerCase().includes(kw)
    );
  }
  return list;
});

// 全选/取消全选
function toggleSelectAll() {
  const ids = filteredQuestions.value.map(q => q.id);
  if (ids.every(id => selectedIds.value.has(id))) {
    ids.forEach(id => selectedIds.value.delete(id));
  } else {
    ids.forEach(id => selectedIds.value.add(id));
  }
}

function toggleSelect(id) {
  if (selectedIds.value.has(id)) {
    selectedIds.value.delete(id);
  } else {
    selectedIds.value.add(id);
  }
}

function isSelected(id) {
  return selectedIds.value.has(id);
}

// AI 检查
async function runAICheck() {
  const ids = [...selectedIds.value];
  if (ids.length === 0) {
    showToast("请先选择题目");
    return;
  }

  checking.value = true;
  try {
    const data = await api.aiCheck(ids);
    // 合并结果
    for (const r of data.results || []) {
      results.value[r.question_id] = r;
      // 更新题目状态
      const q = questions.value.find(q => q.id === r.question_id);
      if (q && r.verdict === "pass") {
        q.status = "ai_reviewed";
      }
    }
    const passed = (data.results || []).filter(r => r.verdict === "pass").length;
    const failed = (data.results || []).filter(r => r.verdict !== "pass").length;
    showToast(`检查完成: ${passed} 通过, ${failed} 有问题`);
  } catch (e) {
    showToast("AI 检查失败: " + e.message);
  } finally {
    checking.value = false;
  }
}

// 查看详情
async function selectQuestion(q) {
  selectedQuestion.value = q;
}

// 获取检查结果
function getResult(questionId) {
  return results.value[questionId] || null;
}

// 检查结果是否过期（题目在检查后被修改过）
function isResultStale(q) {
  const r = getResult(q.id);
  if (!r) return false;
  return (q.version || 0) > (r.question_version || 0);
}

// 状态文本
function statusText(status) {
  const map = {
    ai_draft: "AI 草稿",
    auto_checked: "自动初评",
    ai_reviewed: "AI 已检查",
    reviewing: "审核中",
    revision_required: "需修改",
    rejected: "已驳回",
    approved: "已通过",
    published: "已发布",
    archived: "已归档",
  };
  return map[status] || status;
}

function statusClass(status) {
  if (status === "approved" || status === "published" || status === "ai_reviewed") return "status-good";
  if (status === "rejected") return "status-bad";
  if (status === "reviewing") return "status-active";
  return "";
}

// Verdict 文本
function verdictText(verdict) {
  const map = { pass: "通过", issues_found: "有问题", reject: "驳回", error: "检查失败" };
  return map[verdict] || verdict;
}

function verdictClass(verdict) {
  if (verdict === "pass") return "verdict-pass";
  if (verdict === "reject") return "verdict-reject";
  return "verdict-issues";
}

// 分数颜色
function scoreColor(score) {
  if (score >= 80) return "#087c55";
  if (score >= 60) return "#d4a017";
  return "#c54858";
}

onMounted(() => {
  loadQuestions();
  loadResults();
});
</script>

<template>
  <div class="ai-check-layout">
    <!-- 标题 -->
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>AI 质量检查</h2>
      </div>
      <p class="info-text">
        选择题目发送给 AI 检查质量（科学性、答案正确性、解析准确性等）。
        检查通过的题目状态会自动更新为"AI 已检查"。
      </p>
    </section>

    <div class="two-col">
      <!-- 左侧：题目列表 -->
      <section class="panel list-panel">
        <div class="section-heading">
          <span class="dot blue"></span>
          <h2>题库 ({{ filteredQuestions.length }})</h2>
        </div>

        <!-- 筛选 -->
        <div class="filter-bar">
          <input v-model="searchQuery" placeholder="搜索题干/大纲代码..." class="search-input" />
          <select v-model="filterStatus" class="filter-select">
            <option value="">全部状态</option>
            <option value="ai_draft">AI 草稿</option>
            <option value="auto_checked">自动初评</option>
            <option value="ai_reviewed">AI 已检查</option>
            <option value="reviewing">审核中</option>
            <option value="approved">已通过</option>
          </select>
        </div>

        <!-- 操作栏 -->
        <div class="action-bar">
          <label class="checkbox-row">
            <input type="checkbox" :checked="filteredQuestions.length > 0 && filteredQuestions.every(q => isSelected(q.id))" @change="toggleSelectAll" />
            <span>全选 ({{ selectedIds.size }})</span>
          </label>
          <button class="primary-button" @click="runAICheck" :disabled="checking || selectedIds.size === 0">
            {{ checking ? "检查中..." : "🔍 AI 检查" }}
          </button>
        </div>

        <!-- 题目列表 -->
        <div class="question-list">
          <div v-for="q in filteredQuestions" :key="q.id" class="question-item" :class="{ selected: isSelected(q.id), 'has-result': getResult(q.id) }" @click="selectQuestion(q)">
            <div class="question-item-top">
              <label class="checkbox-row" @click.stop>
                <input type="checkbox" :checked="isSelected(q.id)" @change="toggleSelect(q.id)" />
              </label>
              <div class="question-item-body">
                <div class="question-item-stem">{{ (q.clinical_stem || "").substring(0, 80) }}...</div>
                <div class="question-item-meta">
                  <code>{{ q.outline_code }}</code>
                  <span class="q-status" :class="statusClass(q.status)">{{ statusText(q.status) }}</span>
                  <span v-if="getResult(q.id)" class="verdict-tag" :class="verdictClass(getResult(q.id).verdict)">
                    {{ verdictText(getResult(q.id).verdict) }}
                  </span>
                  <span v-else class="verdict-tag verdict-none">未检查</span>
                  <span v-if="getResult(q.id) && isResultStale(q)" class="verdict-tag verdict-stale" title="题目内容已修改，检查结果可能过期">
                    内容已修改
                  </span>
                </div>
              </div>
            </div>
          </div>
          <button v-if="questionHasMore" class="load-more-btn" type="button" @click="loadMoreQuestions">
            加载更多（已显示 {{ questions.length }} / {{ questionTotal }}）
          </button>
        </div>
      </section>

      <!-- 右侧：检查结果详情 -->
      <section class="panel detail-panel">
        <div v-if="!selectedQuestion" class="empty-state">
          <p>← 选择一道题目查看检查结果</p>
        </div>

        <div v-else>
          <div class="section-heading">
            <span class="dot blue"></span>
            <h2>题目详情</h2>
            <span class="q-status" :class="statusClass(selectedQuestion.status)">
              {{ statusText(selectedQuestion.status) }}
            </span>
          </div>

          <!-- 题目内容 -->
          <div class="question-detail">
            <div class="detail-field">
              <label>题干</label>
              <div class="field-content">{{ selectedQuestion.clinical_stem }}</div>
            </div>
            <div class="detail-field">
              <label>选项</label>
              <div class="options-list">
                <div v-for="opt in selectedQuestion.options" :key="opt.label" class="option-item" :class="{ 'correct-answer': opt.label === selectedQuestion.answer }">
                  <strong>{{ opt.label }}.</strong> {{ opt.text }}
                  <span v-if="opt.label === selectedQuestion.answer" class="answer-badge">✓ 正确答案</span>
                </div>
              </div>
            </div>
            <div class="detail-field" v-if="selectedQuestion.explanation">
              <label>解析</label>
              <div class="field-content">{{ selectedQuestion.explanation }}</div>
            </div>
          </div>

          <!-- AI 检查结果 -->
          <div v-if="getResult(selectedQuestion.id)" class="review-result">
            <div class="section-heading">
              <span class="dot green"></span>
              <h2>AI 检查结果</h2>
              <span class="verdict-tag" :class="verdictClass(getResult(selectedQuestion.id).verdict)">
                {{ verdictText(getResult(selectedQuestion.id).verdict) }}
              </span>
            </div>

            <!-- 分数 -->
            <div class="scores-grid">
              <div class="score-card">
                <div class="score-label">科学性</div>
                <div class="score-value" :style="{ color: scoreColor(getResult(selectedQuestion.id).scores.scientific) }">
                  {{ getResult(selectedQuestion.id).scores.scientific }}
                </div>
              </div>
              <div class="score-card">
                <div class="score-label">逻辑性</div>
                <div class="score-value" :style="{ color: scoreColor(getResult(selectedQuestion.id).scores.logic) }">
                  {{ getResult(selectedQuestion.id).scores.logic }}
                </div>
              </div>
              <div class="score-card">
                <div class="score-label">A2 适配</div>
                <div class="score-value" :style="{ color: scoreColor(getResult(selectedQuestion.id).scores.a2_fit) }">
                  {{ getResult(selectedQuestion.id).scores.a2_fit }}
                </div>
              </div>
              <div class="score-card">
                <div class="score-label">答案准确</div>
                <div class="score-value" :style="{ color: scoreColor(getResult(selectedQuestion.id).scores.answer) }">
                  {{ getResult(selectedQuestion.id).scores.answer }}
                </div>
              </div>
            </div>

            <!-- 问题列表 -->
            <div v-if="getResult(selectedQuestion.id).issues && getResult(selectedQuestion.id).issues.length > 0" class="issues-section">
              <h3>发现的问题</h3>
              <div v-for="(issue, idx) in getResult(selectedQuestion.id).issues" :key="idx" class="issue-item" :class="'issue-' + issue.severity">
                <span class="issue-severity">{{ issue.severity === 'error' ? '❌' : issue.severity === 'warning' ? '⚠️' : 'ℹ️' }}</span>
                <span class="issue-field">[{{ issue.field }}]</span>
                <span class="issue-message">{{ issue.message }}</span>
              </div>
            </div>

            <!-- 修改建议 -->
            <div v-if="getResult(selectedQuestion.id).suggestion" class="suggestion-section">
              <h3>修改建议</h3>
              <div class="suggestion-content">{{ getResult(selectedQuestion.id).suggestion }}</div>
            </div>

            <div class="result-meta">
              <span>模型: {{ getResult(selectedQuestion.id).model }}</span>
              <span>检查时间: {{ new Date(getResult(selectedQuestion.id).created_at).toLocaleString() }}</span>
            </div>
          </div>

          <div v-else class="no-result">
            <p>该题目尚未进行 AI 检查</p>
          </div>
        </div>
      </section>
    </div>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
</template>

<style scoped>
.ai-check-layout {
  max-width: 1200px;
  margin: 0 auto;
}

.info-text {
  font-size: 14px;
  color: #6e7b8f;
  line-height: 1.6;
  margin: 0;
}

.two-col {
  display: grid;
  grid-template-columns: 400px 1fr;
  gap: 16px;
  align-items: start;
}

.list-panel {
  max-height: calc(100vh - 200px);
  overflow-y: auto;
}

.filter-bar {
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

.filter-select {
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 8px;
  font-size: 13px;
}

.action-bar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
  padding-bottom: 12px;
  border-bottom: 1px solid #e5ebf3;
}

.checkbox-row {
  display: flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  font-size: 13px;
}

.checkbox-row input {
  width: 16px;
  height: 16px;
}

.question-list {
  display: grid;
  gap: 8px;
}

.question-item {
  padding: 10px;
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  cursor: pointer;
  transition: border-color 0.15s;
}

.question-item:hover {
  border-color: #1385f8;
}

.question-item.selected {
  border-color: #1385f8;
  background: #f8fbff;
}

.question-item.has-result {
  border-left: 3px solid #1385f8;
}

.question-item-top {
  display: flex;
  gap: 8px;
}

.question-item-body {
  flex: 1;
  min-width: 0;
}

.question-item-stem {
  font-size: 13px;
  color: #1a2332;
  line-height: 1.4;
  margin-bottom: 6px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.question-item-meta {
  display: flex;
  gap: 8px;
  align-items: center;
  font-size: 12px;
}

.question-item-meta code {
  background: #f0f3f7;
  padding: 1px 4px;
  border-radius: 3px;
  font-size: 11px;
}

.q-status {
  font-size: 11px;
  font-weight: 700;
  padding: 2px 6px;
  border-radius: 4px;
  background: #f0f3f7;
  color: #6e7b8f;
}

.status-good { background: #f0fff8; color: #087c55; }
.status-bad { background: #fff0f0; color: #c54858; }
.status-active { background: #eff8ff; color: #0571dc; }

.verdict-tag {
  font-size: 11px;
  font-weight: 700;
  padding: 2px 6px;
  border-radius: 4px;
}

.verdict-pass { background: #f0fff8; color: #087c55; }
.verdict-issues { background: #fff8f0; color: #d4a017; }
.verdict-reject { background: #fff0f0; color: #c54858; }
.verdict-none { background: #f3f6fb; color: #9aa5b4; }
.verdict-stale { background: #fdf2e3; color: #c07b22; }

.load-more-btn {
  width: 100%;
  padding: 10px;
  border: 1px dashed #dce8f7;
  border-radius: 6px;
  background: #f8fbff;
  color: #1385f8;
  font-size: 13px;
  cursor: pointer;
  margin-top: 8px;
}

.load-more-btn:hover {
  background: #eff8ff;
}

.empty-state {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 300px;
  color: #9aa5b4;
  font-size: 14px;
}

.question-detail {
  margin-bottom: 24px;
}

.detail-field {
  margin-bottom: 16px;
}

.detail-field label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 6px;
}

.field-content {
  font-size: 14px;
  line-height: 1.6;
  color: #1a2332;
}

.options-list {
  display: grid;
  gap: 6px;
}

.option-item {
  padding: 8px 10px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  font-size: 13px;
}

.option-item.correct-answer {
  border-color: #087c55;
  background: #f0fff8;
}

.answer-badge {
  margin-left: 8px;
  font-size: 11px;
  color: #087c55;
  font-weight: 600;
}

.review-result {
  border-top: 2px solid #e5ebf3;
  padding-top: 16px;
}

.scores-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 12px;
  margin-bottom: 20px;
}

.score-card {
  text-align: center;
  padding: 12px;
  border: 1px solid #e5ebf3;
  border-radius: 8px;
}

.score-label {
  font-size: 12px;
  color: #6e7b8f;
  margin-bottom: 4px;
}

.score-value {
  font-size: 24px;
  font-weight: 700;
}

.issues-section {
  margin-bottom: 16px;
}

.issues-section h3 {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 8px;
}

.issue-item {
  padding: 8px 10px;
  border-radius: 6px;
  font-size: 13px;
  margin-bottom: 6px;
  display: flex;
  gap: 8px;
  align-items: flex-start;
}

.issue-error { background: #fff0f0; }
.issue-warning { background: #fff8f0; }
.issue-info { background: #f0f8ff; }

.issue-severity { flex-shrink: 0; }
.issue-field { font-weight: 600; color: #6e7b8f; flex-shrink: 0; }
.issue-message { flex: 1; }

.suggestion-section {
  margin-bottom: 16px;
}

.suggestion-section h3 {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 8px;
}

.suggestion-content {
  padding: 12px;
  background: #f8fbff;
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  font-size: 13px;
  line-height: 1.6;
}

.result-meta {
  display: flex;
  gap: 16px;
  font-size: 12px;
  color: #9aa5b4;
}

.no-result {
  padding: 20px;
  text-align: center;
  color: #9aa5b4;
  font-size: 14px;
}

.primary-button {
  height: 36px;
  padding: 0 16px;
  border: none;
  border-radius: 7px;
  background: #1385f8;
  color: #fff;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
}

.primary-button:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.toast {
  position: fixed;
  bottom: 20px;
  left: 50%;
  transform: translateX(-50%) translateY(20px);
  background: #1a2332;
  color: #fff;
  padding: 10px 20px;
  border-radius: 8px;
  font-size: 14px;
  opacity: 0;
  transition: all 0.3s;
  pointer-events: none;
  z-index: 1000;
}

.toast.show {
  opacity: 1;
  transform: translateX(-50%) translateY(0);
}
</style>
