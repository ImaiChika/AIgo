<script setup>
import { ref, computed, onMounted, onActivated, onDeactivated, onBeforeUnmount } from "vue";
import { api } from "../api.js";
import KnowledgePointPicker from "../components/KnowledgePointPicker.vue";

const toast = ref("");
const stats = ref({ question_count: 0, knowledge_count: 0, knowledge_categories: {} });

// 批量任务配置（知识点通过选择器多选；仅保留每知识点题数与跳过已有）
const selectedKPs = ref([]); // 选中的知识点（多选）
const batchConfig = ref({
  skip_existing: true,
  count: 1,
  job_name: "",
});

// 任务状态
const currentJob = ref(null);
const jobHistory = ref([]);
const polling = ref(false);
const manualJobId = ref(""); // 手动输入的 job_id
const searchName = ref(""); // 搜索名称
const searchResults = ref([]); // 搜索结果
const importResult = ref(null); // 导入结果详情

// 进度百分比
const progressPercent = computed(() => {
  if (!currentJob.value || !currentJob.value.total_count) return 0;
  const done = (currentJob.value.completed || 0) + (currentJob.value.failed || 0);
  return Math.round(done / currentJob.value.total_count * 100);
});

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadStats() {
  try {
    stats.value = await api.stats();
  } catch (e) {
    console.error(e);
    showToast("加载统计信息失败: " + e.message);
  }
}

// 保存任务到 localStorage
function saveJobToStorage(job) {
  const jobs = JSON.parse(localStorage.getItem("batch_jobs") || "[]");
  const idx = jobs.findIndex(j => j.job_id === job.job_id);
  if (idx >= 0) {
    jobs[idx] = job;
  } else {
    jobs.unshift(job);
  }
  localStorage.setItem("batch_jobs", JSON.stringify(jobs.slice(0, 20))); // 最多保存20个
}

// 从 API 加载任务历史
async function loadJobsFromDB() {
  try {
    const data = await api.batchList({ limit: 50 });
    jobHistory.value = data.jobs || [];
    if (jobHistory.value.length > 0 && !currentJob.value) {
      currentJob.value = jobHistory.value[0];
      // 如果任务还在运行中，自动开始轮询
      if (currentJob.value.status === "in_progress" || currentJob.value.status === "validating") {
        startPolling(currentJob.value.job_id);
      }
    }
  } catch (e) {
    console.error("加载任务历史失败:", e);
    showToast("加载任务历史失败，显示本地缓存");
    // 回退到 localStorage
    const jobs = JSON.parse(localStorage.getItem("batch_jobs") || "[]");
    jobHistory.value = jobs;
  }
}

// 提交批量任务
async function submitBatch() {
  if (!selectedKPs.value.length) {
    showToast("请先搜索并选择至少一个知识点");
    return;
  }
  try {
    const codes = selectedKPs.value.map((k) => k.outline_code || k.id).join(",");
    const data = await api.batchSubmit({
      skip_existing: batchConfig.value.skip_existing,
      count: batchConfig.value.count,
      outline_codes: codes,
      job_name: batchConfig.value.job_name || "",
    });

    currentJob.value = {
      job_id: data.job_id,
      status: "validating",
      total_count: data.count,
      completed: 0,
      failed: 0,
    };

    jobHistory.value.unshift(currentJob.value);
    saveJobToStorage(currentJob.value);
    showToast(`任务已提交: ${data.job_id}`);

    // 开始轮询
    startPolling(data.job_id);
  } catch (e) {
    showToast("提交失败: " + e.message);
  }
}

// A cached batch page keeps its form, but only the visible page polls. Each
// selected job owns one polling loop; a late response cannot restart an old loop.
let pollTimer = null;
let pollSequence = 0;
let pageActive = true;
let disposed = false;
function stopPolling() {
  ++pollSequence;
  clearTimeout(pollTimer);
  pollTimer = null;
  polling.value = false;
}
function startPolling(jobId) {
  stopPolling();
  if (!pageActive || disposed) return;
  const sequence = pollSequence;
  polling.value = true;
  const poll = async () => {
    try {
      const job = await api.batchStatus(jobId);
      if (sequence !== pollSequence || !pageActive || disposed) return;
      if (currentJob.value?.job_id === jobId) currentJob.value = job;
      const idx = jobHistory.value.findIndex(j => j.job_id === jobId);
      if (idx >= 0) jobHistory.value[idx] = job;
      saveJobToStorage(job);
      if (isTerminal(job.status)) {
        polling.value = false;
        showToast(isCompleted(job.status) ? `任务完成！成功请求: ${job.completed}，失败请求: ${job.failed}` : `任务${statusText(job.status)}`);
        return;
      }
      pollTimer = setTimeout(poll, 10000);
    } catch (e) {
      if (sequence !== pollSequence || !pageActive || disposed) return;
      console.error("轮询失败:", e);
      pollTimer = setTimeout(poll, 30000);
    }
  };
  poll();
}
onActivated(() => {
  pageActive = true;
  if (currentJob.value && isRunning(currentJob.value.status)) startPolling(currentJob.value.job_id);
});
onDeactivated(() => { pageActive = false; stopPolling(); });
onBeforeUnmount(() => { disposed = true; stopPolling(); clearTimeout(showToast.timer); });

// 下载并导入结果
async function downloadResult(jobId) {
  try {
    importResult.value = null;
    const data = await api.batchDownload(jobId);
    importResult.value = data;
    if (data.failed > 0) {
      showToast(`导入完成: 成功 ${data.saved} 题, 失败 ${data.failed} 题`);
    } else {
      showToast(`全部导入成功: ${data.saved} 道题目`);
    }
    loadStats();
  } catch (e) {
    showToast("导入失败: " + e.message);
  }
}

// 手动查询状态
async function checkStatus(jobId) {
  try {
    const job = await api.batchStatus(jobId);
    stopPolling();
    currentJob.value = job;
    if (isRunning(job.status)) startPolling(jobId);
    showToast(`状态: ${job.status}`);
  } catch (e) {
    showToast("查询失败: " + e.message);
  }
}

function statusText(status) {
  // DashScope 状态：validating / in_progress / completed|complete / failed / cancelled
  const map = {
    validating: "校验中",
    in_progress: "运行中",
    completed: "已完成",
    complete: "已完成",   // DashScope 实际可能返回这个
    failed: "失败",
    cancelled: "已取消",
  };
  return map[status] || status;
}

function statusClass(status) {
  if (status === "completed" || status === "complete") return "status-good";
  if (status === "failed") return "status-bad";
  if (status === "in_progress" || status === "validating") return "status-active";
  return "";
}

// 判断任务是否已完成（兼容 complete / completed）
function isCompleted(status) {
  return status === "completed" || status === "complete";
}

function isTerminal(status) {
  return isCompleted(status) || ["failed", "expired", "cancelled"].includes(status);
}

function isRunning(status) {
  return ["validating", "in_progress", "finalizing", "cancelling"].includes(status);
}

// 按名称搜索任务
async function searchJobs() {
  if (!searchName.value.trim()) {
    showToast("请输入搜索关键词");
    return;
  }
  try {
    const data = await api.batchList({ name: searchName.value.trim() });
    searchResults.value = data.jobs || [];
    if (searchResults.value.length === 0) {
      showToast("未找到匹配的任务");
    }
  } catch (e) {
    showToast("搜索失败: " + e.message);
  }
}

// 选择任务
function selectJob(job) {
  stopPolling();
  currentJob.value = job;
  saveJobToStorage(job);
  searchResults.value = [];
  searchName.value = "";
  // 如果任务还在运行中，开始轮询
  if (job.status === "in_progress" || job.status === "validating") {
    startPolling(job.job_id);
  }
}

onMounted(() => {
  loadStats();
  loadJobsFromDB();
});
</script>

<template>
  <div class="batch-layout">
    <!-- 统计信息 -->
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>批量推理</h2>
      </div>

      <div class="metric-row">
        <div>
          <span>题库题量</span>
          <strong>{{ stats.question_count }}道</strong>
        </div>
        <div>
          <span>知识点数</span>
          <strong>{{ stats.knowledge_count }}个</strong>
        </div>
      </div>

      <p class="info-text">
        使用 DashScope 批量 API，任务在云端执行，关闭电脑不影响。
        5折优惠，思考模式已开启。
      </p>
    </section>

    <!-- 配置 -->
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>生成配置</h2>
      </div>

      <div class="config-form">
        <!-- 知识点选择（搜索勾选多选，也可按大纲代码逗号分隔加入） -->
        <div class="field">
          <label>选择知识点（可多选，支持精确/模糊搜索）</label>
          <KnowledgePointPicker v-model="selectedKPs" :multiple="true" placeholder="搜索知识点、大纲代码、专业...（勾选多个，或按大纲代码逗号加入）" />
          <span class="field-hint">已选 {{ selectedKPs.length }} 个知识点，每个知识点将生成 {{ batchConfig.count }} 道题</span>
        </div>

        <div class="form-row">
          <div class="field">
            <label>每知识点题数</label>
            <input v-model.number="batchConfig.count" type="number" min="1" max="20" />
            <span class="field-hint">建议 1-3 道，越多越慢越贵</span>
          </div>
          <div class="field">
            <label>任务名称（可选）</label>
            <input v-model="batchConfig.job_name" placeholder="如: 呼吸系统第一批" />
          </div>
        </div>

        <div class="form-row">
          <label class="checkbox-row">
            <input type="checkbox" v-model="batchConfig.skip_existing" />
            <span>跳过已有题目的知识点</span>
          </label>
        </div>

        <button class="primary-button full" type="button" @click="submitBatch" :disabled="!selectedKPs.length">
          提交批量任务（{{ selectedKPs.length }} 个知识点 × {{ batchConfig.count }} 题）
        </button>
      </div>
    </section>

    <!-- 查询任务 -->
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>查询任务</h2>
      </div>
      <div class="form-row">
        <div class="field">
          <label>按 ID 查询</label>
          <input v-model="manualJobId" placeholder="batch_xxx" />
        </div>
        <div class="field" style="display: flex; align-items: flex-end;">
          <button class="ghost-button" type="button" @click="checkStatus(manualJobId)" :disabled="!manualJobId">
            查询
          </button>
        </div>
      </div>
      <div class="form-row">
        <div class="field">
          <label>按名称搜索</label>
          <input v-model="searchName" placeholder="输入任务名称关键词" @keyup.enter="searchJobs" />
        </div>
        <div class="field" style="display: flex; align-items: flex-end;">
          <button class="ghost-button" type="button" @click="searchJobs">搜索</button>
          <button class="ghost-button" type="button" @click="searchName = ''; searchResults = []">清空</button>
        </div>
      </div>

      <!-- 搜索结果 -->
      <div v-if="searchResults.length > 0" class="search-results">
        <div v-for="job in searchResults" :key="job.job_id" class="search-item" @click="selectJob(job)">
          <div class="search-item-header">
            <strong>{{ job.job_name || '未命名' }}</strong>
            <span class="q-status" :class="statusClass(job.status)">{{ statusText(job.status) }}</span>
          </div>
          <div class="search-item-meta">
            <span>{{ job.job_id }}</span>
            <span>总数: {{ job.total_count }} | 完成: {{ job.completed || 0 }}</span>
          </div>
        </div>
      </div>
    </section>

    <!-- 当前任务 -->
    <section v-if="currentJob" class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>任务状态</h2>
        <span class="q-status" :class="statusClass(currentJob.status)">
          {{ statusText(currentJob.status) }}
        </span>
      </div>

      <div class="job-info">
        <div class="job-meta">
          <p><strong>任务名称:</strong> {{ currentJob.job_name || '未命名' }}</p>
          <p><strong>任务ID:</strong> <code>{{ currentJob.job_id }}</code></p>
        </div>

        <!-- 进度条 -->
        <div class="progress-section">
          <div class="progress-stats">
            <span>总数: <strong>{{ currentJob.total_count }}</strong></span>
            <span>成功: <strong class="text-success">{{ currentJob.completed }}</strong></span>
            <span>失败: <strong class="text-danger">{{ currentJob.failed }}</strong></span>
          </div>
          <div class="progress-bar">
            <div class="progress-fill" :style="{ width: progressPercent + '%' }"></div>
          </div>
          <span class="progress-text">{{ progressPercent }}% 完成</span>
        </div>

        <!-- 操作按钮 -->
        <div v-if="isCompleted(currentJob.status)" class="job-actions">
          <button class="primary-button" type="button" @click="downloadResult(currentJob.job_id)">
            📥 下载结果并导入题库
          </button>
          <span class="action-hint">点击后将 {{ currentJob.completed }} 道题目导入题库</span>
        </div>

        <div v-if="currentJob.status === 'in_progress' || currentJob.status === 'validating'" class="job-actions">
          <button class="ghost-button" type="button" @click="checkStatus(currentJob.job_id)">
            🔄 刷新状态
          </button>
          <span class="polling-hint">⏱ 每10秒自动刷新，任务在云端执行中...</span>
        </div>

        <div v-if="currentJob.status === 'failed'" class="job-actions">
          <span class="text-danger">任务失败: {{ currentJob.error || '未知错误' }}</span>
        </div>
      </div>
    </section>

    <!-- 导入结果详情 -->
    <section v-if="importResult" class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>导入结果</h2>
      </div>

      <div class="import-summary">
        <span class="import-stat success">✅ 成功: {{ importResult.saved }} 题</span>
        <span v-if="importResult.failed > 0" class="import-stat fail">❌ 失败: {{ importResult.failed }} 题</span>
      </div>

      <div class="import-items">
        <div v-for="(item, idx) in importResult.items" :key="idx" class="import-item" :class="item.status">
          <div class="import-item-header">
            <code>{{ item.outline_code }}</code>
            <span v-if="item.status === 'ok'" class="import-ok">✅ 导入 {{ item.count }} 题</span>
            <span v-else class="import-fail">❌ {{ item.error }}</span>
          </div>
        </div>
      </div>
    </section>

    <!-- 任务历史 -->
    <section v-if="jobHistory.length > 0" class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>任务历史（{{ jobHistory.length }}）</h2>
      </div>

      <div class="job-list">
        <div v-for="job in jobHistory" :key="job.job_id" class="job-item" :class="{ 'job-active': currentJob?.job_id === job.job_id }">
          <div class="job-header">
            <div>
              <strong>{{ job.job_name || '未命名任务' }}</strong>
              <span class="job-id">{{ job.job_id }}</span>
            </div>
            <span class="q-status" :class="statusClass(job.status)">
              {{ statusText(job.status) }}
            </span>
          </div>
          <div class="job-detail">
            <span>总数: {{ job.total_count }}</span>
            <span>成功: {{ job.completed || 0 }}</span>
            <span>失败: {{ job.failed || 0 }}</span>
          </div>
          <div class="job-actions">
            <button class="ghost-button" type="button" @click="checkStatus(job.job_id)">刷新状态</button>
            <button v-if="isCompleted(job.status)" class="primary-button" type="button" @click="downloadResult(job.job_id)">
              📥 导入题库
            </button>
            <button class="ghost-button" type="button" @click="currentJob = job">查看详情</button>
          </div>
        </div>
      </div>
    </section>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
</template>

<style scoped>
.batch-layout {
  max-width: 800px;
  margin: 0 auto;
}

.info-text {
  font-size: 14px;
  color: #6e7b8f;
  line-height: 1.6;
  margin: 0;
}

.config-form {
  display: grid;
  gap: 16px;
}

.form-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

.field label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 6px;
}

.field input,
.field select {
  width: 100%;
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 10px;
  font-size: 14px;
}

.field-hint {
  display: block;
  font-size: 11px;
  color: #9aa5b4;
  margin-top: 4px;
}

.checkbox-row {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  font-size: 14px;
}

.checkbox-row input {
  width: 16px;
  height: 16px;
}

.job-info {
  font-size: 14px;
}

.job-info p {
  margin: 8px 0;
}

.job-actions {
  display: flex;
  gap: 10px;
  margin-top: 16px;
  align-items: center;
}

.polling-hint {
  font-size: 12px;
  color: #6e7b8f;
}

.job-list {
  display: grid;
  gap: 12px;
}

.job-item {
  padding: 12px;
  border: 1px solid #e5ebf3;
  border-radius: 8px;
}

.job-item.job-active {
  border-color: #1385f8;
  background: #f8fbff;
}

.job-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}

.job-id {
  font-family: monospace;
  font-size: 12px;
  color: #6e7b8f;
}

.job-detail {
  display: flex;
  gap: 16px;
  font-size: 13px;
  color: #435269;
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

.text-success { color: #087c55; }
.text-danger { color: #c54858; }

.job-meta {
  margin-bottom: 16px;
}

.job-meta code {
  font-size: 12px;
  background: #f0f3f7;
  padding: 2px 6px;
  border-radius: 4px;
}

.progress-section {
  margin: 16px 0;
}

.progress-stats {
  display: flex;
  gap: 20px;
  font-size: 14px;
  margin-bottom: 10px;
}

.progress-bar {
  height: 12px;
  background: #e5ebf3;
  border-radius: 6px;
  overflow: hidden;
}

.progress-fill {
  height: 100%;
  background: linear-gradient(90deg, #0571dc, #087c55);
  border-radius: 6px;
  transition: width 0.3s ease;
}

.progress-text {
  font-size: 12px;
  color: #6e7b8f;
  margin-top: 6px;
  display: block;
}

.action-hint {
  font-size: 12px;
  color: #6e7b8f;
}

.search-results {
  margin-top: 12px;
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  overflow: hidden;
}

.search-item {
  padding: 10px 12px;
  border-bottom: 1px solid #f0f3f7;
  cursor: pointer;
}

.search-item:last-child {
  border-bottom: none;
}

.search-item:hover {
  background: #f8fbff;
}

.search-item-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 4px;
}

.search-item-meta {
  display: flex;
  gap: 16px;
  font-size: 12px;
  color: #6e7b8f;
}

.import-summary {
  display: flex;
  gap: 16px;
  margin-bottom: 12px;
  font-size: 14px;
}

.import-stat.success { color: #087c55; font-weight: 600; }
.import-stat.fail { color: #c54858; font-weight: 600; }

.import-items {
  display: grid;
  gap: 8px;
}

.import-item {
  padding: 10px 12px;
  border-radius: 6px;
  font-size: 13px;
}

.import-item.ok { background: #f0fff8; border: 1px solid #d0f0e0; }
.import-item.failed { background: #fff5f5; border: 1px solid #fdd; }

.import-item-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.import-item-header code {
  font-size: 12px;
  background: rgba(0,0,0,0.05);
  padding: 2px 6px;
  border-radius: 4px;
}

.import-ok { color: #087c55; font-weight: 600; }
.import-fail { color: #c54858; }
</style>
