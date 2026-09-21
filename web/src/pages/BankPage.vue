<script setup>
import { ref, onMounted, computed, watch, nextTick } from "vue";
import { useRoute } from "vue-router";
import { api } from "../api.js";
import { hasPerm, currentUser } from "../auth.js";
import AICheckScoreButton from "../components/AICheckScoreButton.vue";
import ReviewHistoryPanel from "../components/ReviewHistoryPanel.vue";

const route = useRoute();

// 权限判断。题目唯一来源是 AI 生成：无手动新建；编辑窗口仅限专家退回修改（待我修改页）。
// AI 检查仅在首次生成时自动执行一次；故障题阻断流程，不提供复检或强制通过。
const canDelete = computed(() => hasPerm("question:delete"));
const canDownload = computed(() => hasPerm("question:download"));
const canUnpublish = computed(() => hasPerm("user:manage"));
// 有权限者可删正式题库（question:delete_formal）；普通所有者也可删自己流程
// 完全结束的 published 题（无需权限，见 canDeleteQuestion）
const canDeleteFormal = computed(() => hasPerm("question:delete_formal"));

// 删除=移入淘汰题库留档。两类入口：
// - 有权限者（question:delete / 正式题库 question:delete_formal）：个人与全局题库均可删；
// - 所有人：本人的"刚出未送审"（ai_reviewed）或"流程完全结束"（published）的题，
//   无需分配权限（后端按 owner_id 校验）。
// 流程中的题（审核中/待决断/需修改）与淘汰终态不可删。
const DELETABLE_OWNER_STATUSES = ["ai_reviewed", "published"];
function canDeleteQuestion(q) {
  if (["rejected", "archived", "reviewing", "conflict", "revision_required"].includes(q.status)) return false;
  if (q.status === "ai_draft" || q.status === "auto_checked") return false;
  if (q.status === "published" ? canDeleteFormal.value : canDelete.value) return true;
  return !isGlobalScope.value
    && q.owner_id === currentUser.value?.id
    && DELETABLE_OWNER_STATUSES.includes(q.status);
}

// 个人题库按 owner_id 隔离，三个分层都由 question:view 控制；
// 全局题库按分享申请状态推导三层，并额外要求 question:view_global。
const PERSONAL_TIER_DEFS = [
  { key: "formal", name: "正式题库", perm: "question:view" },
  { key: "working", name: "待审核题库", perm: "question:view" },
  { key: "eliminated", name: "淘汰题库", perm: "question:view" },
];
const GLOBAL_TIER_DEFS = [
  { key: "formal", name: "正式题库", perm: "question:view_formal" },
  { key: "working", name: "待审核题库", perm: "question:view" },
  { key: "eliminated", name: "淘汰题库", perm: "question:view_eliminated" },
];
const canViewGlobal = computed(() => hasPerm("question:view_global"));
const requestedScope = route.query.scope === "personal" || route.query.scope === "global" ? route.query.scope : "";
const questionScope = ref(requestedScope === "personal" || (requestedScope === "global" && canViewGlobal.value)
  ? requestedScope
  : (canViewGlobal.value ? "global" : "personal"));
const isGlobalScope = computed(() => questionScope.value === "global");
const visibleTiers = computed(() => (isGlobalScope.value ? GLOBAL_TIER_DEFS : PERSONAL_TIER_DEFS).filter((t) => hasPerm(t.perm)));
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
const statusOptions = computed(() => TIER_STATUS_OPTIONS[activeTier.value] || []);

// 切换生命周期题库：重置筛选与选择后重新加载
function switchTier(tier) {
  if (!tier || tier === activeTier.value) return;
  activeTier.value = tier;
  filterStatus.value = "";
  selectedIds.value.clear();
  selectAll.value = false;
  selectedQuestion.value = null;
  doSearch();
}

function switchScope(scope) {
  if (scope === questionScope.value || (scope === "global" && !canViewGlobal.value)) return;
  questionScope.value = scope;
  activeTier.value = visibleTiers.value[0]?.key || "";
  filterStatus.value = "";
  selectedIds.value.clear();
  selectAll.value = false;
  selectedQuestion.value = null;
  loadShareRequests();
  doSearch();
}

const toast = ref("");
const questions = ref([]);
const loading = ref(false);
const filterStatus = ref("");
const searchQuery = ref("");
const filterProfession = ref(""); // 专业筛选
const professions = ref([]); // 专业列表
const selectedQuestion = ref(null);
const shareStatuses = ref({});
// 审核意见（按题目加载）：出题人可在详情里看到本人题目的轮次评语与驳回/决断理由；
// 进行中的任务由服务端按隔离规则裁剪，已结束任务（已通过/已驳回）保留完整评语。
const reviewInfo = ref(null); // { task, records }
const reviewInfoLoading = ref(false);
let questionSearchTicket = 0;

const mutating = ref(false); // 撤回/删除在途守卫，防重复事务
const canBatchShare = computed(() => !isGlobalScope.value && activeTier.value === 'formal' && hasPerm('question:share'));
const shareDialog = ref(null);
const shareDialogElement = ref(null);
watch(shareDialog, async value => {
  if (value) { await nextTick(); shareDialogElement.value?.showModal(); }
});
const shareNotice = ref('');
const preparingShare = ref(false);
const appliedShareFilters = ref({ q: '', profession: '' });
const shareableSelected = computed(() => [...selectedIds.value].filter(id => !shareStatus(id)));

async function prepareBulkShare(mode) {
  if (!canBatchShare.value || mutating.value || preparingShare.value || loading.value) return;
  preparingShare.value = true;
  shareNotice.value = '';
  const filters = { ...appliedShareFilters.value };
  const ticket = questionSearchTicket;
  try {
    let ids;
    if (mode === 'selected') {
      ids = [...shareableSelected.value];
    } else {
      const data = await api.previewQuestionShares(filters);
      if (ticket !== questionSearchTicket || !canBatchShare.value) return;
      if (data.total > data.limit) {
        shareNotice.value = `当前有 ${data.total} 道可分享题目。每批最多 ${data.limit} 道，请缩小专业或关键词范围，或勾选后提交。`;
        return;
      }
      ids = data.question_ids || [];
    }
    if (!ids.length) { shareNotice.value = '当前范围没有可提交的题目，已申请过的题目会自动跳过。'; return; }
    if (ids.length > 500) { shareNotice.value = '每批最多提交 500 道，请减少勾选数量。'; return; }
    shareDialog.value = {
      ids,
      scope: mode === 'selected' ? '已勾选的个人正式题目' : [
        filters.profession ? `专业：${filters.profession}` : '全部专业',
        filters.q ? `关键词：${filters.q}` : '',
      ].filter(Boolean).join(' · '),
    };
  } catch (e) { shareNotice.value = '准备分享失败：' + e.message; }
  finally { preparingShare.value = false; }
}

async function submitBulkShare() {
  if (!shareDialog.value || mutating.value) return;
  mutating.value = true;
  const ids = [...shareDialog.value.ids];
  try {
    const data = await api.createQuestionShares(ids);
    for (const id of data.question_ids || []) shareStatuses.value[id] = 'pending';
    shareNotice.value = `已提交 ${data.submitted} 道，等待管理员审批。` +
      (data.skipped ? `另有 ${data.skipped} 道已申请或状态已变化，本次已跳过。` : '');
    ids.forEach(id => selectedIds.value.delete(id));
    selectAll.value = false;
    shareDialog.value = null;
    await loadShareRequests();
  } catch (e) { shareNotice.value = '提交失败：' + e.message; }
  finally { mutating.value = false; }
}

// 管理员撤回已通过题目至 AI 检查通过状态
async function unpublishSelected() {
  const q = selectedQuestion.value;
  if (!q) return;
  if (mutating.value) return;
	if (!confirm("确定撤回该已通过题目？题目将进入原出题人的「待我修改」，保存新版本后按原流程从第一轮重新审核。")) return;
  mutating.value = true;
  try {
    const updated = await api.unpublishQuestion(q.id, "题库页撤回修订");
    if (selectedQuestion.value?.id === q.id) selectedQuestion.value = updated;
	showToast("已撤回并进入原出题人的待我修改");
    await loadQuestions(false);
  } catch (e) {
    showToast("撤回失败: " + e.message);
  } finally {
    mutating.value = false;
  }
}

async function loadShareRequests() {
  if (!hasPerm("question:share") || isGlobalScope.value) {
    shareStatuses.value = {};
    return;
  }
  try {
    const data = await api.listQuestionShares("mine");
    if (isGlobalScope.value) return;
    const next = {};
    for (const item of data.items || []) next[item.request.question_id] = item.request.status;
    shareStatuses.value = next;
  } catch (e) {
    console.error(e);
  }
}

function shareStatus(questionID) {
  return shareStatuses.value[questionID] || "";
}

function shareStatusText(status) {
  return { pending: "待管理员审核", approved: "已进入全局正式库", rejected: "分享未通过" }[status] || "";
}

async function requestShare(q) {
  if (mutating.value || !q || q.status !== "published" || shareStatus(q.id)) return;
  if (!confirm("确认申请将这道个人正式题目分享至全局题库？每道题只能申请一次，审批后不能重复申请。")) return;
  mutating.value = true;
  try {
    const data = await api.createQuestionShare(q.id);
    shareStatuses.value = { ...shareStatuses.value, [q.id]: data.request?.status || "pending" };
    showToast("分享申请已提交，等待管理员审批");
  } catch (e) {
    showToast("提交分享申请失败: " + e.message);
  } finally {
    mutating.value = false;
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

async function downloadFile(filename) {
  const blob = await api.downloadExport(filename);
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  URL.revokeObjectURL(url);
}

async function exportXlsx() {
  const ids = Array.from(selectedIds.value);
  if (ids.length === 0) {
    showToast("请先选择要导出的题目");
    return;
  }
  exporting.value = true;
  try {
    const data = await api.exportXlsx({ question_ids: ids, scope: questionScope.value });
    showToast(`已导出 ${data.count} 道题`);
    downloadFile(data.filename);
  } catch (e) {
    showToast("导出失败: " + e.message);
  } finally {
    exporting.value = false;
  }
}

async function exportDocx() {
  const ids = Array.from(selectedIds.value);
  if (ids.length === 0) {
    showToast("请先选择要导出的题目");
    return;
  }
  exporting.value = true;
  try {
    const data = await api.exportDocx({ question_ids: ids, scope: questionScope.value });
    showToast(`已导出 ${data.count} 道题`);
    downloadFile(data.filename);
  } catch (e) {
    showToast("导出失败: " + e.message);
  } finally {
    exporting.value = false;
  }
}

async function exportAllXlsx() {
  exporting.value = true;
  try {
    const data = await api.exportXlsx({ export_all: true, scope: questionScope.value });
    showToast(`已导出正式题库全部 ${data.count} 道题`);
    downloadFile(data.filename);
  } catch (e) {
    showToast("导出失败: " + e.message);
  } finally {
    exporting.value = false;
  }
}

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  toast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadQuestions(resetPage = true) {
  const ticket = ++questionSearchTicket;
  shareNotice.value = '';
  if (!mutating.value) shareDialog.value = null;
  const requestedShareFilters = { q: searchQuery.value, profession: filterProfession.value };
  loading.value = true;
  if (resetPage) page.value = 1;
  try {
    let data;
    if (searchQuery.value || filterStatus.value || filterProfession.value) {
      data = await api.searchQuestions(searchQuery.value, filterStatus.value, page.value, pageSize, "", filterProfession.value ? [filterProfession.value] : [], "", "", activeTier.value, false, questionScope.value);
    } else {
      data = await api.listQuestions(page.value, pageSize, "", activeTier.value, questionScope.value);
    }
    if (ticket !== questionSearchTicket) return;
    questions.value = data.questions || [];
    selectAll.value = questions.value.length > 0 && questions.value.every(q => selectedIds.value.has(q.id));
    totalCount.value = data.total || 0;
    appliedShareFilters.value = requestedShareFilters;
  } catch (e) {
    showToast("加载失败: " + e.message);
  } finally {
    if (ticket === questionSearchTicket) loading.value = false;
  }
}

// 页码换页：钳制页码范围后重新加载，并回到列表顶部
function goPage(p) {
  if (loading.value) return;
  const target = Math.min(Math.max(1, p), pageCount.value);
  if (target === page.value) return;
  page.value = target;
  loadQuestions(false);
  const scroller = document.querySelector(".question-scroll");
  if (scroller) scroller.scrollTop = 0;
}

function selectQuestion(q) {
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
  reviewInfo.value = null;
  if (!q) return;
  reviewInfoLoading.value = true;
  try {
    const task = await api.getTaskByQuestion(q.id);
    if (!task || selectedQuestion.value?.id !== q.id) {
      reviewInfo.value = null;
      return;
    }
    const data = await api.reviewRecords(task.id);
    reviewInfo.value = { task, records: data.records || [] };
  } catch (e) {
    // 审核意见加载失败不打断详情查看；AI 检查评分按钮仍可用
    console.error("加载审核意见失败:", e);
    reviewInfo.value = null;
  } finally {
    reviewInfoLoading.value = false;
  }
}

watch(selectedQuestion, (q) => {
  loadReviewInfo(q);
});

async function deleteQuestion(q) {
  if (mutating.value) return;
  if (!confirm(`确定删除题目？删除后将移入淘汰题库留档（版本与审核记录保留）。\n${(q.clinical_stem || "").slice(0, 50)}...`)) return;
  mutating.value = true;
  try {
    const data = await api.deleteQuestion(q.id);
    showToast(data?.archived ? "已移入淘汰题库留档（审核记录保留）" : "已删除");
    questions.value = questions.value.filter((item) => item.id !== q.id);
    totalCount.value = Math.max(0, totalCount.value - 1);
    if (selectedQuestion.value?.id === q.id) selectedQuestion.value = null;
  } catch (e) {
    showToast("删除失败: " + e.message);
  } finally {
    mutating.value = false;
  }
}

function doSearch() {
  selectedIds.value.clear();
  selectAll.value = false;
  loadQuestions();
}

function clearSearch() {
  searchQuery.value = "";
  filterStatus.value = "";
  filterProfession.value = "";
  selectedIds.value.clear();
  selectAll.value = false;
  loadQuestions();
}

function statusText(status) {
  const lifecycle = {
    ai_draft: "AI草稿", auto_checked: "已初评", ai_reviewed: "AI已检查",
    reviewing: "审核中", conflict: "待决断", approved: "已通过", rejected: "专家审核已驳回",
    revision_required: "需修改", published: "已通过", archived: "已归档",
  };
  if (isGlobalScope.value) {
    if (activeTier.value === "formal") return "已进入全局正式库";
    // 新分享申请的题目本身已经 published；历史全局过程题保持真实审核状态。
    if (activeTier.value === "working") return status === "published" ? "待管理员审核分享" : (lifecycle[status] || status);
    // 分享被拒的个人题仍是 published；历史 rejected/archived 则来自专家审核流程。
    if (activeTier.value === "eliminated") return status === "published" ? "分享未通过" : (lifecycle[status] || status);
  }
  return lifecycle[status] || status;
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
  try {
    const q = await api.getQuestion(id);
    if (q) {
      selectedQuestion.value = q;
    }
  } catch (e) {
    showToast("定位题目失败: " + e.message);
  }
}

async function loadProfessions() {
  try {
    const data = await api.listProfessions();
    professions.value = data.professions || [];
  } catch (e) {
    console.error(e);
  }
}

onMounted(async () => {
  await Promise.all([loadQuestions(), loadProfessions(), loadShareRequests()]);
  // 支持从审核页「去修改题目」跳转：?edit=<题目ID> 自动定位并进入编辑
  const editId = new URLSearchParams(window.location.search).get("edit");
  if (editId) {
    await selectQuestionById(editId);
  }
});
</script>

<template>
  <div class="bank-layout">
    <!-- 左侧列表 -->
    <section class="panel bank-list-panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>{{ isGlobalScope ? "全局题库" : "我的题库" }}</h2>
        <!-- 总数取接口 total（跨分页），当前页只有前 100 条 -->
        <small>{{ totalCount }} 道</small>
      </div>

      <!-- 筛选与题库栏固定：列表滚动时保持可见 -->
      <div class="list-toolbar">
        <div v-if="canViewGlobal" class="scope-tabs" aria-label="题库范围">
          <button type="button" class="scope-tab" :class="{ active: questionScope === 'personal' }" @click="switchScope('personal')">我的题库</button>
          <button type="button" class="scope-tab" :class="{ active: questionScope === 'global' }" @click="switchScope('global')">全局题库</button>
        </div>
        <p v-if="hasPerm('question:view_all')" class="view-all-hint">已授予「查看全部题库」：列表包含所有用户的题目；分享与退修仅限本人题目。</p>
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
          <input v-model="searchQuery" placeholder="搜索题干、选项、解析、专业、系统、大纲要点或ID..." @keyup.enter="doSearch" class="search-input" />
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
        <!-- 导出仅包含已通过（正式题库）题目，由后端强制；仅正式题库页提供 -->
        <div class="export-btns" v-if="canDownload && activeTier === 'formal'">
          <button class="ghost-button" type="button" @click="exportXlsx" :disabled="exporting || selectedIds.size === 0">
            {{ exporting ? "导出中..." : "导出 Excel" }}
          </button>
          <button class="ghost-button" type="button" @click="exportDocx" :disabled="exporting || selectedIds.size === 0">
            {{ exporting ? "导出中..." : "导出 Word" }}
          </button>
          <button class="ghost-button" type="button" @click="exportAllXlsx" :disabled="exporting">
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
                <span class="q-status" :class="statusClass(q.status)">{{ statusText(q.status) }}</span>
                <span v-if="!isGlobalScope && shareStatus(q.id) && q.status !== 'archived'" class="share-status">{{ shareStatusText(shareStatus(q.id)) }}</span>
                <button v-if="canDeleteQuestion(q)" class="delete-btn" type="button" :disabled="mutating" @click.stop="deleteQuestion(q)" title="删除（移入淘汰题库）">×</button>
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
          {{ statusText(selectedQuestion.status) }}
        </span>
        <AICheckScoreButton :question-id="selectedQuestion.id" />
        <div class="edit-actions">
          <button
            v-if="!isGlobalScope && canUnpublish && activeTier === 'formal' && selectedQuestion.status === 'published'"
            class="ghost-button ai-force-btn"
            type="button"
            title="撤回已通过题目至 AI 检查通过状态（写审计日志）"
            :disabled="mutating"
            @click="unpublishSelected"
          >
            {{ mutating ? "处理中..." : "撤回" }}
          </button>
          <button
            v-if="!isGlobalScope && hasPerm('question:share') && activeTier === 'formal' && selectedQuestion.status === 'published' && !shareStatus(selectedQuestion.id)"
            class="ghost-button share-btn"
            type="button"
            :disabled="mutating"
            @click="requestShare(selectedQuestion)"
          >
            申请分享至全局库
          </button>
          <span v-else-if="!isGlobalScope && activeTier === 'formal' && shareStatus(selectedQuestion?.id)" class="share-detail-status">
            {{ shareStatusText(shareStatus(selectedQuestion.id)) }}
          </span>
        </div>
      </div>

      <!-- 详情（只读；修订入口在「待我修改」页） -->
        <!-- 试题参数 -->
        <div class="params-bar">
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

        <div v-if="reviewInfoLoading" class="detail-field review-loading">审核意见加载中</div>
        <!-- 终态任务向有权查看题目的用户开放全部审核记录；进行中仍由服务端保持评语隔离。 -->
        <div v-else-if="reviewInfo" class="detail-field review-info-field">
          <label>审核意见（第 {{ reviewInfo.task.attempt || 1 }} 次送审 · {{ reviewConclusionText(reviewInfo.task.status) }}<template v-if="reviewInfo.task.flow_name || reviewInfo.task.flow_id"> · 流程：{{ reviewInfo.task.flow_name || reviewInfo.task.flow_id }}</template>）</label>
          <ReviewHistoryPanel :records="reviewInfo.records" compact />
        </div>

        <div class="detail-field">
          <label>元信息</label>
          <p class="meta-text">ID: {{ selectedQuestion.id }}</p>
          <p class="meta-text">版本: {{ selectedQuestion.version }}</p>
          <p class="meta-text">创建: {{ selectedQuestion.created_at }}</p>
        </div>
    </section>

    <section v-else class="panel empty-panel">
      <p>← 请从左侧选择一道题目查看详情</p>
    </section>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>

  <dialog v-if="shareDialog" ref="shareDialogElement" class="bulk-share-dialog" aria-labelledby="bulk-share-title" @cancel.prevent="!mutating && (shareDialog = null)">
    <h3 id="bulk-share-title">提交至全局题库</h3>
    <p class="share-scope-summary">{{ shareDialog.scope }}</p>
    <p>本次提交 <strong>{{ shareDialog.ids.length }}</strong> 道个人正式题目，审批通过后进入全局正式题库。</p>
    <p class="share-scope-summary">已申请过的题目自动跳过；个人题库中的原题保留。</p>
    <p v-if="shareNotice" role="status">{{ shareNotice }}</p>
    <div class="bulk-share-dialog-actions">
      <button class="ghost-button" type="button" :disabled="mutating" @click="shareDialog = null">取消</button>
      <button class="primary-button" type="button" :disabled="mutating" @click="submitBulkShare">{{ mutating ? '提交中…' : '确认提交' }}</button>
    </div>
  </dialog>

</template>

<style scoped>
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

.meta-text {
  font-size: 12px !important;
  color: #6e7b8f !important;
  font-family: monospace;
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
