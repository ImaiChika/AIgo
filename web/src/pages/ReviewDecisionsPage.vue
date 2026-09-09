<script setup>
import { ref, onMounted, computed } from "vue";
import { api } from "../api.js";
import StructuredCommentInput from "../components/StructuredCommentInput.vue";
import ReviewCommentCard from "../components/ReviewCommentCard.vue";

const toast = ref("");
const decisions = ref([]);
const loading = ref(false);
const selected = ref(null); // 选中的 MyTaskItem
const reviewRecords = ref([]);
const finalizeAction = ref("approved");
const finalizeComment = ref({ stem: "", options: "", answer: "", other: "" });
const submitting = ref(false);
const reviewers = ref([]); // 名字映射
const activeRoundTab = ref(1); // 当前对比的轮次；0 = 时间线视图

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
    ai_draft: "草稿", auto_checked: "已初评", ai_reviewed: "已检查",
    reviewing: "审核中", conflict: "待决断", revision_required: "需修改",
    rejected: "已驳回", published: "已通过",
  };
  return map[status] || status;
}

function statusClass(status) {
  if (status === "published" || status === "ai_reviewed") return "status-good";
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

// 选择待决断任务：加载题目详情 + 全部审核记录（把关人全量可见）
async function selectDecision(item) {
  selected.value = item;
  reviewRecords.value = [];
  try {
    const recData = await api.reviewRecords(item.task.id);
    reviewRecords.value = recData.records || [];
    // 默认定位到最新一轮的对比
    const rounds = [...new Set((recData.records || []).map((r) => r.round_number))];
    activeRoundTab.value = rounds.length ? Math.max(...rounds) : 1;
  } catch (e) {
    console.error(e);
  }
}

// 当前批次的评语（重提后历史批次仅归档于「审核记录」页，对比视图只看本批次）
const currentAttemptRecords = computed(() => {
  const attempt = selected.value?.task?.attempt || 1;
  return reviewRecords.value.filter((r) => (r.attempt || 1) === attempt);
});

// 轮次页签（含决断记录所在轮次）
const roundTabs = computed(() => {
  const rounds = [...new Set(currentAttemptRecords.value.map((r) => r.round_number))].sort((a, b) => a - b);
  return rounds.map((n) => ({
    round: n,
    count: currentAttemptRecords.value.filter((r) => r.round_number === n).length,
  }));
});

function recordsOfRound(n) {
  return currentAttemptRecords.value
    .filter((r) => r.round_number === n)
    .sort((a, b) => new Date(a.created_at) - new Date(b.created_at));
}

const activeRoundRecords = computed(() => recordsOfRound(activeRoundTab.value));

// 时间线视图：按轮次分组
const roundGroups = computed(() => roundTabs.value.map((t) => ({ round: t.round, records: recordsOfRound(t.round) })));

// 本轮态度汇总（把关人全量可见）
function roundStats(n) {
  const recs = recordsOfRound(n);
  return {
    approved: recs.filter((r) => r.review_status === "approved").length,
    rejected: recs.filter((r) => r.review_status === "rejected").length,
    revision: recs.filter((r) => r.review_status === "revision_required").length,
    total: recs.length,
  };
}

// 提交决断
async function doFinalize() {
  if (!selected.value) return;
  const c = finalizeComment.value;
  if (finalizeAction.value !== "approved" && ![c.stem, c.options, c.answer, c.other].some((v) => (v || "").trim())) {
    showToast("退回修改或驳回时，请至少填写一栏决断理由");
    return;
  }
  submitting.value = true;
  try {
    await api.finalizeReview({
      task_id: selected.value.task.id,
      action: finalizeAction.value,
      comment: { ...finalizeComment.value },
    });
    showToast("决断完成");
    finalizeComment.value = { stem: "", options: "", answer: "", other: "" };
    await loadDecisions();
    if (!selected.value) {
      // 已决断完最后一项，清空右侧
      reviewRecords.value = [];
    }
  } catch (e) {
    const msg = e.message || "";
    // 后端幂等拦截的重复决断给出友好提示（任务状态已流转即视为决断已生效）
    if (msg.includes("只有票数冲突的任务需要最终把关")) {
      showToast("该任务已完成决断，无需重复提交");
    } else if (msg.includes("版本") && msg.includes("请撤销或退回后重新提交")) {
      showToast("题目在决断期间被修改，请刷新后按最新版本处理");
    } else {
      showToast("决断失败: " + msg);
    }
  } finally {
    submitting.value = false;
  }
}

// 投票进度
const votePercent = computed(() => {
  const item = selected.value;
  if (!item) return 0;
  return Math.min(100, Math.round((item.voted / Math.max(item.assigned, 1)) * 100));
});

// AI 检查报告（决断者全量可见，同时展示全部轮次专家评语）
const selectedAIReview = computed(() => selected.value?.ai_review || null);
const selectedAIStale = computed(() => (
  !!selectedAIReview.value && !!selected.value && selected.value.question.version > (selectedAIReview.value.question_version || 0)
));
const selectedAIAvg = computed(() => {
  const s = selectedAIReview.value?.scores;
  if (!s) return null;
  const values = [s.scientific, s.logic, s.a2_fit, s.answer].filter((v) => typeof v === "number");
  if (!values.length) return null;
  return Math.round(values.reduce((a, b) => a + b, 0) / values.length);
});
const selectedAIVerdict = computed(() => {
  const map = { pass: "AI 检查通过", issues_found: "AI 发现问题", reject: "AI 建议驳回" };
  return map[selectedAIReview.value?.verdict] || "";
});
const selectedAIScores = computed(() => {
  const s = selectedAIReview.value?.scores;
  if (!s) return [];
  return [
    { label: "科学性", value: s.scientific },
    { label: "逻辑性", value: s.logic },
    { label: "A2 格式", value: s.a2_fit },
    { label: "答案准确", value: s.answer },
  ].filter((x) => typeof x.value === "number");
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
        <span class="dot blue"></span>
        <h2>待我决断</h2>
        <small>{{ decisions.length }} 项</small>
      </div>
      <div v-if="loading" class="loading">加载中...</div>
      <div v-else-if="!decisions.length" class="empty">
        暂无待决断的任务<br />
        <span class="empty-hint">暂无待决断任务</span>
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
        <span class="dot blue"></span>
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
          <span class="q-param"><label>审核绑定</label>v{{ selected.task.question_version }}</span>
          <span v-if="selected.task.submission_bank_id" class="q-param"><label>提交分类</label>{{ selected.task.submission_bank_id }}</span>
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

      <p v-if="selected.version_mismatch" class="version-mismatch-warning">
        当前题目版本与审核任务不一致，不能决断；请撤销或退回后重新提交审核。
      </p>

      <!-- AI 检查报告（决断者全量可见） -->
      <div v-if="selectedAIReview" class="ai-report">
        <div class="ai-report-head">
          <h3>AI 检查报告</h3>
          <span class="ai-verdict" :class="selectedAIReview.verdict === 'pass' ? 'good' : selectedAIReview.verdict === 'reject' ? 'bad' : 'warn'">
            {{ selectedAIVerdict }}<template v-if="selectedAIAvg !== null">（综合 {{ selectedAIAvg }} 分）</template>
          </span>
          <span v-if="selectedAIStale" class="ai-stale">题目已修改，结果待复检</span>
        </div>
        <div v-if="selectedAIScores.length" class="ai-score-row">
          <span v-for="s in selectedAIScores" :key="s.label" class="ai-score">{{ s.label }} <b :class="s.value >= 70 ? 'good' : s.value >= 60 ? 'warn' : 'bad'">{{ s.value }}</b></span>
        </div>
        <ul v-if="(selectedAIReview.issues || []).length" class="ai-issues">
          <li v-for="(issue, i) in selectedAIReview.issues" :key="i">
            <b :class="issue.severity">{{ issue.severity === "error" ? "错误" : issue.severity === "warning" ? "警告" : "提示" }}</b>
            {{ issue.message }}
          </li>
        </ul>
        <p v-if="selectedAIReview.suggestion" class="ai-suggestion">AI 建议：{{ selectedAIReview.suggestion }}</p>
        <p class="ai-hint">检查时间：{{ selectedAIReview.created_at ? new Date(selectedAIReview.created_at).toLocaleString() : "-" }}<span v-if="selectedAIReview.model"> · 模型：{{ selectedAIReview.model }}</span></p>
      </div>

      <!-- 专家评语对比（把关人全量可见，按轮次切换对比） -->
      <div class="records-block">
        <div class="records-head">
          <h3>专家评语对比</h3>
          <div class="round-tabs" role="tablist">
            <button
              v-for="t in roundTabs"
              :key="t.round"
              type="button"
              role="tab"
              class="round-tab"
              :class="{ active: activeRoundTab === t.round }"
              @click="activeRoundTab = t.round"
            >
              第 {{ t.round }} 轮（{{ t.count }}）
            </button>
            <button
              type="button"
              role="tab"
              class="round-tab"
              :class="{ active: activeRoundTab === 0 }"
              @click="activeRoundTab = 0"
            >
              时间线
            </button>
          </div>
        </div>

        <!-- 单轮对比：各专家评语并排 -->
        <template v-if="activeRoundTab !== 0">
          <div v-if="activeRoundTab && activeRoundRecords.length" class="round-stats-line">
            第 {{ activeRoundTab }} 轮 · {{ activeRoundRecords.length }} 位老师：
            <span class="vote ok">通过 {{ roundStats(activeRoundTab).approved }}</span>
            <span class="vote no">驳回 {{ roundStats(activeRoundTab).rejected }}</span>
            <span class="vote warn">需修改 {{ roundStats(activeRoundTab).revision }}</span>
          </div>
          <div v-if="activeRoundRecords.length" class="compare-grid">
            <ReviewCommentCard
              v-for="rec in activeRoundRecords"
              :key="rec.id"
              :record="rec"
              :fallback-name="reviewerName(rec.expert_id)"
            />
          </div>
          <div v-else class="no-records">该轮暂无评语</div>
        </template>

        <!-- 时间线视图：按轮次顺序展示全部评语 -->
        <template v-else>
          <div v-for="g in roundGroups" :key="g.round" class="tl-group">
            <div class="tl-title">第 {{ g.round }} 轮 · {{ g.records.length }} 条</div>
            <div class="compare-grid">
              <ReviewCommentCard
                v-for="rec in g.records"
                :key="rec.id"
                :record="rec"
                :fallback-name="reviewerName(rec.expert_id)"
              />
            </div>
          </div>
          <div v-if="!roundGroups.length" class="no-records">暂无审核记录</div>
        </template>
      </div>

      <!-- 决断表单 -->
      <div class="finalize-form">
        <h3>最终决断</h3>
        <p class="finalize-hint">通过时评语可不填；退回修改或驳回时请至少填写一栏理由。</p>
        <div class="finalize-row">
          <select v-model="finalizeAction">
            <option value="approved">通过（非最终轮进入下一轮，最终轮完成审核）</option>
            <option value="rejected">驳回（题目不可用）</option>
            <option value="revision_required">退回修改</option>
          </select>
          <button class="primary-button" type="button" :disabled="submitting || selected.version_mismatch" @click="doFinalize">
            {{ submitting ? "决断中..." : "提交决断" }}
          </button>
        </div>
        <StructuredCommentInput v-model:comment="finalizeComment" class="finalize-comment" />
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

.decision-list {
  display: grid;
  gap: 8px;
}

.decision-card {
  border: 1px solid var(--line);
  border-radius: 10px;
  padding: 10px 12px;
  background: #fff;
  cursor: pointer;
  transition: all 0.15s;
}

.decision-card:hover {
  border-color: var(--blue);
  box-shadow: 0 4px 12px rgba(19, 133, 248, 0.1);
}

.decision-card.active {
  border-color: var(--blue);
  background: var(--surface-muted);
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
  color: #3a4658;
  background: #eef2f7;
  border: 1px solid #e0e7ef;
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
  background: var(--blue);
  border-radius: 3px;
}

.dc-action {
  margin-top: 6px;
  text-align: right;
  font-size: 12px;
  font-weight: 700;
  color: var(--blue);
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
  background: #eef2f7;
  color: #3a4658;
}

.task-info {
  background: var(--surface-muted);
  border: 1px solid var(--line);
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
  background: var(--blue);
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

.version-mismatch-warning {
  margin: 12px 0;
  padding: 10px 12px;
  border: 1px solid #f2c7c7;
  border-left: 3px solid #c54858;
  border-radius: 7px;
  background: #fff5f5;
  color: #a83242;
  font-size: 12px;
  line-height: 1.55;
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

/* 专家评语对比 */
.records-block {
  margin-bottom: 12px;
}

/* AI 检查报告 */
.ai-report {
  border: 1px solid #dce8f7;
  background: #f8fbff;
  border-radius: 8px;
  padding: 10px 14px;
  margin-bottom: 12px;
}

.ai-report-head {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 6px;
}

.ai-report-head h3 {
  font-size: 13px;
  color: #172033;
  margin: 0;
}

.ai-verdict {
  font-size: 12px;
  font-weight: 700;
  padding: 2px 8px;
  border-radius: 4px;
}

.ai-verdict.good { background: #f0fff8; color: #087c55; }
.ai-verdict.warn { background: #fdf2e3; color: #c07b22; }
.ai-verdict.bad { background: #fff0f0; color: #c54858; }

.ai-stale {
  font-size: 11px;
  color: #c07b22;
}

.ai-score-row {
  display: flex;
  gap: 14px;
  flex-wrap: wrap;
  margin-bottom: 6px;
}

.ai-score {
  font-size: 12px;
  color: #6e7b8f;
}

.ai-score b.good { color: #087c55; }
.ai-score b.warn { color: #c07b22; }
.ai-score b.bad { color: #c54858; }

.ai-issues {
  margin: 0 0 6px;
  padding-left: 18px;
  font-size: 12px;
  color: #3a4658;
}

.ai-issues b { margin-right: 4px; }
.ai-issues b.error { color: #c54858; }
.ai-issues b.warning { color: #c07b22; }
.ai-issues b.info { color: #0571dc; }

.ai-suggestion {
  margin: 0 0 6px;
  font-size: 12px;
  color: #3a4658;
}

.ai-hint {
  margin: 0;
  font-size: 11px;
  color: #9aa5b4;
}

.records-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}

.records-head h3 {
  font-size: 13px;
  color: #172033;
  margin: 0;
}

.round-tabs {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}

.round-tab {
  height: 28px;
  padding: 0 12px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #6e7b8f;
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
}

.round-tab:hover {
  border-color: var(--blue);
  color: var(--blue);
}

.round-tab.active {
  background: var(--blue);
  border-color: var(--blue);
  color: #fff;
}

.round-stats-line {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: #6e7b8f;
  margin-bottom: 8px;
}

.round-stats-line .vote { font-size: 11px; }

/* 比价式并排对比框 */
.compare-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(270px, 1fr));
  gap: 10px;
  align-items: start;
}

.tl-group {
  margin-bottom: 12px;
}

.tl-title {
  font-size: 12px;
  font-weight: 700;
  color: var(--blue);
  margin-bottom: 6px;
}

.no-records {
  font-size: 12px;
  color: #9aa5b4;
}

/* 决断表单 */
.finalize-form {
  padding: 14px;
  border: 1px solid var(--line);
  border-radius: 8px;
  background: #fff;
}

.finalize-form h3 {
  margin: 0 0 4px;
  font-size: 14px;
  color: var(--text);
}

.finalize-hint {
  margin: 0 0 10px;
  font-size: 12px;
  color: #6e7b8f;
}

.finalize-row {
  display: flex;
  gap: 8px;
  margin-bottom: 10px;
}

.finalize-row select {
  flex: 1;
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 8px;
  font-size: 13px;
}

.finalize-comment {
  max-width: 720px;
}

.finalize-form .primary-button {
  min-height: 36px;
}

.finalize-form .primary-button:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
</style>
