<script setup>
import { ref, onMounted, computed } from "vue";
import { api } from "../api.js";
import { currentUser, hasPerm } from "../auth.js";
import QuestionDetailModal from "../components/QuestionDetailModal.vue";
import StructuredCommentInput from "../components/StructuredCommentInput.vue";
import ReviewCommentCard from "../components/ReviewCommentCard.vue";
import CompactPager from "../components/CompactPager.vue";

const toast = ref("");
const selectedQuestion = ref(null);
const detailQuestion = ref(null); // 题目详情弹窗数据
const reviewTask = ref(null);
const reviewRecords = ref([]);
const reviewAction = ref("approved");
const reviewComment = ref({ stem: "", options: "", answer: "", other: "" });
const loading = ref(false);
const mySubmitted = ref(false); // 本轮已提交标记（任务快照按隔离规则不含票数，防重复提交用）
const experts = ref([]);

// 待我审核（本页唯一视图：审题人投票工作区）
const myTasks = ref([]);
const myTasksLoading = ref(false);
const taskPage = ref(1);
const taskPageSize = 20;
const taskTotal = ref(0);
const taskPageCount = computed(() => Math.max(1, Math.ceil(taskTotal.value / taskPageSize)));
const currentFlowName = computed(() => reviewTask.value?._flow_name || reviewTask.value?.flow_id || "");
const currentFlowRounds = computed(() => reviewTask.value?._round_count || reviewTask.value?.round_results?.length || 0);
const currentRoundName = computed(() => reviewTask.value?._round_name || "");

// 历史轮次脱敏摘要（服务端隔离：审核人只能看到之前轮次的票数统计，看不到评语）
const prevRoundSummaries = computed(() => {
  const task = reviewTask.value;
  if (!task || !task.round_results) return [];
  return task.round_results
    .filter((rr) => rr.round_number < task.current_round)
    .map((rr) => ({
      round: rr.round_number,
      approved: rr.approved_count || 0,
      rejected: rr.rejected_count || 0,
      revision: rr.revision_count || 0,
      passed: !!rr.passed,
    }));
});

function commentHasContent() {
  const c = reviewComment.value;
  return [c.stem, c.options, c.answer, c.other].some((v) => (v || "").trim());
}

const taskVersionMismatch = computed(() => (
  !!reviewTask.value && !!selectedQuestion.value && reviewTask.value.question_version !== selectedQuestion.value.version
));

// AI 检查参考：随任务详情返回的最近一次质量检查结果（供专家参考，不做决定依据）
const aiReview = computed(() => reviewTask.value?.ai_review || null);
const aiReviewStale = computed(() => (
  !!aiReview.value && !!selectedQuestion.value && selectedQuestion.value.version > (aiReview.value.question_version || 0)
));
// 默认折叠为一行轻量标识，避免 AI 评语先入为主；审核人可主动展开
const aiReviewExpanded = ref(false);
const aiReviewAvg = computed(() => {
  const s = aiReview.value?.scores;
  if (!s) return null;
  const values = [s.scientific, s.logic, s.a2_fit, s.answer].filter((v) => typeof v === "number");
  if (!values.length) return null;
  return Math.round(values.reduce((a, b) => a + b, 0) / values.length);
});
const aiReviewVerdict = computed(() => {
  const map = { pass: "AI 预审：已通过", issues_found: "AI 预审：发现问题", reject: "AI 预审：建议驳回" };
  return map[aiReview.value?.verdict] || "";
});
const aiReviewScores = computed(() => {
  const s = aiReview.value?.scores;
  if (!s) return [];
  return [
    { label: "科学性", value: s.scientific },
    { label: "逻辑性", value: s.logic },
    { label: "A2 格式", value: s.a2_fit },
    { label: "答案准确", value: s.answer },
  ].filter((x) => typeof x.value === "number");
});

// 详情弹窗关闭后刷新当前题目
async function refreshSelectedQuestion() {
  if (!selectedQuestion.value) return;
  try {
    const q = await api.getQuestion(selectedQuestion.value.id);
    if (q) selectedQuestion.value = q;
  } catch (e) { /* 保持原数据 */ }
}

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

// 待我审核列表
async function loadMyTasks() {
  myTasksLoading.value = true;
  try {
    let data = await api.myTasks(taskPage.value, taskPageSize);
    taskTotal.value = data.total || 0;
    const validPage = Math.min(taskPage.value, taskPageCount.value);
    if (validPage !== taskPage.value) {
      taskPage.value = validPage;
      data = await api.myTasks(taskPage.value, taskPageSize);
      taskTotal.value = data.total || 0;
    }
    myTasks.value = data.tasks || [];
    // 若当前选中项已不在列表中（投完/进入下一轮），保留右侧供查看
  } catch (e) {
    showToast("加载待审任务失败: " + e.message);
  } finally {
    myTasksLoading.value = false;
  }
}

function goTaskPage(page) {
  if (myTasksLoading.value || page < 1 || page > taskPageCount.value || page === taskPage.value) return;
  taskPage.value = page;
  selectedQuestion.value = null;
  reviewTask.value = null;
  reviewRecords.value = [];
  loadMyTasks();
  document.querySelector(".my-task-list")?.scrollTo({ top: 0 });
}

// 从待审任务卡片进入审核
async function selectMyTask(item) {
  selectedQuestion.value = item.question;
  reviewTask.value = item.task ? {
    ...item.task,
    ai_review: item.ai_review || null,
    _flow_name: item.flow_name || "",
    _round_count: item.round_count || 0,
    _round_name: item.round_name || "",
  } : null;
  reviewRecords.value = [];
  reviewOpinionReset();
  mySubmitted.value = false;

  if (reviewTask.value?.id) {
    const recData = await api.reviewRecords(reviewTask.value.id).catch(() => ({ records: [] }));
    reviewRecords.value = recData.records || [];
  }

}

function reviewOpinionReset() {
  reviewComment.value = { stem: "", options: "", answer: "", other: "" };
}

async function doReview() {
  if (!reviewTask.value) return;
  // 与后端规则一致：通过时评语可选，驳回/需修改必须至少填写一栏
  if (reviewAction.value !== "approved" && !commentHasContent()) {
    showToast("驳回或需修改时，请在题干/选项/答案与解析/其他至少一栏填写评语");
    return;
  }
  loading.value = true;
  try {
    await api.reviewAction({
      task_id: reviewTask.value.id,
      expert_id: currentUser.value?.id || "admin",
      action: reviewAction.value,
      comment: { ...reviewComment.value },
    });
    showToast("审核已提交");
    mySubmitted.value = true;
    reviewOpinionReset();
    await loadMyTasks();
    // 投票后任务可能立即进入下一轮并改派他人；清空当前工作区，避免再请求已无权访问的任务。
    selectedQuestion.value = null;
    reviewTask.value = null;
    reviewRecords.value = [];
  } catch (e) {
    const msg = e.message || "";
    // 后端幂等拦截的重复操作给出友好提示，不当作异常错误
    if (msg.includes("已经审核过")) {
      showToast("您已审核过本轮，无需重复提交");
    } else if (msg.includes("审核任务已结束") || msg.includes("不能再投票")) {
      showToast("该审核任务已结束或票数已定，无需重复提交");
    } else {
      showToast("审核失败: " + msg);
    }
  } finally {
    loading.value = false;
  }
}

function statusText(status) {
  const map = {
    ai_draft: "草稿",
    auto_checked: "已初评",
    ai_reviewed: "已检查",
    reviewing: "审核中",
    conflict: "待决断",
    rejected: "已驳回",
    revision_required: "需修改",
    published: "已通过",
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

// 判断是否可以执行审核（任务进行中 且 当前用户被分配到当前轮 且 本页会话未提交过）
function canReview(task) {
  if (!task || task.status !== "reviewing") return false;
  if (mySubmitted.value) return false;
  if (selectedQuestion.value && task.question_version !== selectedQuestion.value.version) return false;
  const user = currentUser.value;
  if (!user) return false;
  const assigned = (task.assigned_to || []).includes(user.id);
  const alreadyReviewed = (task.round_results || []).some(
    (rr) => rr.round_number === task.current_round && (rr.reviews || []).some((r) => r.expert_id === user.id)
  );
  return assigned && !alreadyReviewed;
}

async function loadExperts() {
  try {
    const data = await api.listReviewers();
    experts.value = data.reviewers || [];
  } catch (e) {
    console.error(e);
  }
}

function expertName(id) {
  const e = experts.value.find((x) => x.id === id);
  return e ? e.display_name || e.username : id;
}

onMounted(() => {
  loadMyTasks();
  loadExperts();
});
</script>

<template>
  <div class="review-layout">
    <!-- 左侧待我审核列表 -->
    <section class="panel question-list-panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>待我审核</h2>
        <small>{{ taskTotal }} 项</small>
      </div>

      <div v-if="myTasksLoading" class="loading">加载中...</div>
      <div v-else-if="!myTasks.length" class="empty">
        暂无待你审核的任务<br />
        <span class="empty-hint">暂无待审任务</span>
      </div>
      <div v-else class="my-task-list">
        <div
          v-for="item in myTasks"
          :key="item.task.id"
          class="my-task-card"
          :class="{ active: selectedQuestion?.id === item.question.id }"
          role="button"
          tabindex="0"
          @click="selectMyTask(item)"
          @keydown.enter="selectMyTask(item)"
        >
          <div class="mt-head">
            <span class="mt-flow">{{ item.flow_name || item.task.flow_id }}</span>
            <span class="mt-round">第 {{ item.round_index }}/{{ item.round_count }} 轮</span>
            <span class="q-status" :class="statusClass(item.task.status)">
              {{ statusText(item.task.status) }}
            </span>
          </div>
          <div class="mt-stem">{{ (item.question.clinical_stem || "").slice(0, 60) }}{{ (item.question.clinical_stem || "").length > 60 ? "..." : "" }}</div>
          <div v-if="item.round_name" class="mt-meta">
            <span>{{ item.round_name }}</span>
          </div>
        </div>
      </div>
      <CompactPager
        v-if="taskTotal"
        :page="taskPage"
        :total-pages="taskPageCount"
        :disabled="myTasksLoading"
        @change="goTaskPage"
      />
    </section>

    <!-- 右侧审核工作区 -->
    <section class="panel" v-if="selectedQuestion">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>审核题目</h2>
        <span class="q-status" :class="statusClass(selectedQuestion.status)">
          {{ statusText(selectedQuestion.status) }}
        </span>
        <button class="ghost-button" type="button" @click="detailQuestion = selectedQuestion">查看题目属性</button>
      </div>

      <div class="detail-section">
        <h3>题干</h3>
        <p>{{ selectedQuestion.clinical_stem }}</p>
      </div>

      <div class="detail-section">
        <h3>选项</h3>
        <div v-for="(opt, i) in (selectedQuestion.options || [])" :key="i" class="option-item">
          <span class="opt-label">{{ String.fromCharCode(65 + i) }}</span>
          <span>{{ opt.text }}</span>
          <span v-if="opt.label === selectedQuestion.answer" class="correct">✓ 正确答案</span>
        </div>
      </div>

      <div class="detail-section">
        <h3>解析</h3>
        <p v-if="selectedQuestion.explanation && selectedQuestion.explanation.trim().length >= 10">{{ selectedQuestion.explanation }}</p>
        <p v-else-if="selectedQuestion.explanation" class="warn-text">解析内容较短（{{ selectedQuestion.explanation.trim().length }} 字），请重点核查</p>
        <p v-else class="warn-text">此题未提供解析，请结合题干和选项审核</p>
      </div>

      <!-- AI 检查参考：默认折叠为轻量标识，展开后可见完整报告 -->
      <div v-if="aiReview" class="detail-section ai-review-box">
        <button type="button" class="ai-toggle" @click="aiReviewExpanded = !aiReviewExpanded">
          <span class="ai-verdict" :class="aiReview.verdict === 'pass' ? 'good' : aiReview.verdict === 'reject' ? 'bad' : 'warn'">{{ aiReviewVerdict }}<template v-if="aiReviewAvg !== null">（综合 {{ aiReviewAvg }} 分）</template></span>
	        <span v-if="aiReviewStale" class="ai-stale">首次生成检查结果；人工修改后不复检</span>
          <span class="ai-caret">{{ aiReviewExpanded ? "收起 ▲" : "展开 ▼" }}</span>
        </button>
        <div v-show="aiReviewExpanded" class="ai-detail">
          <div v-if="aiReviewScores.length" class="ai-score-row">
            <span v-for="s in aiReviewScores" :key="s.label" class="ai-score">{{ s.label }} <b :class="s.value >= 70 ? 'good' : s.value >= 60 ? 'warn' : 'bad'">{{ s.value }}</b></span>
          </div>
          <ul v-if="(aiReview.issues || []).length" class="ai-issues">
            <li v-for="(issue, i) in aiReview.issues" :key="i">
              <b :class="issue.severity">{{ issue.severity === "error" ? "错误" : issue.severity === "warning" ? "警告" : "提示" }}</b>
              {{ issue.message }}
            </li>
          </ul>
          <p v-if="aiReview.suggestion" class="ai-suggestion">AI 建议：{{ aiReview.suggestion }}</p>
          <p class="ai-hint">以上为 AI 自动检查结果，仅供审核参考，请以独立专业判断为准。</p>
        </div>
      </div>

      <!-- 审核操作区 -->
      <div class="review-actions">
        <!-- 审核任务信息 -->
        <div v-if="reviewTask" class="task-info">
          <div class="task-info-kicker">审核任务</div>
          <div class="task-flow-row">
            <span class="task-flow-name">{{ currentFlowName }}</span>
            <span class="task-round">第 {{ reviewTask.current_round }}/{{ currentFlowRounds || 1 }} 轮</span>
            <span v-if="currentRoundName" class="task-round-name">
              {{ currentRoundName }}
            </span>
          </div>
          <p v-if="taskVersionMismatch" class="version-mismatch-warning">
            当前题目已是 v{{ selectedQuestion.version }}，本任务绑定 v{{ reviewTask.question_version }}，请由管理员处理当前审核任务后再重新提交。
          </p>
          <details class="task-extra">
            <summary>查看任务信息</summary>
            <div class="task-extra-grid">
              <span>审核版本 v{{ reviewTask.question_version }}</span>
              <span>审核人：{{ (reviewTask.assigned_to || []).map(id => expertName(id)).join("、") || "自动匹配" }}</span>
            </div>
          </details>
          <!-- 历史轮次脱敏摘要：按隔离规则，只展示之前轮次的票数统计，不展示评语 -->
          <details v-if="prevRoundSummaries.length" class="task-extra">
            <summary>查看前轮概览</summary>
            <div class="prev-rounds">
              <div v-for="s in prevRoundSummaries" :key="s.round" class="prev-round">
                <span class="pr-tag">第 {{ s.round }} 轮</span>
                <span class="pr-vote ok">通过 {{ s.approved }}</span>
                <span class="pr-vote no">驳回 {{ s.rejected }}</span>
                <span class="pr-vote warn">需修改 {{ s.revision }}</span>
                <span class="pr-result" :class="s.passed ? 'passed' : 'not-passed'">{{ s.passed ? "已过轮" : "未过轮" }}</span>
              </div>
            </div>
          </details>
          <p class="isolation-hint">独立审核：本轮意见在你提交前对其他审核人不可见，提交后由系统统一汇总。</p>
        </div>

        <!-- 审核表单：结构化评语 -->
        <div v-if="reviewTask && canReview(reviewTask)" class="review-form">
          <div class="review-form-heading">
            <h3>提交本轮审核</h3>
            <span>通过可不填评语；其他结论需填写理由</span>
          </div>
          <div class="review-form-top">
            <span class="form-label">本轮结论</span>
            <select v-model="reviewAction">
              <option value="approved">通过</option>
              <option value="rejected">驳回</option>
              <option value="revision_required">需修改</option>
            </select>
            <span class="form-rule">{{ reviewAction === "approved" ? "通过时评语可不填" : "驳回/需修改时必须填写至少一栏评语" }}</span>
          </div>
          <StructuredCommentInput v-model:comment="reviewComment" class="sc-input-wrap" />
          <div class="form-submit-row">
            <button class="primary-button" type="button" :disabled="loading" @click="doReview">
              {{ loading ? "提交中..." : "提交审核" }}
            </button>
          </div>
        </div>

        <!-- 我已投票（投完即从列表移开） -->
        <div v-else-if="reviewTask && reviewTask.status === 'reviewing'" class="task-done">
          <p>你已完成本轮的审核，该题已从「待我审核」移开（进入下一轮后会再次出现）</p>
        </div>

        <!-- 已进入最终待决断 -->
        <div v-else-if="reviewTask && reviewTask.status === 'conflict'" class="task-done wait-final">
          <p>所有轮次审核完成，已进入最终待决断（由把关人在「待决断」页面处理）</p>
        </div>

        <!-- 已驳回/需修改 -->
        <div v-else-if="reviewTask" class="task-done">
          <p>审核已结束，最终状态：{{ statusText(reviewTask.status) }}（由管理员在题库页处理）</p>
        </div>

        <!-- 我的审核记录（隔离规则下仅显示自己提交的评语） -->
        <div v-if="reviewRecords.length" class="review-records">
          <h3>我的审核记录</h3>
          <div class="own-record-grid">
            <ReviewCommentCard v-for="rec in reviewRecords" :key="rec.id" :record="rec" :fallback-name="expertName(rec.expert_id)" />
          </div>
        </div>
      </div>
    </section>

    <section v-else class="panel empty-panel">
      <p>← 请从左侧选择一道待审核的题目</p>
    </section>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>

  <!-- 题目详情弹窗 -->
  <QuestionDetailModal v-if="detailQuestion" :question="detailQuestion" @close="detailQuestion = null" @refresh="refreshSelectedQuestion" />
</template>


<style scoped>
.review-layout {
  display: grid;
  grid-template-columns: 304px minmax(0, 1fr);
  gap: 14px;
  align-items: start;
}

.question-list-panel {
  display: flex;
  flex-direction: column;
  max-height: calc(100vh - 154px);
  overflow: hidden;
}

.question-list-panel > .section-heading {
  flex-shrink: 0;
}

.question-list {
  display: grid;
  gap: 6px;
}

.question-item {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
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

.q-stem {
  flex: 1;
  font-size: 13px;
  color: #172033;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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
.status-warn { background: #fdf2e3; color: #c07b22; }
.status-conflict { background: #eef2f7; color: #3a4658; }

/* 视图切换 */
.view-toggle {
  display: flex;
  gap: 6px;
  margin-bottom: 10px;
}

.view-toggle button {
  flex: 1;
  height: 34px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  background: #fff;
  color: #6e7b8f;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
}

.view-toggle button.active {
  background: #1385f8;
  border-color: #1385f8;
  color: #fff;
}

.toggle-badge {
  display: inline-grid;
  place-items: center;
  min-width: 18px;
  height: 18px;
  padding: 0 5px;
  border-radius: 9px;
  background: #ff5252;
  color: #fff;
  font-size: 11px;
}

/* 待我审核卡片 */
.my-task-list {
  display: grid;
  gap: 8px;
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding-right: 2px;
}

.my-task-card {
  border: 1px solid #dce8f7;
  border-radius: 8px;
  padding: 10px 11px;
  background: #fff;
  cursor: pointer;
  transition: border-color 0.15s, background 0.15s;
  position: relative;
}

.my-task-card:hover {
  border-color: #1385f8;
  background: #fbfdff;
}

.my-task-card.active {
  border-color: #1385f8;
  background: #eff8ff;
}

.my-task-card.conflict {
  border-color: #d8e0ea;
}

.my-task-card.conflict:hover {
  border-color: var(--blue);
}

.mt-head {
  display: flex;
  align-items: center;
  gap: 7px;
  margin-bottom: 6px;
}

.mt-flow {
  font-size: 12px;
  font-weight: 700;
  color: #172033;
  padding: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 40%;
}

.mt-round {
  font-size: 11px;
  font-weight: 700;
  color: #58738d;
  padding: 0;
  white-space: nowrap;
}

.mt-stem {
  font-size: 13px;
  color: #172033;
  line-height: 1.5;
  margin-bottom: 6px;
  font-weight: 600;
}

.mt-meta {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  font-size: 11px;
  color: #6e7b8f;
  align-items: center;
  margin-bottom: 0;
}

.mt-action {
  display: none;
}

/* 任务信息：流程/轮次/进度条 */
.task-flow-row {
  display: flex;
  gap: 10px;
  align-items: center;
  margin-bottom: 6px;
  flex-wrap: wrap;
}

.task-flow-name {
  font-size: 13px;
  font-weight: 700;
  color: #172033;
}

.task-round {
  font-size: 12px;
  font-weight: 700;
  color: #28658f;
  background: #f5f9fc;
  border: 1px solid #dce8f1;
  padding: 2px 8px;
  border-radius: 5px;
}

.task-round-name {
  font-size: 12px;
  color: #6e7b8f;
}

.task-version {
  padding: 2px 7px;
  border-radius: 999px;
  background: #edf6ff;
  color: #0571dc;
  font-size: 11px;
  font-weight: 700;
  white-space: nowrap;
}

.task-info-kicker {
  margin-bottom: 6px;
  color: #748398;
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.04em;
}

.task-extra {
  margin-top: 9px;
  border-top: 1px solid #edf1f5;
  padding-top: 8px;
}

.task-extra summary {
  width: fit-content;
  color: #5c7690;
  font-size: 11px;
  cursor: pointer;
  user-select: none;
}

.task-extra summary:hover {
  color: #0571dc;
}

.task-extra-grid {
  display: flex;
  flex-wrap: wrap;
  gap: 5px 14px;
  margin-top: 7px;
  color: #7d8a9a;
  font-size: 11px;
  line-height: 1.5;
}

.version-mismatch-warning {
  margin: 9px 0;
  padding: 9px 11px;
  border: 1px solid #f2c7c7;
  border-left: 3px solid #c54858;
  border-radius: 6px;
  background: #fff5f5;
  color: #a83242 !important;
  font-size: 12px !important;
  line-height: 1.55;
}

/* 历史轮次脱敏摘要 */
.prev-rounds {
  margin-top: 8px;
  display: grid;
  gap: 6px;
}

.prev-round {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  font-size: 12px;
  padding: 6px 10px;
  background: #f8fbff;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
}

.pr-tag {
  font-weight: 700;
  color: #172033;
}

.pr-vote {
  font-size: 11px;
  font-weight: 700;
  padding: 1px 7px;
  border-radius: 4px;
}

.pr-vote.ok { background: #f0fff8; color: #087c55; }
.pr-vote.no { background: #fff0f0; color: #c54858; }
.pr-vote.warn { background: #fdf2e3; color: #c07b22; }

.pr-result {
  margin-left: auto;
  font-size: 11px;
  font-weight: 700;
}

.pr-result.passed { color: #087c55; }
.pr-result.not-passed { color: #c07b22; }

.isolation-hint {
  margin: 10px 0 0;
  padding: 7px 0 0;
  border-top: 1px solid #edf1f5;
  background: transparent;
  border-radius: 6px;
  color: #6e7b8f !important;
  font-size: 12px !important;
}

.wait-final {
  background: var(--surface-muted);
  border-color: var(--line);
}

.wait-final p {
  color: var(--muted);
}

.task-hint {
  border-radius: 8px;
  padding: 12px 14px;
  margin: 10px 0;
  font-size: 13px;
  line-height: 1.6;
  display: flex;
  align-items: center;
  gap: 12px;
  justify-content: space-between;
}

.task-hint p {
  margin: 0;
  flex: 1;
}

.hint-warn { background: #fdf2e3; border: 1px solid #f3d9b0; color: #9a6213; }
.hint-bad { background: #fff0f0; border: 1px solid #f3c2c2; color: #a13535; }

.detail-section {
  margin-bottom: 18px;
}

.detail-section h3 {
  font-size: 13px;
  color: #6e7b8f;
  margin: 0 0 8px;
}

.detail-section p {
  font-size: 15px;
  line-height: 1.8;
  margin: 0;
  color: #172033;
}

.detail-section p.warn-text {
  color: #c07b22;
  background: #fdf2e3;
  border-radius: 6px;
  padding: 8px 12px;
  font-size: 13px;
}

/* AI 检查参考面板 */
.ai-review-box {
  border: 1px solid #dce8f7;
  background: #f8fbff;
  border-radius: 8px;
  padding: 10px 14px;
}

.ai-toggle {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  border: 0;
  background: transparent;
  padding: 0;
  cursor: pointer;
  text-align: left;
  font-size: 13px;
  color: #6e7b8f;
}

.ai-caret {
  margin-left: auto;
  font-size: 11px;
  color: #9aa5b4;
}

.ai-verdict {
  font-size: 12px;
  font-weight: 700;
  padding: 2px 8px;
  border-radius: 4px;
  margin-left: 8px;
}

.ai-verdict.good { background: #f0fff8; color: #087c55; }
.ai-verdict.warn { background: #fdf2e3; color: #c07b22; }
.ai-verdict.bad { background: #fff0f0; color: #c54858; }

.ai-stale {
  font-size: 11px;
  color: #c07b22;
  margin-left: 8px;
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

.option-item {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 7px 0;
  font-size: 14px;
  line-height: 1.6;
}

.opt-label {
  width: 24px;
  height: 24px;
  display: grid;
  place-items: center;
  border-radius: 50%;
  background: #1385f8;
  color: #fff;
  font-size: 12px;
  font-weight: 700;
  flex: 0 0 auto;
}

.correct {
  color: #087c55;
  font-size: 12px;
  font-weight: 700;
}

.review-actions {
  border-top: 1px solid #e5ebf3;
  padding-top: 18px;
  margin-top: 20px;
}

.submit-section {
  display: flex;
  align-items: center;
  gap: 12px;
}

.flow-select {
  display: flex;
  align-items: center;
  gap: 8px;
}

.flow-select label {
  font-size: 13px;
  color: #6e7b8f;
  white-space: nowrap;
}

.flow-select select {
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 10px;
  font-size: 13px;
}

.task-info {
  margin-bottom: 14px;
}

.task-info p {
  font-size: 13px;
  color: #6e7b8f;
  margin: 0;
}

.review-form-top {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}

.review-form {
  border: 1px solid #dce8f7;
  border-radius: 9px;
  padding: 13px 14px 14px;
  background: #f9fcff;
}

.review-form-heading {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 10px;
}

.review-form-heading h3 {
  margin: 0;
  color: #172033;
  font-size: 14px;
}

.review-form-heading span {
  color: #7a8798;
  font-size: 11px;
}

.form-label {
  color: #5e7185;
  font-size: 12px;
  font-weight: 600;
  white-space: nowrap;
}

.review-form-top select {
  width: 130px;
  height: 34px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 8px;
  font-size: 13px;
}

.form-rule {
  font-size: 12px;
  color: #6e7b8f;
}

.sc-input-wrap {
  margin-bottom: 10px;
}

.form-submit-row {
  display: flex;
  justify-content: flex-end;
  padding-top: 2px;
}

.primary-button {
  height: 34px;
  border: 0;
  border-radius: 7px;
  background: #1385f8;
  color: #fff;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  padding: 0 18px;
}

.primary-button:hover {
  background: #0571dc;
}

.primary-button:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.own-record-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(250px, 1fr));
  gap: 10px;
}

.task-done {
  padding: 10px 14px;
  background: #f0fff8;
  border: 1px solid #bdecd9;
  border-radius: 7px;
}

.task-done p {
  margin: 0;
  font-size: 13px;
  color: #087c55;
}

.review-records {
  margin-top: 16px;
}

.review-records h3 {
  font-size: 13px;
  color: #6e7b8f;
  margin: 0 0 10px;
}

.empty-panel {
  display: grid;
  place-items: center;
  color: #6e7b8f;
  min-height: 520px;
}

.empty {
  text-align: center;
  color: #6e7b8f;
  padding: 30px;
}

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

@media (max-width: 900px) {
  .review-layout {
    grid-template-columns: minmax(0, 1fr);
  }

  .question-list-panel {
    max-height: none;
  }

  .my-task-list {
    max-height: 42vh;
    overflow-y: auto;
  }

  .review-form-heading {
    flex-wrap: wrap;
  }
}

@media (max-width: 600px) {
  .review-layout {
    gap: 12px;
  }

  .review-form-top {
    align-items: stretch;
    flex-wrap: wrap;
  }

  .review-form-top select {
    width: 100%;
  }

  .form-rule {
    flex: 1 1 100%;
  }

  .form-label {
    width: 100%;
  }
}

</style>
