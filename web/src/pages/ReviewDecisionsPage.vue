<script setup>
import { ref, onMounted, computed } from "vue";
import { api } from "../api.js";

const toast = ref("");
const decisions = ref([]);
const loading = ref(false);
const selected = ref(null); // 选中的 MyTaskItem
const reviewRecords = ref([]);
const finalizeAction = ref("approved");
const finalizeOpinion = ref("");
const submitting = ref(false);
const reviewers = ref([]); // 名字映射

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadDecisions() {
  loading.value = true;
  try {
    const data = await api.myDecisions();
    decisions.value = data.tasks || [];
    // 若当前选中项已被决断，清空选中
    if (selected.value && !decisions.value.some((d) => d.task.id === selected.value.task.id)) {
      selected.value = null;
      reviewRecords.value = [];
    }
  } catch (e) {
    showToast("加载待决断任务失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

async function loadReviewers() {
  try {
    const data = await api.listReviewers();
    reviewers.value = data.reviewers || [];
  } catch (e) {
    console.error(e);
  }
}

function reviewerName(id) {
  const r = reviewers.value.find((x) => x.id === id);
  return r ? r.display_name || r.username : id;
}

function statusText(status) {
  const map = {
    ai_draft: "AI草稿", auto_checked: "已初评", ai_reviewed: "AI已检查",
    reviewing: "审核中", conflict: "待决断", revision_required: "需修改",
    rejected: "已驳回", approved: "已通过", published: "已入库",
  };
  return map[status] || status;
}

function statusClass(status) {
  if (status === "approved" || status === "published" || status === "ai_reviewed") return "status-good";
  if (status === "rejected") return "status-bad";
  if (status === "reviewing") return "status-active";
  if (status === "revision_required") return "status-warn";
  if (status === "conflict") return "status-conflict";
  return "";
}

function difficultyText(d) {
  const map = { easy: "简单", medium: "中等", hard: "困难" };
  if (map[d]) return map[d];
  return d || "-";
}

// 选择待决断任务：加载题目详情 + 审核记录
async function selectDecision(item) {
  selected.value = item;
  reviewRecords.value = [];
  try {
    const recData = await api.reviewRecords(item.task.id);
    reviewRecords.value = recData.records || [];
  } catch (e) {
    console.error(e);
  }
}

// 提交决断
async function doFinalize() {
  if (!selected.value) return;
  submitting.value = true;
  try {
    await api.finalizeReview({
      task_id: selected.value.task.id,
      action: finalizeAction.value,
      opinion: finalizeOpinion.value,
    });
    showToast("决断完成");
    finalizeOpinion.value = "";
    await loadDecisions();
    if (!selected.value) {
      // 已决断完最后一项，清空右侧
      reviewRecords.value = [];
    }
  } catch (e) {
    showToast("决断失败: " + e.message);
  } finally {
    submitting.value = false;
  }
}

function conclusionText(c) {
  const map = {
    approved: "通过", rejected: "驳回", revision_required: "需修改",
  };
  return map[c] || c;
}

function conclusionClass(c) {
  if (c === "approved") return "c-approved";
  if (c === "rejected") return "c-rejected";
  return "c-revision";
}

// 投票进度
const votePercent = computed(() => {
  const item = selected.value;
  if (!item) return 0;
  return Math.min(100, Math.round((item.voted / Math.max(item.assigned, 1)) * 100));
});

// 卡片进度条宽度（用自身数据计算）
function votePercentValue(item) {
  if (!item.assigned) return 0;
  return Math.min(100, Math.round((item.voted / item.assigned) * 100));
}

onMounted(() => {
  loadDecisions();
  loadReviewers();
});
</script>

<template>
  <div class="decisions-layout">
    <!-- 左侧待决断列表 -->
    <section class="panel decision-list-panel">
      <div class="section-heading">
        <span class="dot purple"></span>
        <h2>待我决断</h2>
        <small>{{ decisions.length }} 项</small>
      </div>
      <div v-if="loading" class="loading">加载中...</div>
      <div v-else-if="!decisions.length" class="empty">
        暂无待决断的任务<br />
        <span class="empty-hint">所有轮次审核完成（或出现分歧）的题目会出现在这里，等你最终把关</span>
      </div>
      <div v-else class="decision-list">
        <div
          v-for="item in decisions"
          :key="item.task.id"
          class="decision-card"
          :class="{ active: selected?.task.id === item.task.id }"
          role="button"
          tabindex="0"
          @click="selectDecision(item)"
          @keydown.enter="selectDecision(item)"
        >
          <div class="dc-head">
            <span class="dc-flow">{{ item.flow_name || item.task.flow_id }}</span>
            <span class="dc-round">第 {{ item.round_index }}/{{ item.round_count }} 轮</span>
            <span class="dc-badge">待决断</span>
          </div>
          <div class="dc-stem">{{ (item.question.clinical_stem || "").slice(0, 55) }}{{ (item.question.clinical_stem || "").length > 55 ? "..." : "" }}</div>
          <div class="dc-votes">
            <span class="vote ok">通过 {{ item.approved }}</span>
            <span class="vote no">驳回 {{ item.rejected }}</span>
            <span class="vote warn">需修改 {{ item.revision }}</span>
            <span class="vote total">已投 {{ item.voted }}/{{ item.assigned }}</span>
          </div>
          <div class="dc-progress"><div class="dc-progress-fill" :style="{ width: votePercentValue(item) + '%' }"></div></div>
          <div class="dc-action">去决断 →</div>
        </div>
      </div>
    </section>

    <!-- 右侧决断工作台 -->
    <section class="panel" v-if="selected">
      <div class="section-heading">
        <span class="dot purple"></span>
        <h2>最终把关</h2>
        <span class="status-tag" :class="statusClass('conflict')">待决断</span>
      </div>

      <!-- 任务信息 -->
      <div class="task-info">
        <strong>{{ selected.flow_name || selected.task.flow_id }}</strong>
        <span>第 {{ selected.round_index }}/{{ selected.round_count }} 轮</span>
        <span v-if="selected.round_name">｜ {{ selected.round_name }}</span>
        <div class="vote-bar">
          <div class="vote-bar-fill" :style="{ width: votePercent + '%' }"></div>
        </div>
        <div class="vote-stats">
          <span class="vote ok">通过 {{ selected.approved }}</span>
          <span class="vote no">驳回 {{ selected.rejected }}</span>
          <span class="vote warn">需修改 {{ selected.revision }}</span>
          <span class="vote total">已投 {{ selected.voted }}/{{ selected.assigned }} 人</span>
        </div>
      </div>

      <!-- 题目完整信息 -->
      <div class="q-detail">
        <div class="q-params">
          <span v-if="selected.question.outline_code" class="q-param"><label>大纲代码</label>{{ selected.question.outline_code }}</span>
          <span v-if="selected.question.profession" class="q-param"><label>专业</label>{{ selected.question.profession }}</span>
          <span v-if="selected.question.system" class="q-param"><label>系统</label>{{ selected.question.system }}</span>
          <span class="q-param"><label>难度</label>{{ difficultyText(selected.question.difficulty) }}</span>
          <span v-if="selected.question.cognitive_level" class="q-param"><label>认知层次</label>{{ selected.question.cognitive_level }}</span>
          <span class="q-param"><label>版本</label>v{{ selected.question.version }}</span>
          <span class="q-param"><label>ID</label>{{ selected.question.id }}</span>
        </div>
        <div class="q-stem-full">
          <label>题干</label>
          <p>{{ selected.question.clinical_stem }}</p>
        </div>
        <div class="q-opts">
          <label>选项</label>
          <div v-for="opt in selected.question.options || []" :key="opt.label" class="q-opt" :class="{ correct: opt.label === selected.question.answer }">
            <span class="q-opt-label">{{ opt.label }}</span>
            <span>{{ opt.text }}</span>
            <span v-if="opt.label === selected.question.answer" class="q-correct">✓ 正确答案</span>
          </div>
        </div>
        <div class="q-sub">
          <label>解析</label>
          <span v-if="selected.question.explanation">{{ selected.question.explanation }}</span>
          <span v-else class="q-none">（无解析）</span>
        </div>
      </div>

      <!-- 审核记录（专家评语） -->
      <div class="records-block">
        <h3>本轮审核意见（{{ reviewRecords.length }} 条）</h3>
        <div v-for="rec in reviewRecords" :key="rec.id" class="record-item">
          <span class="rec-round">第{{ rec.round_number }}轮</span>
          <span class="rec-expert">{{ reviewerName(rec.expert_id) }}</span>
          <span class="rec-conclusion" :class="conclusionClass(rec.review_status)">{{ conclusionText(rec.review_status) }}</span>
          <span class="rec-opinion">{{ rec.opinion || "（无评语）" }}</span>
        </div>
        <div v-if="!reviewRecords.length" class="no-records">暂无审核记录</div>
      </div>

      <!-- 决断表单 -->
      <div class="finalize-form">
        <h3>最终决断</h3>
        <p class="finalize-hint">请综合各位审题人的意见做出最终决定：</p>
        <div class="review-form-row">
          <select v-model="finalizeAction">
            <option value="approved">通过（非最终轮进入下一轮，最终轮直接入库）</option>
            <option value="rejected">驳回（题目不可用）</option>
            <option value="revision_required">退回修改</option>
          </select>
          <input v-model="finalizeOpinion" placeholder="决断意见（建议填写理由）" />
          <button class="primary-button" type="button" :disabled="submitting" @click="doFinalize">
            {{ submitting ? "决断中..." : "提交决断" }}
          </button>
        </div>
      </div>
    </section>

    <section v-else class="panel empty-panel">
      <p>← 请从左侧选择一道待决断的题目</p>
    </section>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
</template>

<style scoped>
.decisions-layout {
  display: grid;
  grid-template-columns: 380px minmax(0, 1fr);
  gap: 16px;
}

.decision-list-panel {
  max-height: calc(100vh - 180px);
  overflow-y: auto;
}

.dot.purple {
  background: #b93a7c;
}

.decision-list {
  display: grid;
  gap: 8px;
}

.decision-card {
  border: 2px solid #f0c4dd;
  border-radius: 10px;
  padding: 10px 12px;
  background: #fff8fc;
  cursor: pointer;
  transition: all 0.15s;
}

.decision-card:hover {
  border-color: #b93a7c;
  box-shadow: 0 4px 12px rgba(185, 58, 124, 0.12);
}

.decision-card.active {
  border-color: #b93a7c;
  background: #fdf0f8;
}

.dc-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.dc-flow {
  font-size: 12px;
  font-weight: 700;
  color: #172033;
  background: #f0f3f7;
  padding: 2px 8px;
  border-radius: 4px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 40%;
}

.dc-round {
  font-size: 11px;
  font-weight: 700;
  color: #6e7b8f;
  background: #f0f3f7;
  padding: 2px 6px;
  border-radius: 4px;
  white-space: nowrap;
}

.dc-badge {
  font-size: 11px;
  font-weight: 700;
  color: #b93a7c;
  background: #fdf0f8;
  border: 1px solid #f0c4dd;
  padding: 2px 8px;
  border-radius: 4px;
  white-space: nowrap;
}

.dc-stem {
  font-size: 13px;
  font-weight: 600;
  color: #172033;
  line-height: 1.5;
  margin-bottom: 6px;
}

.dc-votes {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  font-size: 11px;
  margin-bottom: 6px;
}

.vote {
  padding: 1px 6px;
  border-radius: 3px;
  font-weight: 700;
}

.vote.ok { background: #f0fff8; color: #087c55; }
.vote.no { background: #fff0f0; color: #c54858; }
.vote.warn { background: #fdf2e3; color: #c07b22; }
.vote.total { background: #eff8ff; color: #0571dc; }

.dc-progress {
  height: 5px;
  background: #f0f3f7;
  border-radius: 3px;
  overflow: hidden;
}

.dc-progress-fill {
  height: 100%;
  background: #b93a7c;
  border-radius: 3px;
}

.dc-action {
  margin-top: 6px;
  text-align: right;
  font-size: 12px;
  font-weight: 700;
  color: #b93a7c;
}

.empty {
  text-align: center;
  color: #6e7b8f;
  padding: 40px;
  line-height: 2;
}

.empty-hint {
  font-size: 12px;
  color: #9aa5b4;
}

.loading {
  text-align: center;
  color: #6e7b8f;
  padding: 30px;
}

.empty-panel {
  display: grid;
  place-items: center;
  color: #6e7b8f;
}

/* 右侧 */
.status-tag {
  font-size: 12px;
  font-weight: 700;
  padding: 3px 10px;
  border-radius: 4px;
}

.status-conflict {
  background: #fdf0f8;
  color: #b93a7c;
}

.task-info {
  background: #fff8fc;
  border: 1px solid #f0c4dd;
  border-radius: 8px;
  padding: 10px 12px;
  margin-bottom: 12px;
  font-size: 13px;
}

.task-info strong {
  color: #172033;
  margin-right: 8px;
}

.task-info > span {
  color: #6e7b8f;
  font-size: 12px;
}

.vote-bar {
  height: 6px;
  background: #f0f3f7;
  border-radius: 3px;
  overflow: hidden;
  margin: 8px 0 6px;
}

.vote-bar-fill {
  height: 100%;
  background: #b93a7c;
  border-radius: 3px;
}

.vote-stats {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}

/* 题目详情 */
.q-detail {
  border: 1px solid #dce8f7;
  border-radius: 8px;
  padding: 12px;
  margin-bottom: 12px;
}

.q-params {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 16px;
  margin-bottom: 8px;
}

.q-param {
  font-size: 12px;
  color: #3a4658;
}

.q-param label {
  color: #6e7b8f;
  margin-right: 5px;
}

.q-sub {
  font-size: 13px;
  color: #3a4658;
}

.q-sub label {
  display: block;
  font-size: 11px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 2px;
}

.q-stem-full {
  margin-bottom: 8px;
}

.q-stem-full label {
  display: block;
  font-size: 11px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 2px;
}

.q-stem-full p {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
  color: #172033;
}

.q-opts {
  margin-bottom: 8px;
}

.q-opts label {
  display: block;
  font-size: 11px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 2px;
}

.q-opt {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 3px 0;
  font-size: 13px;
  color: #3a4658;
}

.q-opt.correct {
  color: #087c55;
  font-weight: 600;
}

.q-opt-label {
  width: 20px;
  height: 20px;
  display: grid;
  place-items: center;
  border-radius: 50%;
  background: #e8f0f8;
  color: #1385f8;
  font-size: 11px;
  font-weight: 700;
}

.q-correct {
  font-size: 11px;
  color: #087c55;
}

.q-none {
  color: #9aa5b4;
  font-size: 12px;
}

/* 审核记录 */
.records-block {
  margin-bottom: 12px;
}

.records-block h3 {
  font-size: 13px;
  color: #172033;
  margin: 0 0 8px;
}

.record-item {
  display: flex;
  gap: 10px;
  align-items: baseline;
  padding: 5px 0;
  font-size: 12px;
  border-bottom: 1px dashed #f0f3f7;
}

.rec-round {
  font-weight: 700;
  color: #172033;
  white-space: nowrap;
}

.rec-expert {
  color: #6e7b8f;
  white-space: nowrap;
}

.rec-conclusion {
  font-weight: 700;
  white-space: nowrap;
}

.c-approved { color: #087c55; }
.c-rejected { color: #c54858; }
.c-revision { color: #c07b22; }

.rec-opinion {
  flex: 1;
  color: #3a4658;
}

.no-records {
  font-size: 12px;
  color: #9aa5b4;
}

/* 决断表单 */
.finalize-form {
  padding: 14px;
  border: 1px solid #f0c4dd;
  border-radius: 8px;
  background: #fff8fc;
}

.finalize-form h3 {
  margin: 0 0 4px;
  font-size: 14px;
  color: #b93a7c;
}

.finalize-hint {
  margin: 0 0 10px;
  font-size: 12px;
  color: #6e7b8f;
}

.review-form-row {
  display: flex;
  gap: 8px;
}

.review-form-row select {
  width: 260px;
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 8px;
  font-size: 13px;
}

.review-form-row input {
  flex: 1;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 10px;
  font-size: 13px;
}

.primary-button {
  height: 36px;
  border: 0;
  border-radius: 7px;
  background: #b93a7c;
  color: #fff;
  font-size: 14px;
  font-weight: 600;
  cursor: pointer;
  padding: 0 18px;
  white-space: nowrap;
}

.primary-button:hover {
  background: #a32e6c;
}

.primary-button:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
</style>
