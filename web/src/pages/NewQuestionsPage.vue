<script setup>
import {routeJourney,journeyParams,verifyJourneyList} from "../authoringJourney.js";
const guideRoute=useRoute();
const confirmedNotice=ref(0);
const guideTask=computed(()=>routeJourney(guideRoute.query));
import {questionReviewUi} from "../questionReviewUi.js";
import HelpHint from "../components/HelpHint.vue";
import { ref, computed, onMounted, onBeforeUnmount, watch } from "vue";
import { useRoute,onBeforeRouteUpdate,onBeforeRouteLeave } from "vue-router";
import { api } from "../api.js";
import { reviewCompletionLabel } from "../reviewCompletion.js";
import { isLoggedIn, hasPerm, currentUser, getToken } from "../auth.js";
import { notifyNavigationWorkChanged } from "../navigation.js";
import { useLiveRefresh } from "../useLiveRefresh.js";
import { reviewExpectation, reviewExpectations } from "../reviewExpectation.js";
import { libraryExpectation } from "../libraryExpectation.js";
import { canAccessQuestionArrangement } from "../navigation.js";
import CompactPager from "../components/CompactPager.vue";
import AuthorReviewProgress from "../components/AuthorReviewProgress.vue";
import QuestionDetailModal from "../components/QuestionDetailModal.vue";
import QuestionLearningPanel from '../components/QuestionLearningPanel.vue';
import PersonalExperienceProvenance from '../components/PersonalExperienceProvenance.vue';
import { refreshSessionIdentity } from '../sessionIdentity.js';

const questions = ref([]);
const learningPanel = ref(null);
const flows = ref([]);
const selectedIds = ref(new Set());
const selected = ref(null);
const selectionStale = ref(false);
const selectionOutsideList = ref(false);
const selectionOutsideNotice = ref('');
let questionTicket = 0;
let flowTicket = 0;
let disposed = false;
const sessionKey = computed(() => JSON.stringify([getToken(), currentUser.value?.id, currentUser.value?.role, [...(currentUser.value?.permissions || [])].sort()]));
const canEdit = computed(() => hasPerm("question:view") && hasPerm("question:edit") && !selectionOutsideList.value);
const canArrange = computed(() => canAccessQuestionArrangement(hasPerm));
const flowId = ref("");
const page = ref(1);
const total = ref(0);
const pageSize = 20;
const loading = ref(false);
const submitting = ref(false);
const saving = ref(false);
const toast = ref("");
const errorDialog = ref("");
const errorDialogTitle = ref("操作失败");
const detailQuestion = ref(null);
const editForm = ref({ clinical_stem: "", options: [], answer: "", explanation: "", change_reason: "" });

const pageCount = computed(() => Math.max(1, Math.ceil(total.value / pageSize)));
const allSelected = computed(() => questions.value.length > 0 && questions.value.every((q) => selectedIds.value.has(q.id)));
const selectedCount = computed(() => selectedIds.value.size);
const selectedDirty = computed(() => Boolean(selected.value && JSON.stringify(editPayload(editForm.value)) !== JSON.stringify(editPayload(selected.value))));
const submitDisabledReason = computed(() => {
  if (selectionStale.value) return "题目状态已变化，请放弃旧内容并刷新";
  if (submitting.value) return "正在提交审核，请稍候";
  if (saving.value) return "正在保存当前题目，请稍候";
  if (selectedDirty.value) return "当前题目有未保存修改，请先保存或撤销修改";
  if (!flowId.value) return "请先在右侧选择审核流程";
  return "";
});

function showToast(message) {
  toast.value = message;
  clearTimeout(showToast.timer);
  showToast.timer = setTimeout(() => { toast.value = ""; }, 3600);
}

async function loadQuestions(silent = false) {
  const ticket = ++questionTicket;
  const session = sessionKey.value;
  if (!canArrange.value) return;
  if (!silent) loading.value = true;
  try {
    const data = await api.myNewQuestions(page.value, pageSize,journeyParams(guideTask.value));
    if (disposed || ticket !== questionTicket || session !== sessionKey.value || !canArrange.value) return;
    verifyJourneyList(data,guideTask.value);
    total.value = data.total || 0;
    if (page.value > pageCount.value) {
      page.value = pageCount.value;
      return loadQuestions(silent);
    }
    questions.value = data.questions || [];
    selectedIds.value = new Set([...selectedIds.value].filter((id) => questions.value.some((q) => q.id === id)));
    if (selected.value) {
      const fresh = questions.value.find(q => q.id === selected.value.id);
      if (!fresh && !selectedDirty.value && learningPanel.value?.hasUnsavedWork?.()) {
        const retainedID=selected.value.id;
        selectionOutsideList.value=true;selectionStale.value=true;
        selectionOutsideNotice.value='当前源题已不在待整理列表，题目内容只读；学习候选编辑已保留，正在核对当前状态。';
        try {
          const owned=await api.getQuestion(retainedID,'personal');
          if (disposed || ticket!==questionTicket || session!==sessionKey.value || selected.value?.id!==retainedID) return;
          if (!owned || owned.owner_id!==currentUser.value?.id) { startEdit(null);selectionOutsideNotice.value='原题已不可访问，已清除该题与学习候选缓存。';return; }
          selected.value=owned;editForm.value={...editPayload(owned),change_reason:''};selectionStale.value=false;
          selectionOutsideNotice.value='当前源题已移出待整理列表，题目内容只读；已保留学习编辑，可重新读取版本后按当前资格继续，修改题目请到个人普通题库。';
        } catch (error) {
          if (disposed || ticket!==questionTicket || session!==sessionKey.value) return;
          if ([401,403,404].includes(error.status)) {startEdit(null);selectionOutsideNotice.value='原题已不可访问，已清除该题与学习候选缓存。';await refreshSessionIdentity().catch(()=>{});}
          else {selectionStale.value=true;selectionOutsideNotice.value='暂时无法核对列表外源题，已保留学习编辑供本人阅读；请稍后重试，暂不能保存或导入。';}
        }
        return;
      }
      const changed = !fresh || fresh.version !== selected.value.version || (fresh.review_revision || 0) !== (selected.value.review_revision || 0) || (fresh.library_revision || 0) !== (selected.value.library_revision || 0) || fresh.library_state !== selected.value.library_state;
      if (changed && selectedDirty.value) selectionStale.value = true;
      else if (!selectedDirty.value) startEdit(fresh || questions.value[0] || null);
    } else startEdit(questions.value[0] || null);
  } catch (error) {
    if (ticket === questionTicket && session===sessionKey.value && !error.staleSession) {
      if ([401,403].includes(error.status)) {questions.value=[];total.value=0;selectedIds.value=new Set();startEdit(null);await refreshSessionIdentity().catch(()=>{});}
      showToast("加载新题失败：" + error.message);
    }
  } finally {
    if (ticket === questionTicket) loading.value = false;
  }
}

async function loadFlows() {
  const ticket = ++flowTicket;
  const session = sessionKey.value;
  if (!canSubmitReview.value) return;
  try {
    const data = await api.availableReviewFlows();
    if (disposed || ticket !== flowTicket || session !== sessionKey.value || !canSubmitReview.value) return;
    flows.value = data.flows || [];
    if (flows.value.find(f => f.id === flowId.value)?.available === false) flowId.value = "";
    const valid = flows.value.filter(f => f.available !== false);
    if (!flowId.value && valid.length === 1) flowId.value = valid[0].id;
  } catch (error) {
    if (ticket === flowTicket && session === sessionKey.value && !error.staleSession) showToast("加载审核流程失败：" + error.message);
  }
}

function editPayload(question) {
  if (!question) return null;
  return {
    clinical_stem: question.clinical_stem || "",
    options: (question.options || []).map((option) => ({ label: option.label, text: option.text })),
    answer: question.answer || "",
    explanation: question.explanation || "",
  };
}

function startEdit(question) {
  selectionStale.value = false;
  selectionOutsideList.value=false;selectionOutsideNotice.value='';
  if (!question) {
    selected.value = null;
    return;
  }
  selected.value = question;
  editForm.value = { ...editPayload(question), change_reason: "" };
}

function selectQuestion(question) {
  if (saving.value || submitting.value) return;
  if (selected.value?.id !== question.id && learningPanel.value?.allowLeaving?.() === false) return;
  if (selectedDirty.value) {
    if (selected.value?.id !== question.id) showToast("当前题目有未保存修改，请先保存或撤销后再切换");
    return;
  }
  startEdit(question);
}

function toggleQuestion(question, event) {
  const restoreCheckbox = () => { if (event?.target) event.target.checked = selectedIds.value.has(question.id); };
  if (saving.value || submitting.value) { restoreCheckbox(); return; }
  if (selected.value?.id !== question.id && learningPanel.value?.allowLeaving?.() === false) { restoreCheckbox(); return; }
  if (selectedDirty.value && selected.value?.id !== question.id) {
    restoreCheckbox();
    showToast("当前题目有未保存修改，请先保存或撤销后再切换");
    return;
  }
  if (selected.value?.id !== question.id) startEdit(question);
  const next = new Set(selectedIds.value);
  if (next.has(question.id)) next.delete(question.id);
  else next.add(question.id);
  selectedIds.value = next;
}

async function saveDraft() {
  if (!canEdit.value || !selected.value || !selectedDirty.value || selectionStale.value || saving.value || submitting.value) return;
  saving.value = true;
  const session = sessionKey.value;
  const question = selected.value;
  ++questionTicket;
  try {
    const data = await api.updateQuestion(question.id, {
      ...editPayload(editForm.value),
      ...reviewExpectation(question),
      expected_library_revision: question.library_revision || 0,
      change_reason: editForm.value.change_reason || "题目整理时微调",
    });
    if (disposed || session !== sessionKey.value || !canEdit.value || selected.value?.id !== question.id) return;
    const index = questions.value.findIndex((question) => question.id === data.id);
    if (index >= 0) questions.value[index] = data;
    startEdit(data);
    showToast(`已保存为 v${data.version}，不会触发 AI 复检`);
  } catch (error) {
    if (disposed || session !== sessionKey.value || error.staleSession) return;
    if (error.status === 409) selectionStale.value = true;
    errorDialogTitle.value = "保存失败";
    errorDialog.value = error.message || "保存微调失败，请稍后重试";
  } finally {
    if (session === sessionKey.value) saving.value = false;
  }
}

function toggleAll() {
  if (saving.value || submitting.value) return;
  const next = new Set(selectedIds.value);
  if (allSelected.value) questions.value.forEach((q) => next.delete(q.id));
  else questions.value.forEach((q) => next.add(q.id));
  selectedIds.value = next;
}

function goPage(nextPage) {
  if (loading.value || saving.value || submitting.value || nextPage < 1 || nextPage > pageCount.value || nextPage === page.value) return;
  if (selectedDirty.value) {
    showToast("当前题目有未保存修改，请先保存或撤销后再翻页");
    return;
  }
  if (learningPanel.value?.allowLeaving?.() === false) return;
  page.value = nextPage;
  selected.value = null;
  selectedIds.value = new Set();
  loadQuestions();
  document.querySelector(".new-question-list")?.scrollTo({ top: 0 });
}

const canConfirm = computed(() => canArrange.value && hasPerm("question:confirm"));
const canSubmitReview = computed(() => questionReviewUi.enabled() && canArrange.value && hasPerm("review:submit"));

// 确认入库（0 轮）：本人对当前保存版本的采纳动作，不创建审核任务。
async function confirmLibrary(ids) {
  if (!canConfirm.value || submitting.value || saving.value || selectionStale.value) return;
  if (selectionOutsideList.value && ids.includes(selected.value?.id)) return showToast('列表外题目仅保留学习编辑；请到个人普通题库确认当前状态');
  if (ids.includes(selected.value?.id) && learningPanel.value?.allowLeaving?.() === false) return;
  if (!ids.length) {
    showToast("请先选择至少一道题");
    return;
  }
  if (selectedDirty.value && ids.includes(selected.value?.id)) {
    showToast("当前题目有未保存修改，请先保存或撤销修改");
    return;
  }
  const items = questions.value
    .filter((q) => ids.includes(q.id))
    .map(libraryExpectation);
  if (items.length !== new Set(ids).size) return showToast("选择已变化，请刷新后重新选择");
  if (items.length > 100) return showToast("每次最多确认 100 道题");
  if (!confirm(`确定将 ${items.length} 道题确认入库吗？确认后进入个人普通题库，可导出；本页不再显示。`)) return;
  submitting.value = true;
  const session = sessionKey.value;
  ++questionTicket;
  try {
    const result = await api.confirmQuestions(items);
    if (disposed || session !== sessionKey.value || !canConfirm.value) return;
    const done = new Set(ids);
    selectedIds.value = new Set([...selectedIds.value].filter((id) => !done.has(id)));
    if (selected.value && done.has(selected.value.id)) startEdit(null);
    confirmedNotice.value+=result.confirmed||items.length;
    showToast(`已确认入库 ${result.confirmed || items.length} 道题`);
    await loadQuestions();
    notifyNavigationWorkChanged();
  } catch (error) {
    if (disposed || session !== sessionKey.value || error.staleSession) return;
    if (error.status === 409) selectionStale.value = true;
    showToast("确认入库失败：" + error.message);
  } finally {
    if (session === sessionKey.value) submitting.value = false;
  }
}

async function submit(ids) {
  if (!canSubmitReview.value) return;
  if (selectionStale.value) return showToast("题目状态已变化，请放弃旧内容并刷新");
  if (selectionOutsideList.value && ids.includes(selected.value?.id)) return showToast('列表外题目不能从待整理列表提交');
  if (ids.includes(selected.value?.id) && learningPanel.value?.allowLeaving?.() === false) return;
  if (saving.value) {
    showToast("当前题目正在保存，请保存完成后再提交审核");
    return;
  }
  if (submitting.value) return;
  if (!ids.length) {
    showToast("请先选择至少一道新题");
    return;
  }
  if (!flowId.value) {
    showToast("请先选择审核流程");
    return;
  }
  if (selectedDirty.value) {
    showToast("当前题目有未保存修改，请先保存或撤销修改");
    return;
  }
  const flow = flows.value.find((item) => item.id === flowId.value);
  if (!confirm(`确定将 ${ids.length} 道题提交到“${flow?.name || flowId.value}”吗？提交后将进入审核流程，本页不再显示。`)) return;
  submitting.value = true;
  const session = sessionKey.value;
  ++questionTicket;
  try {
    const result = await api.submitReviewBatch(ids, flowId.value, reviewExpectations(questions.value.filter(q => ids.includes(q.id))));
    if (disposed || session !== sessionKey.value || !canSubmitReview.value) return;
    const failedIDs = new Set((result.failed || []).map((item) => item.question_id));
    const submittedIDs = new Set(ids.filter((id) => !failedIDs.has(id)));
    selectedIds.value = new Set([...selectedIds.value].filter((id) => !submittedIDs.has(id)));
    if (selected.value && submittedIDs.has(selected.value.id)) startEdit(null);
    if (result.failed?.length) {
      showToast(`已提交 ${result.submitted} 道，${result.failed.length} 道未提交，请查看列表状态后重试`);
	  errorDialogTitle.value = "部分题目未提交";
	  errorDialog.value = result.failed.map((item) => `${item.question_id}：${item.error || "未知原因"}`).join("\n");
    } else {
      showToast(`已提交 ${result.submitted} 道题进入审核流程`);
    }
    await loadQuestions();
    notifyNavigationWorkChanged();
  } catch (error) {
    if (session === sessionKey.value && !error.staleSession) showToast("提交审核失败：" + error.message);
  } finally {
    if (session === sessionKey.value) submitting.value = false;
  }
}

function difficultyText(value) {
  const map = { easy: "简单", medium: "中等", hard: "困难" };
  return map[value] || value || "-";
}

function warnBeforeUnload(event) {
  if (!selectedDirty.value) return;
  event.preventDefault();
  event.returnValue = "";
}

onBeforeRouteUpdate((to,from)=>{if(to.query.task_id===from.query.task_id&&to.query.task_kind===from.query.task_kind)return true;if(saving.value||submitting.value)return false;return !selectedDirty.value||confirm("当前题目有未保存修改，确定切换任务并放弃这些修改吗？");});
watch(()=>[guideRoute.query.task_kind,guideRoute.query.task_id],()=>{page.value=1;selectedIds.value=new Set();startEdit(null);confirmedNotice.value=0;loadQuestions();});
onBeforeRouteLeave(() => {
  if (!isLoggedIn.value) return true;
  if (!selectedDirty.value) return true;
  return confirm("当前题目有未保存修改，确定离开并放弃这些修改吗？");
});

watch(sessionKey, () => {
  ++questionTicket; ++flowTicket;
  questions.value = []; total.value = 0; page.value = 1;
  selectedIds.value = new Set(); startEdit(null); detailQuestion.value = null;
  flows.value = []; flowId.value = ""; saving.value = false; submitting.value = false;
  confirmedNotice.value=0;errorDialog.value = ""; toast.value = ""; loading.value = false;
  if (canArrange.value) { loadQuestions(); loadFlows(); }
}, { flush: "sync" });

onMounted(() => {
  window.addEventListener("beforeunload", warnBeforeUnload);
  loadQuestions();
  loadFlows();
});
useLiveRefresh(async () => { if (!saving.value && !submitting.value) await Promise.allSettled([loadQuestions(true), loadFlows()]); });
onBeforeUnmount(() => { disposed = true; ++questionTicket; ++flowTicket; clearTimeout(showToast.timer); window.removeEventListener("beforeunload", warnBeforeUnload); });
</script>

<template>
 <section v-if="confirmedNotice" class="authoring-confirmed" role="status"><div><strong>已确认 {{confirmedNotice}} 题，已放入个人题库</strong><span>可继续整理其余题目，或前往题库导出。</span></div><RouterLink class="primary-button" :to="{path:'/bank',query:{scope:'personal'}}">去个人题库导出 →</RouterLink></section>
  <section class="new-question-workspace">
    <div class="panel workspace-intro">
      <div>
        <div class="section-heading"><span class="dot blue"></span><h2>题目整理</h2><small>{{ total }} 道待处理</small>
        <HelpHint label="题目整理说明">这里集中整理本账号 AI 检查通过的新题：微调保存后确认入个人普通题库并导出。<template v-if="questionReviewUi.enabled()">如需人工审核，再选择审核流程提交。</template></HelpHint></div>
      </div>
      <div class="workspace-actions">
        <span v-if="submitDisabledReason && canSubmitReview" class="submit-hint">{{ submitDisabledReason }}</span>
        <button
          v-if="canConfirm"
          class="primary-button batch-submit-button"
          type="button"
          :disabled="submitting || saving || selectionStale || !selectedCount || (selectedDirty && selectedIds.has(selected?.id))"
          :title="`确认已选 ${selectedCount} 道题入库`"
          @click="confirmLibrary([...selectedIds])"
        >
          {{ submitting ? "处理中..." : `确认入库${selectedCount ? `（${selectedCount}）` : ""}` }}
        </button>
        <button
          v-if="canSubmitReview"
          class="primary-button secondary batch-submit-button"
          type="button"
          :disabled="submitting || saving || selectionStale || !selectedCount || selectedDirty || !flowId"
          :title="submitDisabledReason || `提交已选 ${selectedCount} 道题审核`"
          @click="submit([...selectedIds])"
        >
          {{ submitting ? "提交中..." : `批量提交审核${selectedCount ? `（${selectedCount}）` : ""}` }}
        </button>
      </div>
    </div>

    <AuthorReviewProgress v-if="canSubmitReview" @changed="loadQuestions(true);notifyNavigationWorkChanged()" />
    <div v-if="selectionOutsideNotice" class="panel" role="status">{{selectionOutsideNotice}}</div>
    <div v-if="selectionStale" class="panel" role="alert">题目已被其他操作改变，当前输入内容已保留，但不能继续保存或确认<template v-if="questionReviewUi.enabled()">，也不能送审</template>。<button class="ghost-button" @click="startEdit(null);loadQuestions()">放弃旧内容并刷新</button></div>
    <div class="new-question-layout">
      <section class="panel new-question-list-panel">
        <div class="list-toolbar">
          <label class="select-all"><input type="checkbox" :checked="allSelected" :disabled="saving || submitting" @change="toggleAll" /> 全选本页</label>
          <span class="selected-hint">已选 {{ selectedCount }} 道</span>
        </div>
        <div v-if="loading" class="loading">加载中...</div>
        <div v-else-if="!questions.length" class="empty">暂无待整理的题目<HelpHint label="待整理题目说明">AI 检查通过后，新题会出现在这里；确认入库后的题在题库「普通」页签。</HelpHint></div>
        <div v-else class="new-question-list">
          <article
            v-for="question in questions"
            :key="question.id"
            class="new-question-card"
            :class="{ active: selected?.id === question.id, checked: selectedIds.has(question.id) }"
            @click="selectQuestion(question)"
          >
            <input type="checkbox" :checked="selectedIds.has(question.id)" :disabled="saving || submitting" @click.stop @change="toggleQuestion(question, $event)" />
            <div class="new-question-card-body">
              <strong>{{ question.clinical_stem || "（题干为空）" }}</strong>
              <span>{{ question.profession || "未填写专业" }} · {{ difficultyText(question.difficulty) }}<b v-if="selected?.id === question.id && selectedDirty" class="unsaved-tag">未保存</b></span>
            </div>
          </article>
        </div>
        <CompactPager v-if="total" :page="page" :total-pages="pageCount" :disabled="loading" @change="goPage" />
      </section>

      <section class="panel new-question-detail" v-if="selected">
        <div class="section-heading"><span class="dot blue"></span><h2>新题微调</h2><button class="ghost-button" type="button" @click="detailQuestion = selected">查看属性</button></div>
        <div class="edit-note"><strong>初始版已通过 AI 首检</strong><HelpHint label="题目修订说明">{{ selected.version > 1 ? "当前版本经人工修改；" : "" }}有编辑权限时可微调；仅内容变化才生成新版本，不会再次触发 AI 检查。</HelpHint></div>
        <div class="version-strip" :class="{ dirty: selectedDirty }" role="status">
          <span>当前版本 <strong>v{{ selected.version }}</strong></span>
          <span v-if="selectedDirty" class="version-arrow">→ 待保存的新版本</span>
          <em v-if="selectedDirty">保存生成新版本；确认入库前请先保存<template v-if="questionReviewUi.enabled()">，或选择送审</template></em>
        </div>
        <QuestionLearningPanel ref="learningPanel" :question="selected" :dirty="selectedDirty || selectionStale || saving || submitting" />
        <PersonalExperienceProvenance :question="selected" />
        <div class="edit-field">
          <label>题干 *</label>
          <textarea v-model="editForm.clinical_stem" class="edit-textarea" :disabled="!canEdit || selectionStale || saving || submitting"></textarea>
        </div>
        <div class="edit-field">
          <label>选项 *</label>
          <div v-for="(option, index) in editForm.options" :key="index" class="edit-option-row">
            <span class="opt-label">{{ option.label }}</span>
            <input v-model="option.text" class="edit-input" :disabled="!canEdit || selectionStale || saving || submitting" />
          </div>
        </div>
        <div class="edit-field inline-edit-field">
          <label>正确答案 *</label>
          <select v-model="editForm.answer" class="edit-select" :disabled="!canEdit || selectionStale || saving || submitting">
            <option v-for="option in editForm.options" :key="option.label" :value="option.label">{{ option.label }}</option>
          </select>
        </div>
        <div class="edit-field">
          <label>解析</label>
          <textarea v-model="editForm.explanation" class="edit-textarea" :disabled="!canEdit || selectionStale || saving || submitting"></textarea>
        </div>
        <div class="edit-field">
          <label>微调说明（可选）</label>
          <input v-model="editForm.change_reason" class="edit-input" placeholder="简述提交前做了哪些微调" :disabled="!canEdit || selectionStale || saving || submitting" />
        </div>
        <div class="edit-actions">
          <div class="edit-actions-main">
            <button class="primary-button" type="button" :disabled="!canEdit || selectionStale || saving || submitting || !selectedDirty" @click="saveDraft">{{ saving ? "保存中..." : "保存微调" }}</button>
            <button
              v-if="canConfirm"
              class="primary-button confirm-current-button"
              type="button"
              :disabled="selectionOutsideList || selectionStale || submitting || saving || selectedDirty"
              title="确认当前保存版本入个人普通题库"
              @click="confirmLibrary([selected.id])"
            >
              确认入库
            </button>
            <button
              v-if="canSubmitReview"
              class="primary-button secondary submit-current-button"
              type="button"
              :disabled="selectionOutsideList || selectionStale || submitting || saving || !flowId || selectedDirty"
              :title="submitDisabledReason || '提交当前题进入审核流程'"
              @click="submit([selected.id])"
            >
              {{ submitting ? "提交中..." : "提交当前题" }}
            </button>
          </div>
          <button class="ghost-button" type="button" :disabled="selectionStale || saving || submitting || !selectedDirty" @click="startEdit(selected)">撤销未保存修改</button>
        </div>
        <h3 v-if="canSubmitReview">提交审核（可选）</h3>
        <div v-if="canSubmitReview" class="submit-box">
          <label>审核流程</label>
          <select v-model="flowId" :disabled="saving || submitting">
            <option value="">请选择管理员配置的流程</option>
            <option v-for="flow in flows" :key="flow.id" :value="flow.id" :disabled="flow.available === false">{{ flow.name }}{{ flow.available === false ? "（暂不可用）" : "" }} · {{ reviewCompletionLabel(flow, flow.round_count) }}</option>
          </select>
          <small v-if="flows.length === 0">暂无可用流程，请联系管理员先配置审核流程。</small>

        </div>
      </section>
      <section v-else class="panel new-question-detail empty">请选择左侧新题<span class="empty-hint">右侧可预览、修订并确认入库<template v-if="questionReviewUi.enabled()">，也可选择送审</template></span></section>
    </div>
  </section>

  <QuestionDetailModal v-if="detailQuestion" :question="detailQuestion" @close="detailQuestion = null" />
  <div v-if="errorDialog" class="error-overlay" role="alertdialog" aria-modal="true" aria-labelledby="new-question-error-title">
    <div class="error-modal">
      <h3 id="new-question-error-title">{{ errorDialogTitle }}</h3>
      <p>{{ errorDialog }}</p>
      <button class="primary-button" type="button" @click="errorDialog = ''">知道了</button>
    </div>
  </div>
  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
</template>

<style scoped>
.new-question-workspace { max-width: 1500px; min-width: 0; }
.new-question-layout > .panel { min-width: 0; }
.workspace-intro { display: flex; justify-content: space-between; align-items: center; gap: 18px; margin-bottom: 14px; }
.workspace-intro .section-heading { margin-bottom: 4px; }
.intro-copy { margin: 0; color: #6e7b8f; font-size: 13px; line-height: 1.6; }
.workspace-actions { display: flex; align-items: center; justify-content: flex-end; gap: 10px; margin-left: auto; }
.batch-submit-button { min-width: 132px; white-space: nowrap; }
.submit-hint { max-width: 210px; color: #9a6a24; font-size: 11px; line-height: 1.45; text-align: right; }
.new-question-layout { display: grid; grid-template-columns: minmax(300px, .85fr) minmax(0, 1.15fr); gap: 14px; align-items: start; }
.new-question-list-panel { min-height: 560px; }
.list-toolbar { display: flex; align-items: center; justify-content: space-between; padding-bottom: 12px; border-bottom: 1px solid #edf1f6; color: #52647a; font-size: 12px; }
.select-all { display: inline-flex; align-items: center; gap: 6px; cursor: pointer; }
.selected-hint { color: #1385f8; font-weight: 600; }
.new-question-list { overflow-y: auto; max-height: 620px; padding-top: 8px; }
.new-question-card { display: flex; gap: 10px; align-items: flex-start; padding: 12px 10px; border-bottom: 1px solid #edf1f6; cursor: pointer; }
.new-question-card:hover, .new-question-card.active { background: #f5faff; }
.new-question-card.checked { border-left: 3px solid #1385f8; padding-left: 7px; }
.new-question-card-body { min-width: 0; display: grid; gap: 7px; }
.new-question-card-body strong { color: #172033; font-size: 13px; line-height: 1.55; font-weight: 600; }
.new-question-card-body span { color: #8a97a8; font-size: 11px; }
.unsaved-tag { margin-left: 6px; color: #b56f1f; font-weight: 700; }
.new-question-detail { min-height: 560px; }
.new-question-detail h3 { margin: 20px 0 8px; color: #52647a; font-size: 12px; }
.stem { margin: 0; color: #1e2a3d; line-height: 1.8; font-size: 14px; white-space: pre-wrap; }
.edit-note { display: grid; gap: 3px; padding: 10px 12px; border-radius: 7px; background: #eef8f4; border: 1px solid #c6e7dc; color: #49665b; font-size: 12px; line-height: 1.6; }
.edit-note strong { color: #087c55; }
.version-strip { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; margin: 12px 0; padding: 8px 10px; border: 1px solid #e5ebf3; border-radius: 7px; background: #f7f9fc; color: #64758a; font-size: 12px; }
.version-strip strong { color: #172033; }
.version-strip.dirty { border-color: #f3d9b0; background: #fff8ec; }
.version-strip em { color: #b56f1f; font-style: normal; }
.edit-field { margin: 0 0 12px; }
.edit-field label { display: block; margin-bottom: 5px; color: #52647a; font-size: 12px; font-weight: 650; }
.edit-textarea { width: 100%; min-height: 92px; box-sizing: border-box; border: 1px solid #dce8f7; border-radius: 7px; padding: 9px 10px; color: #1e2a3d; font: inherit; font-size: 13px; line-height: 1.65; resize: vertical; }
.edit-input, .edit-select { width: 100%; min-width:0; height: 36px; box-sizing: border-box; border: 1px solid #dce8f7; border-radius: 7px; padding: 0 10px; color: #1e2a3d; font: inherit; font-size: 13px; background: #fff; }
.edit-option-row { display: flex; align-items: center; gap: 8px; margin: 6px 0; }
.edit-option-row .edit-input { flex: 1; }
.opt-label { display: grid; width: 25px; height: 25px; place-items: center; flex: 0 0 25px; border-radius: 50%; background: #eaf5ff; color: #1385f8; font-size: 12px; font-weight: 700; }
.inline-edit-field { display: flex; align-items: center; gap: 12px; }
.inline-edit-field label { margin: 0; }
.inline-edit-field .edit-select { width: 100px; }
.edit-actions { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin: 2px 0 18px; }
.edit-actions-main { display: flex; align-items: center; gap: 8px; }
.submit-current-button { min-width: 104px; }
.error-overlay { position: fixed; inset: 0; z-index: 1000; display: grid; place-items: center; background: rgba(15, 23, 42, .45); }
.error-modal { width: min(500px, calc(100vw - 40px)); padding: 22px; border-radius: 12px; background: #fff; box-shadow: 0 18px 48px rgba(15, 23, 42, .25); }
.error-modal h3 { margin: 0 0 10px; color: #c54858; font-size: 16px; }
.error-modal p { margin: 0 0 16px; color: #53647a; line-height: 1.6; white-space: pre-wrap; }
.option-row { display: flex; align-items: flex-start; gap: 10px; padding: 9px 10px; border: 1px solid #edf1f6; border-radius: 6px; margin: 6px 0; color: #3a4658; font-size: 13px; line-height: 1.5; }
.option-row b { color: #1385f8; }
.option-row em { margin-left: auto; color: #087c55; font-size: 11px; font-style: normal; white-space: nowrap; }
.option-row.correct { border-color: #bdecd9; background: #f4fff9; }
.submit-box { display: grid; gap: 9px; padding: 14px; border: 1px solid #dce8f7; border-radius: 8px; background: #f8fbff; }
.submit-box label { color: #52647a; font-size: 12px; font-weight: 600; }
.submit-box select { width:100%; max-width:100%; min-width:0; height: 38px; border: 1px solid #dce8f7; border-radius: 6px; padding: 0 10px; background: #fff; color: #24344b; }
.submit-box small { color: #9a6a24; line-height: 1.5; }
.primary-button.secondary { background: #fff; color: #1385f8; border: 1px solid #9dccf5; }
.empty { display: grid; place-items: center; align-content: center; gap: 6px; color: #7d8a9b; min-height: 260px; }
.empty-hint { color: #a3adba; font-size: 12px; }
.loading { padding: 40px 0; text-align: center; color: #7d8a9b; }
@media (max-width: 900px) { .new-question-layout { grid-template-columns: minmax(0, 1fr); } .workspace-intro { align-items: flex-start; flex-direction: column; } .workspace-actions { width: 100%; flex-wrap:wrap; justify-content: space-between; margin-left: 0; } .submit-hint { max-width: none; text-align: left; } .new-question-list { max-height: 360px; } }
@media (max-width: 600px) { .edit-actions { align-items: stretch; flex-direction: column; } .edit-actions-main { width: 100%; } .edit-actions-main > button { flex: 1; } .edit-actions > .ghost-button { align-self: flex-end; } }
</style>
