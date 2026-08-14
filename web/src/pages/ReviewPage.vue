<script setup>
import { ref, onMounted } from "vue";
import { api } from "../api.js";
import { currentUser } from "../auth.js";

const toast = ref("");
const questions = ref([]);
const flows = ref([]);
const selectedQuestion = ref(null);
const reviewTask = ref(null);
const reviewRecords = ref([]);
const reviewAction = ref("approved");
const reviewOpinion = ref("");
const loading = ref(false);
const selectedFlowId = ref("");
const selectedImages = ref([]);
const lightboxImage = ref("");
const experts = ref([]);

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadQuestions() {
  try {
    const data = await api.listQuestions();
    questions.value = data.questions || [];
  } catch (e) {
    showToast("加载题目失败: " + e.message);
  }
}

async function loadFlows() {
  try {
    const data = await api.listFlows();
    flows.value = data.flows || [];
    if (flows.value.length > 0 && !selectedFlowId.value) {
      selectedFlowId.value = flows.value[0].id;
    }
  } catch (e) {
    console.error(e);
    showToast("加载审核流程失败: " + e.message);
  }
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
  return `http://127.0.0.1:8080/images/${filename}`;
}

function openImage(src) {
  lightboxImage.value = src;
}

function closeLightbox() {
  lightboxImage.value = "";
}

async function submitToReview() {
  if (!selectedQuestion.value) return;
  if (!selectedFlowId.value) {
    showToast("请先选择审核流程");
    return;
  }
  loading.value = true;
  try {
    const task = await api.submitReview(selectedQuestion.value.id, selectedFlowId.value);
    reviewTask.value = task;
    // 刷新题目状态
    const q = await api.getQuestion(selectedQuestion.value.id);
    if (q) selectedQuestion.value = q;
    showToast("已提交到审核流程");
  } catch (e) {
    showToast("提交失败: " + e.message);
  } finally {
    loading.value = false;
  }
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
    // 刷新任务和记录
    reviewTask.value = await api.getReviewTask(reviewTask.value.id);
    const recData = await api.reviewRecords(reviewTask.value.id);
    reviewRecords.value = recData.records || [];
    // 刷新题目状态
    const q = await api.getQuestion(selectedQuestion.value.id);
    if (q) selectedQuestion.value = q;
    loadQuestions();
  } catch (e) {
    showToast("审核失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

function statusText(status) {
  const map = {
    ai_draft: "AI草稿",
    auto_checked: "已初评",
    ai_reviewed: "AI已检查",
    reviewing: "审核中",
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
  return "";
}

// 判断是否可以提交审核
function canSubmit(q) {
  return q && (q.status === "ai_draft" || q.status === "auto_checked" || q.status === "ai_reviewed" || q.status === "revision_required");
}

// 判断是否可以执行审核
function canReview(task) {
  return task && (task.status === "reviewing" || task.status === "revision_required");
}

async function loadExperts() {
  try {
    const data = await api.listExperts();
    experts.value = data.experts || [];
  } catch (e) {
    console.error(e);
    showToast("加载专家列表失败: " + e.message);
  }
}

function expertName(id) {
  const e = experts.value.find((x) => x.id === id);
  return e ? e.name : id;
}

onMounted(() => {
  loadQuestions();
  loadFlows();
  loadExperts();
});
</script>

<template>
  <div class="review-layout">
    <!-- 左侧题目列表 -->
    <section class="panel question-list-panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>题目列表</h2>
        <small>{{ questions.length }} 道</small>
      </div>
      <div class="question-list">
        <button
          v-for="q in questions.slice(0, 50)"
          :key="q.id"
          class="question-item"
          :class="{ active: selectedQuestion?.id === q.id }"
          type="button"
          @click="selectQuestion(q)"
        >
          <span class="q-stem">{{ (q.clinical_stem || "").slice(0, 40) }}...</span>
          <span class="q-status" :class="statusClass(q.status)">{{ statusText(q.status) }}</span>
        </button>
        <div v-if="!questions.length" class="empty">暂无题目，请先生成</div>
      </div>
    </section>

    <!-- 右侧审核详情 -->
    <section class="panel" v-if="selectedQuestion">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>题目详情</h2>
        <span class="q-status" :class="statusClass(selectedQuestion.status)">
          {{ statusText(selectedQuestion.status) }}
        </span>
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

      <div v-if="selectedQuestion.explanation" class="detail-section">
        <h3>解析</h3>
        <p>{{ selectedQuestion.explanation }}</p>
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
        <!-- 提交审核 -->
        <div v-if="canSubmit(selectedQuestion) && !reviewTask" class="submit-section">
          <div class="flow-select">
            <label>审核流程：</label>
            <select v-model="selectedFlowId">
              <option v-for="f in flows" :key="f.id" :value="f.id">{{ f.name }}</option>
            </select>
          </div>
          <button class="primary-button" type="button" :disabled="loading" @click="submitToReview">
            提交到审核流程
          </button>
        </div>

        <!-- 审核任务信息 -->
        <div v-if="reviewTask" class="task-info">
          <h3>审核任务 <span class="task-status" :class="statusClass(reviewTask.status)">{{ statusText(reviewTask.status) }}</span></h3>
          <p>当前轮次：第 {{ reviewTask.current_round }} 轮 | 审核人：{{ (reviewTask.assigned_to || []).map(id => expertName(id)).join(", ") }}</p>
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
              提交审核
            </button>
          </div>
        </div>

        <!-- 已完成审核 -->
        <div v-if="reviewTask && !canReview(reviewTask)" class="task-done">
          <p>审核已结束，最终状态：{{ statusText(reviewTask.status) }}</p>
        </div>

        <!-- 审核记录 -->
        <div v-if="reviewRecords.length" class="review-records">
          <h3>审核记录</h3>
          <div v-for="rec in reviewRecords" :key="rec.id" class="record-item">
            <span class="record-round">第{{ rec.round_number }}轮</span>
            <span class="record-expert">{{ rec.expert_id }}</span>
            <span class="record-conclusion" :class="statusClass(rec.review_status)">
              {{ statusText(rec.review_status) }}
            </span>
            <span class="record-opinion">{{ rec.opinion }}</span>
          </div>
        </div>
      </div>
    </section>

    <section v-else class="panel empty-panel">
      <p>← 请从左侧选择一道题目</p>
    </section>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>

  <!-- 图片放大弹窗 -->
  <div v-if="lightboxImage" class="lightbox" @click="closeLightbox">
    <img :src="lightboxImage" @click.stop />
  </div>
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
