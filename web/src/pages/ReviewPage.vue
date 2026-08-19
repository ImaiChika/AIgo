<script setup>
import { ref, onMounted, computed } from "vue";
import { api } from "../api.js";
import { currentUser, hasPerm, getToken } from "../auth.js";
import QuestionDetailModal from "../components/QuestionDetailModal.vue";

const toast = ref("");
const selectedQuestion = ref(null);
const detailQuestion = ref(null); // 题目详情弹窗数据
const reviewTask = ref(null);
const reviewRecords = ref([]);
const reviewAction = ref("approved");
const reviewOpinion = ref("");
const loading = ref(false);
const selectedImages = ref([]);
const lightboxImage = ref("");
const experts = ref([]);

// 待我审核（本页唯一视图：审题人投票工作区）
const myTasks = ref([]);
const myTasksLoading = ref(false);
const flows = ref([]); // 仅用于显示流程名/轮次信息

// 当前任务的流程信息
const currentFlow = computed(() => {
  const task = reviewTask.value;
  if (!task) return null;
  return flows.value.find((f) => f.id === task.flow_id) || null;
});

const currentFlowRounds = computed(() => {
  return currentFlow.value?.rounds?.length || 0;
});

const currentRoundVotes = computed(() => {
  const task = reviewTask.value;
  if (!task || !task.round_results) return null;
  const rr = task.round_results.find((r) => r.round_number === task.current_round);
  if (!rr) return null;
  return {
    approved: rr.approved_count || 0,
    rejected: rr.rejected_count || 0,
    revision: rr.revision_count || 0,
    total: (rr.reviews || []).length,
    assigned: (task.assigned_to || []).length,
  };
});

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

// 待我审核列表
async function loadMyTasks() {
  myTasksLoading.value = true;
  try {
    const data = await api.myTasks();
    myTasks.value = data.tasks || [];
    // 若当前选中项已不在列表中（投完/进入下一轮），保留右侧供查看
  } catch (e) {
    showToast("加载待审任务失败: " + e.message);
  } finally {
    myTasksLoading.value = false;
  }
}

// 从待审任务卡片进入审核
async function selectMyTask(item) {
  await selectQuestion(item.question);
}

function progressPercent(item) {
  if (!item.assigned) return 0;
  return Math.min(100, Math.round((item.voted / item.assigned) * 100));
}

async function selectQuestion(q) {
  selectedQuestion.value = q;
  reviewTask.value = null;
  reviewRecords.value = [];
  reviewOpinion.value = "";
  selectedImages.value = [];

  // 用题目 ID 查审核任务
  try {
    const task = await api.getTaskByQuestion(q.id);
    if (task && task.id) {
      reviewTask.value = task;
      const recData = await api.reviewRecords(task.id).catch(() => ({ records: [] }));
      reviewRecords.value = recData.records || [];
    }
  } catch (e) {
    // 没有审核任务是正常的
  }

  // 加载配图
  try {
    const imgData = await api.listImages(q.id);
    selectedImages.value = imgData.images || [];
  } catch (e) {
    selectedImages.value = [];
  }
}

function imageSrc(path) {
  if (!path) return "";
  const filename = path.split("/").pop();
  const token = getToken();
  return `/images/${filename}${token ? `?token=${encodeURIComponent(token)}` : ""}`;
}

function openImage(src) {
  lightboxImage.value = src;
}

function closeLightbox() {
  lightboxImage.value = "";
}

async function doReview() {
  if (!reviewTask.value) return;
  loading.value = true;
  try {
    await api.reviewAction({
      task_id: reviewTask.value.id,
      expert_id: currentUser.value?.id || "admin",
      action: reviewAction.value,
      opinion: reviewOpinion.value,
    });
    showToast("审核完成");
    reviewOpinion.value = "";
    await refreshTask();
  } catch (e) {
    showToast("审核失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

async function refreshTask() {
  reviewTask.value = await api.getReviewTask(reviewTask.value.id);
  const recData = await api.reviewRecords(reviewTask.value.id);
  reviewRecords.value = recData.records || [];
  const q = await api.getQuestion(selectedQuestion.value.id);
  if (q) selectedQuestion.value = q;
  loadMyTasks();
}

function statusText(status) {
  const map = {
    ai_draft: "AI草稿",
    auto_checked: "已初评",
    ai_reviewed: "AI已检查",
    reviewing: "审核中",
    conflict: "待决断",
    approved: "已通过",
    rejected: "已驳回",
    revision_required: "需修改",
    published: "已入库",
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

// 判断是否可以执行审核（任务进行中 且 当前用户被分配到当前轮）
function canReview(task) {
  if (!task || task.status !== "reviewing") return false;
  const user = currentUser.value;
  if (!user) return false;
  const assigned = (task.assigned_to || []).includes(user.id);
  const alreadyReviewed = (task.round_results || []).some(
    (rr) => rr.round_number === task.current_round && (rr.reviews || []).some((r) => r.expert_id === user.id)
  );
  return assigned && !alreadyReviewed;
}

async function loadFlows() {
  try {
    const data = await api.listFlows();
    flows.value = data.flows || [];
  } catch (e) {
    console.error(e);
  }
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
  loadFlows();
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
        <small>{{ myTasks.length }} 项</small>
      </div>

      <div v-if="myTasksLoading" class="loading">加载中...</div>
      <div v-else-if="!myTasks.length" class="empty">
        暂无待你审核的任务<br />
        <span class="empty-hint">提交审核由管理员在题库页统一操作，题目进入你的审核轮次后会出现在这里</span>
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
          <div class="mt-meta">
            <span v-if="item.round_name">轮次：{{ item.round_name }}</span>
            <span>已投 {{ item.voted }}/{{ item.assigned }} 票</span>
            <span v-if="item.approved" class="mt-vote ok">通过 {{ item.approved }}</span>
            <span v-if="item.rejected" class="mt-vote no">驳回 {{ item.rejected }}</span>
            <span v-if="item.revision" class="mt-vote warn">需修改 {{ item.revision }}</span>
          </div>
          <div class="mt-progress">
            <div class="mt-progress-fill" :style="{ width: progressPercent(item) + '%' }"></div>
          </div>
          <div class="mt-action">去审核 →</div>
        </div>
      </div>
    </section>

    <!-- 右侧审核工作区 -->
    <section class="panel" v-if="selectedQuestion">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>题目详情</h2>
        <span class="q-status" :class="statusClass(selectedQuestion.status)">
          {{ statusText(selectedQuestion.status) }}
        </span>
        <button class="ghost-button" type="button" @click="detailQuestion = selectedQuestion">查看完整信息</button>
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
        <p v-else-if="selectedQuestion.explanation" class="warn-text">⚠ 解析内容过短（{{ selectedQuestion.explanation.trim().length }} 字），可能不完整，请重点核查</p>
        <p v-else class="warn-text">⚠ 此题无解析（解析为可选项，请专家注意评估）</p>
      </div>

      <!-- 配图 -->
      <div v-if="selectedImages.length" class="detail-section">
        <h3>配图（{{ selectedImages.length }} 张）</h3>
        <div class="image-grid">
          <div v-for="(img, i) in selectedImages" :key="img.id" class="image-thumb">
            <img :src="imageSrc(img.image_path)" :alt="`配图${i+1}`" @click="openImage(imageSrc(img.image_path))" />
            <span class="image-status" :class="img.status">{{ img.status === "approved" ? "已通过" : img.status === "rejected" ? "已驳回" : "待审核" }}</span>
          </div>
        </div>
      </div>

      <!-- 审核操作区 -->
      <div class="review-actions">
        <!-- 审核任务信息 -->
        <div v-if="reviewTask" class="task-info">
          <h3>
            审核任务
            <span class="task-status" :class="statusClass(reviewTask.status)">{{ statusText(reviewTask.status) }}</span>
          </h3>
          <div class="task-flow-row">
            <span class="task-flow-name">{{ currentFlow?.name || reviewTask.flow_id }}</span>
            <span class="task-round">第 {{ reviewTask.current_round }}/{{ currentFlowRounds || reviewTask.round_results?.length || 1 }} 轮</span>
            <span v-if="currentFlow?.rounds?.[reviewTask.current_round - 1]?.name" class="task-round-name">
              {{ currentFlow.rounds[reviewTask.current_round - 1].name }}
            </span>
          </div>
          <p>审核人：{{ (reviewTask.assigned_to || []).map(id => expertName(id)).join("、") || "自动匹配" }}</p>
          <!-- 投票进度条 -->
          <div v-if="currentRoundVotes && reviewTask.status === 'reviewing'" class="vote-progress-wrap">
            <div class="vote-progress">
              <div
                class="vote-progress-fill"
                :style="{ width: Math.min(100, Math.round((currentRoundVotes.total / Math.max(currentRoundVotes.assigned, 1)) * 100)) + '%' }"
              ></div>
            </div>
            <div v-if="currentRoundVotes" class="vote-stats">
              <span class="vote approved">通过 {{ currentRoundVotes.approved }}</span>
              <span class="vote rejected">驳回 {{ currentRoundVotes.rejected }}</span>
              <span class="vote revision">需修改 {{ currentRoundVotes.revision }}</span>
              <span class="vote total">已投 {{ currentRoundVotes.total }} / {{ currentRoundVotes.assigned }} 人</span>
            </div>
          </div>
        </div>

        <!-- 审核表单 -->
        <div v-if="reviewTask && canReview(reviewTask)" class="review-form">
          <div class="review-form-row">
            <select v-model="reviewAction">
              <option value="approved">通过</option>
              <option value="rejected">驳回</option>
              <option value="revision_required">需修改</option>
            </select>
            <input v-model="reviewOpinion" placeholder="审核意见（可选）" />
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

        <!-- 审核记录 -->
        <div v-if="reviewRecords.length" class="review-records">
          <h3>审核记录</h3>
          <div v-for="rec in reviewRecords" :key="rec.id" class="record-item">
            <span class="record-round">第{{ rec.round_number }}轮</span>
            <span class="record-expert">{{ expertName(rec.expert_id) }}</span>
            <span class="record-conclusion" :class="statusClass(rec.review_status)">
              {{ statusText(rec.review_status) }}
            </span>
            <span class="record-opinion">{{ rec.opinion }}</span>
          </div>
        </div>
      </div>
    </section>

    <section v-else class="panel empty-panel">
      <p>← 请从左侧选择一道待审核的题目</p>
    </section>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>

  <!-- 图片放大弹窗 -->
  <div v-if="lightboxImage" class="lightbox" @click="closeLightbox">
    <img :src="lightboxImage" @click.stop />
  </div>

  <!-- 题目详情弹窗 -->
  <QuestionDetailModal v-if="detailQuestion" :question="detailQuestion" @close="detailQuestion = null" />
</template>


<style scoped>
.review-layout {
  display: grid;
  grid-template-columns: 320px minmax(0, 1fr);
  gap: 16px;
}

.question-list-panel {
  max-height: calc(100vh - 180px);
  overflow-y: auto;
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
.status-conflict { background: #fdf0f8; color: #b93a7c; }

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
  max-height: calc(100vh - 260px);
  overflow-y: auto;
}

.my-task-card {
  border: 2px solid #dce8f7;
  border-radius: 10px;
  padding: 10px 12px;
  background: #fff;
  cursor: pointer;
  transition: all 0.15s;
  position: relative;
}

.my-task-card:hover {
  border-color: #1385f8;
  box-shadow: 0 4px 12px rgba(19, 133, 248, 0.12);
}

.my-task-card.active {
  border-color: #1385f8;
  background: #eff8ff;
}

.my-task-card.conflict {
  border-color: #f0c4dd;
  background: #fff8fc;
}

.my-task-card.conflict:hover {
  border-color: #b93a7c;
}

.my-task-card.conflict .mt-action {
  color: #b93a7c;
}

.mt-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.mt-flow {
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

.mt-round {
  font-size: 11px;
  font-weight: 700;
  color: #1385f8;
  background: #eff8ff;
  padding: 2px 6px;
  border-radius: 4px;
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
  margin-bottom: 6px;
}

.mt-vote {
  padding: 1px 6px;
  border-radius: 3px;
  font-weight: 700;
}

.mt-vote.ok { background: #f0fff8; color: #087c55; }
.mt-vote.no { background: #fff0f0; color: #c54858; }
.mt-vote.warn { background: #fdf2e3; color: #c07b22; }

.mt-progress {
  height: 5px;
  background: #f0f3f7;
  border-radius: 3px;
  overflow: hidden;
}

.mt-progress-fill {
  height: 100%;
  background: #1385f8;
  border-radius: 3px;
  transition: width 0.3s;
}

.mt-action {
  margin-top: 6px;
  text-align: right;
  font-size: 12px;
  font-weight: 700;
  color: #1385f8;
}

/* 任务信息：流程/轮次/进度条 */
.task-flow-row {
  display: flex;
  gap: 8px;
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
  color: #1385f8;
  background: #eff8ff;
  padding: 2px 8px;
  border-radius: 4px;
}

.task-round-name {
  font-size: 12px;
  color: #6e7b8f;
}

.vote-progress-wrap {
  margin-top: 8px;
}

.vote-progress {
  height: 6px;
  background: #f0f3f7;
  border-radius: 3px;
  overflow: hidden;
  margin-bottom: 6px;
}

.vote-progress-fill {
  height: 100%;
  background: #1385f8;
  border-radius: 3px;
  transition: width 0.3s;
}

.vote-stats {
  display: flex;
  gap: 8px;
  margin-top: 8px;
  flex-wrap: wrap;
}

.vote {
  font-size: 12px;
  font-weight: 600;
  padding: 2px 8px;
  border-radius: 4px;
}

.vote.approved { background: #f0fff8; color: #087c55; }
.vote.rejected { background: #fff0f0; color: #c54858; }
.vote.revision { background: #fdf2e3; color: #c07b22; }
.vote.total { background: #eff8ff; color: #0571dc; }

.hint-conflict {
  background: #fdf0f8;
  border: 1px solid #f0c4dd;
  color: #a3326c;
}

.finalize-form {
  margin: 12px 0;
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

.wait-final {
  background: #fdf0f8;
  border-color: #f0c4dd;
}

.wait-final p {
  color: #a3326c;
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
  margin-bottom: 16px;
}

.detail-section h3 {
  font-size: 13px;
  color: #6e7b8f;
  margin: 0 0 8px;
}

.detail-section p {
  font-size: 14px;
  line-height: 1.7;
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

.option-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 0;
  font-size: 14px;
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
}

.correct {
  color: #087c55;
  font-size: 12px;
  font-weight: 700;
}

.review-actions {
  border-top: 1px solid #e5ebf3;
  padding-top: 16px;
  margin-top: 16px;
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
  margin-bottom: 12px;
}

.task-info h3 {
  font-size: 14px;
  margin: 0 0 6px;
}

.task-status {
  font-size: 12px;
  padding: 2px 8px;
  border-radius: 4px;
  margin-left: 8px;
}

.task-info p {
  font-size: 13px;
  color: #6e7b8f;
  margin: 0;
}

.review-form-row {
  display: flex;
  gap: 8px;
}

.review-form-row select {
  width: 120px;
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 8px;
}

.review-form-row input {
  flex: 1;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 10px;
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

.record-item {
  display: flex;
  gap: 10px;
  align-items: center;
  padding: 8px 0;
  border-bottom: 1px solid #f0f3f7;
  font-size: 13px;
}

.record-round {
  font-weight: 700;
  color: #172033;
}

.record-expert {
  color: #6e7b8f;
}

.record-conclusion {
  font-weight: 700;
}

.record-opinion {
  flex: 1;
  color: #6e7b8f;
}

.empty-panel {
  display: grid;
  place-items: center;
  color: #6e7b8f;
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

.image-grid {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
}

.image-thumb {
  display: flex;
  flex-direction: column;
  gap: 4px;
  align-items: center;
}

.image-thumb img {
  width: 150px;
  height: 120px;
  object-fit: cover;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  cursor: pointer;
}

.image-thumb img:hover {
  border-color: #1385f8;
}

.image-status {
  font-size: 11px;
  font-weight: 600;
  padding: 2px 6px;
  border-radius: 4px;
  background: #f0f3f7;
  color: #6e7b8f;
}

.image-status.approved {
  background: #f0fff8;
  color: #087c55;
}

.image-status.rejected {
  background: #fff0f0;
  color: #c54858;
}

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
