<script setup>
import { ref, computed, watch, nextTick, onMounted, onActivated, onDeactivated, onBeforeUnmount } from "vue";
import { api } from "../api.js";
import { hasPerm, currentUser } from "../auth.js";
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
const importResult = ref(null); // 导入结果详情（落库为 AI 草稿，需经 AI 检查后才可见）
const detailQuestion = ref(null); // 弹窗查看的题目全貌（复用题库详情弹窗）
const currentJobOwned = computed(() => !!currentJob.value && currentJob.value.owner_id === currentUser.value?.id);

// 等待计时：每秒刷新一次当前时间，任务项展示自提交起已用时。
const nowTick = ref(Date.now());
let tickTimer = null;

function jobStartMs(job) {
  if (!job) return null;
  if (job.created_at) return Number(job.created_at) * 1000;
  if (job.submitted_at) return Number(job.submitted_at);
  return null;
}

function elapsedText(job) {
  const start = jobStartMs(job);
  if (!start) return "—";
  let sec = Math.max(0, Math.floor((jobEndMs(job) - start) / 1000));
  const hours = Math.floor(sec / 3600);
  sec %= 3600;
  const minutes = Math.floor(sec / 60);
  const seconds = sec % 60;
  const mmss = `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
  return hours > 0 ? `${hours}:${mmss}` : mmss;
}

// 计时终点：执行中任务实时走表；终态任务冻结在后端记录的完成时间
// （completed_at），旧数据缺失时退回导入时间（导入只发生在完成后），
// 修复"任务已完成仍在计时"的问题。
function jobEndMs(job) {
  if (Number(job?.completed_at) > 0) return Number(job.completed_at) * 1000;
  if (isTerminal(job?.status) && job.imported_at) {
    const t = Date.parse(job.imported_at);
    if (!Number.isNaN(t)) return t;
  }
  return nowTick.value;
}

// 逐单元生成明细：顺序与提交的知识点展开一致，老师按知识点核对成功/失败。
const unitRows = computed(() => {
  const items = currentJob.value?.items || [];
  return items.map((it, idx) => ({
    idx: idx + 1,
    code: it.outline_code || "—",
    topic: it.topic || "",
    ok: it.status === "ok",
    error: it.status === "ok" ? "" : (it.error || "生成失败"),
  }));
});

// 生成明细分页（与题库一致的左右翻页），避免大量明细只能滚动查看。
const unitPage = ref(1);
const unitPageSize = 10;
const unitPageCount = computed(() => Math.max(1, Math.ceil(unitRows.value.length / unitPageSize)));
const pagedUnitRows = computed(() => {
  const start = (unitPage.value - 1) * unitPageSize;
  return unitRows.value.slice(start, start + unitPageSize);
});
function goUnitPage(p) {
  unitPage.value = Math.min(Math.max(1, p), unitPageCount.value);
}
watch(() => currentJob.value?.job_id, () => { unitPage.value = 1; });
watch(unitPageCount, (n) => { if (unitPage.value > n) unitPage.value = n; });

// 任务历史分页：任务卡片较高，每页 5 条，页码翻页不必长滚动。
const historyPage = ref(1);
const historyPageSize = 5;
const historyPageCount = computed(() => Math.max(1, Math.ceil(jobHistory.value.length / historyPageSize)));
const pagedJobHistory = computed(() => {
  const start = (historyPage.value - 1) * historyPageSize;
  return jobHistory.value.slice(start, start + historyPageSize);
});
function goHistoryPage(p) {
  historyPage.value = Math.min(Math.max(1, p), historyPageCount.value);
}
watch(historyPageCount, (n) => { if (historyPage.value > n) historyPage.value = n; });

// 尚未出结果的单元数：排队等待或生成中。
const pendingUnitCount = computed(() => {
  const job = currentJob.value;
  if (!job) return 0;
  return Math.max(0, (job.total_count || 0) - (job.items || []).length);
});

// 任务已进队列但还没有任何单元出结果：提示并发名额等待。
const queueWaiting = computed(() => {
  const job = currentJob.value;
  return !!job && isRunning(job.status) && (job.items || []).length === 0 && (job.total_count || 0) > 0;
});

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
      selectJob(jobHistory.value[0], { silent: true });
    }
  } catch (e) {
    console.error("加载任务历史失败:", e);
    showToast("加载任务历史失败，显示本地缓存");
    // 回退到 localStorage
    const jobs = JSON.parse(localStorage.getItem("batch_jobs") || "[]");
    jobHistory.value = jobs;
  }
}

// 选中任务：先展示列表摘要，再拉取完整状态补齐逐单元明细（明细只在
// 状态接口返回，列表接口不带；终态任务没有轮询，必须主动水合一次）。
// silent=true 用于页面初次自动选中：不弹提示也不滚动。
const jobPanel = ref(null);
function selectJob(job, { silent = false } = {}) {
  if (!job) return;
  currentJob.value = job;
  hydrateJobDetail(job.job_id);
  if (isRunning(job.status)) {
    if (!silent) {
      showToast("任务仍在进行中：生成明细实时更新，完整结果与 AI 检查将在任务彻底结束后展示");
    }
    startPolling(job.job_id);
  }
  if (!silent) {
    nextTick(() => jobPanel.value?.scrollIntoView({ behavior: "smooth", block: "start" }));
  }
}

async function hydrateJobDetail(jobId) {
  try {
    const full = await api.batchStatus(jobId);
    if (currentJob.value?.job_id !== jobId) return;
    currentJob.value = full;
    const idx = jobHistory.value.findIndex(j => j.job_id === jobId);
    if (idx >= 0) jobHistory.value[idx] = full;
    maybeLoadImportReplay(full);
  } catch (e) {
    // 明细拉取失败静默降级：摘要信息（计数/进度）仍然可用
  }
}

// 历史任务回看：已完成的本人任务在选中时重放后端保存的导入结果
// （幂等，不重复入库），让"查看详情"在重新登录后仍有结果可看。
function maybeLoadImportReplay(job) {
  if (!job || !isCompleted(job.status) || !job.imported_at) return;
  if (job.owner_id && job.owner_id !== currentUser.value?.id) return;
  if (importJobId.value === job.job_id && importResult.value) return;
  downloadResult(job.job_id, { replay: true });
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

    // 新任务开始：清空上一任务的结果展示，避免旧明细与新任务混淆。
    importResult.value = null;
    importJobId.value = "";
    importError.value = "";
    currentJob.value = {
      job_id: data.job_id,
      owner_id: currentUser.value?.id || "",
      status: "validating",
      total_count: data.count,
      completed: 0,
      failed: 0,
      submitted_at: Date.now(),
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

const importError = ref("");
const importJobId = ref(""); // importResult 所属任务；切换任务时隐藏过期结果
const autoImportJobs = new Set(); // 本会话已触发过自动导入的任务；后端按任务幂等兜底

// 任务完成后自动导入一次，无需手动点击。仅针对本地有记录的任务（tracked）：
// 仅存在于云端列表的历史任务没有导入跟踪，自动导入会把历史结果重复写入。
// 存在失败项时不自动导入：等出题人决定“重跑失败项”或“直接导入成功部分”——
// 一旦导入，失败项视为放弃、不能再重跑。
// 重复触发由后端导入幂等拦截：已导入的任务只会重放上次的导入结果。
function maybeAutoImport(job) {
	if (!job || job.owner_id !== currentUser.value?.id || !isCompleted(job.status) || job.imported_at || !job.tracked) return;
  if ((job.failed || 0) > 0) return;
  if (autoImportJobs.has(job.job_id)) return;
  autoImportJobs.add(job.job_id);
  downloadResult(job.job_id);
}
watch(currentJob, (job) => maybeAutoImport(job));

// 重跑失败项：任务回到执行中，复用既有轮询；完成后若无失败项则自动导入。
const retrying = ref(false);
async function retryFailed(job) {
  if (retrying.value || !job) return;
  retrying.value = true;
  try {
    const updated = await api.batchRetryFailed(job.job_id);
    currentJob.value = updated;
    const idx = jobHistory.value.findIndex(j => j.job_id === job.job_id);
    if (idx >= 0) jobHistory.value[idx] = updated;
    saveJobToStorage(updated);
    showToast("失败项重跑已开始，任务回到执行中");
    startPolling(job.job_id);
  } catch (e) {
    showToast("重跑失败: " + e.message);
  } finally {
    retrying.value = false;
  }
}

function markJobImported(jobId) {
  const now = new Date().toISOString();
  if (currentJob.value?.job_id === jobId) currentJob.value = { ...currentJob.value, imported_at: now };
  const idx = jobHistory.value.findIndex(j => j.job_id === jobId);
  if (idx >= 0) jobHistory.value[idx] = { ...jobHistory.value[idx], imported_at: now };
}

// 下载并导入结果。replay=true 用于历史任务回看：后端幂等重放上次的导入
// 结果，不重复入库，仅恢复展示；题目落库为 AI 草稿，需通过 AI 检查后才
// 进入个人题库（待审核）。
async function downloadResult(jobId, { replay = false } = {}) {
  importError.value = "";
  try {
    if (!replay) importResult.value = null;
    const data = await api.batchDownload(jobId);
    importResult.value = data;
    importJobId.value = jobId;
    markJobImported(jobId);
    if (data.simulated) {
      showToast(data.message || "旧批量任务已完成");
      return;
    }
    if (replay) {
      showToast("已载入该任务的生成与 AI 检查结果");
    } else if (data.failed > 0) {
      showToast(`AI 生成完毕：${data.saved} 题已提交 AI 质量检查，失败 ${data.failed} 项`);
    } else {
      showToast(`AI 生成完毕：共 ${data.saved} 题，已提交 AI 质量检查`);
    }
    startAIProgress(data.question_ids || []);
    loadStats();
  } catch (e) {
    if (replay) return; // 回看失败静默：详情面板仍显示任务摘要
    importError.value = e.message;
    showToast("结果提交失败: " + e.message);
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

// 手动查询状态：同步当前任务与历史列表，保持两处一致
async function checkStatus(jobId) {
  try {
    const job = await api.batchStatus(jobId);
    stopPolling();
    currentJob.value = job;
    const idx = jobHistory.value.findIndex(j => j.job_id === jobId);
    if (idx >= 0) jobHistory.value[idx] = job;
    maybeLoadImportReplay(job);
    if (isRunning(job.status)) startPolling(jobId);
    showToast(`状态: ${statusText(job.status)}`);
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
  tickTimer = setInterval(() => { nowTick.value = Date.now(); }, 1000);
});
onBeforeUnmount(() => {
  disposed = true;
  stopPolling();
  clearInterval(tickTimer);
  clearTimeout(showToast.timer);
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
    <section v-if="currentJob" ref="jobPanel" class="panel job-status-panel">
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
          <p><strong>已用时:</strong> <span class="elapsed-time">{{ elapsedText(currentJob) }}</span><span v-if="isRunning(currentJob.status)" class="field-hint">（含排队等待）</span></p>
        </div>

        <!-- 进度条 -->
        <div class="progress-section">
          <div class="progress-stats">
            <span>生成项：<strong>{{ currentJob.total_count }}</strong></span>
            <span>已完成：<strong class="text-success">{{ currentJob.completed }}</strong></span>
            <span>失败：<strong class="text-danger">{{ currentJob.failed }}</strong></span>
            <span v-if="pendingUnitCount > 0 && isRunning(currentJob.status)">排队/生成中：<strong>{{ pendingUnitCount }}</strong></span>
          </div>
          <div class="progress-bar">
            <div class="progress-fill" :style="{ width: progressPercent + '%' }"></div>
          </div>
          <span class="progress-text">{{ progressPercent }}% 完成</span>
          <p v-if="queueWaiting" class="queue-hint">
            任务已进入本地队列：正在等待全局并发名额（上限 {{ batchRuntime?.concurrency || '—' }}）或生成首批题目；排队与生成时间均计入已用时。
          </p>
        </div>

        <!-- 逐单元明细：按提交顺序展示每个知识点的成功/失败与原因（分页查看） -->
        <div v-if="unitRows.length" class="unit-section">
          <div class="unit-head">
            <span>生成明细 {{ unitRows.length }}/{{ currentJob.total_count || unitRows.length }}</span>
            <span v-if="pendingUnitCount > 0 && isRunning(currentJob.status)" class="unit-pending">剩余 {{ pendingUnitCount }} 项排队/生成中</span>
          </div>
          <div class="unit-rows">
            <div v-for="row in pagedUnitRows" :key="row.idx" class="unit-row">
              <span class="unit-no">{{ row.idx }}</span>
              <span class="unit-code">{{ row.code }}</span>
              <span class="unit-topic" :title="row.topic">{{ row.topic || "—" }}</span>
              <span v-if="row.ok" class="unit-state ok">成功</span>
              <span v-else class="unit-state bad">失败</span>
              <span v-if="row.error" class="unit-error" :title="row.error">{{ row.error }}</span>
            </div>
          </div>
          <div v-if="unitPageCount > 1" class="pager-row">
            <span class="page-total">共 {{ unitRows.length }} 项</span>
            <div class="page-pager">
              <button class="page-btn" type="button" :disabled="unitPage <= 1" @click="goUnitPage(unitPage - 1)">‹ 上一页</button>
              <span class="page-info">第 {{ unitPage }} / {{ unitPageCount }} 页</span>
              <button class="page-btn" type="button" :disabled="unitPage >= unitPageCount" @click="goUnitPage(unitPage + 1)">下一页 ›</button>
            </div>
          </div>
        </div>

        <!-- 任务完成后的处理：自动触发，无需手动点击 -->
	    <div v-if="!currentJobOwned" class="job-actions">
	      <span class="field-hint">其他用户任务仅供管理员查看，只有原提交人可以导入结果。</span>
	    </div>
	    <div v-else-if="isCompleted(currentJob.status) && currentJob.imported_at" class="job-actions">
	      <span class="action-hint">AI 生成完毕，结果已提交 AI 质量检查；通过检查的题目才会进入个人题库（待审核）</span>
	    </div>
	    <div v-else-if="isCompleted(currentJob.status) && (currentJob.failed || 0) > 0" class="job-actions job-retry-actions">
	      <button class="ghost-button" type="button" :disabled="retrying" @click="retryFailed(currentJob)">
	        {{ retrying ? "重跑中…" : `重跑失败项（${currentJob.failed}）` }}
	      </button>
	      <button class="ghost-button" type="button" @click="downloadResult(currentJob.job_id)">直接导入成功部分</button>
	      <span class="action-hint">失败项可重跑；一旦导入成功部分，本任务剩余失败项将不能再重跑</span>
	    </div>
	    <div v-else-if="isCompleted(currentJob.status) && importError" class="job-actions">
	      <button class="ghost-button" type="button" @click="downloadResult(currentJob.job_id)">重新导入</button>
	      <span class="action-hint text-danger">结果提交失败：{{ importError }}</span>
	    </div>
	    <div v-else-if="isCompleted(currentJob.status)" class="job-actions">
	      <span class="action-hint">AI 生成完毕，正在提交 AI 质量检查…</span>
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

    <!-- 生成结果与 AI 检查：一行一题的固定横栏，只显示关键缩略信息，点击查看全貌 -->
    <section v-if="importResult && (!currentJob || importJobId === currentJob.job_id)" class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>生成结果 · AI 检查</h2>
      </div>

      <p v-if="importResult.simulated" class="result-summary simulated-result">{{ importResult.message }}</p>
      <p v-else class="result-summary">
        AI 共生成 <b>{{ importRows.length }}</b> 题，已提交 AI 质量检查<template v-if="importResult.failed > 0">，失败 <b class="text-danger">{{ importResult.failed }}</b> 项</template>；通过检查后进入个人题库（待审核）
      </p>
      <p v-if="aiProgress && aiProgress.checking > 0" class="result-check">AI 检查中（已完成 {{ aiProgress.total - aiProgress.checking }}/{{ aiProgress.total }}）：通过检查的题目才会进入个人题库并展示内容</p>
      <p v-else-if="aiProgress && aiProgress.stalled" class="result-check">AI 检查仍在后台进行，可稍后查看结果</p>
      <p v-else-if="checkDone" class="result-check">
        AI 检查完成：通过 {{ aiProgress.passed }}<template v-if="aiProgress.issues"> · 有问题 {{ aiProgress.issues }}</template><template v-if="aiProgress.exhausted"> · 检查异常 {{ aiProgress.exhausted }}</template><template v-if="aiProgress.discarded"> · 淘汰 {{ aiProgress.discarded }}</template>
        <template v-if="aiProgress.passed > 0">；通过的题目已进入个人题库（待审核）</template>
        <RouterLink v-if="aiProgress.passed > 0" class="inline-link" to="/bank">去题库查看</RouterLink>
      </p>

      <div class="result-list">
        <div v-for="row in passedRows" :key="row.id" class="result-row" :class="{ clickable: row.passed }" @click="row.passed && openQuestion(row.id)">
          <span class="row-no">{{ row.no }}</span>
          <span class="row-stem">{{ row.stem || (row.passed ? row.id : "检查通过后展示题目内容") }}</span>
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

    <!-- 任务历史（分页，每页 5 条） -->
    <section v-if="jobHistory.length > 0" class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>任务历史（{{ jobHistory.length }}）</h2>
      </div>

      <div class="job-list">
        <div v-for="job in pagedJobHistory" :key="job.job_id" class="job-item" :class="{ 'job-active': currentJob?.job_id === job.job_id }">
          <div class="job-header">
            <div>
              <strong>{{ job.job_name || '未命名任务' }}</strong>
              <span class="job-id">{{ job.job_id }}</span>
            </div>
            <span class="q-status" :class="statusClass(job.status)">
              {{ statusText(job.status) }}
            </span>
            <!-- 已导入徽标只在任务彻底结束后展示，进行中任务不显示 -->
            <span v-if="isCompleted(job.status) && job.imported_at" class="q-status status-good">已导入</span>
          </div>
	          <div class="job-detail">
	            <span>生成项：{{ job.total_count }}</span>
            <span>已完成：{{ job.completed || 0 }}</span>
            <span>失败：{{ job.failed || 0 }}</span>
            <span>已用时：{{ elapsedText(job) }}</span>
            <span v-if="job.owner_id && job.owner_id !== currentUser?.id">其他用户任务 · 只读</span>
          </div>
          <div class="job-actions">
            <button class="ghost-button" type="button" @click="checkStatus(job.job_id)">刷新状态</button>
            <button class="ghost-button" type="button" @click="selectJob(job)">查看详情</button>
            <span v-if="isRunning(job.status)" class="polling-hint">任务进行中，详情实时更新</span>
          </div>
        </div>
      </div>
      <div v-if="historyPageCount > 1" class="pager-row">
        <span class="page-total">共 {{ jobHistory.length }} 个任务</span>
        <div class="page-pager">
          <button class="page-btn" type="button" :disabled="historyPage <= 1" @click="goHistoryPage(historyPage - 1)">‹ 上一页</button>
          <span class="page-info">第 {{ historyPage }} / {{ historyPageCount }} 页</span>
          <button class="page-btn" type="button" :disabled="historyPage >= historyPageCount" @click="goHistoryPage(historyPage + 1)">下一页 ›</button>
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
.elapsed-time {
  font-variant-numeric: tabular-nums;
  color: #1f5eff;
  font-weight: 600;
}
.job-retry-actions {
  flex-wrap: wrap;
}
.unit-section {
  margin-top: 12px;
  border: 1px solid #dce8f7;
  border-radius: 8px;
  background: #fbfdff;
}
.unit-head {
  display: flex;
  justify-content: space-between;
  gap: 10px;
  padding: 8px 12px;
  border-bottom: 1px solid #e7eef8;
  color: #3f4e63;
  font-size: 12px;
  font-weight: 600;
}
.unit-pending {
  color: #8a97a8;
  font-weight: 400;
}
.unit-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 5px 12px;
  font-size: 12px;
  border-bottom: 1px dashed #eef3fa;
}
.unit-row:last-child { border-bottom: none; }
.unit-no { width: 26px; color: #8a97a8; flex: none; text-align: right; }
.unit-code { width: 130px; color: #1f5eff; font-family: ui-monospace, Menlo, monospace; flex: none; }
.unit-topic { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: #3f4e63; }
.unit-state { flex: none; font-weight: 600; }
.unit-state.ok { color: #12805c; }
.unit-state.bad { color: #c23b3b; }
.unit-error { flex: 1.4; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: #a15a5a; }
.queue-hint {
  margin: 8px 0 0;
  color: #8a6d1f;
  background: #fdf6e7;
  border: 1px solid #f0e0b5;
  border-radius: 6px;
  padding: 6px 10px;
  font-size: 12px;
}
/* 分页（与题库页同款）：明细与任务历史共用 */
.pager-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  border-top: 1px solid #e7eef8;
  font-size: 12px;
  color: #556;
}
.unit-section .pager-row {
  border-radius: 0 0 8px 8px;
}
.page-pager {
  display: flex;
  align-items: center;
  gap: 10px;
}
.page-btn {
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  background: #fff;
  color: #556;
  font-size: 12px;
  padding: 5px 10px;
  cursor: pointer;
}
.page-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.page-btn:not(:disabled):hover {
  border-color: #9fc3f5;
  color: #0571dc;
}
.page-info {
  font-size: 12px;
  color: #556;
}
.page-total {
  color: #8a97a8;
}
.job-status-panel {
  scroll-margin-top: 16px;
}
</style>
