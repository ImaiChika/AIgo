<script setup>
import {useRoute,useRouter} from "vue-router";
import {rememberJourney,clearJourney,journeyTask} from "../authoringJourney.js";
const guideRoute=useRoute()||{query:{}};
const guideRouter=useRouter();
import {questionReviewUi,isolatedQuestionState} from "../questionReviewUi.js";
import HelpHint from "../components/HelpHint.vue";
import {defineAsyncComponent} from "vue";
const WorkflowDiagnostics=defineAsyncComponent(()=>import("../components/WorkflowDiagnostics.vue"));
import { ref, computed, watch, nextTick, onMounted, onActivated, onDeactivated, onBeforeUnmount } from "vue";
import { api } from "../api.js";
import { validGenerationCount } from "../generationCount.js";
import { validPresentation, presentationPayload } from '../presentation.js';
import { hasPerm, currentUser, getToken } from "../auth.js";
import { notifyNavigationWorkChanged, canAccessQuestionArrangement } from "../navigation.js";
import { useAICheckProgress } from "../aiCheckProgress.js";
import { checkOutcomeMessage, checkFailureItems, retryingCheckItems, checkItemState } from "../aiCheckProgressBatch.js";
import KnowledgeWorkflowSelector from "../components/KnowledgeWorkflowSelector.vue";
import { learningCapabilities } from "../personalLearning.js";
import PersonalExperienceSelector from '../components/PersonalExperienceSelector.vue';
import KnowledgePointPicker from "../components/KnowledgePointPicker.vue";
import GenerationPolicyNotice from "../components/GenerationPolicyNotice.vue";
import {isBasicMedicalPoint} from '../generationPolicy.js';
import QuestionDetailModal from "../components/QuestionDetailModal.vue";
import DiscardDetailModal from "../components/DiscardDetailModal.vue";
import { initialBatchJob } from "../batchHistory.js";
import { loadBatchWorkspace, saveBatchWorkspace, batchUnitRows, batchResultRows, batchPipelinePhase, batchGenerationFailures } from "../batchWorkspace.js";

const props = defineProps({ historyOnly: { type: Boolean, default: false } });

// AI 检查分段进度（导入成功后轮询，见 aiCheckProgress.js）
const { progress: aiProgress, start: startAIProgress, stop: stopAIProgress } = useAICheckProgress();
const retryingChecks = computed(() => retryingCheckItems(aiProgress.value));
const pipelinePhase = computed(() => batchPipelinePhase(currentJob.value, aiProgress.value));
const failedChecks = computed(() => checkFailureItems(aiProgress.value || {}));
const batchPermission = computed(() => hasPerm("batch:run") || (props.historyOnly && hasPerm("question:view_global")));

const toast = ref("");
const submissionError = ref("");
watch(() => batchSessionIdentity(), () => { submissionError.value = ""; });
const stats = ref({ question_count: 0, knowledge_count: 0, knowledge_categories: {} });
const selectedKnowledgeVersion = ref(null);
const batchRuntime = ref({
  backend: "unknown",
  available: false,
  execution_mode: "unavailable",
  model: "",
  message: "正在读取批量执行器状态...",
});

// 按当前可见旧审核分层汇总本人题目，包含待整理与历史留档；此数不是普通题库入库数。
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
const containsBasic=computed(()=>selectedKPs.value.some(isBasicMedicalPoint));
const batchConfig = ref({
  skip_existing: false,
  count: 1,
  total_count: 3,
  mode: "random_total",
  job_name: "",
});
// 批量模式：random_total=所选范围随机共N题（默认推荐）；per_point=每点K题。
const batchMode = computed({
  get: () => batchConfig.value.mode || "random_total",
  set: (value) => { batchConfig.value.mode = value; },
});
const batchPreviewResult = ref(null);
const batchPreviewing = ref(false);
const batchPreviewError = ref("");
// Pending intent keys live in the account workspace before the HTTP request.
// A lost response, refresh, or temporary navigation must retain the same key.
function newBatchRequestKey() {
  if (globalThis.crypto?.randomUUID) return globalThis.crypto.randomUUID();
  return "bk-" + Date.now().toString(36) + "-" + Math.random().toString(36).slice(2, 10);
}
let batchPreviewTicket = 0;
let batchPreviewTimer = null;
function invalidateBatchPreview() {
  ++batchPreviewTicket;
  clearTimeout(batchPreviewTimer);
  batchPreviewTimer = null;
  batchPreviewResult.value = null;
  batchPreviewing.value = false;
  batchPreviewError.value = "";
}
function scheduleBatchPreview() {
  invalidateBatchPreview();
  if (props.historyOnly || disposed || !pageActive || !batchPermission.value || !selectedKPs.value.length) return;
  const ticket = batchPreviewTicket;
  const intent = batchCoverageIntent.value;
  const session = batchSessionIdentity();
  const active = () => !disposed && pageActive && ticket === batchPreviewTicket && intent === batchCoverageIntent.value && session === batchSessionIdentity();
  batchPreviewing.value = true;
  batchPreviewTimer = setTimeout(async () => {
    batchPreviewTimer = null;
    if (!active()) return;
    try {
      const result = await api.batchPreview(batchCoveragePayload());
      if (active()) batchPreviewResult.value = result;
    } catch (e) {
      if (active()) batchPreviewError.value = e.message || "请稍后重试";
    } finally { if (active()) batchPreviewing.value = false; }
  }, 200);
}

// 任务状态
const currentJob = ref(null);
const jobHistory = ref([]);
const polling = ref(false);
const importResult = ref(null); // 导入结果详情（落库为 AI 草稿，需经 AI 检查后才可见）
const detailQuestion = ref(null); // 弹窗查看的题目全貌（复用题库详情弹窗）
const discardDetail = ref(null); // 淘汰详情弹窗：当前点开的淘汰明细项
const currentJobOwned = computed(() => !!currentJob.value && currentJob.value.owner_id === currentUser.value?.id);
let formRestored = false;
function persistBatchForm() {
  if (props.historyOnly || !formRestored) return;
  const userId = currentUser.value?.id;
  saveBatchWorkspace(userId, currentJob.value?.job_id || loadBatchWorkspace(userId)?.jobId || "", globalThis.localStorage, {
    selectedKPs: selectedKPs.value.map(({ id, version_id }) => ({ id, version_id })),
    batchConfig: { ...batchConfig.value },
    workflowMode: workflowMode.value,
    presentation: presentation.value,
    applyPersonalExperiences: applyPersonalExperiences.value,
    experienceRole: currentUser.value?.role || '',
  });
}

// 等待计时：每秒刷新一次当前时间，任务项展示自提交起已用时。
const nowTick = ref(Date.now());
const jobConnectionError = ref("");
const retryingGenerationItems = computed(() => (currentJob.value?.items || []).map((item, index) => ({ ...item, unitIndex: index + 1 })).filter(item => !["ok", "failed"].includes(item.status) && item.attempts > 0 && (item.last_error || item.attempts > 1)));
function retryDescription(item) {
  if (item.status === "running") return `正在进行第 ${item.attempts}/${item.max_attempts || 3} 次生成尝试`;
  const seconds = Math.max(0, Math.ceil(((item.next_attempt_at || 0) - nowTick.value) / 1000));
  return seconds > 0 ? `约 ${seconds} 秒后自动重试（已尝试 ${item.attempts}/${item.max_attempts || 3} 次）` : `等待自动重试（已尝试 ${item.attempts}/${item.max_attempts || 3} 次）`;
}
let tickTimer = null;

function jobStartMs(job) {
  if (!job) return null;
  if (Number(job.generation_started_at) > 0) return Number(job.generation_started_at);
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
  return batchUnitRows(items);
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

// 历史任务由服务端先按账号过滤再分页；左侧只渲染当前页。
const historyPage = ref(1);
const historyPageSize = 12;
const historyTotal = ref(0);
const historyLoading = ref(false);
const historyQuery = ref("");
const historyStatus = ref("");
const historyScope = ref(hasPerm("question:view_global") ? "global" : "mine");
let historyTicket = 0;
const historyPageCount = computed(() => Math.max(1, Math.ceil(historyTotal.value / historyPageSize)));
function goHistoryPage(p) {
  historyPage.value = Math.min(Math.max(1, p), historyPageCount.value);
  loadHistory();
}

// 尚未出结果的单元数：排队等待或生成中。
const pendingUnitCount = computed(() => {
  const job = currentJob.value;
  if (!job) return 0;
  return Math.max(0, (job.total_count || 0) - (job.completed || 0) - (job.failed || 0));
});

// 任务已进队列但还没有任何单元出结果：提示并发名额等待。
const queueWaiting = computed(() => {
  const job = currentJob.value;
  return !!job && isRunning(job.status) && (job.completed || 0) + (job.failed || 0) === 0 && (job.total_count || 0) > 0;
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

async function loadHistory() {
  const ticket = ++historyTicket;
  historyLoading.value = true;
  try {
    const data = await api.batchHistory({ scope: historyScope.value, page: historyPage.value, page_size: historyPageSize, name: historyQuery.value.trim(), status: historyStatus.value });
    if (ticket !== historyTicket) return;
    jobHistory.value = data.jobs || [];
    historyTotal.value = data.total || 0;
  } catch (e) {
    if (ticket === historyTicket) showToast("加载任务历史失败: " + e.message);
  } finally { if (ticket === historyTicket) historyLoading.value = false; }
}

function filterHistory() {
  historyPage.value = 1;
  loadHistory();
}

// 当前任务仅存任务 ID；结果和进度以服务端持久化状态恢复，直到用户主动清空。
async function restoreCurrentTask() {
 if(typeof guideRoute.query.job_id==='string'){
  const session=batchSessionIdentity(),target=guideRoute.query.job_id;try{const job=await api.batchStatus(target);if(disposed||session!==batchSessionIdentity()||target!==guideRoute.query.job_id)return;if(job?.owner_id===currentUser.value?.id){selectJob(job,{silent:true});return;}showToast('本次任务不属于当前账号，无法定位。');return;}catch(e){if(!e.staleSession&&!disposed&&session===batchSessionIdentity()&&target===guideRoute.query.job_id)showToast('无法读取本次任务：'+e.message);return;}
 }
  const userId = currentUser.value?.id;
  const saved = loadBatchWorkspace(userId);
  if (saved) {
    selectedKPs.value = saved.selectedKPs || [];
    batchConfig.value = { ...batchConfig.value, ...saved.batchConfig };
    // 旧工作区没有 mode：按保存时的“每点K题”语义恢复，不因新默认值改变含义。
    if (!saved.batchConfig?.mode) batchConfig.value.mode = "per_point";
    if (["direct", "knowledge"].includes(saved.workflowMode)) workflowMode.value = saved.workflowMode;
    presentation.value = saved.presentation || null;
    if (saved.experienceRole === (currentUser.value?.role || '') && typeof saved.applyPersonalExperiences === 'boolean') applyPersonalExperiences.value = saved.applyPersonalExperiences;
  }
  formRestored = true;
  if (saved?.jobId) {
    try {
      const job = await api.batchStatus(saved.jobId);
      if (job.owner_id !== userId) throw new Error("任务归属不符");
      selectJob(job, { silent: true });
      return;
    } catch {
      // A status read failure must not erase an unconfirmed submission key.
      saveBatchWorkspace(userId, "");
    }
  }
  try {
    const data = await api.batchHistory({ scope: "mine", page: 1, page_size: 20 });
    const pending = initialBatchJob(data.jobs || [], userId);
    if (pending) {
      saveBatchWorkspace(userId, pending.job_id);
      selectJob(pending, { silent: true });
    }
  } catch (e) { showToast("恢复当前任务失败: " + e.message); }
}

watch(()=>guideRoute.query.job_id,id=>{if(id)restoreCurrentTask();});

function clearCurrentTask() {
  if (!currentJob.value || isRunning(currentJob.value.status)) return;
  if (!window.confirm("仅清空本页当前任务记录，历史记录和已生成题目都会保留。确定继续吗？")) return;
  if(journeyTask.value?.kind==='batch'&&journeyTask.value.id===currentJob.value.job_id)clearJourney(currentUser.value);
  if(guideRoute.query.job_id===currentJob.value.job_id){const query={...guideRoute.query};delete query.job_id;guideRouter?.replace({path:guideRoute.path,query});}
  stopPolling(); stopAIProgress();
  currentJob.value = null; importResult.value = null; importJobId.value = "";
  aiProgress.value = null; detailQuestion.value = null; discardDetail.value = null;
  saveBatchWorkspace(currentUser.value?.id, "");
  persistBatchForm();
  showToast("当前任务已从工作台清空，可在历史出题记录中查看");
}

// 选中任务：先展示列表摘要，再拉取完整状态补齐逐单元明细（明细只在
// 状态接口返回，列表接口不带；终态任务没有轮询，必须主动水合一次）。
// silent=true 用于页面初次自动选中：不弹提示也不滚动。
const jobPanel = ref(null);
const resultPanel = ref(null);
function selectJob(job, { silent = false } = {}) {
  if (!job) return;
  if(job.owner_id===currentUser.value?.id)rememberJourney(currentUser.value,{kind:"batch",id:job.job_id});
  currentJob.value = job;
  discardDetail.value = null; // 切换任务时关闭上一任务的淘汰详情弹窗
  hydrateJobDetail(job.job_id);
  if (isRunning(job.status)) {
    if (!silent && !job.recovery) {
      showToast("任务仍在进行中：生成明细实时更新，通过首检的题目会陆续展示，不必等待整批完成");
    }
    startPolling(job.job_id);
  }
  if (!silent) {
    // 平滑滚动定位到任务状态面板；轮询刷新引起的重渲染可能打断动画，
    // 400ms 后重试一次（已到位时为无操作）。
    const scrollPanel = () => jobPanel.value?.scrollIntoView({ behavior: "smooth", block: "start" });
    nextTick(scrollPanel);
    window.setTimeout(scrollPanel, 400);
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
// 全局查看权限的管理员可回放任何已导入任务（后端同样放行已导入回放），
// 角色拆分后原主失去批量权限的存量任务由管理员兜底查看。
function maybeLoadImportReplay(job) {
  if (job?.pipeline) { syncPipelineResult(job); return; }
  if (!job || !isCompleted(job.status) || !job.imported_at) return;
  const isOwner = !job.owner_id || job.owner_id === currentUser.value?.id;
  if (!isOwner && !hasPerm("question:view_global")) return;
  if (importJobId.value === job.job_id && importResult.value) return;
  downloadResult(job.job_id, { replay: true });
}

// 提交批量任务
const applyPersonalExperiences = ref(true);
const workflowMode=ref("direct");
const presentation=ref(null);
const submitting = ref(false); // 提交在途守卫，防重复创建批量任务
watch([selectedKPs, batchConfig, workflowMode, presentation, applyPersonalExperiences], persistBatchForm, { deep: true });

function batchSubmissionPayload() {
  const mode = batchMode.value;
  // Send the same canonical input used by the persisted intent. Selection order
  // is irrelevant only for random sampling over the whole selected set. Preserve
  // per-point execution order in both the payload and the request fingerprint.
  const pointIds = [...new Set(selectedKPs.value.map(k => k.id))];
  if (mode === "random_total") pointIds.sort();
  return JSON.parse(JSON.stringify({
    apply_personal_experiences: learningCapabilities(currentUser.value?.permissions).use && applyPersonalExperiences.value,
    workflow_mode: workflowMode.value,
    presentation: presentationPayload(workflowMode.value, presentation.value),
    skip_existing: batchConfig.value.skip_existing,
    knowledge_point_ids: pointIds,
    version_id: selectedKPs.value[0]?.version_id || "",
    job_name: batchConfig.value.job_name || "",
    mode,
    ...(mode === "random_total" ? { total_count: batchConfig.value.total_count } : { count: batchConfig.value.count }),
  }));
}
function canonicalJSON(value) {
  if (Array.isArray(value)) return `[${value.map(canonicalJSON).join(",")}]`;
  if (value && typeof value === "object") return `{${Object.keys(value).sort().map(key => `${JSON.stringify(key)}:${canonicalJSON(value[key])}`).join(",")}}`;
  return JSON.stringify(value);
}
const batchIntent = computed(() => canonicalJSON({
  owner_id: currentUser.value?.id || "",
  role: currentUser.value?.role || "",
  payload: batchSubmissionPayload(),
}));
function batchSessionIdentity() {
  return JSON.stringify([currentUser.value?.id || "", currentUser.value?.role || "", getToken(), [...new Set(currentUser.value?.permissions || [])].sort()]);
}
// Names and optional authoring preferences do not change the candidate pool.
// Keep the current coverage while typing a name, and only refresh scope inputs.
function batchCoveragePayload() {
  const payload = batchSubmissionPayload();
  return { knowledge_point_ids: payload.knowledge_point_ids, version_id: payload.version_id,
    mode: payload.mode, skip_existing: payload.skip_existing };
}
const batchCoverageIntent = computed(() => canonicalJSON(batchCoveragePayload()));
watch([batchCoverageIntent, () => batchSessionIdentity(), batchPermission], scheduleBatchPreview, { flush: "sync" });

function pendingSubmissionKey(ownerId, intent) {
  const saved = loadBatchWorkspace(ownerId);
  const pending = Array.isArray(saved?.pendingBatchSubmissions) ? saved.pendingBatchSubmissions : [];
  const existing = pending.find(item => item?.intent === intent && typeof item.requestKey === "string" && item.requestKey);
  if (existing) return existing.requestKey;
  const requestKey = newBatchRequestKey();
  if (!saveBatchWorkspace(ownerId, saved?.jobId || "", globalThis.localStorage, {
    pendingBatchSubmissions: [...pending, { intent, requestKey }],
  })) throw new Error("无法保存本次提交记录，请检查浏览器存储后重试，避免网络中断后重复出题");
  return requestKey;
}
function completePendingSubmission(ownerId, requestKey, jobId) {
  const saved = loadBatchWorkspace(ownerId);
  const pending = Array.isArray(saved?.pendingBatchSubmissions) ? saved.pendingBatchSubmissions : [];
  saveBatchWorkspace(ownerId, jobId, globalThis.localStorage, {
    pendingBatchSubmissions: pending.filter(item => item?.requestKey !== requestKey),
  });
}

async function submitBatch() {
  if (submitting.value) return;
  if(containsBasic.value){showToast('基础医学知识点已从有效大纲剔除，请取消相关知识点');return;}
  if(workflowMode.value==='knowledge'&&!validPresentation(presentation.value)){showToast('请重新核对病例顺序');return;}
  const mode = batchMode.value;
  if (mode === "per_point" && !validGenerationCount(batchConfig.value.count)) {
    showToast("每大纲要点题数应为 1–20 的整数");
    return;
  }
  if (mode === "random_total" && !Number.isInteger(batchConfig.value.total_count) || (mode === "random_total" && batchConfig.value.total_count < 1)) {
    showToast("随机批量总题数应为正整数");
    return;
  }
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
  submissionError.value = "";
  submitting.value = true;
  const ownerId = currentUser.value?.id;
  const session = batchSessionIdentity();
  try {
    const payload = batchSubmissionPayload();
    payload.request_key = pendingSubmissionKey(ownerId, batchIntent.value);
    const data = await api.batchSubmit(payload);
    if (session !== batchSessionIdentity()) return;
    completePendingSubmission(ownerId, payload.request_key, data.job_id);
    rememberJourney(currentUser.value,{kind:"batch",id:data.job_id});
    if (disposed) return;
    if (data.replayed) showToast("该请求与已提交任务一致，已返回原任务");
    invalidateBatchPreview();

    // 新任务开始：清空上一任务的结果展示，避免旧明细与新任务混淆。
    importResult.value = null;
    importJobId.value = "";
    importError.value = "";
    discardDetail.value = null;
    currentJob.value = {
      job_id: data.job_id,
      owner_id: ownerId || "",
      status: "validating",
      total_count: data.count,
      plan_mode: data.mode,
      plan_candidates: data.candidate_count,
      plan_requested_total: data.count,
      completed: 0,
      failed: 0,
      submitted_at: Date.now(),
    };

    jobHistory.value.unshift(currentJob.value);
    saveBatchWorkspace(ownerId, currentJob.value.job_id);
    showToast(`任务已提交: ${data.job_id}`);

    // 开始轮询
    startPolling(data.job_id);
    nextTick(() => jobPanel.value?.scrollIntoView({ behavior: "smooth", block: "start" }));
  } catch (e) {
    if (!disposed && session === batchSessionIdentity()) { submissionError.value = "提交失败: " + e.message; showToast(submissionError.value); }
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
      jobConnectionError.value = "";
      if (currentJob.value?.job_id === jobId) currentJob.value = job;
      const idx = jobHistory.value.findIndex(j => j.job_id === jobId);
      if (idx >= 0) jobHistory.value[idx] = job;
      if (isTerminal(job.status)) {
        polling.value = false;
        showToast(isCompleted(job.status) ? (job.pipeline ? (job.completed ? `生成阶段完成：${job.completed} 道已进入首检；通过的题目可立即查看` : `本次生成失败：${job.failed} 项未产生题目，可补生成失败项`) : `任务完成：成功 ${job.completed}，失败 ${job.failed}`) : `任务${statusText(job.status)}`);
        return;
      }
      pollTimer = setTimeout(poll, job.pipeline ? 3000 : 10000);
    } catch (e) {
      if (sequence !== pollSequence || !pageActive || disposed) return;
      console.error("轮询失败:", e);
      jobConnectionError.value = "暂时无法获取任务进度，正在重连；已有题目保留，请勿重复提交整批任务。";
      pollTimer = setTimeout(poll, 5000);
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
  scheduleBatchPreview();
  if (currentJob.value && isRunning(currentJob.value.status)) startPolling(currentJob.value.job_id);
});
onDeactivated(() => { pageActive = false; invalidateBatchPreview(); stopPolling(); });

const importError = ref("");
const importJobId = ref(""); // importResult 所属任务；切换任务时隐藏过期结果
const autoImportJobs = new Set(); // 本会话已触发过自动导入的任务；后端按任务幂等兜底

// 任务完成后自动导入一次，无需手动点击。仅针对本地有记录的任务（tracked）：
// 仅存在于云端列表的历史任务没有导入跟踪，自动导入会把历史结果重复写入。
// 存在失败项时不自动导入：等出题人决定“重跑失败项”或“直接导入成功部分”——
// 一旦导入，失败项视为放弃、不能再重跑。
// 重复触发由后端导入幂等拦截：已导入的任务只会重放上次的导入结果。
function maybeAutoImport(job) {
  if (job?.pipeline) { syncPipelineResult(job); return; }
	if (props.historyOnly) return;
	if (!job || job.owner_id !== currentUser.value?.id || !isCompleted(job.status) || job.imported_at || !job.tracked) return;
  if ((job.failed || 0) > 0) return;
  if (autoImportJobs.has(job.job_id)) return;
  autoImportJobs.add(job.job_id);
  downloadResult(job.job_id);
}
watch(currentJob, (job) => maybeAutoImport(job));

let pipelineTrackedJob = "";
let pipelineTrackedIds = "";
function syncPipelineResult(job) {
  if (!job?.pipeline || currentJob.value?.job_id !== job.job_id) return;
  const sameJob = pipelineTrackedJob === job.job_id;
  importJobId.value = job.job_id;
  importResult.value = job.pipeline_result || { saved: 0, failed: 0, items: [], question_ids: [] };
  const ids = importResult.value.question_ids || [];
  const key = ids.join("|");
  if (!sameJob) { stopAIProgress(); aiProgress.value = null; }
  pipelineTrackedJob = job.job_id;
  if ((!sameJob || pipelineTrackedIds !== key) && ids.length) startAIProgress(ids, () => loadStats(), { preserve: sameJob });
  pipelineTrackedIds = key;
}

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
// 进入题目整理，待本人确认入库。
async function downloadResult(jobId, { replay = false } = {}) {
  importError.value = "";
  try {
    if (!replay) importResult.value = null;
    const data = await api.batchDownload(jobId);
    importResult.value = data;
    importJobId.value = jobId;
    markJobImported(jobId);
    if (data.simulated) {
      if (!replay) showToast(data.message || "旧批量任务已完成");
      return;
    }
    // 回看只是读取，不打断正在配置新任务的用户。
    if (!replay && data.failed > 0) {
      showToast(`AI 生成完毕：${data.saved} 题已提交 AI 质量检查，失败 ${data.failed} 项`);
    } else if (!replay) {
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
// 检查通过的题会继续走审核生命周期（送审→定稿/驳回、或被归档），回放时按
// 当前真实状态展示，不能把非 ai_reviewed 的一律当"待检查"。
const importRows = computed(() => batchResultRows(importResult.value, aiProgress.value));

// 行状态：标签 + 样式类；有题干（未淘汰且题目仍存在）的行可点击查看全貌。
function rowStateFor(row) {
  if (row.discarded) return { label: "已淘汰", cls: "failed" };
  if (row.missing) return { label: "已删除", cls: "failed" };
  if (row.exhausted) return { label: "检查失败", cls: "failed" };
  if (row.checking) return checkItemState(row.check, nowTick.value);
  const historic=isolatedQuestionState(row.status);
  if(historic)return {label:historic,cls:"muted"};
  switch (row.status) {
    case "ai_reviewed":
    case "auto_checked":
      return { label: "已通过", cls: "passed" };
    case "published":
      return { label: "已定稿", cls: "passed" };
    case "reviewing":
      return { label: "审核中", cls: "checking" };
    case "archived":
      return { label: "已归档", cls: "muted" };
    case "rejected":
      return { label: "已驳回", cls: "failed" };
    default:
      return { label: "待检查", cls: "" };
  }
}
const rowClickable = (row) => !row.discarded && !row.missing && !!row.stem;
// 主列表只保留未淘汰的题；淘汰题移入下方失败提醒，点击查看原题与淘汰原因
const passedRows = computed(() => importRows.value.filter(r => !r.discarded && !r.exhausted));
const discardedRows = computed(() => importRows.value.filter(r => r.discarded && !r.exhausted));
const noticePageSize = 6;
const discardPage = ref(1);
const discardPageCount = computed(() => Math.max(1, Math.ceil(discardedRows.value.length / noticePageSize)));
const visibleDiscardRows = computed(() => discardedRows.value.slice((discardPage.value - 1) * noticePageSize, discardPage.value * noticePageSize));
// 淘汰明细项（含评分与题目快照），供弹窗展示；行内补充大纲信息后传入
const discardedItems = computed(() => new Map((aiProgress.value?.items || []).map(i => [i.question_id, i])));
function openDiscardDetail(row) {
  const item = discardedItems.value.get(row.id);
  if (item) discardDetail.value = { ...item, outline_code: item.outline_code || row.outlineCode, topic: row.topic };
}
const failedItems = computed(() => batchGenerationFailures(importResult.value));
const failedPage = ref(1);
const failedPageCount = computed(() => Math.max(1, Math.ceil(failedItems.value.length / noticePageSize)));
const visibleFailedItems = computed(() => failedItems.value.slice((failedPage.value - 1) * noticePageSize, failedPage.value * noticePageSize));
watch(() => currentJob.value?.job_id, () => { discardPage.value = 1; failedPage.value = 1; });
watch(discardPageCount, count => { if (discardPage.value > count) discardPage.value = count; });
watch(failedPageCount, count => { if (failedPage.value > count) failedPage.value = count; });
const checkDone = computed(() => !!aiProgress.value && !aiProgress.value.stalled && aiProgress.value.checking === 0);
// 检查完成后通过数即“题目整理”角标的变化来源，通知侧栏立即重算。
watch(checkDone, (done) => { if (done) { notifyNavigationWorkChanged(); scheduleBatchPreview(); } });
watch(() => aiProgress.value?.passed || 0, (passed, previous) => {
  if (passed > previous) { notifyNavigationWorkChanged(); loadStats(); }
});

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
  return isolatedQuestionState(status) || map[status] || status;
}

function jobStatusLabel(job) {
  if (job.recovery) return job.recovery.state === "failed" ? "需关注" : "自动恢复中";
  if (job.pipeline && isCompleted(job.status) && job.failed > 0) return job.completed > 0 ? "部分完成" : "生成失败";
  return statusText(job.status);
}
function jobPlanSummary(job) {
  if (!["random_total", "per_point"].includes(job?.plan_mode)) return "";
  const candidates = Number(job.plan_candidates), total = Number(job.plan_requested_total);
  if (!Number.isInteger(candidates) || candidates < 1 || !Number.isInteger(total) || total < 1) return "";
  return job.plan_mode === "random_total"
    ? `范围随机 · ${candidates} 个候选 · 共 ${total} 题`
    : `每点生成 · ${candidates} 个要点 · 共 ${total} 题`;
}
function jobStatusClass(job) {
  if (job.recovery) return job.recovery.state === "failed" ? "status-bad" : "status-partial";
  if (job.provider_waiting && job.job_id === currentJob.value?.job_id && pendingUnitCount.value > 0) return "status-partial";
  if (job.pipeline && isCompleted(job.status) && job.failed > 0) return job.completed > 0 ? "status-partial" : "status-bad";
  if (job.job_id === currentJob.value?.job_id && pipelinePhase.value === "检查失败") return "status-bad";
  return statusClass(job.status);
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
  if (props.historyOnly) loadHistory();
  else { loadStats(); loadBatchCapabilities(); restoreCurrentTask(); scheduleBatchPreview(); }
  tickTimer = setInterval(() => { nowTick.value = Date.now(); }, 1000);
});
onBeforeUnmount(() => {
  disposed = true;
  invalidateBatchPreview();
  historyTicket++;
  stopPolling();
  clearInterval(tickTimer);
  clearTimeout(showToast.timer);
});
</script>

<template>
  <section v-if="!batchPermission" class="panel batch-permission-panel" role="alert">
    <div class="section-heading">
      <span class="dot red"></span>
      <h2>{{ historyOnly ? '暂无历史记录查看权限' : '暂无批量推理权限' }}</h2>
    </div>
    <p>{{ historyOnly ? '当前身份没有批量推理或全局查看权限，请切换到有权限的身份。' : '当前账号不能查看、提交或导入批量推理任务；已分配的单题出题权限不受影响。' }}</p>
    <HelpHint label="批量权限说明">如需开放批量推理，请联系超级管理员在「用户管理 → 分配权限」中单独勾选“批量推理”。</HelpHint>
    <RouterLink class="ghost-button inline-link" :to="hasPerm('question:generate') ? '/generate' : '/knowledge'">
      {{ hasPerm('question:generate') ? '返回单题出题' : '返回知识点' }}
    </RouterLink>
  </section>

  <div v-else class="batch-layout" :class="{ 'history-layout': historyOnly }">
   <div class="batch-primary">
    <!-- 统计信息 -->
    <section v-if="!historyOnly" class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>批量生成</h2>
      </div>

      <div v-if="hasPerm('stats:view')" class="metric-row">
        <div>
          <span>本人题目（含待整理）<HelpHint label="题目统计口径">按当前授权范围汇总，非普通题库入库数。</HelpHint></span>
          <strong>{{ personalQuestionCount }}道</strong>
          <small v-if="questionReviewUi.enabled() && personalTierSummary">旧审核状态：{{ personalTierSummary }}</small>
        </div>
        <div>
          <span>当前大纲要点</span>
          <strong>{{ currentKnowledgeCount }}个</strong>
          <small v-if="currentKnowledgeVersionName" :title="currentKnowledgeVersionName">{{ currentKnowledgeVersionName }}</small>
        </div>
      </div>

    </section>

    <!-- 配置 -->
    <section v-if="!historyOnly" class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>生成配置</h2>
      </div>

      <div class="config-form">
        <div class="batch-runtime-note" :class="{ local: batchRuntime.execution_mode === 'local_single_api', unavailable: !batchRuntime.available }">
          <span class="runtime-badge">{{ batchRuntime.execution_mode === 'local_single_api' ? '后台逐题处理' : batchRuntime.available ? '已配置' : '不可用' }}</span>
          <div>
            <strong>题目生成后自动首检，通过题目可提前查看</strong>
            <p>当前模型：{{ batchRuntime.model || '未配置' }} · {{ batchRuntime.message }}</p>
          </div>
        </div>
        <!-- 大纲要点选择（搜索勾选多选，也可按大纲代码逗号分隔加入） -->
        <div class="field">
          <label class="ui-help-heading">选择大纲要点<GenerationPolicyNotice /></label>
          <KnowledgePointPicker id="authoring-points" v-model="selectedKPs" :multiple="true" placeholder="搜索考点内容或大纲代码" @version-change="handleKnowledgeVersionChange" />
          <span class="field-hint">已选 {{ selectedKPs.length }} 个大纲要点；{{ batchMode === 'random_total' ? `随机抽 ${batchConfig.total_count || 0} 题` : `每个要点生成 ${batchConfig.count} 道题` }}</span>
        </div>

        <div class="batch-candidate-summary" role="status" aria-live="polite">
          <span class="coverage-dot" :class="{ checking: batchPreviewing, unavailable: batchPreviewError }" aria-hidden="true"></span>
          <span v-if="!selectedKPs.length">选择临床要点后，自动核对可参与出题的范围</span>
          <span v-else-if="batchPreviewing">已选 {{ selectedKPs.length }} 个要点，正在核对范围…</span>
          <span v-else-if="batchPreviewError" class="coverage-error">范围暂时无法核对：{{ batchPreviewError }}</span>
          <span v-else-if="batchPreviewResult">可参与出题 <strong>{{ batchPreviewResult.candidate_count }}</strong> 个要点<span v-if="batchPreviewResult.excluded_basic"> · 已剔除基础医学 {{ batchPreviewResult.excluded_basic }} 个</span><span v-if="batchPreviewResult.skipped_existing"> · 已跳过有题要点 {{ batchPreviewResult.skipped_existing }} 个</span></span>
          <span v-else>已选 {{ selectedKPs.length }} 个要点</span>
        </div>


        <div class="batch-mode-options" role="radiogroup" aria-label="批量出题模式">
          <label class="batch-mode-choice" :class="{ active: batchMode === 'random_total' }"><input type="radio" name="batch-generation-mode" value="random_total" v-model="batchMode" /><span><strong>范围随机 · 共 N 题</strong><small>随机选择考点，可重复，不要求覆盖全部</small></span></label>
          <label class="batch-mode-choice" :class="{ active: batchMode === 'per_point' }"><input type="radio" name="batch-generation-mode" value="per_point" v-model="batchMode" /><span><strong>每点 K 题</strong><small>每个考点各生成指定数量，适合完整覆盖</small></span></label>
        </div>

        <div class="form-row batch-job-fields">
          <div class="field" v-if="batchMode === 'random_total'">
            <label for="batch-question-count" class="ui-help-heading">总题数 N<HelpHint v-if="batchMode === 'random_total'" label="随机总量说明">总题数可大于或小于已选要点数，同一考点允许重复。</HelpHint></label>
            <input id="batch-question-count" v-model.number="batchConfig.total_count" type="number" min="1" :max="batchRuntime.max_questions || 100" />
          </div>
          <div class="field" v-else>
            <label for="batch-question-count">每大纲要点题数</label>
            <input id="batch-question-count" v-model.number="batchConfig.count" type="number" min="1" max="20" />
          </div>
          <div class="field">
            <label for="batch-job-name">任务名称（可选）</label>
            <input id="batch-job-name" v-model="batchConfig.job_name" placeholder="如: 呼吸系统第一批" />
          </div>
        </div>
        <p v-if="batchMode === 'per_point' && !validGenerationCount(batchConfig.count)" class="field-hint batch-count-hint" role="status">每大纲要点题数应为 1–20 的整数</p>


        <div class="form-row">
          <label class="checkbox-row">
            <input type="checkbox" v-model="batchConfig.skip_existing" />
            <span>跳过已有题目的大纲要点</span>
          </label>
        </div>

        <KnowledgeWorkflowSelector v-model="workflowMode" v-model:presentation="presentation" :knowledge-points="selectedKPs" />
        <p v-if="containsBasic" role="alert">当前选择包含基础医学知识点，请取消后提交。</p>
        <PersonalExperienceSelector v-model="applyPersonalExperiences" />
        <button class="primary-button full" type="button" @click="submitBatch" :disabled="submitting || !selectedKPs.length || containsBasic || !batchRuntime.available || (batchMode === 'per_point' && !validGenerationCount(batchConfig.count)) || (batchMode === 'random_total' && !(Number.isInteger(batchConfig.total_count) && batchConfig.total_count >= 1))">
          {{ submitting ? "提交中..." : batchMode === 'random_total' ? `提交随机生成任务（${selectedKPs.length} 个要点中抽 ${batchConfig.total_count || 0} 题）` : `提交生成任务（${selectedKPs.length} 个大纲要点 × ${batchConfig.count} 题）` }}
        </button>
        <p v-if="submissionError" class="batch-submission-error" role="alert">{{ submissionError }}</p>
        <span v-if="!batchRuntime.available" class="field-hint">批量生成功能当前不可用，请联系系统管理员。</span>
      </div>
    </section>

    <section v-if="historyOnly && !currentJob" class="panel history-empty"><h2>选择历史任务</h2></section>

    <!-- 当前任务 / 历史详情共用同一套进度与结果展示 -->
    <section v-if="currentJob" ref="jobPanel" class="panel job-status-panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>任务状态</h2>
        <span class="q-status" :class="jobStatusClass(currentJob)">
          {{ currentJob.recovery ? (currentJob.recovery.state === 'failed' ? '需关注' : '自动恢复中') : currentJob.provider_waiting && pendingUnitCount > 0 ? '等待模型恢复' : pipelinePhase || statusText(currentJob.status) }}
        </span>
        <button v-if="!historyOnly && !isRunning(currentJob.status)" class="ghost-button clear-current" type="button" @click="clearCurrentTask">清空本次记录</button>
      </div>

      <WorkflowDiagnostics kind="batch" :task-id="currentJob.job_id" :owner-id="currentJob.owner_id||''" />
      <div v-if="currentJob.recovery" class="recovery-notice" role="alert">
        <strong>{{ currentJob.recovery.state === 'failed' ? '未完成生成已停止' : '任务暂遇系统异常，正在自动恢复' }}</strong>
        <p>{{ currentJob.recovery.message }}</p>
        <p v-if="currentJob.recovery.attempts >= 3">系统仍未恢复，可稍后返回此任务；已通过结果保持可用，无需重复提交整批。</p>
        <p>已生成 {{ currentJob.completed || 0 }} 道 · 尚有 {{ pendingUnitCount }} 项未完成。已通过的题目可继续查看。</p>
      </div>
      <p v-if="currentJob.provider_waiting && pendingUnitCount > 0 && isRunning(currentJob.status)" class="recovery-notice" role="status">生成模型服务暂时不稳定，系统正等待自动恢复；已有结果保留，无需重复提交。</p>
      <p v-if="jobConnectionError" class="recovery-notice" role="status">{{ jobConnectionError }}</p>
      <div class="job-info">
        <div class="job-meta">
          <p><strong>任务名称:</strong> {{ currentJob.job_name || '未命名' }}</p>
          <p v-if="jobPlanSummary(currentJob)" class="job-plan-summary"><strong>出题计划:</strong> {{ jobPlanSummary(currentJob) }}</p>
          <p><strong>{{ currentJob.generation_started_at ? "本轮生成用时:" : currentJob.pipeline ? "生成用时:" : "已用时:" }}</strong> <span class="elapsed-time">{{ elapsedText(currentJob) }}</span><span v-if="isRunning(currentJob.status)" class="field-hint">（含排队等待）</span></p>
        </div>

        <div v-if="currentJob.pipeline" class="pipeline-overview" :class="{ 'pipeline-overview-failed': isCompleted(currentJob.status) && !currentJob.completed && currentJob.failed > 0 }" role="status" aria-live="polite">
          <strong>可用 {{ aiProgress?.passed || 0 }} 道</strong>
          <span>{{ currentJob.recovery || currentJob.provider_waiting ? "待恢复" : "仍在生成" }} {{ pendingUnitCount }} 道 · 首检处理中 {{ aiProgress?.checking || 0 }} 道</span>
          <span v-if="pendingUnitCount > 0 && !currentJob.recovery">通过的题目会先显示，可切换页面，后台继续生成和首检。</span>
          <button v-if="aiProgress?.passed > 0" class="ghost-button" type="button" @click="resultPanel?.scrollIntoView({ behavior: 'smooth', block: 'start' })">查看已通过题目</button>
        </div>

        <!-- 进度条 -->
        <div class="progress-section">
          <div class="progress-stats">
            <span>生成项：<strong>{{ currentJob.total_count }}</strong></span>
            <span>已生成：<strong class="text-success">{{ currentJob.completed }}</strong></span>
            <span>失败：<strong class="text-danger">{{ currentJob.failed }}</strong></span>
            <span v-if="pendingUnitCount > 0 && isRunning(currentJob.status)">{{ currentJob.recovery || currentJob.provider_waiting ? "待恢复" : "排队/生成中" }}：<strong>{{ pendingUnitCount }}</strong></span>
          </div>
          <div class="progress-bar">
            <div class="progress-fill" :style="{ width: progressPercent + '%' }"></div>
          </div>
          <span class="progress-text">{{ progressPercent }}% 完成</span>
          <span v-if="queueWaiting && !currentJob.recovery && !currentJob.provider_waiting && !retryingGenerationItems.length" class="queue-state" role="status">等待生成名额 <HelpHint label="排队计时说明">
            任务已进入本地队列：正在等待全局并发名额（上限 {{ currentJob?.concurrency || batchRuntime?.concurrency || '—' }}）或生成首批题目；排队与生成时间均计入已用时。
          </HelpHint></span>
        </div>

        <div v-if="retryingGenerationItems.length && !currentJob.recovery" class="recovery-notice" role="status">
          <strong>{{ retryingGenerationItems.length }} 项生成正在自动重试，其他题目继续处理</strong>
          <p v-for="item in retryingGenerationItems.slice(0, 6)" :key="item.unitIndex">第 {{ item.unitIndex }} 项 · {{ item.outline_code }} {{ item.topic }}：{{ retryDescription(item) }}</p>
        </div>
        <!-- 逐单元明细：按提交顺序展示每个知识点的成功/失败与原因（分页查看） -->
        <div v-if="unitRows.length" class="unit-section">
          <div class="unit-head">
            <span>生成明细 {{ unitRows.length }}/{{ currentJob.total_count || unitRows.length }}</span>
            <span v-if="pendingUnitCount > 0 && isRunning(currentJob.status)" class="unit-pending">剩余 {{ pendingUnitCount }} 项{{ currentJob.recovery || currentJob.provider_waiting ? "待恢复" : "排队/生成中" }}</span>
          </div>
          <div class="unit-rows">
            <div v-for="row in pagedUnitRows" :key="row.idx" class="unit-row">
              <span class="unit-no">{{ row.idx }}</span>
              <span class="unit-code">{{ row.code }}</span>
              <span class="unit-topic" :title="row.topic">{{ row.topic || "—" }}</span>
              <span v-if="row.ok" class="unit-state ok">已生成</span>
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
	    <div v-if="!currentJobOwned || !hasPerm('batch:run')" class="job-actions">
	      <span class="field-hint">只读任务 · {{ currentJob.owner_name || "其他用户" }} 提交 <HelpHint label="历史任务权限说明">已导入任务可查看生成与 AI 检查结果（含淘汰明细），重跑与导入仅限原提交人。</HelpHint></span>
	    </div>
	    <div v-else-if="currentJob.pipeline" class="job-actions">
        <button v-if="isCompleted(currentJob.status) && currentJob.failed > 0" class="ghost-button" type="button" :disabled="retrying" @click="retryFailed(currentJob)">{{ retrying ? "正在补生成…" : `补生成失败项（${currentJob.failed}）` }}</button>
        <HelpHint label="补生成说明">生成完成一题即自动首检；补生成只处理生成失败项，已检查题目保持原结果。</HelpHint>
      </div>
      <div v-else-if="isCompleted(currentJob.status) && currentJob.imported_at" class="job-actions">
	      <HelpHint label="结果去向说明">AI 生成完毕，结果已提交 AI 质量检查；通过检查的题目才会进入题目整理，待本人确认入库</HelpHint>
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
          <span class="polling-hint">{{ currentJob.recovery ? "任务已保留，系统正在自动恢复" : "后台持续处理，进度自动刷新" }}</span>
        </div>

        <div v-if="currentJob.status === 'failed' || currentJob.status === 'expired'" class="job-actions">
          <span class="text-danger">任务{{ statusText(currentJob.status) }}: {{ currentJob.error || '请检查任务详情或重新提交' }}</span>
        </div>
      </div>
    </section>

    <!-- 生成结果与 AI 检查：一行一题的固定横栏，只显示关键缩略信息，点击查看全貌 -->
    <section v-if="importResult && (!currentJob || importJobId === currentJob.job_id)" ref="resultPanel" class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>生成结果 · AI 检查</h2>
      </div>

      <p v-if="importResult.simulated" class="result-summary simulated-result">{{ importResult.message }}</p>
      <p v-else-if="currentJob?.pipeline && !importResult.saved && isRunning(currentJob.status)" class="result-summary">正在生成首批题目；通过首检后会立即显示在这里。</p>
      <p v-else-if="currentJob?.pipeline && !importResult.saved && currentJob.failed > 0" class="result-summary text-danger">本次未生成可用题目，{{ currentJob.failed }} 项生成失败，尚未进入首检；可补生成失败项。</p>
      <p v-else class="result-summary">
        AI 共生成 <b>{{ importRows.length }}</b> 题，已提交 AI 质量检查<template v-if="importResult.failed > 0">，失败 <b class="text-danger">{{ importResult.failed }}</b> 项</template>；通过检查后进入题目整理，待本人确认入库
      </p>
      <p v-if="aiProgress && aiProgress.checking > 0" class="result-check"><template v-if="aiProgress.passed > 0">已有 {{ aiProgress.passed }} 道通过首检，可立即查看；</template><template v-else>尚无通过题目；</template>{{ aiProgress.checking }} 道首检处理中，{{ pendingUnitCount }} 道仍在生成</p>
      <p v-else-if="aiProgress && aiProgress.stalled" class="result-check">AI 检查仍在后台进行，可稍后查看结果</p>
      <p v-else-if="currentJob?.pipeline && pendingUnitCount > 0 && aiProgress" class="result-check">已有 {{ aiProgress.passed }} 道通过首检，可先查看；其余 {{ pendingUnitCount }} 道{{ currentJob.recovery || currentJob.provider_waiting ? "等待恢复" : "仍在生成" }}。</p>
      <p v-else-if="checkDone" class="result-check">
        {{ checkOutcomeMessage(aiProgress) }}
        <template v-if="aiProgress.passed > 0">；首检通过后先进入题目整理，确认后可在普通题库导出</template>
        <RouterLink v-if="aiProgress.passed > 0 && canAccessQuestionArrangement(hasPerm)" class="inline-link primary-button" to="/new-questions">去题目整理</RouterLink>
      </p>

      <div class="result-list">
        <div v-for="row in passedRows" :key="row.id" :data-question-id="row.id" class="result-row" :class="{ clickable: rowClickable(row) }" @click="rowClickable(row) && openQuestion(row.id)">
          <span class="row-no">{{ row.no }}</span>
          <div class="row-content">
            <span v-if="row.topic || row.outlineCode" class="row-topic">{{ row.outlineCode }} {{ row.topic }}</span>
            <span class="row-stem">{{ row.stem || (row.missing ? "题目已删除" : "检查通过后展示题目内容") }}</span>
            <span v-if="row.checking && !row.missing" class="row-check-detail" :class="rowStateFor(row).cls">{{ rowStateFor(row).detail }}</span>
          </div>
          <span class="row-state" :class="rowStateFor(row).cls">{{ rowStateFor(row).label }}</span>
        </div>
      </div>


      <p v-if="retryingChecks.length" class="result-check">{{ retryingChecks.length }} 道检查正在自动重试，已通过的题目可正常使用；达到尝试上限仍失败才会丢弃。</p>
      <p v-if="aiProgress?.providerWaiting && aiProgress.checking > 0" class="recovery-notice" role="status">检查模型服务暂时不稳定，正等待自动恢复；已通过题目仍可查看。</p>
      <p v-if="aiProgress?.connectionError" class="recovery-notice" role="status">{{ aiProgress.connectionError }}</p>

      <div v-if="aiProgress && aiProgress.exhausted > 0" class="result-notice check-failure-notice" role="alert" aria-live="polite">
        <p class="notice-title">AI 检查失败，已丢弃 {{ aiProgress.exhausted }} 道题；本次成功出题 {{ aiProgress.passed }} 道（首检通过，待整理确认）</p>
        <p>检查失败的题目没有进入题库；可以重新生成新题。</p>
        <p v-for="item in failedChecks.slice(0, 6)" :key="item.question_id" class="notice-line failed-line">{{ item.suggestion || `AI 检查尝试 ${item.attempts || item.max_attempts} 次仍未完成，本题已丢弃。` }}</p>
        <p v-if="failedChecks.length > 6">其余 {{ failedChecks.length - 6 }} 道检查失败也已丢弃，全部计入上方数量。</p>
      </div>

      <!-- 失败与淘汰提醒：持久显示，不随 toast 消失 -->
      <div v-if="failedItems.length" class="result-notice">
        <p class="notice-title">{{ currentJob?.pipeline ? "生成失败" : "导入失败" }}（{{ failedItems.length }} 项）</p>
        <div class="notice-items">
          <p v-for="(f, i) in visibleFailedItems" :key="`${failedPage}-${i}`" class="notice-line failed-line" :title="f.error"><span class="discard-code">{{ f.outline_code || '无大纲代码' }}</span><span>{{ f.error }}</span></p>
        </div>
        <div v-if="failedPageCount > 1" class="notice-pager"><button type="button" :disabled="failedPage <= 1" @click="failedPage--">上一页</button><span>第 {{ failedPage }} / {{ failedPageCount }} 页</span><button type="button" :disabled="failedPage >= failedPageCount" @click="failedPage++">下一页</button></div>
      </div>
      <div v-if="discardedRows.length" class="result-notice">
        <p class="notice-title">未通过 AI 检查，已自动淘汰（{{ discardedRows.length }} 题）——点击查看原题、AI 评分与淘汰原因</p>
        <div class="notice-items">
        <button
          v-for="row in visibleDiscardRows"
          :key="row.id"
          type="button"
          class="notice-line discard-line"
          :title="row.reason || '点击查看淘汰原因'"
          @click="openDiscardDetail(row)"
        ><span class="discard-code">{{ row.outlineCode || "无大纲代码" }}</span><span class="discard-summary">{{ row.topic ? `${row.topic}：` : '' }}{{ row.reason || "质量不达标" }}</span><span class="discard-line-open">详情 ›</span></button>
        </div>
        <div v-if="discardPageCount > 1" class="notice-pager"><button type="button" :disabled="discardPage <= 1" @click="discardPage--">上一页</button><span>第 {{ discardPage }} / {{ discardPageCount }} 页</span><button type="button" :disabled="discardPage >= discardPageCount" @click="discardPage++">下一页</button></div>
      </div>
    </section>

   </div>

    <!-- 历史页：左侧任务列表，右侧是上方复用的详情。 -->
    <section v-if="historyOnly" class="panel history-sidebar">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>历史出题记录</h2>
        <span class="history-total">{{ historyTotal }} 条</span>
      </div>
      <div v-if="hasPerm('question:view_global')" class="history-scopes"><button type="button" :class="{ active: historyScope === 'global' }" @click="historyScope = 'global'; filterHistory()">全部任务</button><button type="button" :class="{ active: historyScope === 'mine' }" @click="historyScope = 'mine'; filterHistory()">我的任务</button></div>
      <form class="history-filters" @submit.prevent="filterHistory"><input v-model="historyQuery" type="search" placeholder="搜索任务名称" aria-label="搜索历史任务" /><select v-model="historyStatus" aria-label="筛选任务状态" @change="filterHistory"><option value="">全部状态</option><option value="completed">已完成</option><option value="in_progress">进行中</option><option value="failed">失败</option><option value="cancelled">已取消</option></select><button class="ghost-button" type="submit">搜索</button></form>
      <p v-if="historyLoading" class="history-hint">加载任务中...</p>
      <p v-else-if="!jobHistory.length" class="history-hint">当前条件下暂无历史任务</p>
      <div v-else class="history-list">
        <button v-for="job in jobHistory" :key="job.job_id" class="history-job" :class="{ active: currentJob?.job_id === job.job_id }" type="button" @click="selectJob(job)">
          <span class="history-job-top"><strong>{{ job.job_name || '未命名任务' }}</strong><span class="q-status" :class="jobStatusClass(job)">{{ jobStatusLabel(job) }}</span></span>
          <span class="history-job-meta">{{ job.total_count }} 项 · 已完成 {{ job.completed || 0 }} · 失败 {{ job.failed || 0 }}</span>
          <span v-if="jobPlanSummary(job)" class="history-job-meta job-plan-summary">{{ jobPlanSummary(job) }}</span>
          <span v-if="job.owner_name" class="history-job-meta">提交人：{{ job.owner_name }}</span>
        </button>
      </div>
      <div v-if="historyPageCount > 1" class="pager-row">
        <span class="page-total">共 {{ historyTotal }} 个任务</span>
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

  <!-- 淘汰题目详情弹窗（原题快照 + AI 评分 + 淘汰原因） -->
  <DiscardDetailModal v-if="discardDetail" :item="discardDetail" @close="discardDetail = null" />
</template>

<style scoped>
.recovery-notice { margin: 12px 0; padding: 12px 16px; border: 1px solid #e5bf77; border-radius: 10px; background: #fff8e8; color: #805b12; line-height: 1.6; }
.recovery-notice p { margin: 5px 0 0; }
.batch-layout {
  max-width: 800px;
  margin: 0 auto;
}
.batch-submission-error { color:#b91c1c; white-space:pre-wrap; overflow-wrap:anywhere; line-height:1.7; margin:12px 0 0; }
.batch-primary { display: grid; gap: 14px; min-width: 0; }
.batch-primary > .panel { min-width: 0; }
.history-layout { width: 100%; max-width: 1340px; display: grid; grid-template-columns: minmax(250px, 310px) minmax(0, 1fr); align-items: start; gap: 16px; }
.history-layout .batch-primary { grid-column: 2; grid-row: 1; }
.history-sidebar { grid-column: 1; grid-row: 1; min-width: 0; position: sticky; top: 16px; }
.history-total { margin-left: auto; color: #70839a; font-size: 12px; }
.history-scopes { display: flex; border: 1px solid #dce8f7; border-radius: 7px; overflow: hidden; margin: 14px 0 10px; }
.history-scopes button { flex: 1; padding: 7px 4px; border: 0; background: #fff; color: #59718c; font-size: 12px; }
.history-scopes button.active { background: #e9f3ff; color: #075eac; font-weight: 700; }
.history-filters { display: grid; grid-template-columns: 1fr auto; gap: 7px; margin: 10px 0; }
.history-filters input { grid-column: 1 / -1; }
.history-filters input, .history-filters select { min-width: 0; height: 34px; padding: 0 8px; border: 1px solid #dce8f7; border-radius: 6px; background: #fff; font-size: 12px; }
.history-filters .ghost-button { height: 34px; }
.history-list { display: grid; gap: 7px; max-height: calc(100vh - 270px); min-height: 100px; overflow-y: auto; padding-right: 3px; }
.history-job { display: grid; gap: 6px; width: 100%; padding: 11px 10px; border: 1px solid #e1e9f3; border-radius: 7px; background: #fff; text-align: left; color: #33465e; }
.history-job:hover { border-color: #9fc8ee; background: #f8fbff; }
.history-job.active { border-color: #1385f8; background: #eff7ff; box-shadow: inset 3px 0 #1385f8; }
.history-job-top { display: flex; align-items: start; gap: 7px; }
.history-job-top strong { flex: 1; min-width: 0; font-size: 13px; line-height: 1.4; }
.history-job-meta { display: block; color: #78889b; font-size: 11px; }
.history-hint { padding: 24px 6px; color: #7d8a9b; font-size: 12px; text-align: center; }
.history-empty { min-height: 260px; display: grid; align-content: center; justify-items: center; text-align: center; color: #6e7b8f; }
.history-empty h2 { color: #33465e; }
.history-empty p { max-width: 330px; font-size: 13px; line-height: 1.7; }
.clear-current { margin-left: auto; font-size: 12px; }
@media (max-width: 900px) { .history-layout { grid-template-columns: minmax(0, 1fr); } .history-sidebar, .history-layout .batch-primary { grid-column: 1; position: static; } .history-sidebar { grid-row: 1; } .history-layout .batch-primary { grid-row: 2; } .history-list { max-height: 350px; } }

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

.field > label.ui-help-heading { display:flex; align-items:center; }

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
.status-partial { background: #fff7e4; color: #946100; }
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
  min-width: 0;
}

.result-row {
  display: flex;
  align-items: center;
  gap: 12px;
  min-height: 40px;
  padding: 10px 12px;
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
.row-content { flex: 1; min-width: 0; display: grid; gap: 4px; }
.row-topic { font-size: 11px; color: #6e7b8f; overflow-wrap: anywhere; }
.row-check-detail { font-size: 12px; line-height: 1.6; color: #52657c; overflow-wrap: anywhere; }
.row-check-detail.retrying { color: #94600f; }
.row-state.retrying { background: #fff3dc; color: #94600f; }
@media (max-width: 600px) {
  .result-row { flex-wrap: wrap; gap: 6px; }
  .row-no { width: 18px; }
  .row-content { flex-basis: calc(100% - 30px); }
  .row-state { margin-left: 24px; }
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
.row-state.muted { background: #f0f3f7; color: #6e7b8f; }

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
  min-width: 0;
  max-width: 100%;
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
.notice-items { max-height: 320px; overflow-y: auto; overflow-x: hidden; min-width: 0; }
.notice-line.failed-line { display: flex; align-items: flex-start; gap: 8px; white-space: normal; min-width: 0; }
.discard-summary, .failed-line span:last-child { flex: 1; min-width: 0; display: -webkit-box; -webkit-box-orient: vertical; -webkit-line-clamp: 2; overflow: hidden; overflow-wrap: anywhere; white-space: normal; line-height: 1.5; }
/* 淘汰行可点击：弹出原题快照与 AI 评分 */
.notice-line.discard-line {
  display: flex;
  align-items: flex-start;
  gap: 7px;
  width: 100%;
  padding: 4px 6px;
  margin: 2px 0;
  border: 1px solid transparent;
  border-radius: 5px;
  background: transparent;
  text-align: left;
  cursor: pointer;
  font: inherit;
  white-space: normal;
  min-width: 0;
}
.notice-line.discard-line:hover {
  background: #fff0f0;
  border-color: #f0c9cf;
}
/* 行首大纲代码：等宽字体突出，老师一眼定位知识点 */
.discard-code {
  flex: none;
  font-family: ui-monospace, Menlo, monospace;
  font-weight: 700;
  color: #a23b4b;
  margin-right: 6px;
}
.discard-line-open {
  flex: none;
  color: #0571dc;
  font-weight: 600;
}
.notice-pager { display: flex; justify-content: flex-end; align-items: center; gap: 10px; padding-top: 8px; color: #8a3b47; font-size: 11px; }
.notice-pager button { border: 1px solid #edc9cc; background: #fff; border-radius: 5px; padding: 4px 8px; color: #a23b4b; }
.notice-pager button:disabled { opacity: .45; cursor: default; }
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

.check-failure-notice { border: 1px solid #efb0b0; border-left: 4px solid #c43c3c; background: #fff5f5; }
.check-failure-notice h2, .check-failure-notice .notice-title { color: #9d2424; }

.pipeline-overview { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 18px; padding: 14px 16px; margin: 12px 0; border: 1px solid #b9ded2; border-radius: 8px; background: #f3fbf8; }
.pipeline-overview.pipeline-overview-failed { border-color: #efb0b0; background: #fff5f5; }
.pipeline-overview.pipeline-overview-failed strong { color: #9d2424; }
.pipeline-overview strong { color: #08745b; font-size: 18px; }
.pipeline-overview span { color: #526473; font-size: 13px; }

.batch-mode-options { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
.batch-mode-choice { display: grid; grid-template-columns: 16px minmax(0, 1fr); gap: 10px; align-items: start; min-width: 0; padding: 13px 14px; border: 1px solid #dde7f2; border-radius: 8px; background: #fbfcfe; cursor: pointer; transition: border-color .15s, background-color .15s; }
.batch-mode-choice:hover { border-color: #a8c9ed; }
.batch-mode-choice.active { border-color: #1385f8; background: #eff7ff; }
.batch-mode-choice:has(input:focus-visible) { outline: 2px solid #85bbf3; outline-offset: 3px; }
.batch-mode-choice input { width: 16px; height: 16px; margin: 2px 0 0; accent-color: #1385f8; }
.batch-mode-choice strong { display: block; color: #334b67; font-size: 13px; line-height: 1.5; }
.batch-mode-choice.active strong { color: #1167b4; }
.batch-mode-choice small { display: block; margin-top: 4px; color: #72849a; font-size: 11px; line-height: 1.6; font-weight: 400; }
.batch-job-fields { align-items: start; }
.batch-job-fields .field { min-width: 0; align-content: start; }
.batch-job-fields .field > label { display:flex; align-items:center; gap:8px; min-height:20px; margin-bottom:6px; }
.batch-job-fields input { box-sizing: border-box; }
.batch-count-hint { margin: -10px 0 0; line-height: 1.6; }
.batch-candidate-summary { display: flex; align-items: baseline; gap: 7px; margin-top: -9px; color: #687d93; font-size: 12px; line-height: 1.7; overflow-wrap: anywhere; }
.batch-candidate-summary strong { color: #1d669f; font-weight: 700; }
.coverage-dot { flex: none; width: 6px; height: 6px; border-radius: 50%; background: #6fa8d8; align-self: center; }
.coverage-dot.checking { background: #a9b8c8; }
.coverage-dot.unavailable { background: #c49255; }
.coverage-error { color: #987144; }
.batch-coverage-notices:empty { display: none; }
.batch-coverage-notices p { margin: -5px 0 0; font-size: 12px; line-height: 1.8; overflow-wrap: anywhere; }
.batch-coverage-notices p+p { margin-top: 5px; }
.batch-coverage-notices .preview-warn { color: #a15143; }
.batch-coverage-notices .coverage-checked { color: #748396; }
@media (max-width: 600px) { .batch-mode-options, .batch-job-fields { grid-template-columns: minmax(0, 1fr); } .batch-mode-choice { padding: 12px; } }
.sr-only { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0 0 0 0); border: 0; }
</style>
