<script setup>
import {questionReviewUi,isolatedQuestionState} from "../questionReviewUi.js";
import {routeJourney,journeyParams,verifyJourneyList} from "../authoringJourney.js";
import HelpHint from "../components/HelpHint.vue";
import { ref, onMounted, onBeforeUnmount, computed, watch, nextTick } from "vue";
import { useRoute, onBeforeRouteUpdate,onBeforeRouteLeave } from "vue-router";
import { api } from "../api.js";
import { useLiveRefresh } from "../useLiveRefresh.js";
import PreviousReviewHistory from "../components/PreviousReviewHistory.vue";
import { hasPerm, hasAccountPerm, identitiesWithPerm, currentUser, getToken } from "../auth.js";
import { canAccessGlobalBank, canAccessPersonalBank, initialBankScope } from "../navigation.js";
import { libraryExpectation } from "../libraryExpectation.js";
import { notifyNavigationWorkChanged } from "../navigation.js";
import AICheckScoreButton from "../components/AICheckScoreButton.vue";
import ReviewHistoryPanel from "../components/ReviewHistoryPanel.vue";
import QuestionLearningPanel from '../components/QuestionLearningPanel.vue';
import PersonalExperienceProvenance from '../components/PersonalExperienceProvenance.vue';

const route = useRoute();
const guideTask=computed(()=>routeJourney(route.query));
let disposed = false;
const sessionKey = computed(() => JSON.stringify([getToken(), currentUser.value?.id, currentUser.value?.role, [...(currentUser.value?.permissions || [])].sort()]));
const editingQuestion = ref(null);
const revisionNotice=ref(false);
const learningPanel = ref(null);
const editForm = ref(null);
const editStale = ref(false);
const editing = computed(() => Boolean(editingQuestion.value));
function editableContent(q) {
  return { clinical_stem: q.clinical_stem || "", options: (q.options || []).map(o => ({label:o.label,text:o.text})), answer:q.answer || "", explanation:q.explanation || "" };
}
const editDirty = computed(() => editing.value && JSON.stringify(editableContent(editForm.value)) !== JSON.stringify(editableContent(editingQuestion.value)));
function canRevise(q) {
  return !isGlobalScope.value && activeTier.value === "normal" && q?.library_state === "active"
    && q.owner_id === currentUser.value?.id && hasPerm("question:view") && hasPerm("question:edit")
    && ["ai_reviewed", "published"].includes(q.status) && shareStatus(q.id) !== "pending";
}
function startRevision() {
  if (mutating.value || !canRevise(selectedQuestion.value)) return;
  editingQuestion.value = selectedQuestion.value;
  editForm.value = { ...editableContent(selectedQuestion.value), change_reason: "" };
  editStale.value = false;
}
function discardRevision() { editingQuestion.value = null; editForm.value = null; editStale.value = false; }
function allowLeavingEditor() {
  if (mutating.value) return false;
  if (learningPanel.value?.allowLeaving?.() === false) return false;
  if (editDirty.value) { showToast("当前修订有未保存修改，请先保存或取消修订"); return false; }
  discardRevision(); return true;
}
async function saveRevision() {
  const q = editingQuestion.value;
  if (!q || !canRevise(q) || !editDirty.value || editStale.value || mutating.value) return;
  const session = sessionKey.value;
  mutating.value = true; ++questionSearchTicket;
  try {
    const {question_id, ...expected} = libraryExpectation(q);
    const saved = await api.updateQuestion(q.id, {...editableContent(editForm.value), ...expected, change_reason:editForm.value.change_reason || "普通题库修订"});
    if (disposed || session !== sessionKey.value) return;
    discardRevision();
    if (saved.library_state === "active") {
      questions.value = questions.value.map(item => item.id === saved.id ? saved : item);
      selectedQuestion.value = saved;
      showToast(`内容未变化，仍为 v${saved.version}，保留原确认`);
      return;
    }
    revisionNotice.value=true;
    questions.value = questions.value.filter(item => item.id !== q.id);
    selectedIds.value.delete(q.id); selectAll.value = false;
    selectedQuestion.value = null;
    totalCount.value = Math.max(0, totalCount.value - 1);
    showToast(`已保存 v${saved.version}，已回到题目整理，重新确认后可导出；不会触发 AI 复检`);
    notifyNavigationWorkChanged();
  } catch(e) {
    if (disposed || session !== sessionKey.value || e.staleSession) return;
    if (e.status === 409) editStale.value = true;
    showToast("保存修订失败: " + e.message);
  } finally { if (session === sessionKey.value) mutating.value = false; }
}

// 题目来自 AI 生成；本人普通题库可修订，审核退修仍在待我修改页处理。
// AI 检查仅在首次生成时自动执行一次；故障题阻断流程，不提供复检或强制通过。
const canDelete = computed(() => hasPerm("question:delete"));
// 导出属于题库范畴的账号能力：按钮对账号任一身份具备 question:download 的
// 用户可见（题库各身份都可见）；当前身份无该权限时点击仅提示引导切换身份，
// 不发请求（后端仍按当前 token 鉴权，业务口径不变）。
const canDownload = computed(() => hasPerm("question:download") || hasAccountPerm("question:download"));
const canUnpublish = computed(() => questionReviewUi.enabled() && hasPerm("user:manage"));
// 有权限者可删正式题库（question:delete_formal）；普通所有者也可删自己流程
// 完全结束的 published 题（无需权限，见 canDeleteQuestion）
const canDeleteFormal = computed(() => hasPerm("question:delete_formal"));

// 当前身份可执行导出时放行；否则提示需切换到具备权限的身份（如命题教师）。
function ensureDownloadPermHere() {
  if (hasPerm("question:download")) return true;
  const target = identitiesWithPerm("question:download")[0];
  showToast(target
    ? `导出需要在【${target.name}】身份下进行，请通过左下角切换工作身份后再导出`
    : "导出需要在具备题库下载权限的身份下进行，请联系管理员");
  return false;
}

// 删除=移入淘汰题库留档。两类入口：
// - 有权限者（question:delete / 正式题库 question:delete_formal）：个人与全局题库均可删；
// - 所有人：本人的"刚出未送审"（ai_reviewed）或"流程完全结束"（published）的题，
//   无需分配权限（后端按 owner_id 校验）。
// 流程中的题（审核中/待决断/需修改）与淘汰终态不可删。
const DELETABLE_OWNER_STATUSES = ["ai_reviewed", "published"];
function canRestoreQuestion(q) {
  if (isGlobalScope.value || activeTier.value !== "deleted") return false;
  return hasPerm("question:view") && q.library_state === "deleted" && q.deleted_reason === "owner_delete"
    && q.owner_id === currentUser.value?.id;
}

function canDeleteQuestion(q) {
  if (isGlobalScope.value) return false; // fixed shared content is a read/export view, never a source-delete shortcut
  if (!isGlobalScope.value && (activeTier.value === "deleted" || q.library_state === "deleted" || q.owner_id !== currentUser.value?.id)) return false;
  if (!isGlobalScope.value && ["normal", "deleted"].includes(activeTier.value) && !hasPerm("question:view")) return false;
  if (["pending", "approved"].includes(shareStatus(q.id))) return false;
  if (["rejected", "archived", "reviewing", "conflict", "revision_required"].includes(q.status)) return false;
  if (q.status === "ai_draft" || q.status === "auto_checked") return false;
  if (q.status === "published" ? canDeleteFormal.value : canDelete.value) return true;
  return !isGlobalScope.value
    && q.owner_id === currentUser.value?.id
    && DELETABLE_OWNER_STATUSES.includes(q.status);
}

// 个人题库按 owner_id 隔离，三个分层都由 question:view 控制；
// 全局题库按分享申请状态推导三层，并额外要求 question:view_global。
// C3 个人题库双页签：普通（active，已确认当前版本）/删除（deleted 留档）。
// 待整理（unfiled）在「题目整理」页处理，不设第三个页签。
const PERSONAL_LIBRARY_DEFS = [
  { key: "normal", library: "active", name: "普通题库", perm: "question:view", tier: "" },
  // 删除库只按归属过滤：删除原因可能是 owner_delete（题本身仍是 ai_reviewed/
  // published），不能叠加淘汰层状态条件把本人删除项排除在外。
  { key: "deleted", library: "deleted", name: "删除题库", perm: "question:view", tier: "" },
];
const PERSONAL_TIER_DEFS = [
  { key: "formal", name: "正式题库（历史授权）", perm: "question:view_formal" },
  { key: "eliminated", name: "淘汰题库（历史授权）", perm: "question:view_eliminated" },
];
const GLOBAL_TIER_DEFS = [
  { key: "formal", name: "正式题库", perm: "question:view_formal" },
  { key: "working", name: "待审核题库", perm: "question:view" },
  { key: "eliminated", name: "淘汰题库", perm: "question:view_eliminated" },
];
const canViewPersonal = computed(() => canAccessPersonalBank(hasPerm));
const canViewGlobal = computed(() => canAccessGlobalBank(hasPerm));
const requestedScope = route.query.scope === "personal" || route.query.scope === "global" ? route.query.scope : "";
const questionScope = ref(initialBankScope(requestedScope, hasPerm));
const isGlobalScope = computed(() => questionScope.value === "global");
const visibleTiers = computed(() => (isGlobalScope.value ? GLOBAL_TIER_DEFS : hasPerm("question:view") ? PERSONAL_LIBRARY_DEFS : PERSONAL_TIER_DEFS).filter((t) => hasPerm(t.perm) && (questionReviewUi.enabled() || !isGlobalScope.value || t.key === "formal")));
const requestedTier = typeof route.query.tier === "string" ? route.query.tier : "";
const activeTier = ref(visibleTiers.value.some((tier) => tier.key === requestedTier) ? requestedTier : (visibleTiers.value[0]?.key || ""));

// 各分类下可选的状态筛选（暂存态 ai_draft/auto_checked 检查通过前不可见，无筛选项）
const TIER_STATUS_OPTIONS = {
  formal: [{ value: "published", label: "已通过" }],
  working: [
    { value: "ai_reviewed", label: "AI已检查" },
    { value: "reviewing", label: "审核中" },
    { value: "conflict", label: "待决断" },
    { value: "revision_required", label: "需修改" },
  ],
  eliminated: [
    { value: "rejected", label: "已驳回" },
    { value: "archived", label: "已归档" },
  ],
};
const LIBRARY_STATUS_OPTIONS = {
  normal: [
    { value: "published", label: questionReviewUi.enabled() ? "已审核入库" : "历史入库" },
    { value: "ai_reviewed", label: "本人确认" },
  ],
  deleted: [],
};
const statusOptions = computed(() => {
  if (!isGlobalScope.value && PERSONAL_LIBRARY_DEFS.some((t) => t.key === activeTier.value)) {
    return LIBRARY_STATUS_OPTIONS[activeTier.value] || [];
  }
  return TIER_STATUS_OPTIONS[activeTier.value] || [];
});

// 个人轴页签 → 查询参数：normal→library=active（不分层），deleted→
// library=deleted（淘汰层终态留档）。全局轴沿用分层。
function currentQueryParams() {
  if (isGlobalScope.value) return { tier: activeTier.value, library: "" };
  const def = PERSONAL_LIBRARY_DEFS.find((t) => t.key === activeTier.value);
  return { tier: def ? def.tier : activeTier.value, library: def?.library || "" };
}

// 切换生命周期题库：重置筛选与选择后重新加载
function switchTier(tier) {
  if (exporting.value || !tier || tier === activeTier.value || !allowLeavingEditor()) return;
  activeTier.value = tier;
  filterStatus.value = "";
  selectedIds.value.clear();
  selectAll.value = false;
  selectedQuestion.value = null;
  doSearch();
}

function switchScope(scope) {
  if (exporting.value || !allowLeavingEditor() || scope === questionScope.value || (scope === "global" && !canViewGlobal.value) || (scope === "personal" && !canViewPersonal.value)) return;
  questionScope.value = scope;
  activeTier.value = visibleTiers.value[0]?.key || "";
  filterStatus.value = "";
  selectedIds.value.clear();
  selectAll.value = false;
  selectedQuestion.value = null;
  loadShareRequests();
  doSearch();
}

watch([canViewPersonal, canViewGlobal], () => {
  const availableScope = initialBankScope(questionScope.value, hasPerm);
  if (availableScope !== questionScope.value) switchScope(availableScope);
});
watch(visibleTiers, (tiers) => {
  if (tiers.some(tier => tier.key === activeTier.value)) return;
  activeTier.value = tiers[0]?.key || "";
  filterStatus.value = "";
  selectedIds.value.clear();
  selectAll.value = false;
  selectedQuestion.value = null;
  if (activeTier.value) doSearch();
});

const toast = ref("");
const questions = ref([]);
const loading = ref(false);
const filterStatus = ref("");
const searchQuery = ref("");
const filterProfession = ref(""); // 专业筛选
const professions = ref([]); // 专业列表
const selectedQuestion = ref(null);
const shareStatuses = ref({});
const shareVersions = ref({});
// 审核意见（按题目加载）：出题人可在详情里看到本人题目的轮次评语与驳回/决断理由；
// 进行中的任务由服务端按隔离规则裁剪，已结束任务（已通过/已驳回）保留完整评语。
let reviewInfoTicket = 0;
const reviewInfo = ref(null); // { task, records }
const reviewInfoLoading = ref(false);
let questionSearchTicket = 0;

const mutating = ref(false); // 撤回/删除在途守卫，防重复事务
const canBatchShare = computed(() => questionReviewUi.enabled() && !isGlobalScope.value && activeTier.value === 'normal' && hasPerm('question:share'));
const shareDialog = ref(null);
const shareDialogElement = ref(null);
watch(shareDialog, async value => {
  if (value) { await nextTick(); shareDialogElement.value?.showModal(); }
});
const shareNotice = ref('');
const preparingShare = ref(false);
const appliedShareFilters = ref({ q: '', profession: '' });
// Export the result the user actually loaded, not unsubmitted filter drafts.
const appliedExportFilters = ref(null);
function isCurrentConfirmed(q) { return q?.library_state === 'active' && q.confirmed_version === q.version && ['ai_reviewed','published'].includes(q.status); }
const shareableSelected = computed(() => [...selectedIds.value].filter(id => !shareStatus(id) && questions.value.some(q => q.id === id && isCurrentConfirmed(q))));

async function prepareBulkShare(mode) {
  if (!canBatchShare.value || mutating.value || preparingShare.value || loading.value || !allowLeavingEditor()) return;
  preparingShare.value = true;
  shareNotice.value = '';
  const filters = { ...appliedShareFilters.value };
  const ticket = questionSearchTicket;
  const session = sessionKey.value;
  try {
    let items;
    if (mode === 'selected') {
      items = shareableSelected.value.map(id => libraryExpectation(questions.value.find(q => q.id === id)));
    } else {
      const data = await api.previewQuestionShares(filters);
      if (ticket !== questionSearchTicket || !canBatchShare.value) return;
      if (data.total > data.limit) {
        shareNotice.value = `当前有 ${data.total} 道可分享题目。每批最多 ${data.limit} 道，请缩小专业或关键词范围，或勾选后提交。`;
        return;
      }
      items = data.items || [];
      if ((data.question_ids || []).length && !items.length) throw new Error('分享预览缺少当前版本信息，请刷新后重试');
    }
    const ids = items.map(item => item.question_id);
    if (!ids.length) { shareNotice.value = '当前范围没有可提交的题目，已申请过的题目会自动跳过。'; return; }
    if (ids.length > 500) { shareNotice.value = '每批最多提交 500 道，请减少勾选数量。'; return; }
    shareDialog.value = {
      ids, items,
      scope: mode === 'selected' ? '已勾选的个人普通题目' : [
        filters.profession ? `专业：${filters.profession}` : '全部专业',
        filters.q ? `关键词：${filters.q}` : '',
      ].filter(Boolean).join(' · '),
    };
  } catch (e) { if (ticket === questionSearchTicket && session === sessionKey.value && !e.staleSession) shareNotice.value = '准备分享失败：' + e.message; }
  finally { if (session === sessionKey.value) preparingShare.value = false; }
}

async function submitBulkShare() {
  if (!shareDialog.value || mutating.value || !canBatchShare.value || !allowLeavingEditor()) return;
  const session = sessionKey.value;
  mutating.value = true;
  const ids = [...shareDialog.value.ids];
  try {
    const data = await api.createQuestionShares(shareDialog.value.items.map(item => ({...item})));
    if (disposed || session !== sessionKey.value) return;
    for (const id of data.question_ids || []) shareStatuses.value[id] = 'pending';
    shareNotice.value = `已提交 ${data.submitted} 道，等待管理员审批。` +
      (data.skipped ? `另有 ${data.skipped} 道已申请或状态已变化，本次已跳过。` : '');
    ids.forEach(id => selectedIds.value.delete(id));
    selectAll.value = false;
    shareDialog.value = null;
    await loadShareRequests();
  } catch (e) { if (disposed || session !== sessionKey.value || e.staleSession) return; shareNotice.value = '提交失败：' + e.message; }
  finally { if (session === sessionKey.value) mutating.value = false; }
}

// 管理员撤回已通过题目至 AI 检查通过状态
async function unpublishSelected() {
  const q = selectedQuestion.value;
  if (!q || !allowLeavingEditor()) return;
  if (mutating.value || !canUnpublish.value || q.status !== "published") return;
	if (!confirm("确定撤回该已通过题目？题目将进入原出题人的「待我修改」，保存新版本后按原流程从第一轮重新审核。")) return;
  const session = sessionKey.value;
  mutating.value = true;
  try {
    const updated = await api.unpublishQuestion(q.id, "题库页撤回修订");
    if (disposed || session !== sessionKey.value) return;
    if (selectedQuestion.value?.id === q.id) selectedQuestion.value = updated;
	showToast("已撤回并进入原出题人的待我修改");
    await loadQuestions(false);
  } catch (e) {
    if (disposed || session !== sessionKey.value || e.staleSession) return;
    showToast("撤回失败: " + e.message);
  } finally {
    if (session === sessionKey.value) mutating.value = false;
  }
}

let shareTicket = 0;
async function loadShareRequests() {
  const ticket = ++shareTicket;
  const session = sessionKey.value;
  if (!hasPerm("question:share") || isGlobalScope.value) {
    shareStatuses.value = {}; shareVersions.value = {};
    return;
  }
  try {
    const data = await api.listQuestionShares("mine");
    if (disposed || ticket !== shareTicket || session !== sessionKey.value || isGlobalScope.value || !hasPerm("question:share")) return;
    const next = {}, versions = {};
    for (const item of data.items || []) {next[item.request.question_id] = item.request.status; if (item.request.question_version > 0) versions[item.request.question_id] = item.request.question_version;}
    shareStatuses.value = next; shareVersions.value = versions;
  } catch (e) {
    console.error(e);
  }
}

function shareStatus(questionID) {
  return shareStatuses.value[questionID] || "";
}

function shareStatusText(status, id) {
  const version = shareVersions.value[id];
  return ({ pending: '待管理员审批', approved: '共享已获批', rejected: '分享未通过' }[status] || '') + (version > 0 ? ` · v${version}` : '');
}

async function requestShare(q) {
  if (!questionReviewUi.enabled() || !allowLeavingEditor() || !hasPerm("question:share") || mutating.value || !q || !isCurrentConfirmed(q) || shareStatus(q.id)) return;
  if (!confirm("确认分享这道本人已确认的当前版本至全局题库？审批中不能修改，获批后共享版本保持固定；每道题只能申请一次。")) return;
  const session = sessionKey.value;
  mutating.value = true;
  try {
    const {question_id, ...expectation} = libraryExpectation(q);
    const data = await api.createQuestionShare(q.id, expectation);
    if (disposed || session !== sessionKey.value) return;
    shareStatuses.value = { ...shareStatuses.value, [q.id]: data.request?.status || "pending" };
    shareVersions.value = {...shareVersions.value, [q.id]: data.request?.question_version || q.version};
    showToast("分享申请已提交，等待管理员审批");
  } catch (e) {
    if (disposed || session !== sessionKey.value || e.staleSession) return;
    showToast("提交分享申请失败: " + e.message);
  } finally {
    if (session === sessionKey.value) mutating.value = false;
  }
}

// 分页：固定每页 100 条，页码换页
const page = ref(1);
const pageSize = 100;
const totalCount = ref(0);
const pageCount = computed(() => Math.max(1, Math.ceil(totalCount.value / pageSize)));

// 批量选择
const selectedIds = ref(new Set());
const selectAll = ref(false);
const exporting = ref(false);

function toggleSelect(id) {
  if (selectedIds.value.has(id)) {
    selectedIds.value.delete(id);
  } else {
    selectedIds.value.add(id);
  }
  selectAll.value = questions.value.length > 0 && questions.value.every(q => selectedIds.value.has(q.id));
}

function toggleSelectAll() {
  if (selectAll.value) {
    questions.value.forEach(q => selectedIds.value.delete(q.id));
    selectAll.value = false;
  } else {
    questions.value.forEach(q => selectedIds.value.add(q.id));
    selectAll.value = true;
  }
}

async function downloadFile(filename, session) {
  const blob = await api.downloadExport(filename);
  if (disposed || session !== sessionKey.value || !hasPerm("question:download")) return;
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
  return true;
}

function ensureExportableTab() {
  if (editing.value) { showToast("请先保存或取消修订后再导出"); return false; }
  if (isGlobalScope.value) return activeTier.value === "formal";
  if (!["normal", "formal"].includes(activeTier.value)) {
    showToast("删除题库的题目不可导出；可恢复后再导出");
    return false;
  }
  return true;
}

async function exportXlsx() {
  if (!ensureDownloadPermHere() || !ensureExportableTab()) return;
  const ids = Array.from(selectedIds.value);
  if (ids.length === 0) {
    showToast("请先选择要导出的题目");
    return;
  }
  if (exporting.value || mutating.value) return;
  const session = sessionKey.value;
  exporting.value = true;
  try {
    const data = await api.exportXlsx({ question_ids: ids, scope: !isGlobalScope.value && activeTier.value === "formal" ? "" : questionScope.value });
    if (disposed || session !== sessionKey.value || !hasPerm("question:download")) return;
    const downloaded = await downloadFile(data.filename, session);
    if (downloaded && !disposed && session === sessionKey.value) showToast(`已导出所选 ${data.count} 道题`);
  } catch (e) {
    if (disposed || session !== sessionKey.value || e.staleSession) return;
    showToast("导出失败: " + e.message);
  } finally {
    if (session === sessionKey.value) exporting.value = false;
  }
}

async function exportDocx() {
  if (!ensureDownloadPermHere() || !ensureExportableTab()) return;
  const ids = Array.from(selectedIds.value);
  if (ids.length === 0) {
    showToast("请先选择要导出的题目");
    return;
  }
  if (exporting.value || mutating.value) return;
  const session = sessionKey.value;
  exporting.value = true;
  try {
    const data = await api.exportDocx({ question_ids: ids, scope: !isGlobalScope.value && activeTier.value === "formal" ? "" : questionScope.value });
    if (disposed || session !== sessionKey.value || !hasPerm("question:download")) return;
    const downloaded = await downloadFile(data.filename, session);
    if (downloaded && !disposed && session === sessionKey.value) showToast(`已导出所选 ${data.count} 道题`);
  } catch (e) {
    if (disposed || session !== sessionKey.value || e.staleSession) return;
    showToast("导出失败: " + e.message);
  } finally {
    if (session === sessionKey.value) exporting.value = false;
  }
}

async function exportAllXlsx() {
  if (!ensureDownloadPermHere() || !ensureExportableTab()) return;
  if (exporting.value || mutating.value || loading.value) return;
  const filters = appliedExportFilters.value;
  if (!filters || filters.scope !== questionScope.value || filters.tier !== activeTier.value) {
    showToast("当前题库尚未加载成功，请重新查询后再导出"); return;
  }
  const session = sessionKey.value;
  exporting.value = true;
  try {
    const data = await api.exportXlsx({
      export_all: true, scope: !isGlobalScope.value && activeTier.value === "formal" ? "" : questionScope.value,
      q: filters.q, status: filters.status, professions: [...filters.professions],...journeyParams(guideTask.value),
    });
    if (disposed || session !== sessionKey.value || !hasPerm("question:download")) return;
    const downloaded = await downloadFile(data.filename, session);
    if (downloaded && !disposed && session === sessionKey.value) showToast(`已导出本次筛选的全部 ${data.count} 道题`);
  } catch (e) {
    if (disposed || session !== sessionKey.value || e.staleSession) return;
    showToast("导出失败: " + e.message);
  } finally {
    if (session === sessionKey.value) exporting.value = false;
  }
}

const toastError = ref(false);

// 占用冲突等长提示按"；"分条换行，时长随内容长度自适应，可点击立即关闭。
function formatToastMessage(msg) {
  const idx = msg.indexOf("：");
  if (idx === -1 || !msg.includes("；")) return msg;
  const head = msg.slice(0, idx + 1);
  const items = msg.slice(idx + 1).split("；").map(s => s.trim()).filter(Boolean);
  if (items.length < 2) return msg;
  return head + "\n" + items.map(s => "• " + s).join("\n");
}

function showToast(msg) {
  const text = formatToastMessage(String(msg || ""));
  toastError.value = /失败|无法|不能|已被|请先|无权/.test(text);
  toast.value = toastError.value ? text + "\n（点击关闭）" : text;
  window.clearTimeout(showToast.timer);
  const duration = Math.min(4000 + toast.value.length * 70, 16000);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, duration);
}

function dismissToast() {
  window.clearTimeout(showToast.timer);
  toast.value = "";
}

async function loadQuestions(resetPage = true, silent = false) {
  const ticket = ++questionSearchTicket;
  const session = sessionKey.value;
  if (!visibleTiers.value.length) { questions.value = []; totalCount.value = 0; return; }
  if (!silent) { shareNotice.value = ''; if (!mutating.value) shareDialog.value = null; }
  const requestedShareFilters = { q: searchQuery.value, profession: filterProfession.value };
  const requestedExportFilters = { q: searchQuery.value, status: filterStatus.value, professions: filterProfession.value ? [filterProfession.value] : [], scope: questionScope.value, tier: activeTier.value,...journeyParams(guideTask.value) };
  if (!silent) loading.value = true;
  if (resetPage) page.value = 1;
  try {
    let data;
    const qp = currentQueryParams();
    if (searchQuery.value || filterStatus.value || filterProfession.value) {
      data = await api.searchQuestions(searchQuery.value, filterStatus.value, page.value, pageSize, "", filterProfession.value ? [filterProfession.value] : [], "", "", qp.tier, false, questionScope.value, qp.library,journeyParams(guideTask.value));
    } else {
      data = await api.listQuestions(page.value, pageSize, "", qp.tier, questionScope.value, qp.library,journeyParams(guideTask.value));
    }
    if (disposed || ticket !== questionSearchTicket || session !== sessionKey.value) return;
    verifyJourneyList(data,guideTask.value);
    questions.value = data.questions || [];
    selectAll.value = questions.value.length > 0 && questions.value.every(q => selectedIds.value.has(q.id));
    totalCount.value = data.total || 0;
    if (page.value > pageCount.value) { page.value = Math.max(1, pageCount.value); return loadQuestions(false, silent); }
    selectedIds.value = new Set([...selectedIds.value].filter(id => questions.value.some(q => q.id === id)));
    if (editingQuestion.value) {
      const fresh = questions.value.find(q => q.id === editingQuestion.value.id);
      if (!fresh || fresh.version !== editingQuestion.value.version || (fresh.library_revision || 0) !== (editingQuestion.value.library_revision || 0) || (fresh.review_revision || 0) !== (editingQuestion.value.review_revision || 0)) editStale.value = true;
    } else if (selectedQuestion.value) { const fresh = questions.value.find(q => q.id === selectedQuestion.value.id); selectedQuestion.value = fresh || null; }
    appliedShareFilters.value = requestedShareFilters;
    appliedExportFilters.value = requestedExportFilters;
  } catch (e) {
    if (ticket === questionSearchTicket && session === sessionKey.value && !silent && !e.staleSession) showToast("加载失败: " + e.message);
  } finally {
    if (ticket === questionSearchTicket) loading.value = false;
  }
}

// 页码换页：钳制页码范围后重新加载，并回到列表顶部
function goPage(p) {
  if (exporting.value || loading.value || !allowLeavingEditor()) return;
  const target = Math.min(Math.max(1, p), pageCount.value);
  if (target === page.value) return;
  page.value = target;
  loadQuestions(false);
  const scroller = document.querySelector(".question-scroll");
  if (scroller) scroller.scrollTop = 0;
}

function selectQuestion(q) {
  if (!allowLeavingEditor()) return;
  selectedQuestion.value = q;
}

const REVIEW_CONCLUSION_TEXT = {
  approved: "通过",
  rejected: "驳回",
  revision_required: "需修改",
  published: "已通过",
};

function reviewConclusionText(status) {
  return REVIEW_CONCLUSION_TEXT[status] || status;
}

async function loadReviewInfo(q) {
  const ticket = ++reviewInfoTicket;
  reviewInfo.value = null;
  if (!questionReviewUi.enabled() || !q || isGlobalScope.value) { reviewInfoLoading.value = false; return; }
  reviewInfoLoading.value = true;
  try {
    const task = await api.getTaskByQuestion(q.id);
    if (ticket !== reviewInfoTicket) return;
    if (!task || selectedQuestion.value?.id !== q.id) {
      reviewInfo.value = null;
      return;
    }
    const data = await api.reviewRecords(task.id);
    if (ticket === reviewInfoTicket && selectedQuestion.value?.id === q.id) reviewInfo.value = { task, records: data.records || [] };
  } catch (e) {
    // 审核意见加载失败不打断详情查看；AI 检查评分按钮仍可用
    console.error("加载审核意见失败:", e);
    if (ticket === reviewInfoTicket) reviewInfo.value = null;
  } finally {
    if (ticket === reviewInfoTicket) reviewInfoLoading.value = false;
  }
}

watch(selectedQuestion, (q) => {
  loadReviewInfo(q);
});

// C3 删除库恢复：仅本人删除项可恢复；恢复后回「题目整理」待重新确认。
async function restoreQuestion(q) {
  if (mutating.value || !canRestoreQuestion(q) || !allowLeavingEditor()) return;
  if (!confirm(`恢复题目到「题目整理」？恢复后需重新确认入库，不会触发 AI 复检。
${(q.clinical_stem || "").slice(0, 50)}...`)) return;
  const session = sessionKey.value;
  mutating.value = true;
  try {
    await api.libraryRestore([libraryExpectation(q)]);
    if (disposed || session !== sessionKey.value) return;
    showToast("已恢复到题目整理（待重新确认）");
    questions.value = questions.value.filter((item) => item.id !== q.id);
    totalCount.value = Math.max(0, totalCount.value - 1);
    selectedIds.value.delete(q.id);
    selectAll.value = questions.value.length > 0 && questions.value.every(item => selectedIds.value.has(item.id));
    notifyNavigationWorkChanged();
    if (selectedQuestion.value?.id === q.id) selectedQuestion.value = null;
  } catch (e) {
    if (disposed || session !== sessionKey.value || e.staleSession) return;
    showToast("恢复失败: " + e.message);
  } finally {
    if (session === sessionKey.value) mutating.value = false;
  }
}

async function deleteQuestion(q) {
  if (mutating.value || !canDeleteQuestion(q) || !allowLeavingEditor()) return;
  if (!confirm(`确定删除题目？删除后将移入删除题库留档（版本与历史记录保留）。\n${(q.clinical_stem || "").slice(0, 50)}...`)) return;
  const session = sessionKey.value;
  mutating.value = true;
  try {
    if (isGlobalScope.value || !hasPerm("question:view")) {
      const data = await api.deleteQuestion(q.id);
      if (disposed || session !== sessionKey.value) return;
      showToast(data?.archived ? "已移入淘汰题库留档（历史记录保留）" : "已删除");
    } else {
      // 个人轴：软删除进入删除题库（本人可恢复；驳回/归档终态另行留档）。
      await api.libraryDelete([libraryExpectation(q)]);
      if (disposed || session !== sessionKey.value) return;
      showToast("已移入删除题库（可恢复）");
    }
    questions.value = questions.value.filter((item) => item.id !== q.id);
    totalCount.value = Math.max(0, totalCount.value - 1);
    selectedIds.value.delete(q.id);
    selectAll.value = questions.value.length > 0 && questions.value.every(item => selectedIds.value.has(item.id));
    notifyNavigationWorkChanged();
    if (selectedQuestion.value?.id === q.id) selectedQuestion.value = null;
  } catch (e) {
    if (disposed || session !== sessionKey.value || e.staleSession) return;
    showToast("删除失败: " + e.message);
  } finally {
    if (session === sessionKey.value) mutating.value = false;
  }
}

function doSearch() {
  if (!allowLeavingEditor()) return;
  selectedIds.value.clear();
  selectAll.value = false;
  loadQuestions();
}

function clearSearch() {
  if (!allowLeavingEditor()) return;
  searchQuery.value = "";
  filterStatus.value = "";
  filterProfession.value = "";
  selectedIds.value.clear();
  selectAll.value = false;
  loadQuestions();
}

function statusText(status, question) {
  const historic=isolatedQuestionState(status);
  if(historic)return historic;
  const lifecycle = {
    ai_draft: "AI草稿", auto_checked: "已初评", ai_reviewed: "AI已检查",
    reviewing: "审核中", conflict: "待决断", approved: "已通过", rejected: "专家审核已驳回",
    revision_required: "需修改", published: "已通过", archived: "已归档",
  };
  // 个人普通题库：区分"本人确认"与"人工审核入库"（禁止把 0 轮确认显示成专家通过）。
  if (!isGlobalScope.value && activeTier.value === "normal") {
    if (question?.confirmation_source === "self") return "本人确认";
    if (status === "published") return questionReviewUi.enabled() ? "审核入库" : "历史入库";
    if (status === "ai_reviewed") return question?.confirmation_source === "self" ? "本人确认" : "待确认";
    return lifecycle[status] || status;
  }
  if (!isGlobalScope.value && activeTier.value === "deleted") {
    const reason = deletedReasonText(question);
    return reason || lifecycle[status] || status;
  }
  if (isGlobalScope.value) {
    if (activeTier.value === "formal") return questionReviewUi.enabled() ? "共享已获批" : "共享版本";
    // 新分享申请的题目本身已经 published；历史全局过程题保持真实审核状态。
    if (activeTier.value === "working") return "待管理员审批分享";
    // 分享被拒的个人题仍是 published；历史 rejected/archived 则来自专家审核流程。
    if (activeTier.value === "eliminated") return "分享未通过";
  }
  return lifecycle[status] || status;
}

function deletedReasonText(question) {
  switch (question?.deleted_reason) {
    case "owner_delete": return "本人删除";
    case "owner_archive": return "本人归档";
    case "admin_delete": return "管理员删除";
    case "review_reject":
    case "legacy_rejected": return questionReviewUi.enabled() ? "审核驳回" : "历史驳回";
    case "legacy_archive": return "归档留档";
    default: return "";
  }
}

function statusClass(status) {
  if (isGlobalScope.value && activeTier.value === "eliminated") return "status-bad";
  if (isGlobalScope.value && activeTier.value === "working" && status === "published") return "status-warn";
  if (isGlobalScope.value && activeTier.value === "formal") return "status-good";
  if (status === "approved" || status === "published" || status === "ai_reviewed") return "status-good";
  if (status === "rejected") return "status-bad";
  if (status === "reviewing") return "status-active";
  if (status === "revision_required") return "status-warn";
  if (status === "conflict") return "status-conflict";
  return "";
}

async function selectQuestionById(id) {
  const session = sessionKey.value;
  const ticket = questionSearchTicket;
  try {
    const q = await api.getQuestion(id, isGlobalScope.value ? "global" : "");
    if (q && session === sessionKey.value && ticket === questionSearchTicket) {
      selectedQuestion.value = q;
    }
  } catch (e) {
    if (session === sessionKey.value && ticket === questionSearchTicket && !e.staleSession) showToast("定位题目失败: " + e.message);
  }
}

let professionTicket = 0;
async function loadProfessions() {
  const ticket = ++professionTicket;
  const session = sessionKey.value;
  try {
    const data = await api.listProfessions();
    if (ticket === professionTicket && session === sessionKey.value) professions.value = data.professions || [];
  } catch (e) {
    console.error(e);
  }
}

function warnBeforeUnload(event) {
  if (!editDirty.value) return;
  event.preventDefault(); event.returnValue = "";
}
onBeforeRouteUpdate((to,from)=>{if(to.query.task_id===from.query.task_id&&to.query.task_kind===from.query.task_kind)return true;if(exporting.value||mutating.value||preparingShare.value)return false;return !editDirty.value||confirm("当前修订有未保存修改，确定切换任务并放弃这些修改吗？");});
watch(()=>[route.query.task_kind,route.query.task_id],()=>{page.value=1;revisionNotice.value=false;selectedIds.value=new Set();discardRevision();selectedQuestion.value=null;loadQuestions();});
onBeforeRouteLeave(() => !editDirty.value || confirm("当前修订有未保存修改，确定离开并放弃这些修改吗？"));
watch(sessionKey, () => {
  ++questionSearchTicket; ++reviewInfoTicket; ++shareTicket; ++professionTicket;
  questions.value = []; totalCount.value = 0; selectedQuestion.value = null;
  selectedIds.value = new Set(); selectAll.value = false; shareStatuses.value = {}; shareVersions.value = {};
  shareDialog.value = null; shareNotice.value = ""; toast.value = ""; appliedExportFilters.value = null;
  reviewInfo.value = null; reviewInfoLoading.value = false; professions.value = [];
  revisionNotice.value=false;discardRevision(); mutating.value = false; exporting.value = false; preparingShare.value = false;
  if (visibleTiers.value.length) { loadQuestions(); loadProfessions(); loadShareRequests(); }
}, {flush:"sync"});
useLiveRefresh(() => { if (!mutating.value && !shareDialog.value) return loadQuestions(false, true); });
onBeforeUnmount(() => { disposed = true; ++questionSearchTicket; ++reviewInfoTicket; ++shareTicket; ++professionTicket; window.clearTimeout(showToast.timer); window.removeEventListener("beforeunload", warnBeforeUnload); });
onMounted(async () => {
  window.addEventListener("beforeunload", warnBeforeUnload);
  await Promise.all([loadQuestions(), loadProfessions(), loadShareRequests()]);
  // 支持从审核页「去修改题目」跳转：?edit=<题目ID> 自动定位并进入编辑
  const editId = new URLSearchParams(window.location.search).get("edit");
  if (editId) {
    await selectQuestionById(editId);
  }
});
</script>

<template>
 <section v-if="revisionNotice" class="authoring-confirmed" role="status"><div><strong>修订已保存，请重新确认后导出</strong><span>题目已回到题目整理，原始首次检查保留。</span></div><RouterLink class="primary-button" to="/new-questions">回题目整理继续确认 →</RouterLink></section>
  <div class="bank-layout">
    <!-- 左侧列表 -->
    <section class="panel bank-list-panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>{{ isGlobalScope ? "全局题库" : "我的题库" }}</h2>
        <HelpHint v-if="hasPerm('question:view_all')" label="题库范围说明">我的题库仅显示本人题目；全局题库仅显示已分享题目。</HelpHint>
        <!-- 总数取接口 total（跨分页），当前页只有前 100 条 -->
        <small>{{ totalCount }} 道</small>
      </div>

      <!-- 筛选与题库栏固定：列表滚动时保持可见 -->
      <div class="list-toolbar">
        <div v-if="canViewPersonal && canViewGlobal && !guideTask" class="scope-tabs" aria-label="题库范围">
          <button type="button" class="scope-tab" :class="{ active: questionScope === 'personal' }" @click="switchScope('personal')">我的题库</button>
          <button type="button" class="scope-tab" :class="{ active: questionScope === 'global' }" @click="switchScope('global')">全局题库</button>
        </div>

        <!-- 题库分类：正式 / 待审核 / 淘汰（黑字精简样式） -->
        <div v-if="visibleTiers.length" class="tier-tabs">
          <button
            v-for="t in visibleTiers"
            :key="t.key"
            type="button"
            class="tier-tab"
            :class="{ active: activeTier === t.key }"
            @click="switchTier(t.key)"
          >{{ t.name }}</button>
        </div>
        <div v-else class="empty">暂无题库访问权限，请联系管理员分配相应权限</div>

        <div class="filter-row">
          <input v-model="searchQuery" placeholder="搜索题干、选项、解析、专业、系统或大纲要点..." @keyup.enter="doSearch" class="search-input" />
          <select v-model="filterProfession" @change="doSearch" title="按专业筛选">
            <option value="">全部专业</option>
            <option v-for="p in professions" :key="p" :value="p">{{ p }}</option>
          </select>
          <select v-model="filterStatus" @change="doSearch">
            <option value="">全部状态</option>
            <option v-for="opt in statusOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
          </select>
          <button class="ghost-button" type="button" @click="doSearch">搜索</button>
          <button class="ghost-button" type="button" @click="clearSearch">重置</button>
        </div>

      </div>

      <p v-if="appliedExportFilters && searchQuery.trim() !== appliedExportFilters.q" class="field-hint" role="status">当前输入尚未搜索；列表和“导出全部”仍使用已应用的筛选。</p>

      <!-- 批量操作栏 -->
      <div class="batch-bar">
        <label class="select-all">
          <input type="checkbox" :checked="selectAll" @change="toggleSelectAll" />
          <span>本页全选</span>
        </label>
        <span class="selected-count" v-if="selectedIds.size > 0">已选 {{ selectedIds.size }} 道</span>
        <div v-if="canBatchShare" class="bulk-share-actions">
          <button class="ghost-button share-btn" type="button" :disabled="mutating || preparingShare || loading || !shareableSelected.length" @click="prepareBulkShare('selected')">提交所选至全局库</button>
          <button class="text-button" type="button" :disabled="mutating || preparingShare || loading" @click="prepareBulkShare('filtered')">{{ preparingShare ? '查询中…' : '提交筛选结果' }}</button>
        </div>
        <!-- 导出：个人轴普通题库（确认当前版本）或全局正式题库；删除库不提供 -->
        <div class="export-btns" v-if="canDownload && (activeTier === 'formal' || (!isGlobalScope && activeTier === 'normal'))">
          <button class="ghost-button" type="button" @click="exportXlsx" :disabled="exporting || selectedIds.size === 0">
            {{ exporting ? "导出中..." : "导出 Excel" }}
          </button>
          <button class="ghost-button" type="button" @click="exportDocx" :disabled="exporting || selectedIds.size === 0">
            {{ exporting ? "导出中..." : "导出 Word" }}
          </button>
          <button class="ghost-button" type="button" @click="exportAllXlsx" :disabled="exporting || loading || mutating || !appliedExportFilters">
            导出全部 Excel
          </button>
        </div>
      </div>

      <p v-if="shareNotice" class="bulk-share-notice" role="status">{{ shareNotice }}</p>
      <div v-if="loading" class="loading">加载中...</div>
      <div v-else class="question-scroll">
        <div class="question-list">
          <div
            v-for="q in questions"
            :key="q.id"
            class="question-item"
            :class="{ active: selectedQuestion?.id === q.id, selected: selectedIds.has(q.id) }"
          >
            <input
              type="checkbox"
              :checked="selectedIds.has(q.id)"
              @change="toggleSelect(q.id)"
              @click.stop
              class="q-checkbox"
            />
            <div
              class="q-content"
              role="button"
              tabindex="0"
              @click="selectQuestion(q)"
              @keydown.enter="selectQuestion(q)"
            >
              <div class="q-info">
                <span class="q-stem">{{ (q.clinical_stem || "").slice(0, 60) }}{{ (q.clinical_stem || "").length > 60 ? "..." : "" }}</span>
                <span class="q-meta">
                  答案: {{ q.answer }}
                  <span v-if="q.outline_code"> | 大纲: {{ q.outline_code }}</span>
                  <span v-if="q.profession"> | 专业: {{ q.profession }}</span>
                </span>
              </div>
              <div class="q-actions">
                <span class="q-status" :class="statusClass(q.status)">{{ statusText(q.status, q) }}</span>
                <span v-if="questionReviewUi.enabled() && !isGlobalScope && shareStatus(q.id) && q.status !== 'archived'" class="share-status">{{ shareStatusText(shareStatus(q.id), q.id) }}</span>
                <button v-if="canRestoreQuestion(q)" class="text-button" type="button" :disabled="mutating" @click.stop="restoreQuestion(q)" title="恢复到题目整理（仅本人删除项）">恢复</button>
                <button v-if="canDeleteQuestion(q)" class="delete-btn" type="button" :disabled="mutating" @click.stop="deleteQuestion(q)" title="删除（移入删除题库）">×</button>
              </div>
            </div>
          </div>
          <div v-if="!questions.length" class="empty">暂无题目</div>
        </div>
      </div>

      <!-- 页码换页（固定在列表下方，无论题目多少始终显示） -->
      <div class="list-footer">
        <span class="page-total">共 {{ totalCount }} 条</span>
        <div class="page-pager">
          <button class="page-btn" type="button" :disabled="page <= 1 || loading" @click="goPage(page - 1)">‹ 上一页</button>
          <span class="page-info">第 {{ page }} / {{ pageCount }} 页</span>
          <button class="page-btn" type="button" :disabled="page >= pageCount || loading" @click="goPage(page + 1)">下一页 ›</button>
        </div>
      </div>
    </section>

    <!-- 右侧详情 -->
    <section class="panel" v-if="selectedQuestion">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>题目详情</h2>
        <span class="q-status" :class="statusClass(selectedQuestion.status)">
          {{ statusText(selectedQuestion.status, selectedQuestion) }}
        </span>
        <AICheckScoreButton v-if="!isGlobalScope" :question-id="selectedQuestion.id" />
        <span v-else class="shared-version">固定共享版本 v{{ selectedQuestion.version }}<template v-if="selectedQuestion.confirmation_source === 'self'"> · 本人确认后分享</template></span>
        <div class="edit-actions">
          <button v-if="canRevise(selectedQuestion) && !editing" class="ghost-button" type="button" :disabled="mutating" @click="startRevision">修订题目</button>
          <button
            v-if="!isGlobalScope && canUnpublish && activeTier === 'normal' && selectedQuestion.status === 'published'"
            class="ghost-button ai-force-btn"
            type="button"
            title="撤回已通过题目至 AI 检查通过状态（写审计日志）"
            :disabled="mutating"
            @click="unpublishSelected"
          >
            {{ mutating ? "处理中..." : "撤回" }}
          </button>
          <button
            v-if="questionReviewUi.enabled() && !isGlobalScope && hasPerm('question:share') && activeTier === 'normal' && isCurrentConfirmed(selectedQuestion) && !shareStatus(selectedQuestion.id)"
            class="ghost-button share-btn"
            type="button"
            :disabled="mutating"
            @click="requestShare(selectedQuestion)"
          >
            申请分享至全局库
          </button>
          <span v-else-if="questionReviewUi.enabled() && !isGlobalScope && activeTier === 'normal' && shareStatus(selectedQuestion?.id)" class="share-detail-status">
            {{ shareStatusText(shareStatus(selectedQuestion.id), selectedQuestion.id) }}
          </span>
        </div>
      </div>
      <QuestionLearningPanel ref="learningPanel" v-if="!isGlobalScope && activeTier === 'normal'" :question="selectedQuestion" :dirty="editDirty || editStale || mutating" />
      <PersonalExperienceProvenance v-if="!isGlobalScope" :question="selectedQuestion" />

      <section v-if="editing" class="revision-editor" aria-label="普通题库修订">
        <HelpHint label="个人题目修订说明">仅内容变化才生成新版本；保存后回到题目整理，重新确认后可导出。初始版已通过首检，当前修改不会触发 AI 复检。</HelpHint>
        <p v-if="editStale" role="alert">题目已变化，已保留输入；请取消修订并刷新后重试。</p>
        <label>题干<textarea aria-label="修订题干" v-model="editForm.clinical_stem" :disabled="mutating || editStale" class="edit-textarea"></textarea></label>
        <label v-for="(option,index) in editForm.options" :key="index" class="edit-option-row">{{ option.label }}<input v-model="option.text" :aria-label="`修订选项 ${option.label}`" :disabled="mutating || editStale" class="edit-input" /></label>
        <label>正确答案<select aria-label="修订答案" v-model="editForm.answer" :disabled="mutating || editStale" class="edit-select"><option v-for="option in editForm.options" :key="option.label" :value="option.label">{{ option.label }}</option></select></label>
        <label>解析<textarea aria-label="修订解析" v-model="editForm.explanation" :disabled="mutating || editStale" class="edit-textarea"></textarea></label>
        <label>修订说明<input aria-label="修订说明" v-model="editForm.change_reason" :disabled="mutating || editStale" class="edit-input" /></label>
        <div class="edit-buttons"><button class="primary-button" type="button" :disabled="!editDirty || editStale || mutating" @click="saveRevision">{{ mutating ? "保存中..." : "保存修订" }}</button><button class="ghost-button" type="button" :disabled="mutating" @click="discardRevision();loadQuestions(false)">取消修订</button></div>
      </section>
      <!-- 详情与修订前保存版本 -->
        <!-- 试题参数 -->
        <div v-if="[selectedQuestion.outline_code, selectedQuestion.difficulty, selectedQuestion.cognitive_level, selectedQuestion.profession, selectedQuestion.system].some(Boolean)" class="params-bar">
          <span v-if="selectedQuestion.outline_code" class="param-item">
            <span class="param-label">大纲代码</span>
            <span class="param-value">{{ selectedQuestion.outline_code }}</span>
          </span>
          <span v-if="selectedQuestion.difficulty" class="param-item">
            <span class="param-label">预估难度</span>
            <span class="param-value">{{ selectedQuestion.difficulty }}</span>
          </span>
          <span v-if="selectedQuestion.cognitive_level" class="param-item">
            <span class="param-label">认知层次</span>
            <span class="param-value">{{ selectedQuestion.cognitive_level }}</span>
          </span>
          <span v-if="selectedQuestion.profession" class="param-item">
            <span class="param-label">专业</span>
            <span class="param-value">{{ selectedQuestion.profession }}</span>
          </span>
          <span v-if="selectedQuestion.system" class="param-item">
            <span class="param-label">系统</span>
            <span class="param-value">{{ selectedQuestion.system }}</span>
          </span>
        </div>

        <div v-if="selectedQuestion.exam_points" class="detail-field">
          <label>考核要点</label>
          <p>{{ selectedQuestion.exam_points }}</p>
        </div>

        <div class="detail-field">
          <label>题干</label>
          <p class="stem-text">{{ selectedQuestion.clinical_stem }}</p>
        </div>

        <div class="detail-field">
          <label>选项</label>
          <div v-for="(opt, i) in (selectedQuestion.options || [])" :key="i" class="option-row-detail">
            <span class="opt-label">{{ opt.label }}</span>
            <span>{{ opt.text }}</span>
            <span v-if="opt.label === selectedQuestion.answer" class="correct">✓ 正确答案</span>
          </div>
        </div>

        <div v-if="selectedQuestion.explanation" class="detail-field">
          <label>解析</label>
          <p class="explanation-text">{{ selectedQuestion.explanation }}</p>
        </div>

        <div class="detail-field">
          <label>考试大纲要点</label>
          <div v-for="kp in (selectedQuestion.knowledge_points || [])" :key="kp.id" class="kp-tag">
            {{ kp.topic }}
          </div>
        </div>

        <div v-if="questionReviewUi.enabled() && reviewInfoLoading" class="detail-field review-loading">审核意见加载中</div>
        <!-- 终态任务向有权查看题目的用户开放全部审核记录；进行中仍由服务端保持评语隔离。 -->
        <div v-else-if="questionReviewUi.enabled() && reviewInfo" class="detail-field review-info-field">
          <label>审核意见（第 {{ reviewInfo.task.attempt || 1 }} 次送审 · {{ reviewConclusionText(reviewInfo.task.status) }}<template v-if="reviewInfo.task.flow_name || reviewInfo.task.flow_id"> · 流程：{{ reviewInfo.task.flow_name || reviewInfo.task.flow_id }}</template>）</label>
          <ReviewHistoryPanel :records="reviewInfo.records" compact />
        </div>
        <PreviousReviewHistory v-if="questionReviewUi.enabled() && !isGlobalScope" :question-id="selectedQuestion.id" />

    </section>

    <section v-else class="panel empty-panel">
      <p>← 请从左侧选择一道题目查看详情</p>
    </section>
  </div>

  <div class="toast" :class="{ show: toast, error: toastError }" role="status" aria-live="polite" @click="dismissToast">{{ toast }}</div>

  <dialog v-if="questionReviewUi.enabled() && shareDialog" ref="shareDialogElement" class="bulk-share-dialog" aria-labelledby="bulk-share-title" @cancel.prevent="!mutating && (shareDialog = null)">
    <h3 id="bulk-share-title">提交至全局题库</h3>
    <p class="share-scope-summary">{{ shareDialog.scope }}</p>
    <p>本次提交 <strong>{{ shareDialog.ids.length }}</strong> 道本人已确认的当前版本；分享获批后全局保留这次固定版本，个人后续修订不改写它。</p>
    <p class="share-scope-summary">已申请过的题目自动跳过；个人题库中的原题保留。</p>
    <p v-if="shareNotice" role="status">{{ shareNotice }}</p>
    <div class="bulk-share-dialog-actions">
      <button class="ghost-button" type="button" :disabled="mutating" @click="shareDialog = null">取消</button>
      <button class="primary-button" type="button" :disabled="mutating" @click="submitBulkShare">{{ mutating ? '提交中…' : '确认提交' }}</button>
    </div>
  </dialog>

</template>

<style scoped>
.revision-editor { display:grid; gap:12px; padding:14px; margin-bottom:16px; border:1px solid #dce8f7; border-radius:8px; }
.revision-editor label { display:grid; gap:6px; font-size:13px; }
.revision-editor p { font-size:13px; line-height:1.6; margin:0; color:#52647a; }
.bulk-share-actions { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.bulk-share-actions button { font-size: 12px; }
.bulk-share-actions button:disabled { opacity: .5; cursor: not-allowed; }
.bulk-share-notice { flex: none; margin: 0 0 12px; padding: 10px; background: #f3f7fc; color: #435269; font-size: 12px; line-height: 1.6; }
.bulk-share-dialog { width: min(480px, calc(100vw - 32px)); padding: 24px; border: 1px solid #e5ebf3; border-radius: 10px; color: #172033; }
.bulk-share-dialog::backdrop { background: rgb(23 32 51 / 35%); }
.bulk-share-dialog h3 { margin: 0 0 16px; }
.bulk-share-dialog p { line-height: 1.7; font-size: 14px; }
.share-scope-summary { color: #6e7b8f; overflow-wrap: anywhere; }
.bulk-share-dialog-actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 20px; }
.ai-force-btn {
  color: #c07b22;
  border-color: #f3d9b0;
}

.ai-force-btn:hover {
  background: #fff8ec;
}

.bank-layout {
  display: grid;
  grid-template-columns: 420px minmax(0, 1fr);
  gap: 16px;
}

.bank-list-panel {
  display: flex;
  flex-direction: column;
  max-height: calc(100vh - 180px);
  overflow: hidden;
}

/* 面板内的固定部分不参与压缩，列表在独立滚动盒内滚动（不会钻到筛选栏后面） */
.bank-list-panel .section-heading,
.list-toolbar,
.batch-bar,
.list-footer {
  flex-shrink: 0;
}

/* 筛选与题库栏：固定在列表上方 */
.list-toolbar {
	margin-bottom: 4px;
}

.scope-tabs {
	display: flex;
	gap: 16px;
	margin: 0 0 13px;
	border-bottom: 1px solid #dce8f7;
}

.scope-tab {
	border: 0;
	border-bottom: 2px solid transparent;
	margin-bottom: -1px;
	padding: 0 2px 9px;
	background: none;
	color: #6e7b8f;
	font-size: 13px;
	cursor: pointer;
}

.view-all-hint {
  margin: 4px 0 0;
  font-size: 12px;
  color: #6e7b8f;
}

.scope-tab.active {
	border-bottom-color: #1385f8;
	color: #172033;
	font-weight: 700;
}

.scope-tab:hover {
	color: #1385f8;
}

/* 题库分类切换：黑字精简，仅下划线指示当前分类 */
.tier-tabs {
  display: flex;
  gap: 18px;
  margin-bottom: 12px;
  border-bottom: 1px solid #eef2f7;
}

.tier-tab {
  border: 0;
  background: none;
  padding: 0 2px 8px;
  margin-bottom: -1px;
  color: #172033;
  font-size: 13px;
  cursor: pointer;
  border-bottom: 2px solid transparent;
}

.tier-tab:hover {
  color: #000;
}

.tier-tab.active {
  color: #172033;
  font-weight: 600;
  border-bottom-color: #172033;
}

/* 列表滚动盒 */
.question-scroll {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
}

.filter-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 12px;
}

.search-input {
  flex: 1;
  min-width: 140px;
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 10px;
  font-size: 13px;
}

.filter-row select {
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 8px;
  font-size: 13px;
}

/* 页码换页 */
.list-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 0 2px;
  border-top: 1px solid #eef2f7;
  font-size: 13px;
  color: #556;
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

/* 批量操作栏 */
.batch-bar {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 0;
  border-bottom: 1px solid #e5ebf3;
  margin-bottom: 8px;
  flex-wrap: wrap;
}

/* 管理员提交审核 */
.submit-review-bar {
  display: flex;
  align-items: center;
  gap: 6px;
}

.flow-select-input {
  height: 32px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 8px;
  font-size: 13px;
  max-width: 220px;
}

.submit-btn {
  height: 32px;
  padding: 0 14px;
  border: 0;
  border-radius: 6px;
  background: #1385f8;
  color: #fff;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  white-space: nowrap;
}

.submit-btn:hover {
  background: #0571dc;
}

.submit-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.submit-single-section {
  margin-top: 12px;
  padding: 10px 12px;
  background: #eff8ff;
  border: 1px solid #dce8f7;
  border-radius: 8px;
}

.submit-single-section label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 6px;
}

.submit-single-row {
  display: flex;
  gap: 8px;
  align-items: center;
}

.create-form {
  padding: 12px;
  background: #f8fbff;
  border: 1px solid #dce8f7;
  border-radius: 8px;
  margin-bottom: 8px;
}

.create-field {
  margin-bottom: 10px;
}

.create-field label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 4px;
}

.create-actions {
  display: flex;
  gap: 8px;
}

.status-conflict {
  background: #fdf0f8;
  color: #b93a7c;
}

.select-all {
  display: flex;
  align-items: center;
  gap: 6px;
  cursor: pointer;
  font-size: 13px;
  color: #6e7b8f;
}

.select-all input { width: 16px; height: 16px; }

.selected-count {
  font-size: 12px;
  color: #0571dc;
  font-weight: 600;
}

.export-btns {
  display: flex;
  gap: 8px;
  margin-left: auto;
}

/* 单列网格：minmax(0,1fr) 强制行宽等于面板宽，题干超长时省略号截断而不是横向撑开 */
.question-list { display: grid; grid-template-columns: minmax(0, 1fr); gap: 6px; }

.question-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 12px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  background: #fff;
}

.question-item:hover { background: #f8fbff; }
.question-item.active { border-color: #1385f8; background: #eff8ff; }
.question-item.selected { border-color: #0571dc; background: #eff8ff; }

.q-checkbox {
  width: 16px;
  height: 16px;
  flex-shrink: 0;
  cursor: pointer;
}

.q-content {
  flex: 1;
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 10px;
  background: transparent;
  border: 0;
  padding: 0;
  text-align: left;
  cursor: pointer;
  min-width: 0;
  font-size: inherit;
  font-family: inherit;
  color: inherit;
}

.q-info { flex: 1; min-width: 0; }

.q-stem {
  display: block;
  font-size: 13px;
  color: #172033;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.q-meta { font-size: 11px; color: #6e7b8f; }

.share-status,
.share-detail-status { color: #a16618; font-size: 11px; white-space: nowrap; }

.share-btn { color: #087c55; border-color: #bdecd9; }
.share-btn:hover { background: #f0fff8; }

.q-actions { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }

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

.delete-btn {
  width: 28px; height: 28px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #c54858;
  font-size: 18px;
  cursor: pointer;
  display: grid;
  place-items: center;
}

.delete-btn:hover { background: #fff0f0; border-color: #c54858; }

.delete-btn:disabled { opacity: 0.6; cursor: not-allowed; }

.publish-btn {
  padding: 2px 8px;
  border: 1px solid #087c55;
  border-radius: 6px;
  background: #f0fff8;
  color: #087c55;
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
  white-space: nowrap;
}

.publish-btn:hover { background: #087c55; color: #fff; }

.publish-section {
  border-top: 1px solid #e5ebf3;
  padding-top: 16px;
  margin-top: 16px;
}

/* 参数栏 */
.params-bar {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  padding: 12px 16px;
  background: #f8fbff;
  border: 1px solid #dce8f7;
  border-radius: 8px;
  margin-bottom: 16px;
}

.param-item {
  display: flex;
  align-items: center;
  gap: 6px;
}

.param-label {
  font-size: 11px;
  color: #6e7b8f;
  font-weight: 600;
}

.param-value {
  font-size: 12px;
  color: #172033;
  padding: 2px 8px;
  background: #fff;
  border: 1px solid #e5ebf3;
  border-radius: 4px;
  font-family: monospace;
}

.detail-field { margin-bottom: 16px; }

.detail-field label {
  display: block;
  font-size: 12px;
  font-weight: 700;
  color: #6e7b8f;
  margin-bottom: 6px;
}

.detail-field p {
  font-size: 14px;
  line-height: 1.7;
  margin: 0;
  color: #172033;
}

.stem-text {
  white-space: pre-wrap;
  word-break: break-all;
}

.explanation-text {
  white-space: pre-wrap;
  word-break: break-all;
  color: #444 !important;
}

.option-row-detail {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 0;
  font-size: 14px;
}

.opt-label {
  width: 22px; height: 22px;
  display: grid;
  place-items: center;
  border-radius: 50%;
  background: #1385f8;
  color: #fff;
  font-size: 11px;
  font-weight: 700;
}

.correct { color: #087c55; font-weight: 700; }

.kp-tag {
  display: inline-block;
  padding: 3px 10px;
  background: #eff8ff;
  color: #0571dc;
  border-radius: 5px;
  font-size: 12px;
  font-weight: 600;
  margin: 2px 4px 2px 0;
}

.review-loading {
  color: #6e7b8f;
  font-size: 12px;
}

.empty-panel { display: grid; place-items: center; color: #6e7b8f; }
.empty, .loading { text-align: center; color: #6e7b8f; padding: 30px; }
.edit-actions { margin-left: auto; }

.edit-textarea {
  width: 100%;
  min-height: 100px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 10px;
  font-size: 14px;
  line-height: 1.6;
  resize: vertical;
}

.edit-option-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.edit-input {
  flex: 1;
  height: 34px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 10px;
  font-size: 14px;
}

.edit-select {
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 10px;
  font-size: 14px;
}

.edit-buttons {
  display: flex;
  gap: 10px;
  margin-top: 16px;
  padding-top: 16px;
  border-top: 1px solid #e5ebf3;
}

.text-button {
  border: 0;
  background: transparent;
  color: #0571dc;
  cursor: pointer;
  font-size: 13px;
  padding: 4px 0;
}

.remove-btn {
  width: 28px; height: 28px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #c54858;
  font-size: 18px;
  cursor: pointer;
}

.remove-btn:hover:not(:disabled) { background: #fff0f0; }
.remove-btn:disabled { opacity: 0.3; cursor: not-allowed; }

@media (max-width: 900px) {
  .bank-layout { grid-template-columns: minmax(0, 1fr); }
  .bank-list-panel { min-width: 0; }
  .export-btns { flex-wrap: wrap; margin-left: 0; }
  .list-footer { flex-wrap: wrap; gap: 8px; }
}

</style>
