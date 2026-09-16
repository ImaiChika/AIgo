<script setup>
import { ref, computed, watch, onMounted, onActivated, onDeactivated, onBeforeUnmount } from "vue";
import { api } from "../api.js";
import { hasPerm } from "../auth.js";
import { useAICheckProgress } from "../aiCheckProgress.js";
import KnowledgePointPicker from "../components/KnowledgePointPicker.vue";
import QuestionDetailModal from "../components/QuestionDetailModal.vue";

// AI 检查分段进度（导入成功后轮询，见 aiCheckProgress.js）
const { progress: aiProgress, start: startAIProgress } = useAICheckProgress();
const batchPermission = computed(() => hasPerm("batch:run"));

const toast = ref("");
const stats = ref({ question_count: 0, knowledge_count: 0, knowledge_categories: {} });
const selectedKnowledgeVersion = ref(null);
const batchRuntime = ref({
  backend: "unknown",
  available: false,
  execution_mode: "unavailable",
  model: "",
  message: "正在读取批量执行器状态...",
});

// 与单题出题页使用同一统计口径：题库题量包含当前用户可见的正式、待审核、淘汰三层。
// question_count 只代表待审核层，不能直接用于这里的“个人题库”展示。
const personalQuestionCount = computed(() => {
  const counts = stats.value?.tier_counts;
  if (counts && Object.keys(counts).length) {
    return Object.values(counts).reduce((sum, value) => sum + (Number(value) || 0), 0);
  }
  return Number(stats.value?.question_count) || 0;
});

const personalTierSummary = computed(() => {
  const counts = stats.value?.tier_counts || {};
  return [
    ["formal", "正式"],
    ["working", "待审核"],
    ["eliminated", "淘汰"],
  ]
    .filter(([key]) => Object.prototype.hasOwnProperty.call(counts, key))
    .map(([key, label]) => `${label} ${Number(counts[key]) || 0}`)
    .join(" · ");
});

const currentKnowledgeCount = computed(() => {
  const versionCount = Number(selectedKnowledgeVersion.value?.point_count);
  if (Number.isFinite(versionCount)) return versionCount;
  return Number(stats.value?.kp_version?.total ?? stats.value?.knowledge_count) || 0;
});

const currentKnowledgeVersionName = computed(() => (
  selectedKnowledgeVersion.value?.name || stats.value?.kp_version?.version_name || ""
));

function handleKnowledgeVersionChange(payload) {
  selectedKnowledgeVersion.value = payload?.version || null;
}

// 批量任务配置（大纲要点通过选择器多选；仅保留每要点题数与跳过已有）
const selectedKPs = ref([]); // 选中的大纲要点（多选）
const batchConfig = ref({
  skip_existing: true,
  count: 1,
  job_name: "",
});

// 任务状态
const currentJob = ref(null);
const jobHistory = ref([]);
const polling = ref(false);
const importResult = ref(null); // 导入结果详情
const detailQuestion = ref(null); // 弹窗查看的题目全貌（复用题库详情弹窗）

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
  if (!hasPerm("stats:view")) return;
  try {
    stats.value = await api.stats("personal");
  } catch (e) {
    console.error(e);
    showToast("加载统计信息失败: " + e.message);
  }
}

async function loadBatchCapabilities() {
  try {
    batchRuntime.value = await api.batchCapabilities();
  } catch (e) {
    batchRuntime.value = {
      backend: "unknown",
      available: false,
      execution_mode: "unavailable",
      model: "",
      message: "无法读取批量执行器状态: " + e.message,
    };
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
      if (isRunning(currentJob.value.status)) {
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
const submitting = ref(false); // 提交在途守卫，防重复创建批量任务

async function submitBatch() {
  if (submitting.value) return;
  if (!batchPermission.value) {
    showToast("当前账号未分配批量推理权限，请联系超级管理员。");
    return;
  }
  if (!batchRuntime.value.available) {
    showToast("批量生成功能当前不可用，请联系系统管理员。");
    return;
  }
  if (!selectedKPs.value.length) {
    showToast("请先搜索并选择至少一个大纲要点");
    return;
  }
  submitting.value = true;
  try {
    const data = await api.batchSubmit({
      skip_existing: batchConfig.value.skip_existing,
      count: batchConfig.value.count,
      knowledge_point_ids: selectedKPs.value.map(k => k.id),
      version_id: selectedKPs.value[0].version_id,
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
  } finally {
    submitting.value = false;
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
        showToast(isCompleted(job.status) ? `任务完成：成功 ${job.completed}，失败 ${job.failed}` : `任务${statusText(job.status)}`);
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
  if (!batchPermission.value) {
    stopPolling();
    return;
  }
  if (currentJob.value && isRunning(currentJob.value.status)) startPolling(currentJob.value.job_id);
});
onDeactivated(() => { pageActive = false; stopPolling(); });
onBeforeUnmount(() => { disposed = true; stopPolling(); clearTimeout(showToast.timer); });

const importError = ref("");
const autoImportJobs = new Set(); // 本会话已触发过自动导入的任务；后端按任务幂等兜底

// 任务完成后自动导入一次，无需手动点击。仅针对本地有记录的任务（tracked）：
// 仅存在于云端列表的历史任务没有导入跟踪，自动导入会把历史结果重复写入。
// 重复触发由后端导入幂等拦截：已导入的任务只会重放上次的导入结果。
function maybeAutoImport(job) {
  if (!job || !isCompleted(job.status) || job.imported_at || !job.tracked) return;
  if (autoImportJobs.has(job.job_id)) return;
  autoImportJobs.add(job.job_id);
  downloadResult(job.job_id);
}
watch(currentJob, (job) => maybeAutoImport(job));

function markJobImported(jobId) {
  const now = new Date().toISOString();
  if (currentJob.value?.job_id === jobId) currentJob.value = { ...currentJob.value, imported_at: now };
  const idx = jobHistory.value.findIndex(j => j.job_id === jobId);
  if (idx >= 0) jobHistory.value[idx] = { ...jobHistory.value[idx], imported_at: now };
}

// 下载并导入结果
async function downloadResult(jobId) {
  importError.value = "";
  try {
    importResult.value = null;
    const data = await api.batchDownload(jobId);
    importResult.value = data;
    markJobImported(jobId);
    if (data.simulated) {
      showToast(data.message || "旧批量任务已完成");
      return;
    }
    if (data.failed > 0) {
      showToast(`导入完成：成功 ${data.saved} 题，失败 ${data.failed} 项（AI 检查进行中）`);
    } else {
      showToast(`导入成功：共 ${data.saved} 题（AI 检查进行中）`);
    }
    startAIProgress(data.question_ids || []);
    loadStats();
  } catch (e) {
    importError.value = e.message;
    showToast("自动导入失败: " + e.message);
  }
}

// 本次导入的题目行：以导入顺序为基线编号，AI 检查进度提供状态与题干缩略信息。
// 列表只承载关键信息；点击检查通过的行弹窗查看完整题目。
const importRows = computed(() => {
  const items = new Map((aiProgress.value?.items || []).map(i => [i.question_id, i]));
  return (importResult.value?.question_ids || []).map((id, idx) => {
    const it = items.get(id) || {};
    return {
      no: idx + 1,
      id,
      stem: it.stem_summary || "",
      passed: it.question_status === "ai_reviewed",
      checking: !it.task_status || it.task_status === "pending" || it.task_status === "running",
      exhausted: it.task_status === "exhausted",
      discarded: !!it.discarded,
      reason: it.suggestion || "",
    };
  });
});
// 主列表只保留未淘汰的题；淘汰题移入下方失败提醒，附原因
const passedRows = computed(() => importRows.value.filter(r => !r.discarded));
const discardedRows = computed(() => importRows.value.filter(r => r.discarded));
const failedItems = computed(() => (importResult.value?.items || []).filter(i => i.status !== "ok"));
const checkDone = computed(() => !!aiProgress.value && !aiProgress.value.stalled && aiProgress.value.checking === 0);

async function openQuestion(id) {
  try {
    detailQuestion.value = await api.getQuestion(id);
  } catch (e) {
    showToast("获取题目详情失败: " + e.message);
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
  // DashScope 状态：validating / in_progress / finalizing / completed / expired / cancelling / cancelled
  const map = {
    validating: "校验中",
    in_progress: "运行中",
    finalizing: "结果整理中",
    completed: "已完成",
    complete: "已完成",   // DashScope 实际可能返回这个
    failed: "失败",
    expired: "已过期",
    cancelling: "取消中",
    cancelled: "已取消",
  };
  return map[status] || status;
}

function statusClass(status) {
  if (status === "completed" || status === "complete") return "status-good";
  if (status === "failed" || status === "expired") return "status-bad";
  if (["in_progress", "validating", "finalizing", "cancelling"].includes(status)) return "status-active";
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

onMounted(() => {
  if (!batchPermission.value) return;
  loadStats();
  loadBatchCapabilities();
  loadJobsFromDB();
});
</script>

<template>
  <section v-if="!batchPermission" class="panel batch-permission-panel" role="alert">
    <div class="section-heading">
      <span class="dot red"></span>
      <h2>暂无批量推理权限</h2>
    </div>
    <p>当前账号不能查看、提交或导入批量推理任务；已分配的单题出题权限不受影响。</p>
    <p class="permission-help">如需开放批量推理，请联系超级管理员在「用户管理 → 分配权限」中单独勾选“批量推理”。</p>
    <RouterLink class="ghost-button inline-link" :to="hasPerm('question:generate') ? '/generate' : '/knowledge'">
      {{ hasPerm('question:generate') ? '返回单题出题' : '返回知识点' }}
    </RouterLink>
  </section>

  <div v-else class="batch-layout">
    <!-- 统计信息 -->
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>批量生成</h2>
      </div>

      <div v-if="hasPerm('stats:view')" class="metric-row">
        <div>
          <span>个人题库</span>
          <strong>{{ personalQuestionCount }}道</strong>
          <small v-if="personalTierSummary">{{ personalTierSummary }}</small>
        </div>
        <div>
          <span>当前大纲要点</span>
          <strong>{{ currentKnowledgeCount }}个</strong>
          <small v-if="currentKnowledgeVersionName" :title="currentKnowledgeVersionName">{{ currentKnowledgeVersionName }}</small>
        </div>
      </div>

    </section>

    <!-- 配置 -->
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>生成配置</h2>
      </div>

      <div class="config-form">
        <div class="batch-runtime-note" :class="{ local: batchRuntime.execution_mode === 'local_single_api', unavailable: !batchRuntime.available }">
          <span class="runtime-badge">{{ batchRuntime.execution_mode === 'local_single_api' ? '单题 API 队列' : batchRuntime.available ? '已配置' : '不可用' }}</span>
          <div>
            <strong>批量任务逐题调用单题生成 API</strong>
            <p>当前模型：{{ batchRuntime.model || '未配置' }} · {{ batchRuntime.message }}</p>
          </div>
        </div>
        <!-- 大纲要点选择（搜索勾选多选，也可按大纲代码逗号分隔加入） -->
        <div class="field">
          <label>选择大纲要点</label>
          <KnowledgePointPicker v-model="selectedKPs" :multiple="true" placeholder="搜索大纲要点、大纲代码或专业" @version-change="handleKnowledgeVersionChange" />
          <span class="field-hint">已选 {{ selectedKPs.length }} 个大纲要点，每个要点将生成 {{ batchConfig.count }} 道题</span>
        </div>

        <div class="form-row">
          <div class="field">
            <label>每大纲要点题数</label>
            <input v-model.number="batchConfig.count" type="number" min="1" max="20" />
            <span class="field-hint">建议每个大纲要点生成 1—3 道</span>
          </div>
          <div class="field">
            <label>任务名称（可选）</label>
            <input v-model="batchConfig.job_name" placeholder="如: 呼吸系统第一批" />
          </div>
        </div>

        <div class="form-row">
          <label class="checkbox-row">
            <input type="checkbox" v-model="batchConfig.skip_existing" />
            <span>跳过已有题目的大纲要点</span>
          </label>
        </div>

        <button class="primary-button full" type="button" @click="submitBatch" :disabled="submitting || !selectedKPs.length || !batchRuntime.available">
          {{ submitting ? "提交中..." : `提交生成任务（${selectedKPs.length} 个大纲要点 × ${batchConfig.count} 题）` }}
        </button>
        <span v-if="!batchRuntime.available" class="field-hint">批量生成功能当前不可用，请联系系统管理员。</span>
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
            <span>生成项：<strong>{{ currentJob.total_count }}</strong></span>
            <span>已完成：<strong class="text-success">{{ currentJob.completed }}</strong></span>
            <span>失败：<strong class="text-danger">{{ currentJob.failed }}</strong></span>
          </div>
          <div class="progress-bar">
            <div class="progress-fill" :style="{ width: progressPercent + '%' }"></div>
          </div>
          <span class="progress-text">{{ progressPercent }}% 完成</span>
        </div>

        <!-- 任务完成后的导入：自动触发，无需手动点击 -->
        <div v-if="isCompleted(currentJob.status) && currentJob.imported_at" class="job-actions">
          <span class="action-hint">结果已自动导入题库，详见下方导入结果</span>
        </div>
        <div v-else-if="isCompleted(currentJob.status) && importError" class="job-actions">
          <button class="ghost-button" type="button" @click="downloadResult(currentJob.job_id)">重新导入</button>
          <span class="action-hint text-danger">自动导入失败：{{ importError }}</span>
        </div>
        <div v-else-if="isCompleted(currentJob.status)" class="job-actions">
          <span class="action-hint">正在自动导入生成结果…</span>
        </div>

        <div v-if="isRunning(currentJob.status)" class="job-actions">
          <button class="ghost-button" type="button" @click="checkStatus(currentJob.job_id)">
            刷新状态
          </button>
          <span class="polling-hint">任务执行中，每 10 秒刷新一次</span>
        </div>

        <div v-if="currentJob.status === 'failed' || currentJob.status === 'expired'" class="job-actions">
          <span class="text-danger">任务{{ statusText(currentJob.status) }}: {{ currentJob.error || '请检查任务详情或重新提交' }}</span>
        </div>
      </div>
    </section>

    <!-- 导入结果：一行一题的固定横栏，只显示关键缩略信息，点击查看全貌 -->
    <section v-if="importResult" class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>导入结果</h2>
      </div>

      <p v-if="importResult.simulated" class="result-summary simulated-result">{{ importResult.message }}</p>
      <p v-else class="result-summary">
        成功导入 <b>{{ importRows.length }}</b> 题<template v-if="importResult.failed > 0">，失败 <b class="text-danger">{{ importResult.failed }}</b> 项</template>
      </p>
      <p v-if="aiProgress && aiProgress.checking > 0" class="result-check">AI 检查中（已完成 {{ aiProgress.total - aiProgress.checking }}/{{ aiProgress.total }}），只有通过检查的题目才能点击查看</p>
      <p v-else-if="aiProgress && aiProgress.stalled" class="result-check">AI 检查仍在后台进行，可稍后查看结果</p>
      <p v-else-if="checkDone" class="result-check">AI 检查完成：通过 {{ aiProgress.passed }}<template v-if="aiProgress.issues"> · 有问题 {{ aiProgress.issues }}</template><template v-if="aiProgress.exhausted"> · 检查异常 {{ aiProgress.exhausted }}</template><template v-if="aiProgress.discarded"> · 淘汰 {{ aiProgress.discarded }}</template></p>

      <div class="result-list">
        <div v-for="row in passedRows" :key="row.id" class="result-row" :class="{ clickable: row.passed }" @click="row.passed && openQuestion(row.id)">
          <span class="row-no">{{ row.no }}</span>
          <span class="row-stem">{{ row.stem || row.id }}</span>
          <span v-if="row.passed" class="row-state passed">已通过</span>
          <span v-else-if="row.checking" class="row-state checking">检查中</span>
          <span v-else-if="row.exhausted" class="row-state failed">检查失败</span>
          <span v-else class="row-state">待检查</span>
        </div>
      </div>
      <p class="result-hint">点击题目行查看完整内容；未通过 AI 检查的题目不会出现在上表。</p>

      <!-- 失败与淘汰提醒：持久显示，不随 toast 消失 -->
      <div v-if="failedItems.length" class="result-notice">
        <p class="notice-title">导入失败（{{ failedItems.length }} 项）</p>
        <p v-for="(f, i) in failedItems" :key="i" class="notice-line">{{ f.outline_code }}：{{ f.error }}</p>
      </div>
      <div v-if="discardedRows.length" class="result-notice">
        <p class="notice-title">未通过 AI 检查，已自动淘汰（{{ discardedRows.length }} 题）</p>
        <p v-for="row in discardedRows" :key="row.id" class="notice-line">序号{{ row.no }} {{ row.stem || row.id }}：{{ row.reason || "质量不达标" }}</p>
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
            <span v-if="job.imported_at" class="q-status status-good">已导入</span>
          </div>
          <div class="job-detail">
            <span>生成项：{{ job.total_count }}</span>
            <span>已完成：{{ job.completed || 0 }}</span>
            <span>失败：{{ job.failed || 0 }}</span>
          </div>
          <div class="job-actions">
            <button class="ghost-button" type="button" @click="checkStatus(job.job_id)">刷新状态</button>
            <button class="ghost-button" type="button" @click="currentJob = job">查看详情</button>
          </div>
        </div>
      </div>
    </section>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>

  <!-- 题目全貌弹窗（与题库/审核页共用同一组件） -->
  <QuestionDetailModal v-if="detailQuestion" :question="detailQuestion" @close="detailQuestion = null" />
</template>

<style scoped>
.batch-layout {
  max-width: 800px;
  margin: 0 auto;
}

.batch-permission-panel {
  max-width: 700px;
  margin: 24px auto;
  padding: 28px 30px;
  border-color: #f1d4d8;
  background: #fffafb;
}

.batch-permission-panel p {
  margin: 8px 0;
  color: #59677a;
  font-size: 13px;
  line-height: 1.7;
}

.batch-permission-panel .permission-help {
  color: #a23b4b;
}

.inline-link {
  display: inline-flex;
  margin-top: 12px;
  text-decoration: none;
}

.config-form {
  display: grid;
  gap: 16px;
}

.batch-runtime-note {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  border: 1px solid #dce8f7;
  border-left: 3px solid #1385f8;
  border-radius: 7px;
  padding: 10px 12px;
  background: #f8fbff;
  color: #52657c;
}

.batch-runtime-note.local {
  border-color: #c6e7dc;
  border-left-color: #12b981;
  background: #f3fcf8;
}

.batch-runtime-note.unavailable {
  border-color: #f0c9cf;
  border-left-color: #c54858;
  background: #fff7f8;
}

.batch-runtime-note strong { display: block; color: #33475f; font-size: 12px; }
.batch-runtime-note p { margin: 3px 0 0; color: #718197; font-size: 11px; line-height: 1.5; }
.runtime-badge { flex: none; border-radius: 12px; padding: 3px 8px; color: #1268ae; background: #eaf5ff; font-size: 11px; font-weight: 700; }
.local .runtime-badge { color: #16724f; background: #e3f7ef; }
.unavailable .runtime-badge { color: #a23b4b; background: #ffeaed; }

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

/* 导入结果：汇总一行 + 固定横栏逐行延伸 */
.result-summary {
  font-size: 14px;
  margin: 0 0 8px;
}

.simulated-result {
  border: 1px solid #c6e7dc;
  border-radius: 6px;
  padding: 10px 12px;
  color: #16724f;
  background: #f3fcf8;
}

.result-check {
  font-size: 13px;
  color: #6e7b8f;
  margin: 0 0 10px;
}

.result-list {
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  overflow-y: auto;
  max-height: 360px;
}

.result-row {
  display: flex;
  align-items: center;
  gap: 12px;
  height: 40px;
  padding: 0 12px;
  border-bottom: 1px solid #f0f3f7;
  font-size: 13px;
  background: #fff;
}

.result-row:last-child {
  border-bottom: none;
}

.result-row.clickable {
  cursor: pointer;
}

.result-row.clickable:hover {
  background: #f8fbff;
}

.row-no {
  flex: none;
  width: 28px;
  font-size: 12px;
  color: #9aa5b4;
}

.row-stem {
  flex: 1;
  color: #3a4658;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.row-state {
  flex: none;
  font-size: 11px;
  font-weight: 700;
  padding: 2px 8px;
  border-radius: 4px;
  background: #f0f3f7;
  color: #6e7b8f;
}

.row-state.passed { background: #f0fff8; color: #087c55; }
.row-state.checking { background: #eff8ff; color: #0571dc; }
.row-state.failed { background: #fff0f0; color: #c54858; }

.result-hint {
  margin: 8px 0 0;
  font-size: 11px;
  color: #9aa5b4;
}

.result-notice {
  margin-top: 12px;
  padding: 10px 12px;
  border-radius: 6px;
  background: #fff5f5;
  border: 1px solid #fdd;
}

.notice-title {
  margin: 0 0 4px;
  font-size: 13px;
  font-weight: 700;
  color: #c54858;
}

.notice-line {
  margin: 2px 0;
  font-size: 12px;
  color: #8a3b47;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
