<script setup>
import { computed, onMounted, ref } from "vue";
import { api } from "../api.js";
import { currentUser, hasPerm } from "../auth.js";
import AICheckScoreButton from "../components/AICheckScoreButton.vue";
import ReviewHistoryPanel from "../components/ReviewHistoryPanel.vue";

const loading = ref(false);
const items = ref([]);
const toast = ref("");
const showAll = ref(false);
const actingID = ref("");
const reviewPanels = ref({});

const canReview = computed(() => hasPerm("question:share_review"));
const listScope = computed(() => canReview.value ? (showAll.value ? "all" : "pending") : "mine");

function showToast(message) {
  toast.value = message;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3200);
}

function statusText(status) {
  return {
    pending: "待管理员审核",
    approved: "已进入全局正式库",
    rejected: "分享未通过",
  }[status] || status;
}

function statusClass(status) {
  return { pending: "pending", approved: "approved", rejected: "rejected" }[status] || "";
}

async function load() {
  loading.value = true;
  try {
    const data = await api.listQuestionShares(listScope.value);
    items.value = data.items || [];
  } catch (error) {
    showToast("加载分享申请失败：" + error.message);
  } finally {
    loading.value = false;
  }
}

async function review(item, status, note = "") {
  if (actingID.value) return;
  actingID.value = item.request.id;
  try {
    await api.reviewQuestionShare(item.request.id, status, note);
    showToast(status === "approved" ? "已批准加入全局正式库" : "已拒绝分享申请");
    await load();
  } catch (error) {
    showToast("处理申请失败：" + error.message);
  } finally {
    actingID.value = "";
  }
}

async function toggleReviewHistory(item) {
  const questionID = item.question?.id || item.request?.question_id;
  if (!questionID) return;
  const current = reviewPanels.value[questionID];
  if (current?.loaded) {
    reviewPanels.value = { ...reviewPanels.value, [questionID]: { ...current, open: !current.open } };
    return;
  }
  reviewPanels.value = { ...reviewPanels.value, [questionID]: { open: true, loading: true, loaded: false, records: [] } };
  try {
    const task = await api.getTaskByQuestion(questionID);
    const data = task?.id ? await api.reviewRecords(task.id) : { records: [] };
    reviewPanels.value = {
      ...reviewPanels.value,
      [questionID]: { open: true, loading: false, loaded: true, records: data.records || [] },
    };
  } catch (error) {
    reviewPanels.value = { ...reviewPanels.value, [questionID]: { open: false, loading: false, loaded: false, records: [] } };
    showToast("加载审核意见失败：" + error.message);
  }
}

function approve(item) {
  if (confirm("确认批准这道题进入全局正式题库？每道题的分享审批只能处理一次。")) {
    review(item, "approved");
  }
}

function reject(item) {
  const note = prompt("请输入拒绝原因（可选）：", "");
  if (note === null) return;
  review(item, "rejected", note);
}

onMounted(load);
</script>

<template>
  <div class="share-layout">
    <section class="panel share-hero">
      <div>
        <span class="eyebrow">PERSONAL → GLOBAL</span>
        <h2>{{ canReview ? "全局题库分享审批" : "我的全局库申请" }}</h2>
        <p v-if="canReview">个人正式题目先在所属用户的审核流程中定稿，再由管理员一次审批进入全局正式题库。待审批申请构成全局待审核库，被拒绝的申请留在全局淘汰库。</p>
        <p v-else>只有本人已通过专家审核的正式题目可以申请分享。每道题只能提交一次，审批结果会进入全局题库对应分层。</p>
      </div>
      <div class="share-legend" aria-label="全局题库流转说明">
        <span><i class="dot pending"></i>待审批</span>
        <span><i class="dot approved"></i>全局正式</span>
        <span><i class="dot rejected"></i>全局淘汰</span>
      </div>
    </section>

    <section class="panel request-panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>{{ canReview ? "分享申请队列" : "申请记录" }}</h2>
        <small>{{ items.length }} 条</small>
        <div v-if="canReview" class="view-switch">
          <button type="button" :class="{ active: !showAll }" @click="showAll = false; load()">待审批</button>
          <button type="button" :class="{ active: showAll }" @click="showAll = true; load()">全部记录</button>
        </div>
      </div>

      <div v-if="loading" class="loading">加载中...</div>
      <div v-else-if="!items.length" class="empty">
        {{ canReview && !showAll ? "暂无待审批的分享申请" : "暂无分享申请记录" }}
      </div>
      <div v-else class="request-list">
        <article v-for="item in items" :key="item.request.id" class="request-card">
          <div class="request-head">
            <div>
              <span class="request-status" :class="statusClass(item.request.status)">{{ statusText(item.request.status) }}</span>
              <span v-if="canReview && item.owner_name" class="applicant">申请人：{{ item.owner_name }}<template v-if="item.owner_username">（{{ item.owner_username }}）</template></span>
            </div>
            <time>{{ new Date(item.request.created_at).toLocaleString() }}</time>
          </div>
          <div class="question-copy">
            <p class="question-stem">{{ item.question.clinical_stem }}</p>
            <div class="question-options">
              <span v-for="option in item.question.options || []" :key="option.label" :class="{ correct: option.label === item.question.answer }">
                {{ option.label }}. {{ option.text }}<b v-if="option.label === item.question.answer">✓</b>
              </span>
            </div>
          </div>
          <div class="request-meta">
            <span v-if="item.question.outline_code">大纲 {{ item.question.outline_code }}</span>
            <span v-if="item.question.profession">专业 {{ item.question.profession }}</span>
            <span>题目 ID {{ item.request.question_id }}</span>
            <AICheckScoreButton class="share-ai-score" :question-id="item.question.id" />
          </div>
          <div class="request-review-history">
            <button class="review-history-toggle" type="button" @click="toggleReviewHistory(item)">
              {{ reviewPanels[item.question.id]?.open ? "收起审核意见" : "查看审核意见" }}
            </button>
            <span v-if="reviewPanels[item.question.id]?.loading" class="review-history-loading">加载中</span>
            <ReviewHistoryPanel
              v-if="reviewPanels[item.question.id]?.open && reviewPanels[item.question.id]?.loaded"
              :records="reviewPanels[item.question.id].records"
              compact
            />
          </div>
          <p v-if="item.request.review_note" class="review-note">审批说明：{{ item.request.review_note }}</p>
          <div v-if="canReview && item.request.status === 'pending'" class="request-actions">
            <button class="approve-button" type="button" :disabled="actingID === item.request.id" @click="approve(item)">批准进入全局正式库</button>
            <button class="reject-button" type="button" :disabled="actingID === item.request.id" @click="reject(item)">拒绝并留档</button>
          </div>
        </article>
      </div>
    </section>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
</template>

<style scoped>
.share-layout {
  display: grid;
  gap: 16px;
}

.share-hero {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 28px;
  background: linear-gradient(120deg, #f4fbf8 0%, #fff 68%);
  border-color: #d8ebe2;
}

.eyebrow {
  color: #4b8c70;
  font-size: 10px;
  font-weight: 800;
  letter-spacing: .16em;
}

.share-hero h2 {
  margin: 7px 0 8px;
  color: #254b3b;
  font-size: 22px;
}

.share-hero p {
  max-width: 760px;
  margin: 0;
  color: #5c7368;
  font-size: 13px;
  line-height: 1.7;
}

.share-legend {
  display: grid;
  gap: 8px;
  min-width: 130px;
  color: #5c7368;
  font-size: 12px;
}

.share-legend span {
  display: flex;
  align-items: center;
  gap: 7px;
}

.dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
}

.dot.pending { background: #d38a2e; }
.dot.approved { background: #27936c; }
.dot.rejected { background: #c45662; }

.request-panel {
  min-width: 0;
}

.view-switch {
  display: flex;
  gap: 4px;
  margin-left: auto;
  padding: 3px;
  border-radius: 7px;
  background: #f3f6fa;
}

.view-switch button {
  border: 0;
  border-radius: 5px;
  padding: 5px 10px;
  background: transparent;
  color: #738094;
  font-size: 12px;
  cursor: pointer;
}

.view-switch button.active {
  background: #fff;
  color: #172033;
  box-shadow: 0 1px 4px rgba(30, 55, 80, .1);
}

.request-list {
  display: grid;
  gap: 12px;
}

.request-card {
  overflow: hidden;
  border: 1px solid #e3eaf1;
  border-radius: 10px;
  background: #fff;
}

.request-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 14px;
  border-bottom: 1px solid #edf1f5;
  background: #fbfcfd;
}

.request-head > div {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 9px;
}

.request-status {
  padding: 3px 8px;
  border-radius: 5px;
  font-size: 11px;
  font-weight: 700;
}

.request-status.pending { background: #fff3e2; color: #a16618; }
.request-status.approved { background: #e8f7f0; color: #187450; }
.request-status.rejected { background: #fff0f1; color: #b54854; }

.applicant,
.request-head time {
  color: #738094;
  font-size: 11px;
}

.question-copy {
  padding: 14px;
}

.question-stem {
  margin: 0 0 12px;
  color: #172033;
  font-size: 14px;
  line-height: 1.7;
}

.question-options {
  display: grid;
  gap: 6px;
  color: #526174;
  font-size: 12px;
  line-height: 1.5;
}

.question-options span.correct {
  color: #187450;
  font-weight: 700;
}

.question-options b {
  margin-left: 6px;
}

.request-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  padding: 0 14px 12px;
  color: #8a96a5;
  font-size: 11px;
}

.request-review-history {
  display: grid;
  gap: 10px;
  padding: 0 14px 12px;
}

.review-history-toggle {
  justify-self: start;
  padding: 4px 9px;
  border: 1px solid #dce5e1;
  border-radius: 5px;
  color: #49665b;
  background: #fff;
  font-size: 11px;
}

.review-history-toggle:hover {
  border-color: #8ba99d;
  background: #f7faf8;
}

.review-history-loading {
  color: #8a96a5;
  font-size: 11px;
}

.review-note {
  margin: 0;
  padding: 9px 14px;
  border-top: 1px solid #edf1f5;
  background: #fffaf3;
  color: #89631f;
  font-size: 12px;
}

.request-actions {
  display: flex;
  gap: 8px;
  padding: 12px 14px;
  border-top: 1px solid #edf1f5;
}

.request-actions button {
  border-radius: 6px;
  padding: 7px 12px;
  font-size: 12px;
  cursor: pointer;
}

.approve-button {
  border: 1px solid #bdecd9;
  background: #f0fff8;
  color: #087c55;
}

.reject-button {
  border: 1px solid #f3c2c2;
  background: #fff;
  color: #b54854;
}

.request-actions button:disabled {
  cursor: wait;
  opacity: .55;
}

.loading,
.empty {
  padding: 46px 20px;
  text-align: center;
  color: #7c8897;
  font-size: 13px;
}

.toast {
  position: fixed;
  right: 24px;
  bottom: 24px;
  z-index: 20;
  transform: translateY(12px);
  padding: 10px 15px;
  border-radius: 7px;
  background: #172033;
  color: #fff;
  font-size: 13px;
  opacity: 0;
  pointer-events: none;
  transition: all .2s;
}

.toast.show {
  transform: translateY(0);
  opacity: 1;
}

@media (max-width: 700px) {
  .share-hero { display: grid; align-items: start; }
  .share-legend { grid-template-columns: repeat(3, 1fr); min-width: 0; }
  .request-head { align-items: flex-start; flex-direction: column; }
}

/* 元信息行右侧的 AI 检查评分按钮 */
.share-ai-score {
  margin-left: auto;
}
</style>
