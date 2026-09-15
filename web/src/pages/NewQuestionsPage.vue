<script setup>
import { ref, computed, onMounted } from "vue";
import { api } from "../api.js";
import CompactPager from "../components/CompactPager.vue";
import QuestionDetailModal from "../components/QuestionDetailModal.vue";

const questions = ref([]);
const flows = ref([]);
const selectedIds = ref(new Set());
const selected = ref(null);
const flowId = ref("");
const page = ref(1);
const total = ref(0);
const pageSize = 20;
const loading = ref(false);
const submitting = ref(false);
const saving = ref(false);
const toast = ref("");
const errorDialog = ref("");
const detailQuestion = ref(null);
const editForm = ref({ clinical_stem: "", options: [], answer: "", explanation: "", change_reason: "" });

const pageCount = computed(() => Math.max(1, Math.ceil(total.value / pageSize)));
const allSelected = computed(() => questions.value.length > 0 && questions.value.every((q) => selectedIds.value.has(q.id)));
const selectedCount = computed(() => selectedIds.value.size);
const selectedDirty = computed(() => selected.value && JSON.stringify(editPayload(editForm.value)) !== JSON.stringify(editPayload(selected.value)));

function showToast(message) {
  toast.value = message;
  clearTimeout(showToast.timer);
  showToast.timer = setTimeout(() => { toast.value = ""; }, 3600);
}

async function loadQuestions() {
  loading.value = true;
  try {
    const data = await api.myNewQuestions(page.value, pageSize);
    total.value = data.total || 0;
    if (page.value > pageCount.value) {
      page.value = pageCount.value;
      return loadQuestions();
    }
    questions.value = data.questions || [];
    selectedIds.value = new Set([...selectedIds.value].filter((id) => questions.value.some((q) => q.id === id)));
    if (selected.value && !questions.value.some((q) => q.id === selected.value.id)) startEdit(questions.value[0] || null);
    if (!selected.value) startEdit(questions.value[0] || null);
  } catch (error) {
    showToast("加载新题失败：" + error.message);
  } finally {
    loading.value = false;
  }
}

async function loadFlows() {
  try {
    const data = await api.availableReviewFlows();
    flows.value = data.flows || [];
    if (!flowId.value && flows.value.length === 1) flowId.value = flows.value[0].id;
  } catch (error) {
    showToast("加载审核流程失败：" + error.message);
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
  if (!question) {
    selected.value = null;
    return;
  }
  selected.value = question;
  editForm.value = { ...editPayload(question), change_reason: "" };
}

function toggleQuestion(question) {
  startEdit(question);
  const next = new Set(selectedIds.value);
  if (next.has(question.id)) next.delete(question.id);
  else next.add(question.id);
  selectedIds.value = next;
}

async function saveDraft() {
  if (!selected.value || !selectedDirty.value || saving.value) return;
  saving.value = true;
  try {
    const data = await api.updateQuestion(selected.value.id, {
      ...editPayload(editForm.value),
      change_reason: editForm.value.change_reason || "提交审核前微调",
    });
    const index = questions.value.findIndex((question) => question.id === data.id);
    if (index >= 0) questions.value[index] = data;
    startEdit(data);
    showToast(`已保存为 v${data.version}，不会触发 AI 复检`);
  } catch (error) {
    errorDialog.value = error.message || "保存微调失败，请稍后重试";
  } finally {
    saving.value = false;
  }
}

function toggleAll() {
  const next = new Set(selectedIds.value);
  if (allSelected.value) questions.value.forEach((q) => next.delete(q.id));
  else questions.value.forEach((q) => next.add(q.id));
  selectedIds.value = next;
}

function goPage(nextPage) {
  if (loading.value || nextPage < 1 || nextPage > pageCount.value || nextPage === page.value) return;
  page.value = nextPage;
  selected.value = null;
  selectedIds.value = new Set();
  loadQuestions();
  document.querySelector(".new-question-list")?.scrollTo({ top: 0 });
}

async function submit(ids) {
  if (!ids.length) {
    showToast("请先选择至少一道新题");
    return;
  }
  if (!flowId.value) {
    showToast("请先选择审核流程");
    return;
  }
  if (selectedDirty.value && ids.includes(selected.value?.id)) {
    showToast("当前题目有未保存修改，请先保存微调");
    return;
  }
  const flow = flows.value.find((item) => item.id === flowId.value);
  if (!confirm(`确定将 ${ids.length} 道题提交到“${flow?.name || flowId.value}”吗？提交后将进入审核流程，本页不再显示。`)) return;
  if (submitting.value) return;
  submitting.value = true;
  try {
    const result = await api.submitReviewBatch(ids, flowId.value);
    const failedIDs = new Set((result.failed || []).map((item) => item.question_id));
    const submittedIDs = new Set(ids.filter((id) => !failedIDs.has(id)));
    selectedIds.value = new Set([...selectedIds.value].filter((id) => !submittedIDs.has(id)));
    if (selected.value && submittedIDs.has(selected.value.id)) startEdit(null);
    if (result.failed?.length) {
      showToast(`已提交 ${result.submitted} 道，${result.failed.length} 道未提交，请查看列表状态后重试`);
    } else {
      showToast(`已提交 ${result.submitted} 道题进入审核流程`);
    }
    await loadQuestions();
  } catch (error) {
    showToast("提交审核失败：" + error.message);
  } finally {
    submitting.value = false;
  }
}

function difficultyText(value) {
  const map = { easy: "简单", medium: "中等", hard: "困难" };
  return map[value] || value || "-";
}

onMounted(() => {
  loadQuestions();
  loadFlows();
});
</script>

<template>
  <section class="new-question-workspace">
    <div class="panel workspace-intro">
      <div>
        <div class="section-heading"><span class="dot blue"></span><h2>新题修改与提交审核</h2><small>{{ total }} 道待提交</small></div>
        <p class="intro-copy">这里集中处理本账号刚生成且 AI 检查通过的新题。提交后由管理员配置的审核流程继续处理。</p>
      </div>
      <RouterLink class="ghost-button" to="/my-revisions">打开待我修改</RouterLink>
    </div>

    <div class="new-question-layout">
      <section class="panel new-question-list-panel">
        <div class="list-toolbar">
          <label class="select-all"><input type="checkbox" :checked="allSelected" @change="toggleAll" /> 全选本页</label>
          <span class="selected-hint">已选 {{ selectedCount }} 道</span>
        </div>
        <div v-if="loading" class="loading">加载中...</div>
        <div v-else-if="!questions.length" class="empty">暂无待提交的新题<span class="empty-hint">AI 检查通过后，新题会出现在这里</span></div>
        <div v-else class="new-question-list">
          <article
            v-for="question in questions"
            :key="question.id"
            class="new-question-card"
            :class="{ active: selected?.id === question.id, checked: selectedIds.has(question.id) }"
            @click="startEdit(question)"
          >
            <input type="checkbox" :checked="selectedIds.has(question.id)" @click.stop @change="toggleQuestion(question)" />
            <div class="new-question-card-body">
              <strong>{{ question.clinical_stem || "（题干为空）" }}</strong>
              <span>{{ question.profession || "未填写专业" }} · {{ difficultyText(question.difficulty) }} · v{{ question.version }}<b v-if="selected?.id === question.id && selectedDirty" class="unsaved-tag">未保存</b></span>
            </div>
          </article>
        </div>
        <CompactPager v-if="total" :page="page" :total-pages="pageCount" :disabled="loading" @change="goPage" />
      </section>

      <section class="panel new-question-detail" v-if="selected">
        <div class="section-heading"><span class="dot blue"></span><h2>新题微调</h2><button class="ghost-button" type="button" @click="detailQuestion = selected">查看属性</button></div>
        <div class="edit-note"><strong>AI 检查已通过</strong><span>这里允许出题老师提交前微调；保存会生成新版本，但不会再次触发 AI 检查。</span></div>
        <div class="version-strip" :class="{ dirty: selectedDirty }" role="status">
          <span>当前版本 <strong>v{{ selected.version }}</strong></span>
          <span v-if="selectedDirty" class="version-arrow">→ 待保存的新版本</span>
          <em v-if="selectedDirty">保存后直接沿用人工审核流程</em>
        </div>
        <div class="edit-field">
          <label>题干 *</label>
          <textarea v-model="editForm.clinical_stem" class="edit-textarea"></textarea>
        </div>
        <div class="edit-field">
          <label>选项 *</label>
          <div v-for="(option, index) in editForm.options" :key="index" class="edit-option-row">
            <span class="opt-label">{{ option.label }}</span>
            <input v-model="option.text" class="edit-input" />
          </div>
        </div>
        <div class="edit-field inline-edit-field">
          <label>正确答案 *</label>
          <select v-model="editForm.answer" class="edit-select">
            <option v-for="option in editForm.options" :key="option.label" :value="option.label">{{ option.label }}</option>
          </select>
        </div>
        <div class="edit-field">
          <label>解析</label>
          <textarea v-model="editForm.explanation" class="edit-textarea"></textarea>
        </div>
        <div class="edit-field">
          <label>微调说明（可选）</label>
          <input v-model="editForm.change_reason" class="edit-input" placeholder="简述提交前做了哪些微调" />
        </div>
        <div class="edit-actions">
          <button class="primary-button" type="button" :disabled="saving || !selectedDirty" @click="saveDraft">{{ saving ? "保存中..." : "保存微调" }}</button>
          <button class="ghost-button" type="button" :disabled="saving || !selectedDirty" @click="startEdit(selected)">撤销未保存修改</button>
        </div>
        <h3>提交审核</h3>
        <div class="submit-box">
          <label>审核流程</label>
          <select v-model="flowId">
            <option value="">请选择管理员配置的流程</option>
            <option v-for="flow in flows" :key="flow.id" :value="flow.id">{{ flow.name }} · {{ flow.round_count }} 轮</option>
          </select>
          <small v-if="flows.length === 0">暂无可用流程，请联系管理员先配置审核流程。</small>
          <button class="primary-button" type="button" :disabled="submitting || !flowId" @click="submit([selected.id])">提交当前题</button>
          <button class="primary-button secondary" type="button" :disabled="submitting || !flowId || !selectedCount" @click="submit([...selectedIds])">提交已选 {{ selectedCount }} 道</button>
        </div>
      </section>
      <section v-else class="panel new-question-detail empty">请选择左侧新题<span class="empty-hint">右侧会显示题目预览与送审操作</span></section>
    </div>
  </section>

  <QuestionDetailModal v-if="detailQuestion" :question="detailQuestion" @close="detailQuestion = null" />
  <div v-if="errorDialog" class="error-overlay" role="alertdialog" aria-modal="true" aria-labelledby="new-question-error-title">
    <div class="error-modal">
      <h3 id="new-question-error-title">保存失败</h3>
      <p>{{ errorDialog }}</p>
      <button class="primary-button" type="button" @click="errorDialog = ''">知道了，继续修改</button>
    </div>
  </div>
  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
</template>

<style scoped>
.new-question-workspace { max-width: 1500px; }
.workspace-intro { display: flex; justify-content: space-between; align-items: center; gap: 18px; margin-bottom: 14px; }
.workspace-intro .section-heading { margin-bottom: 4px; }
.intro-copy { margin: 0; color: #6e7b8f; font-size: 13px; line-height: 1.6; }
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
.edit-input, .edit-select { width: 100%; height: 36px; box-sizing: border-box; border: 1px solid #dce8f7; border-radius: 7px; padding: 0 10px; color: #1e2a3d; font: inherit; font-size: 13px; background: #fff; }
.edit-option-row { display: flex; align-items: center; gap: 8px; margin: 6px 0; }
.edit-option-row .edit-input { flex: 1; }
.opt-label { display: grid; width: 25px; height: 25px; place-items: center; flex: 0 0 25px; border-radius: 50%; background: #eaf5ff; color: #1385f8; font-size: 12px; font-weight: 700; }
.inline-edit-field { display: flex; align-items: center; gap: 12px; }
.inline-edit-field label { margin: 0; }
.inline-edit-field .edit-select { width: 100px; }
.edit-actions { display: flex; justify-content: flex-end; gap: 8px; margin: 2px 0 18px; }
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
.submit-box select { height: 38px; border: 1px solid #dce8f7; border-radius: 6px; padding: 0 10px; background: #fff; color: #24344b; }
.submit-box small { color: #9a6a24; line-height: 1.5; }
.primary-button.secondary { background: #fff; color: #1385f8; border: 1px solid #9dccf5; }
.empty { display: grid; place-items: center; align-content: center; gap: 6px; color: #7d8a9b; min-height: 260px; }
.empty-hint { color: #a3adba; font-size: 12px; }
.loading { padding: 40px 0; text-align: center; color: #7d8a9b; }
@media (max-width: 900px) { .new-question-layout { grid-template-columns: 1fr; } .workspace-intro { align-items: flex-start; flex-direction: column; } .new-question-list { max-height: 360px; } }
</style>
