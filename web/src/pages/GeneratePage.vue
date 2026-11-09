<script setup>
import {rememberJourney,clearJourney,journeyTask} from "../authoringJourney.js";
import {questionReviewUi} from "../questionReviewUi.js";
import HelpHint from "../components/HelpHint.vue";
import {defineAsyncComponent} from "vue";
const WorkflowDiagnostics=defineAsyncComponent(()=>import("../components/WorkflowDiagnostics.vue"));
import { ref, computed, onMounted, onBeforeUnmount, watch } from "vue";
import { api } from "../api.js";
import { validGenerationCount } from "../generationCount.js";
import { validPresentation, presentationPayload } from '../presentation.js';
import { currentUser, hasPerm, getToken } from "../auth.js";
import { notifyNavigationWorkChanged } from "../navigation.js";
import { useRoute, useRouter } from "vue-router";
import { useAICheckProgress } from "../aiCheckProgress.js";
import { checkOutcomeMessage, checkFailureItems, availableGeneratedQuestions, retryingCheckItems, checkItemState, mergeGenerationResults, generationPendingMessage, generationFailureMessage } from "../aiCheckProgressBatch.js";
import { clearGenerationWorkspace, loadGenerationWorkspace, saveGenerationWorkspace } from "../generationWorkspaceState.js";
const KnowledgeProvenance=defineAsyncComponent(()=>import("../components/KnowledgeProvenance.vue"));
import KnowledgeWorkflowSelector from "../components/KnowledgeWorkflowSelector.vue";
import { learningCapabilities } from "../personalLearning.js";
import PersonalExperienceSelector from '../components/PersonalExperienceSelector.vue';
import PersonalExperienceProvenance from '../components/PersonalExperienceProvenance.vue';
import KnowledgePointPicker from "../components/KnowledgePointPicker.vue";
import GenerationPolicyNotice from "../components/GenerationPolicyNotice.vue";
import {isBasicMedicalPoint} from '../generationPolicy.js';
import AICheckScoreButton from "../components/AICheckScoreButton.vue";
import DiscardDetailModal from "../components/DiscardDetailModal.vue";

// AI 检查分段进度（生成成功后轮询，见 aiCheckProgress.js）
const { progress: aiProgress, isTracking: trackingChecks, start: startAIProgress, stop: stopAIProgress } = useAICheckProgress();
// 检查完成后通过数即“题目整理”角标的变化来源，通知侧栏立即重算。
watch(() => !!aiProgress.value && !aiProgress.value.stalled && aiProgress.value.checking === 0, (done) => {
  if (done) notifyNavigationWorkChanged();
});
const failedChecks = computed(() => checkFailureItems(aiProgress.value || {}));
const route = useRoute();
const router = useRouter();

// 知识点选择（单选，使用完善的知识点选择器：精确+模糊搜索）
const selectedKPs = ref([]); // 单选模式下始终 0/1 个
const selectedKP = computed(() => (selectedKPs.value.length ? selectedKPs.value[0] : null));
const containsBasic=computed(()=>selectedKPs.value.some(isBasicMedicalPoint));

// 出题配置
const selectedCount = ref(1);
const selectedDifficulty = ref("0.65");

const applyPersonalExperiences = ref(true);
const workflowMode=ref("direct");
const presentation=ref(null);

// 状态
const toast = ref("");
const accessNotice = ref("");
const loading = ref(false);
const progressMsg = ref("");
const stats = ref({ question_count: 0, knowledge_count: 0 });
const selectedKnowledgeVersion = ref(null);
const generationRunId = ref("");
let restoredRunId = "";
const generationQuestionIds = ref([]);
const hiddenDeletedIds = ref([]);
const hiddenDeletedCount = ref(0);
const runKnowledgePoints = ref([]);
const generationStartedAt = ref(0);
const generationCompletedAt = ref(0);
const checkStartedAt = ref(0);
const checkCompletedAt = ref(0);
const workspaceStatus = ref("idle");
const clock = ref(Date.now());
let clockTimer = null;
let recoveryTimer = null;
let recoveringRun = false;
let workspaceReady = false;
let disposed = false;
// A detached page may still have a pending fetch. Bind every continuation and
// cache write to the session that created this workspace, not the next login.
const workspaceIdentity = JSON.stringify([getToken(), currentUser.value?.id, currentUser.value?.role]);
function workspaceIsCurrent() {
  return !disposed && workspaceIdentity === JSON.stringify([getToken(), currentUser.value?.id, currentUser.value?.role]);
}
function runIsCurrent(runId) { return workspaceIsCurrent() && generationRunId.value === runId; }

// 生成设置中的数量必须和当前用户能看到的范围一致：
// tier_counts 是正式/待审核/淘汰三层的独立计数，question_count 仅是旧的待审核层字段。
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

// 生成结果
const generatedQuestions = ref([]);
const currentIndex = ref(0);
const stem = ref("");
const options = ref([]);
const answer = ref("");
const explanation = ref("");
// 淘汰详情弹窗：当前点开的淘汰明细项（aiProgress.items 中 discarded 的条目）
const discardDetail = ref(null);
let optionIdCounter = 0;

function timestamp(value) {
  const parsed = value ? new Date(value).getTime() : 0;
  return Number.isFinite(parsed) ? parsed : 0;
}

function formatDuration(start, end) {
  if (!start) return "—";
  const milliseconds = Math.max(0, (end || clock.value) - start);
  if (end && milliseconds < 60_000) return `${(milliseconds / 1000).toFixed(1)} 秒`;
  const totalSeconds = Math.floor(milliseconds / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = String(totalSeconds % 60).padStart(2, "0");
  return `${String(minutes).padStart(2, "0")}:${seconds}`;
}

const generationDuration = computed(() => formatDuration(generationStartedAt.value, generationCompletedAt.value));
const generationFailed = computed(() => workspaceStatus.value === "failed");
const checkDuration = computed(() => generationFailed.value || !aiProgress.value || !generationQuestionIds.value.length ? "—" : formatDuration(checkStartedAt.value, checkCompletedAt.value));
const hasWorkspaceRecord = computed(() => !!generationRunId.value || generatedQuestions.value.length > 0 || !!aiProgress.value);

// 单题检查通过即可展示；同组其余题继续处理，暂存内容始终隐藏。
// 服务端同样不下发暂存题目，这里是第二道门：旧缓存快照也不会提前露出内容。
const checksSettled = computed(() => !["generating", "recovering", "checking"].includes(workspaceStatus.value));

// 展示列表：检查中被淘汰的题自动移出预览（淘汰原因单独展示）
const displayQuestions = computed(() => availableGeneratedQuestions(generatedQuestions.value, aiProgress.value).filter(q => q.library_state !== "deleted"));
const retryingChecks = computed(() => retryingCheckItems(aiProgress.value));
const questionCheckRows = computed(() => {
  const items = new Map((aiProgress.value?.items || []).map(item => [item.question_id, item]));
  return generationQuestionIds.value.map((id, index) => ({
    id, no: index + 1, state: checkItemState(items.get(id), clock.value),
  }));
});
// 当前题在展示列表中的序号（1 起）
const displayIndex = computed(() => {
  const q = generatedQuestions.value[currentIndex.value];
  if (!q) return 0;
  return displayQuestions.value.findIndex((x) => x.id === q.id);
});
function showDisplayQuestion(dIndex) {
  const q = displayQuestions.value[dIndex];
  if (!q) return;
  const realIndex = generatedQuestions.value.findIndex((x) => x.id === q.id);
  if (realIndex >= 0) showQuestion(realIndex);
}
function prevDisplay() {
  if (displayIndex.value > 0) showDisplayQuestion(displayIndex.value - 1);
}
function nextDisplay() {
  if (displayIndex.value < displayQuestions.value.length - 1) showDisplayQuestion(displayIndex.value + 1);
}

// 当前展示题目的 ID（供 AI 检查评分按钮查询检查结果）
const currentQuestionId = computed(() => generatedQuestions.value[currentIndex.value]?.id || "");

// 当前预览的题被淘汰时，自动跳回第一道通过的题
watch(displayQuestions, (list) => {
  const q = generatedQuestions.value[currentIndex.value];
  if (q && !list.some((x) => x.id === q.id) && list.length) {
    showDisplayQuestion(0);
  }
});

const workflowSteps = computed(() => [
  {
    number: "01",
    title: "题目生成",
    detail: loading.value ? (progressMsg.value || "命题请求已提交，系统正在生成试题") : generationRunId.value ? (runKnowledgePoints.value.length ? `围绕「${runKnowledgePoints.value[0].topic || runKnowledgePoints.value[0].outline_code}」生成` : "本次已提交的命题任务") : "选择大纲要点与难度后开始",
    // 全部题目被淘汰时没有可预览的题干，完成态以生成结束时间为准，
    // 不能依赖 stem（预览字段）判断第一步是否完成。
    state: generationFailed.value ? "attention" : loading.value ? "active" : generationCompletedAt.value ? "done" : "waiting",
    status: generationFailed.value ? "生成失败" : "",
    timing: generationDuration.value,
    timingLabel: loading.value ? "已用时" : "处理耗时",
  },
  {
    number: "02",
    title: "质量初检",
    detail: generationFailed.value ? "本次未开始首检" : aiProgress.value?.checking > 0
      ? `正在检查 ${aiProgress.value.checking} 道题`
      : aiProgress.value?.stalled
        ? "检查仍在后台进行，可稍后查看"
        : aiProgress.value?.exhausted > 0
	        ? `${aiProgress.value.exhausted} 道检查未完成，已丢弃；本次成功出题 ${aiProgress.value.passed || 0} 道，可重新生成新题`
          : aiProgress.value
            ? "格式、答案与医学质量已检查"
            : "落库后自动异步执行一次",
    state: aiProgress.value?.checking > 0 ? "active" : aiProgress.value?.stalled || aiProgress.value?.exhausted > 0 ? "attention" : aiProgress.value ? "done" : "waiting",
    timing: checkDuration.value,
    timingLabel: aiProgress.value?.checking > 0 ? "已用时" : "处理耗时",
  },
  {
    number: "03",
    title: "进入题目整理",
	  detail: aiProgress.value
	    ? checkOutcomeMessage(aiProgress.value)
	    : "质检通过后进入题目整理，确认后进入普通题库",
    state: aiProgress.value?.checking > 0 ? ((aiProgress.value?.passed || 0) > 0 ? "active" : "waiting") : aiProgress.value?.stalled || aiProgress.value?.exhausted > 0 ? "attention" : aiProgress.value ? "done" : "waiting",
  },
].map((step) => ({
  ...step,
  status: step.status || {
    waiting: "待执行",
    active: "执行中",
    done: "已完成",
    attention: "需关注",
  }[step.state],
})));

function showToast(message) {
  toast.value = message;
  window.clearTimeout(showToast.timer);
  toast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

function persistWorkspace() {
  const userId = currentUser.value?.id;
  if (!workspaceReady || !userId || !workspaceIsCurrent()) return;
  if (!hasWorkspaceRecord.value) {
    clearGenerationWorkspace(userId);
    return;
  }
  saveGenerationWorkspace(userId, {
    runId: generationRunId.value,
    questionIds: generationQuestionIds.value,
    hiddenDeletedIds: hiddenDeletedIds.value,
    hiddenDeletedCount: hiddenDeletedCount.value,
    runKnowledgePoints: runKnowledgePoints.value,
    workspaceStatus: workspaceStatus.value,
    selectedKPs: selectedKPs.value,
    selectedCount: selectedCount.value,
    selectedDifficulty: selectedDifficulty.value,
    applyPersonalExperiences: applyPersonalExperiences.value,
    experienceRole: currentUser.value?.role || '',
    generatedQuestions: generatedQuestions.value,
    currentIndex: currentIndex.value,
    progressMsg: progressMsg.value,
    aiProgress: aiProgress.value,
    generationStartedAt: generationStartedAt.value,
    generationCompletedAt: generationCompletedAt.value,
    checkStartedAt: checkStartedAt.value,
    checkCompletedAt: checkCompletedAt.value,
  });
}

watch([
  selectedKPs, selectedCount, selectedDifficulty, applyPersonalExperiences,
  generatedQuestions, currentIndex, progressMsg, aiProgress,
  generationRunId, generationQuestionIds, hiddenDeletedIds, hiddenDeletedCount, runKnowledgePoints, workspaceStatus,
  generationStartedAt, generationCompletedAt, checkStartedAt, checkCompletedAt,
], persistWorkspace, { deep: true });

function resetQuestionPreview() {
  generatedQuestions.value = [];
  currentIndex.value = 0;
  stem.value = "";
  options.value = [];
  answer.value = "";
  explanation.value = "";
}

function makeGenerationRunId() {
  if (globalThis.crypto?.randomUUID) return `gen-${globalThis.crypto.randomUUID()}`;
  return `gen-${Date.now()}-${Math.random().toString(16).slice(2)}`;
}

function updateCheckTiming(snapshot) {
  if (!snapshot || generationFailed.value || !generationQuestionIds.value.length) return;
  const items = snapshot?.items || [];
  const starts = items.map((item) => timestamp(item.check_started_at)).filter(Boolean);
  const completions = items.map((item) => timestamp(item.check_completed_at)).filter(Boolean);
  if (starts.length) {
    checkStartedAt.value = Math.min(...starts);
  } else if (!checkStartedAt.value) {
    checkStartedAt.value = (starts.length ? Math.min(...starts) : 0) || generationCompletedAt.value || Date.now();
  }
  if (snapshot && snapshot.checking === 0 && !snapshot.stalled) {
    checkCompletedAt.value = (completions.length ? Math.max(...completions) : 0) || checkCompletedAt.value || Date.now();
    workspaceStatus.value = snapshot.exhausted > 0 ? "attention" : "completed";
  }
}

watch(aiProgress, updateCheckTiming);

function onCheckFinished(snapshot) {
  updateCheckTiming(snapshot);
  progressMsg.value = checkOutcomeMessage(snapshot);
  // 检查落定后回捞命题运行：通过的题目此时才随运行详情下发（暂存题目服务端不下发）。
  if ((snapshot?.passed || 0) > 0 && generatedQuestions.value.length + hiddenDeletedCount.value < snapshot.passed) {
    refreshGenerationRun();
  }
  persistWorkspace();
}

watch(() => aiProgress.value?.passed || 0, (passed, previous) => {
  if (passed > previous) notifyNavigationWorkChanged();
  if (passed > previous && passed > generatedQuestions.value.length + hiddenDeletedCount.value) refreshGenerationRun();
});

function startCheckTracking(questionIds, preserve = false) {
  if (!questionIds.length) return;
  if (!checkStartedAt.value) checkStartedAt.value = generationCompletedAt.value || Date.now();
  workspaceStatus.value = "checking";
  startAIProgress(questionIds, onCheckFinished, { preserve });
}

function applyGenerationRun(data, { restoring = false } = {}) {
  const run = data?.run;
  if (!run) return true;
  generationRunId.value = run.id;
  rememberJourney(currentUser.value,{kind:"single",id:run.id});
  if (data.request_summary?.knowledge_points?.length) runKnowledgePoints.value = data.request_summary.knowledge_points;
  generationStartedAt.value = timestamp(run.started_at) || generationStartedAt.value || Date.now();
  generationQuestionIds.value = run.question_ids || generationQuestionIds.value;
  const ids = new Set(generationQuestionIds.value);
  hiddenDeletedIds.value = [...new Set((Array.isArray(data.hidden_deleted_ids) ? data.hidden_deleted_ids : []).filter(id => ids.has(id)))];
  const deletedCount = Number(data.hidden_deleted_count);
  hiddenDeletedCount.value = Math.max(hiddenDeletedIds.value.length, Number.isInteger(deletedCount) && deletedCount >= 0 ? deletedCount : 0);

  // 只有 succeeded/failed 是终态；其余状态（pending 已入队、running 执行中，
  // 以及未来新增的中间状态）一律视为进行中并继续轮询，避免版本偏差时把
  // 进行中的任务误判成“已生成 0 道题”。
  if (run.status !== "succeeded" && run.status !== "failed") {
    loading.value = true;
    workspaceStatus.value = "generating";
    progressMsg.value = data.provider_waiting ? "模型服务暂时不稳定，系统正等待自动恢复；任务已保留，无需重复提交。" : generationPendingMessage(run, restoring);
    persistWorkspace();
    return false;
  }

  loading.value = false;
  generationCompletedAt.value = timestamp(run.completed_at) || generationCompletedAt.value || Date.now();
  if (run.status === "failed") {
    stopAIProgress();
    aiProgress.value = null;
    checkStartedAt.value = 0;
    checkCompletedAt.value = 0;
    workspaceStatus.value = "failed";
    progressMsg.value = generationFailureMessage(run.error);
    persistWorkspace();
    return true;
  }

  const selectedId = generatedQuestions.value[currentIndex.value]?.id;
  const deleted = new Set(hiddenDeletedIds.value);
  const received = (data.questions || []).filter(q => !deleted.has(q.id) && q.library_state !== "deleted");
  const cached = generatedQuestions.value.filter(q => !deleted.has(q.id) && q.library_state !== "deleted");
  const merged = mergeGenerationResults(generationQuestionIds.value, received, cached, selectedId);
  generatedQuestions.value = merged.questions;
  if (generatedQuestions.value.length) showQuestion(merged.index);
  else resetQuestionPreview();
  if (
    (workspaceStatus.value === "generating" || workspaceStatus.value === "recovering") &&
    generatedQuestions.value.length
  ) {
    showToast(`已有 ${generatedQuestions.value.length} 道通过首检，可立即查看`);
  }
  progressMsg.value = checksSettled.value
    ? (progressMsg.value || `已生成 ${generationQuestionIds.value.length} 道题`)
    : `已生成 ${generationQuestionIds.value.length} 道题，正在自动检查质量`;
  checkStartedAt.value ||= generationCompletedAt.value;
  // 检查已落定（如检查完成后回捞）时不重启进度轮询，避免状态反复与循环回捞
  if (!checkCompletedAt.value) {
    if (!trackingChecks.value) startCheckTracking(generationQuestionIds.value, restoring);
  }
  loadStats();
  persistWorkspace();
  return true;
}

watch(()=>route.query.run_id,id=>{if(typeof id==='string'&&id&&id!==generationRunId.value){generationRunId.value=id;refreshGenerationRun();}});

function scheduleRunRecovery() {
  if (!workspaceIsCurrent()) return;
  window.clearTimeout(recoveryTimer);
  recoveryTimer = window.setTimeout(refreshGenerationRun, 2500);
}

async function refreshGenerationRun() {
  if (!workspaceIsCurrent() || !generationRunId.value || recoveringRun) return;
  recoveringRun = true;
  const runId = generationRunId.value;
  try {
    const data = await api.getGenerationRun(runId);
    if (!runIsCurrent(runId)) return;
    const finished = applyGenerationRun(data, { restoring: restoredRunId === runId });
    if (!finished) scheduleRunRecovery();
  } catch (e) {
    if (!runIsCurrent(runId) || e.staleSession) return;
    if (e.status === 400 || e.status === 404) {
      loading.value = false;
      workspaceStatus.value = "failed";
      generationCompletedAt.value ||= Date.now();
      progressMsg.value = "生成任务未能建立，请重新提交";
      persistWorkspace();
      return;
    }
    loading.value = true;
    workspaceStatus.value = "recovering";
    progressMsg.value = "正在重新连接命题任务，已生成结果不会丢失";
    scheduleRunRecovery();
  } finally {
    recoveringRun = false;
    if (workspaceIsCurrent() && generationRunId.value && generationRunId.value !== runId) refreshGenerationRun();
  }
}

function restoreWorkspace() {
  const snapshot = loadGenerationWorkspace(currentUser.value?.id);
  if (snapshot) {
    selectedKPs.value = Array.isArray(snapshot.selectedKPs) ? snapshot.selectedKPs : [];
    selectedCount.value = snapshot.selectedCount || 1;
    selectedDifficulty.value = snapshot.selectedDifficulty || "0.65";
    if (snapshot.experienceRole === (currentUser.value?.role || '') && typeof snapshot.applyPersonalExperiences === 'boolean') applyPersonalExperiences.value = snapshot.applyPersonalExperiences;
    generationRunId.value = snapshot.runId || "";
    restoredRunId = generationRunId.value;
    generationQuestionIds.value = snapshot.questionIds || [];
    hiddenDeletedIds.value = Array.isArray(snapshot.hiddenDeletedIds) ? snapshot.hiddenDeletedIds : [];
    hiddenDeletedCount.value = Number(snapshot.hiddenDeletedCount) || 0;
    runKnowledgePoints.value = snapshot.runKnowledgePoints || snapshot.generatedQuestions?.[0]?.knowledge_points || [];
    workspaceStatus.value = snapshot.workspaceStatus || "idle";
    generatedQuestions.value = (snapshot.generatedQuestions || []).filter(q => !hiddenDeletedIds.value.includes(q.id) && q.library_state !== "deleted");
    currentIndex.value = snapshot.currentIndex || 0;
    progressMsg.value = snapshot.progressMsg || "";
    aiProgress.value = snapshot.aiProgress || null;
    generationStartedAt.value = snapshot.generationStartedAt || 0;
    generationCompletedAt.value = snapshot.generationCompletedAt || 0;
    checkStartedAt.value = snapshot.checkStartedAt || 0;
    checkCompletedAt.value = snapshot.checkCompletedAt || 0;
    if (workspaceStatus.value === "failed" || !generationQuestionIds.value.length) {
      aiProgress.value = null;
      checkStartedAt.value = 0;
      checkCompletedAt.value = 0;
    }
    if (generatedQuestions.value.length) {
      showQuestion(Math.min(currentIndex.value, generatedQuestions.value.length - 1));
    }
    loading.value = workspaceStatus.value === "generating" || workspaceStatus.value === "recovering";
  }
  workspaceReady = true;
  if (generationRunId.value) refreshGenerationRun();
}

function clearWorkspace() {
  const hint = loading.value
    ? "仅清空本页记录，不会取消已提交的命题，也不会删除已生成题目。确定继续吗？"
    : "仅清空本页记录，不会删除已生成题目。确定继续吗？";
  if (!window.confirm(hint)) return;
  if(journeyTask.value?.kind==="single"&&journeyTask.value.id===generationRunId.value)clearJourney(currentUser.value);
  if(route.query.run_id===generationRunId.value){const query={...route.query};delete query.run_id;router.replace({path:route.path,query});}
  stopAIProgress();
  window.clearTimeout(recoveryTimer);
  loading.value = false;
  progressMsg.value = "";
  generationRunId.value = "";
  generationQuestionIds.value = [];
  hiddenDeletedIds.value = []; hiddenDeletedCount.value = 0;
  runKnowledgePoints.value = [];
  generationStartedAt.value = 0;
  generationCompletedAt.value = 0;
  checkStartedAt.value = 0;
  checkCompletedAt.value = 0;
  workspaceStatus.value = "idle";
  aiProgress.value = null;
  discardDetail.value = null;
  resetQuestionPreview();
  clearGenerationWorkspace(currentUser.value?.id);
  showToast("本次命题记录已从工作台清空，题库数据未受影响");
}

function consumeAccessNotice() {
  if (route.query.notice !== "batch-permission") return;
  accessNotice.value = "当前账号未分配批量推理权限；如需使用批量推理，请联系超级管理员在「用户管理」中单独勾选该权限。";
  const query = { ...route.query };
  delete query.notice;
  router.replace({ path: route.path, query });
}

watch(() => route.query.notice, consumeAccessNotice, { immediate: true });

async function loadStats() {
  if (!workspaceIsCurrent() || !hasPerm("stats:view")) return;
  try {
    const fresh = await api.stats("personal");
    if (workspaceIsCurrent()) stats.value = fresh;
  } catch (e) {
    if (!workspaceIsCurrent() || e.staleSession) return;
    console.error(e);
    showToast("加载统计信息失败: " + e.message);
  }
}

// 生成题目
async function generateQuestion() {
  if (!workspaceIsCurrent()) return;
  if (!selectedKP.value) {
    showToast("请先选择大纲要点");
    return;
  }
  if (!validGenerationCount(selectedCount.value)) {
    showToast("生成数量应为 1–20 的整数");
    return;
  }
  if(containsBasic.value){showToast('基础医学知识点已从有效大纲剔除，请选择临床知识点');return;}
  if(workflowMode.value==='knowledge'&&!validPresentation(presentation.value)){showToast('请重新核对病例顺序');return;}
  stopAIProgress();
  window.clearTimeout(recoveryTimer);
  aiProgress.value = null;
  discardDetail.value = null;
  resetQuestionPreview();
  loading.value = true;
  progressMsg.value = "正在生成试题，请稍候...";
  generationRunId.value = makeGenerationRunId();
  runKnowledgePoints.value = selectedKPs.value.map(point => ({ ...point }));
  generationQuestionIds.value = [];
  hiddenDeletedIds.value = []; hiddenDeletedCount.value = 0;
  generationStartedAt.value = Date.now();
  generationCompletedAt.value = 0;
  checkStartedAt.value = 0;
  checkCompletedAt.value = 0;
  workspaceStatus.value = "generating";
  persistWorkspace();
  const runId = generationRunId.value;

  try {
    const genParams = {
      apply_personal_experiences: learningCapabilities(currentUser.value?.permissions).use && applyPersonalExperiences.value,
 workflow_mode:workflowMode.value,
      presentation:presentationPayload(workflowMode.value,presentation.value),
      run_id: runId,
      subject: selectedKP.value.subject || "临床医学",
      category: selectedKP.value.category || "",
      difficulty: selectedDifficulty.value,
      topic: selectedKP.value.topic,
      outline_code: selectedKP.value.outline_code || "",
      knowledge_point_id: selectedKP.value.id,
      version_id: selectedKP.value.version_id,
      count: selectedCount.value,
    };
    console.log("生成参数:", genParams, "选中知识点:", JSON.stringify(selectedKP.value));
    const data = await api.generate(genParams);
    if (!runIsCurrent(runId)) return;
    const finished = applyGenerationRun(data);
    if (!finished) scheduleRunRecovery();
    if (data.run?.status === "succeeded") {
      showToast(`本次题目已生成，命题耗时 ${generationDuration.value}`);
    }
  } catch (e) {
    if (!runIsCurrent(runId) || e.staleSession) return;
    // 服务端已明确返回错误（4xx/5xx）：直接展示原因，不再误报为连接中断。
    if (e && e.status) {
      loading.value = false;
      workspaceStatus.value = "failed";
      generationCompletedAt.value ||= Date.now();
      progressMsg.value = e.message || "生成失败，请重新提交";
      showToast(progressMsg.value);
      persistWorkspace();
      return;
    }
    // 网络中断不等于后台任务失败：保留 run_id 并尝试从服务端恢复。
    progressMsg.value = "连接中断，正在恢复命题任务状态";
    workspaceStatus.value = "recovering";
    showToast("连接中断，正在从服务端恢复本次命题");
    await refreshGenerationRun();
  } finally {
    if (runIsCurrent(runId) && workspaceStatus.value !== "generating" && workspaceStatus.value !== "recovering") {
      loading.value = false;
    }
  }
}

function showQuestion(index) {
  if (index < 0 || index >= generatedQuestions.value.length) return;
  currentIndex.value = index;
  const q = generatedQuestions.value[index];
  stem.value = q.clinical_stem || "";
  options.value = (q.options || []).map((o) => ({ id: ++optionIdCounter, text: o.text }));
  answer.value = q.answer || "";
  explanation.value = q.explanation || "";
}

function optionLabel(index) {
  return String.fromCharCode(65 + index);
}

onMounted(() => {
  clockTimer = window.setInterval(() => { clock.value = Date.now(); }, 1000);
  loadStats();
  restoreWorkspace();
  if(typeof route.query.run_id==="string"&&route.query.run_id!==generationRunId.value){generationRunId.value=route.query.run_id;refreshGenerationRun();}
});

onBeforeUnmount(() => {
  disposed = true;
  workspaceReady = false;
  window.clearTimeout(showToast.timer);
  window.clearInterval(clockTimer);
  window.clearTimeout(recoveryTimer);
});
</script>

<template>
  <div v-if="accessNotice" class="permission-notice" role="status">
    <span>{{ accessNotice }}</span>
    <button type="button" aria-label="关闭提示" @click="accessNotice = ''">×</button>
  </div>
  <div class="main-grid single-generation-grid">
    <div class="editor-column">
      <!-- 已通过题目可立即切换，后续结果不打断当前阅读 -->
      <section v-if="displayQuestions.length" class="panel question-nav">
        <button class="ghost-button" type="button" :disabled="displayIndex <= 0" @click="prevDisplay">←</button>
        <button
          v-for="(q, i) in displayQuestions"
          :key="q.id"
          class="nav-chip"
          :class="{ active: i === displayIndex }"
          type="button"
          :title="`第 ${i + 1} 题`"
          @click="showDisplayQuestion(i)"
        >
          {{ i + 1 }}
        </button>
        <button class="ghost-button" type="button" :disabled="displayIndex >= displayQuestions.length - 1" @click="nextDisplay">→</button>
        <span class="nav-info">第 {{ displayIndex + 1 }} / {{ displayQuestions.length }} 题</span>
      </section>

      <WorkflowDiagnostics :task-id="generationRunId" />
      <p v-if="hiddenDeletedCount > 0" class="connection-notice" role="status">有 {{ hiddenDeletedCount }} 道题已移入删除题库，本页不再展示；本次生成与首检记录仍保留。</p>
      <!-- 生成结果只读展示，逐题通过后展示内容 -->
      <section class="panel">
        <div class="section-heading">
          <span class="dot blue"></span>
          <h2>题目预览</h2>
          <small v-if="answer && displayQuestions.length">正确答案：{{ answer }}</small>
          <AICheckScoreButton v-if="currentQuestionId && displayQuestions.length" class="preview-ai-score" :question-id="currentQuestionId" />
        </div>
        <div v-if="generationFailed" class="preview-empty generation-failure-notice" role="alert">
          <span class="preview-empty-mark">失败</span>
          <div><strong>本次题目生成失败</strong><p>{{ progressMsg }}</p><p>本次未产生可用题目。可检查提示、调整设置后重新生成。</p></div>
        </div>
        <div v-else-if="!displayQuestions.length && hiddenDeletedCount > 0 && checksSettled" class="preview-empty">
          <span class="preview-empty-mark">已删除</span>
          <div><strong>题目已移入删除题库</strong><p>删除后的题目不在本页展示，本次生成与首检记录仍保留。</p></div>
        </div>
        <div v-else-if="!displayQuestions.length && !checksSettled" class="preview-empty">
          <span class="preview-empty-mark">{{ loading ? "生成" : "质检" }}</span>
          <div>
            <div class="ui-help-heading"><strong>{{ loading ? "正在生成试题" : "AI 质量检查进行中" }}</strong>
            <HelpHint v-if="loading" label="后台任务说明">任务已在后台开始生成，可继续使用其他页面；完整题目生成后自动首检，通过的题目会陆续展示。</HelpHint>
            <HelpHint v-else label="生成结果说明">检查通过后题目才会在此展示并进入题目整理，待本人确认入库；未通过的题目将被自动淘汰，淘汰原因见下方说明。</HelpHint></div>
          </div>
        </div>
        <template v-else-if="stem && displayQuestions.length">
          <KnowledgeProvenance :question="generatedQuestions[currentIndex]" />
          <PersonalExperienceProvenance :question="generatedQuestions[currentIndex]" />
          <p class="readonly-stem">{{ stem }}</p>
          <div class="readonly-options">
            <div v-for="(opt, index) in options" :key="opt.id" class="readonly-option" :class="{ correct: optionLabel(index) === answer }">
              <span class="option-label">{{ optionLabel(index) }}</span>
              <span>{{ opt.text }}</span>
              <span v-if="optionLabel(index) === answer" class="correct-mark">✓ 正确答案</span>
            </div>
          </div>
          <div v-if="explanation" class="readonly-explanation">
            <label>解析</label>
            <p>{{ explanation }}</p>
          </div>
        </template>
        <div v-else class="preview-empty">
          <span class="preview-empty-mark">A2</span>
          <div>
            <strong>{{ aiProgress && checksSettled ? "本次没有可入库的题目" : "从右侧选择一个大纲要点开始命题" }}</strong>
            <p v-if="aiProgress && checksSettled">{{ checkOutcomeMessage(aiProgress) }}。可重新生成新题。</p>

          </div>
        </div>
      </section>

      <!-- AI 检查进度与本次生题具体情况 -->
      <div v-if="aiProgress" class="progress-bar ai-check-progress" :class="{ done: aiProgress.checking === 0 && !aiProgress.stalled, error: aiProgress.stalled || aiProgress.exhausted > 0 }">
        <span v-if="aiProgress.checking > 0" class="spinner"></span>
        <template v-if="aiProgress.checking > 0">{{ aiProgress.passed > 0 ? `已有 ${aiProgress.passed} 道通过首检，可立即查看；其余` : "正在首检，尚无通过题目；还有" }} {{ aiProgress.checking }} 道处理中…</template>
        <template v-else-if="aiProgress.stalled">AI 检查仍在后台进行，可稍后回到本页查看结果</template>
        <template v-else>
          {{ checkOutcomeMessage(aiProgress) }}
        </template>
      </div>

      <p v-if="retryingChecks.length" class="discard-hint">{{ retryingChecks.length }} 道检查正在自动重试，已通过的题目可正常查看；达到尝试上限仍失败才会丢弃。</p>

      <section v-if="aiProgress && questionCheckRows.length" class="panel question-check-panel" aria-label="逐题首检进度">
        <div class="section-heading"><span class="dot blue"></span><h2>逐题首检进度</h2></div>
        <div v-for="row in questionCheckRows" :key="row.id" :data-question-id="row.id" class="question-check-row">
          <div class="question-check-heading"><strong>第 {{ row.no }} 题<template v-if="runKnowledgePoints[0]"> · {{ runKnowledgePoints[0].topic || runKnowledgePoints[0].outline_code }}</template></strong><span class="check-state" :class="row.state.cls">{{ row.state.label }}</span></div>
          <p v-if="row.state.detail" class="check-detail" :class="row.state.cls">{{ row.state.detail }}</p>
        </div>
      </section>
	  <p v-if="aiProgress?.providerWaiting && aiProgress.checking > 0" class="connection-notice" role="status">检查模型服务暂时不稳定，正等待自动恢复；已通过题目仍可查看。</p>
	  <p v-if="aiProgress?.connectionError" class="connection-notice" role="status">{{ aiProgress.connectionError }}</p>

      <section v-if="aiProgress && aiProgress.exhausted > 0" class="panel discard-panel check-failure-notice" role="alert" aria-live="polite">
        <div class="section-heading"><span class="dot red"></span><h2>AI 检查失败，已丢弃 {{ aiProgress.exhausted }} 道题</h2></div>
        <p><strong>本次成功出题 {{ aiProgress.passed }} 道（首检通过，待整理确认）</strong>。检查失败的题目没有进入题库；可以重新生成新题。</p>
        <p v-for="item in failedChecks.slice(0, 6)" :key="item.question_id" class="discard-hint">{{ item.suggestion || `AI 检查尝试 ${item.attempts || item.max_attempts} 次仍未完成，本题已丢弃。` }}</p>
        <p v-if="failedChecks.length > 6">其余 {{ failedChecks.length - 6 }} 道检查失败也已丢弃，全部计入上方数量。</p>
      </section>

      <!-- 淘汰明细：检查不通过的题目已自动删除，点击查看原题、AI 评分与淘汰原因 -->
      <section v-if="aiProgress && aiProgress.discarded > 0" class="panel discard-panel">
        <div class="section-heading">
          <span class="dot red"></span>
          <h2>未通过检查的题目（已自动淘汰）</h2>
        </div>
        <button
          v-for="item in (aiProgress.items || []).filter(i => i.discarded && !i.check_failed)"
          :key="item.question_id"
          type="button"
          class="discard-item"
          @click="discardDetail = item"
        >
          <span class="discard-verdict">{{ item.verdict === "reject" ? "不合格" : "存在问题" }}</span>
          <span class="discard-code" :title="item.outline_code || ''">{{ item.outline_code || "无大纲代码" }}</span>
          <span class="discard-stem">{{ item.stem_summary || item.question_id }}</span>
          <span v-if="item.scores" class="discard-score">最低分 {{ Math.min(item.scores.scientific ?? 100, item.scores.logic ?? 100, item.scores.a2_fit ?? 100, item.scores.answer ?? 100) }}</span>
          <span class="discard-open">查看详情 ›</span>
        </button>
        <HelpHint label="淘汰记录说明">点击淘汰题目查看原题快照、AI 评分与淘汰原因；被淘汰的题目不会进入题库，可调整生成参数后重新出题。</HelpHint>
      </section>

      <section v-if="generatedQuestions.some(q=>q.library_state==='unfiled') && hasPerm('question:confirm')" class="panel authoring-next-panel">
       <strong>通过检查的题目已进入题目整理</strong><p>查看题目，按需修改并确认入个人题库，之后即可导出。</p>
       <RouterLink class="primary-button" to="/new-questions">去题目整理 →</RouterLink>
      </section>
      <section class="panel workflow-panel" id="authoring-task">
        <div class="section-heading">
          <span class="dot blue"></span>
          <h2>本次生成与检查</h2>
          <small>实时显示本次任务的处理阶段与题目结果</small>
          <button v-if="hasWorkspaceRecord" class="workflow-clear" type="button" @click="clearWorkspace">清空本次记录</button>
        </div>
        <div class="workflow-rail">
          <div v-for="step in workflowSteps" :key="step.number" class="workflow-step" :class="step.state">
            <div class="workflow-number">{{ step.state === "done" ? "✓" : step.number }}</div>
            <div>
              <div class="workflow-title">
                <strong>{{ step.title }}</strong>
                <span class="workflow-status">{{ step.status }}</span>
              </div>
              <p>{{ step.detail }}</p>
              <div v-if="step.timing" class="workflow-timer" :class="step.state">
                <span>{{ step.timingLabel }}</span>
                <b>{{ step.timing }}</b>
              </div>
            </div>
          </div>
        </div>
      </section>
    </div>

    <!-- 右侧配置 -->
    <aside class="config-column">
      <section class="panel sticky-panel">
        <h2>生成设置</h2>

        <div v-if="hasPerm('stats:view')" class="metric-row generation-metrics">
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

        <!-- 知识点选择器（精确+模糊搜索，单选） -->
        <div class="kp-selector">
          <label class="field-label ui-help-heading">选择大纲要点<GenerationPolicyNotice /></label>
          <KnowledgePointPicker id="authoring-points" v-model="selectedKPs" :multiple="false" placeholder="搜索考点内容或大纲代码..." @version-change="handleKnowledgeVersionChange" />
        </div>

        <label class="field">
          <span>预估难度</span>
          <select v-model="selectedDifficulty">
            <option value="0.55">简单 (0.55)</option>
            <option value="0.65">中等 (0.65)</option>
            <option value="0.75">偏难 (0.75)</option>
            <option value="0.85">困难 (0.85)</option>
          </select>
        </label>

        <KnowledgeWorkflowSelector v-model="workflowMode" v-model:presentation="presentation" :knowledge-points="selectedKPs" />
        <p v-if="containsBasic" role="alert">所选包含基础医学知识点（已从有效大纲剔除），请更换临床知识点。</p>
        <PersonalExperienceSelector v-model="applyPersonalExperiences" />

        <div class="count-row">
          <label>生成数量：</label>
          <div class="segmented" aria-label="生成数量">
            <button
              v-for="count in [1, 3, 5]"
              :key="count"
              :class="{ active: selectedCount === count }"
              type="button"
              @click="selectedCount = count"
            >
              {{ count }}道
            </button>
          </div>
          <input
            v-model.number="selectedCount"
            type="number"
            min="1"
            max="20"
            class="count-input"
            placeholder="自定义"
          />
        </div>
	    <p v-if="!validGenerationCount(selectedCount)" class="field-hint" role="status">生成数量应为 1–20 的整数</p>

        <button
          class="primary-button full"
          type="button"
          :disabled="loading || !selectedKP || containsBasic || !validGenerationCount(selectedCount)"
          @click="generateQuestion"
        >
          {{ loading ? "生成中..." : "生成试题" }}
        </button>

        <!-- 生成进度提示 -->
        <div v-if="progressMsg && !aiProgress" class="progress-bar">
          <div class="progress-inner" :class="{ done: progressMsg.startsWith('已生成'), error: progressMsg.startsWith('生成失败') }">
            <span v-if="loading" class="spinner"></span>
            {{ progressMsg }}
          </div>
        </div>

        <section v-if="stem && checksSettled" class="preview-card">
          <div class="section-heading compact">
            <span class="dot blue"></span>
            <h3>题目预览</h3>
          </div>
          <p>{{ stem.slice(0, 150) }}{{ stem.length > 150 ? "..." : "" }}</p>
          <ol type="A">
            <li v-for="opt in options" :key="opt.id">{{ opt.text }}</li>
          </ol>
          <strong v-if="answer">答案：{{ answer }}</strong>
        </section>
      </section>
    </aside>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>

  <!-- 淘汰题目详情弹窗（原题快照 + AI 评分 + 淘汰原因） -->
  <DiscardDetailModal v-if="discardDetail" :item="discardDetail" @close="discardDetail = null" />
</template>

<style scoped>
.connection-notice { padding: 12px 16px; border: 1px solid #ead29a; border-radius: 10px; background: #fff9eb; color: #805b12; }
.single-generation-grid { grid-template-columns: minmax(0, 1fr) minmax(470px, 540px); }
.single-generation-grid > .editor-column { grid-template-columns: minmax(0, 1fr); }
@media (max-width: 1450px) { .single-generation-grid .workflow-rail { grid-template-columns: minmax(0, 1fr); } .single-generation-grid .workflow-step { min-height: 0; } }
@media (max-width: 1320px) { .single-generation-grid { grid-template-columns: minmax(0, 1fr); } .single-generation-grid .sticky-panel { position: static; } }
.permission-notice {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin: 0 0 14px;
  padding: 11px 14px;
  border: 1px solid #f1d4d8;
  border-left: 3px solid #c54858;
  border-radius: 7px;
  background: #fff8f9;
  color: #8a3b47;
  font-size: 13px;
  line-height: 1.6;
}

.permission-notice button {
  flex: none;
  border: 0;
  background: transparent;
  color: #a23b4b;
  font-size: 18px;
  line-height: 1;
  cursor: pointer;
}

.generation-metrics {
  align-items: start;
}

.generation-metrics > div {
  min-width: 0;
}

.generation-metrics small {
  display: block;
  overflow: hidden;
  margin-top: 4px;
  color: #8a98a8;
  font-size: 10px;
  line-height: 1.4;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 知识点选择器 */
.kp-selector {
  margin-bottom: 16px;
}

.field-label {
  display: block;
  font-size: 13px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 6px;
}

.selected-kp {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  background: #eff8ff;
  border: 1px solid #b9ddff;
  border-radius: 8px;
}

.kp-info {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.kp-code {
  font-size: 11px;
  font-family: monospace;
  color: #0571dc;
}

.kp-subject {
  font-size: 11px;
  color: #6e7b8f;
}

.kp-topic {
  font-size: 13px;
  color: #172033;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.clear-btn {
  width: 24px;
  height: 24px;
  border: 1px solid #b9ddff;
  border-radius: 50%;
  background: #fff;
  color: #0571dc;
  font-size: 16px;
  cursor: pointer;
  display: grid;
  place-items: center;
  flex-shrink: 0;
}

.clear-btn:hover {
  background: #0571dc;
  color: #fff;
}

.kp-search-box {
  position: relative;
}

.kp-search-input {
  width: 100%;
  height: 38px;
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  padding: 0 12px;
  font-size: 13px;
}

.kp-search-input:focus {
  border-color: #1385f8;
  outline: none;
  box-shadow: 0 0 0 3px rgba(19, 133, 248, 0.1);
}

.kp-dropdown {
  position: fixed;
  top: auto;
  left: auto;
  width: 300px;
  margin-top: 4px;
  background: #fff;
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.12);
  z-index: 1000;
  max-height: 400px;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.kp-list {
  overflow-y: auto;
  max-height: 350px;
}

.kp-item {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 8px 12px;
  border: 0;
  background: transparent;
  text-align: left;
  cursor: pointer;
  font-size: 13px;
  border-bottom: 1px solid #f0f3f7;
}

.kp-item:hover {
  background: #f8fbff;
}

.kp-item-code {
  font-size: 10px;
  font-family: monospace;
  color: #0571dc;
  background: #eff8ff;
  padding: 2px 6px;
  border-radius: 3px;
  flex-shrink: 0;
}

.kp-item-subject {
  font-size: 11px;
  color: #6e7b8f;
  flex-shrink: 0;
}

.kp-item-topic {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.kp-loading, .kp-empty {
  padding: 20px;
  text-align: center;
  color: #6e7b8f;
  font-size: 13px;
}

.kp-load-more {
  padding: 8px;
  text-align: center;
  border-top: 1px solid #f0f3f7;
}

.dropdown-overlay {
  position: fixed;
  inset: 0;
  z-index: 150;
}

/* 其他样式 */
.count-row {
  display: flex;
  align-items: center;
  gap: 10px;
}

.count-row label {
  font-size: 13px;
  color: #6e7b8f;
  white-space: nowrap;
}

.count-input {
  width: 60px;
  height: 34px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 8px;
  text-align: center;
  font-size: 13px;
}

.question-nav {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  flex-wrap: wrap;
}

.nav-chip {
  min-width: 30px;
  height: 30px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #6e7b8f;
  font-size: 13px;
  font-weight: 700;
  cursor: pointer;
}

.nav-chip:hover {
  border-color: #1385f8;
  color: #1385f8;
}

.nav-chip.active {
  background: #1385f8;
  border-color: #1385f8;
  color: #fff;
}

.nav-info {
  font-size: 14px;
  font-weight: 600;
  color: #172033;
}

.remove-btn {
  width: 28px;
  height: 28px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #c54858;
  font-size: 18px;
  cursor: pointer;
  display: grid;
  place-items: center;
  flex-shrink: 0;
}

.remove-btn:hover:not(:disabled) {
  background: #fff0f0;
  border-color: #c54858;
}

.remove-btn:disabled {
  opacity: 0.3;
  cursor: not-allowed;
}

.answer-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 12px;
}

.answer-row label {
  font-size: 13px;
  color: #6e7b8f;
  font-weight: 600;
}

.answer-row select {
  height: 34px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 12px;
  font-size: 14px;
  font-weight: 700;
  color: #1385f8;
}

.explanation-input {
  width: 100%;
  min-height: 120px;
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  padding: 12px;
  font-size: 14px;
  line-height: 1.7;
  color: #435269;
  resize: vertical;
}

.save-section {
  display: flex;
  align-items: center;
  gap: 14px;
}

.save-hint {
  font-size: 13px;
  color: #6e7b8f;
}

.progress-bar {
  margin-top: 10px;
}

.progress-inner {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 12px;
  border-radius: 7px;
  background: #eff8ff;
  color: #0571dc;
  font-size: 13px;
  font-weight: 600;
}

.progress-inner.done {
  background: #f0fff8;
  color: #087c55;
}

.progress-inner.error {
  background: #fff0f0;
  color: #c54858;
}

.spinner {
  width: 14px;
  height: 14px;
  border: 2px solid #b9ddff;
  border-top-color: #0571dc;
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

/* 生成结果只读展示 */
.readonly-stem {
  font-size: 14px;
  line-height: 1.7;
  color: #172033;
  margin: 0 0 12px;
}

.readonly-options {
  display: grid;
  gap: 6px;
  margin-bottom: 12px;
}

.readonly-option {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  color: #3a4658;
}

.readonly-option.correct {
  color: #087c55;
  font-weight: 600;
}

.correct-mark {
  font-size: 12px;
  color: #087c55;
}

.readonly-explanation label {
  font-size: 12px;
  color: #9aa5b4;
}

.readonly-explanation p {
  font-size: 13px;
  line-height: 1.7;
  color: #3a4658;
  margin: 4px 0 0;
}

.preview-empty {
  min-height: 220px;
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  align-content: center;
  align-items: center;
  gap: 22px;
  padding: 26px;
  border: 1px dashed #c9d9ea;
  border-radius: 12px;
  background: linear-gradient(135deg, #f8fbff 0%, #f2f8f6 100%);
}

.preview-empty-mark {
  width: 74px;
  height: 74px;
  display: grid;
  place-items: center;
  border-radius: 22px 8px 22px 8px;
  background: #103b61;
  color: #fff;
  font: 800 20px/1 Georgia, serif;
  letter-spacing: 0.08em;
  box-shadow: 0 12px 24px rgba(16, 59, 97, 0.18);
}

.preview-empty strong {
  display: block;
  margin-bottom: 8px;
  color: #172033;
  font-size: 17px;
}

.preview-empty p {
  max-width: 620px;
  margin: 0;
  color: #607086;
  font-size: 13px;
  line-height: 1.75;
}

.generation-failure-notice { border: 1px solid #efb0b0; background: #fff5f5; }
.generation-failure-notice .preview-empty-mark { background: #a83535; box-shadow: 0 12px 24px rgba(168, 53, 53, 0.16); }
.generation-failure-notice strong, .generation-failure-notice p { color: #9d2424; }

.workflow-panel {
  overflow: hidden;
  background: linear-gradient(160deg, #ffffff 0%, #f7faff 100%);
}

.workflow-clear {
  min-height: 28px;
  border: 1px solid #d7e1ec;
  border-radius: 6px;
  padding: 0 9px;
  background: #fff;
  color: #607086;
  font-size: 11px;
  font-weight: 600;
}

.workflow-clear:hover {
  border-color: #9eb9d5;
  color: #315879;
  background: #f8fbff;
}

.workflow-rail {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px;
}

.workflow-step {
  position: relative;
  display: grid;
  grid-template-columns: 36px minmax(0, 1fr);
  gap: 10px;
  min-height: 138px;
  padding: 15px;
  border: 1px solid #e1e9f2;
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.78);
}

.workflow-step.active {
  border-color: #8dc7ff;
  box-shadow: inset 0 0 0 1px rgba(19, 133, 248, 0.1);
}

.workflow-step.done {
  border-color: #a9ddc8;
  background: rgba(244, 252, 248, 0.9);
}

.workflow-step.attention {
  border-color: #efd28d;
  background: #fffcf4;
}

.workflow-number {
  width: 36px;
  height: 36px;
  display: grid;
  place-items: center;
  border-radius: 50%;
  background: #edf2f7;
  color: #78879a;
  font-size: 11px;
  font-weight: 800;
}

.workflow-step.active .workflow-number { background: #1385f8; color: #fff; }
.workflow-step.done .workflow-number { background: #11835d; color: #fff; font-size: 15px; }
.workflow-step.attention .workflow-number { background: #c78112; color: #fff; }

.workflow-title {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}

.workflow-step strong {
  display: block;
  margin: 3px 0 6px;
  color: #1c2a3d;
  font-size: 14px;
}

.workflow-status {
  flex: none;
  padding: 2px 7px;
  border-radius: 999px;
  background: #edf2f7;
  color: #6e7b8f;
  font-size: 10px;
  font-weight: 700;
  line-height: 1.5;
}

.workflow-step.active .workflow-status { background: #e7f3ff; color: #0571dc; }
.workflow-step.done .workflow-status { background: #e4f5ed; color: #087c55; }
.workflow-step.attention .workflow-status { background: #fff0cc; color: #9a650e; }

.workflow-step p {
  margin: 0;
  color: #6e7b8f;
  font-size: 12px;
  line-height: 1.55;
}

.workflow-timer {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 10px;
  margin-top: 13px;
  padding-top: 10px;
  border-top: 1px solid #e9eef4;
  color: #7a8798;
  font-size: 10px;
}

.workflow-timer b {
  color: #34465b;
  font-size: 13px;
  font-variant-numeric: tabular-nums;
  letter-spacing: 0.02em;
}

.workflow-timer.active b { color: #0571dc; }
.workflow-timer.done b { color: #087c55; }
.workflow-timer.attention b { color: #9a650e; }

@media (max-width: 760px) {
  .preview-empty { grid-template-columns: 1fr; min-height: 0; padding: 20px; }
  .workflow-panel .section-heading { display: grid; grid-template-columns: 9px minmax(0, 1fr) auto; align-items: center; }
  .workflow-panel .section-heading .dot { grid-column: 1; grid-row: 1; }
  .workflow-panel .section-heading h2 { grid-column: 2; grid-row: 1; white-space: nowrap; }
  .workflow-panel .section-heading small { grid-column: 2 / 4; grid-row: 2; margin-left: 0; }
  .workflow-panel .section-heading .workflow-clear { grid-column: 3; grid-row: 1; }
  .workflow-rail { grid-template-columns: 1fr; }
  .workflow-step { min-height: 0; }
}

/* 淘汰明细：一行一题，点击弹出原题快照与评分 */
.discard-panel {
  border-color: #f3d9b0;
}

.discard-item {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  border: 1px solid #f3e2c2;
  background: #fffdf7;
  border-radius: 8px;
  padding: 10px 12px;
  margin-bottom: 8px;
  text-align: left;
  cursor: pointer;
  font: inherit;
}

.discard-item:hover {
  border-color: #e4b96a;
  background: #fff9ec;
}

.discard-verdict {
  flex: none;
  padding: 2px 8px;
  border-radius: 4px;
  background: #ffe9e9;
  color: #a23b4b;
  font-size: 11px;
  font-weight: 700;
}

/* 行首大纲代码：等宽字体突出，老师一眼定位知识点 */
.discard-code {
  flex: none;
  font-family: ui-monospace, Menlo, monospace;
  font-weight: 700;
  color: #c07b22;
}

.discard-item .discard-code {
  flex: 0 1 auto;
  min-width: 0;
  max-width: 35%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.discard-stem {
  flex: 1;
  min-width: 0;
  font-size: 13px;
  font-weight: 600;
  color: #172033;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.discard-score {
  flex: none;
  font-size: 11px;
  color: #c07b22;
}

.discard-open {
  flex: none;
  font-size: 12px;
  color: #0571dc;
}

.discard-hint {
  font-size: 12px;
  color: #9aa5b4;
  margin: 4px 0 0;
}

/* 预览标题行右侧的 AI 检查评分按钮 */
.preview-ai-score {
  margin-left: auto;
}

.check-failure-notice { border: 1px solid #efb0b0; border-left: 4px solid #c43c3c; background: #fff5f5; }
.check-failure-notice h2, .check-failure-notice .notice-title { color: #9d2424; }
.question-check-panel { max-height: 360px; overflow-y: auto; }
.question-check-row { padding: 10px 0; border-bottom: 1px solid #edf1f6; }
.question-check-row:last-child { border-bottom: 0; }
.question-check-heading { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; justify-content: space-between; font-size: 13px; }
.question-check-heading strong { min-width: 0; overflow-wrap: anywhere; }
.check-state { padding: 3px 8px; border-radius: 4px; font-size: 12px; background: #eff8ff; color: #0571dc; }
.check-state.retrying { background: #fff3dc; color: #94600f; }
.check-state.passed { background: #f0fff8; color: #087c55; }
.check-state.failed { background: #fff0f0; color: #c54858; }
.check-state.muted { background: #f0f3f7; color: #6e7b8f; }
.check-detail { margin: 6px 0 0; font-size: 12px; line-height: 1.7; color: #52657c; overflow-wrap: anywhere; }
.check-detail.retrying { color: #94600f; }
</style>
