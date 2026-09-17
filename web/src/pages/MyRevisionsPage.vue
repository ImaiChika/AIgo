<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from "vue";
import { onBeforeRouteLeave } from "vue-router";
import { api } from "../api.js";
import AICheckScoreButton from "../components/AICheckScoreButton.vue";

const toast = ref("");
const items = ref([]);
const loading = ref(false);
const selected = ref(null); // 当前编辑的条目
const editForm = ref({ clinical_stem: "", options: [], answer: "", explanation: "", change_reason: "" });
const saving = ref(false);
const submitting = ref(false);
const selectedIds = ref(new Set());
const statusFilter = ref(""); // ""=全部 / pending=待修改 / done=已提交修改
// 保存被送审格式校验拒绝时的明确弹窗：内容较长，不能用 3 秒 toast 一闪而过
const errorDialog = ref("");
const selectedDirty = computed(() => {
  if (!selected.value) return false;
  return JSON.stringify(editPayload(editForm.value)) !== JSON.stringify(editPayload(selected.value.question));
});
const readyItems = computed(() => items.value.filter((item) => item.modified));
const selectedCount = computed(() => selectedIds.value.size);

function editPayload(question) {
  return {
    clinical_stem: question?.clinical_stem || "",
    options: (question?.options || []).map((o) => ({ label: o.label, text: o.text })),
    answer: question?.answer || "",
    explanation: question?.explanation || "",
  };
}

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

// 退回原因：后端从最近一次"需修改"意见（轮内投票或把关人决断）中提取
function revisionReason(item) {
  return item.reason || "";
}

const visibleItems = ref([]);

function applyFilter() {
  visibleItems.value = items.value.filter((it) => {
    if (statusFilter.value === "pending") return !it.modified;
    if (statusFilter.value === "done") return it.modified;
    return true;
  });
}

async function load() {
  loading.value = true;
  try {
    const data = await api.myRevisions();
	    items.value = data.items || [];
	    const readyIDs = new Set(items.value.filter((item) => item.modified).map((item) => item.question.id));
	    selectedIds.value = new Set([...selectedIds.value].filter((id) => readyIDs.has(id)));
    applyFilter();
    // 当前编辑条目若已不在列表中则清空
    if (selected.value && !items.value.some((it) => it.task.id === selected.value.task.id)) {
      selected.value = null;
    }
  } catch (e) {
    showToast("加载待修改列表失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

function selectItem(item) {
	if (selectedDirty.value && selected.value?.task.id !== item.task.id) {
	  showToast("当前题目有未保存修改，请先保存或撤销后再切换");
	  return;
	}
  selected.value = item;
  const q = item.question;
  editForm.value = {
    clinical_stem: q.clinical_stem || "",
    options: (q.options || []).map((o) => ({ ...o })),
    answer: q.answer || "",
    explanation: q.explanation || "",
    change_reason: "",
  };
}

async function saveRevision() {
  if (!selected.value || !selectedDirty.value) return;
  saving.value = true;
  try {
    const data = await api.updateQuestion(selected.value.question.id, {
      clinical_stem: editForm.value.clinical_stem,
      options: editForm.value.options.map((o) => ({ label: o.label, text: o.text })),
      answer: editForm.value.answer,
      explanation: editForm.value.explanation,
      change_reason: editForm.value.change_reason || "按审核退修意见修改",
    });
	showToast("修改已保存，现在可以按原流程重新提交审核");
    await load();
    // 更新选中条目为最新题目
    const found = items.value.find((it) => it.task.id === selected.value?.task.id);
    if (found) {
      found.question = data;
      selected.value = found;
    }
  } catch (e) {
    // 校验失败信息需要出题人仔细对照修改，弹窗展示而不是自动消失的 toast
    errorDialog.value = e.message || "提交修改失败，请稍后重试";
  } finally {
    saving.value = false;
  }
}

function toggleSelected(item) {
  if (!item.modified || submitting.value) return;
  const next = new Set(selectedIds.value);
  next.has(item.question.id) ? next.delete(item.question.id) : next.add(item.question.id);
  selectedIds.value = next;
}

function toggleAllReady() {
  const next = new Set(selectedIds.value);
  const all = readyItems.value.length > 0 && readyItems.value.every((item) => next.has(item.question.id));
  readyItems.value.forEach((item) => all ? next.delete(item.question.id) : next.add(item.question.id));
  selectedIds.value = next;
}

async function resubmit(ids) {
  if (selectedDirty.value) return showToast("当前题目有未保存修改，请先保存");
  const ready = [...new Set(ids)].filter((id) => items.value.some((item) => item.question.id === id && item.modified));
  if (!ready.length) return showToast("请先选择已经保存修改的题目");
  if (!confirm(`确定将 ${ready.length} 道题按各自原审核流程从第一轮重新提交吗？`)) return;
  submitting.value = true;
  try {
    const result = await api.resubmitRevisions(ready);
    const failed = result.failed || [];
    if (failed.length) {
      errorDialog.value = failed.map((item) => `${item.question_id}：${item.error}`).join("\n");
    } else {
      showToast(`已重新提交 ${result.submitted} 道题，从原流程第一轮开始审核`);
    }
    selectedIds.value = new Set();
    selected.value = null;
    await load();
  } catch (e) {
    errorDialog.value = e.message || "重新提交审核失败";
  } finally {
    submitting.value = false;
  }
}

function warnBeforeUnload(event) {
  if (!selectedDirty.value) return;
  event.preventDefault();
  event.returnValue = "";
}

onBeforeRouteLeave(() => !selectedDirty.value || confirm("当前题目有未保存修改，确定离开并放弃吗？"));

onMounted(() => { window.addEventListener("beforeunload", warnBeforeUnload); load(); });
onBeforeUnmount(() => window.removeEventListener("beforeunload", warnBeforeUnload));
</script>

<template>
  <div class="revisions-layout">
    <!-- 左侧：退回给我的题目列表 -->
	    <section class="panel revisions-list-panel">
	      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>待我修改</h2>
	        <small>{{ items.length }} 项</small>
	        <button class="primary-button" type="button" :disabled="!selectedCount || submitting || selectedDirty" @click="resubmit([...selectedIds])">
	          {{ submitting ? "提交中..." : `批量重新送审${selectedCount ? `（${selectedCount}）` : ""}` }}
	        </button>
	      </div>

	      <div class="filter-row">
        <select v-model="statusFilter" @change="applyFilter">
          <option value="">全部</option>
          <option value="pending">待修改</option>
          <option value="done">已提交修改</option>
        </select>
	        <button class="ghost-button compact-button" type="button" @click="load">刷新</button>
	        <label class="ready-select-all"><input type="checkbox" :checked="readyItems.length > 0 && readyItems.every((item) => selectedIds.has(item.question.id))" @change="toggleAllReady" /> 全选已保存</label>
      </div>

      <div v-if="loading" class="loading">加载中...</div>
      <div v-else-if="!visibleItems.length" class="empty">
        暂无退回修改的题目<br />
        <span class="empty-hint">专家审核退回修改的题目会出现在这里</span>
      </div>
      <div v-else class="revision-list">
        <div
          v-for="item in visibleItems"
          :key="item.task.id"
          class="revision-card"
          :class="{ active: selected?.task.id === item.task.id, done: item.modified }"
          role="button"
          tabindex="0"
          @click="selectItem(item)"
          @keydown.enter="selectItem(item)"
	        >
	          <input class="revision-checkbox" type="checkbox" :checked="selectedIds.has(item.question.id)" :disabled="!item.modified || submitting" title="保存修改后才可选择重送审" @click.stop @change="toggleSelected(item)" />
          <div class="rc-head">
            <span class="rc-flow">{{ item.task.flow_id }}</span>
            <span class="rc-round">第 {{ item.task.current_round }} 轮</span>
            <span class="rc-version">送审 v{{ item.task.question_version }} · 当前 v{{ item.question.version }}</span>
            <span class="rc-state" :class="item.modified ? 'done' : 'pending'">
	              {{ item.modified ? "已保存修改" : "待修改" }}
            </span>
          </div>
          <div class="rc-stem">{{ (item.question.clinical_stem || "").slice(0, 55) }}{{ (item.question.clinical_stem || "").length > 55 ? "..." : "" }}</div>
          <div v-if="revisionReason(item)" class="rc-reason">退修意见：{{ revisionReason(item).slice(0, 60) }}{{ revisionReason(item).length > 60 ? "..." : "" }}</div>
	          <div class="rc-action">{{ item.modified ? "可重新送审 · 查看 / 继续修改 →" : "去修改 →" }}</div>
        </div>
      </div>
    </section>

    <!-- 右侧：修改工作台 -->
    <section class="panel" v-if="selected">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>按退修意见修改</h2>
        <span class="rc-state" :class="selected.modified ? 'done' : 'pending'">
	          {{ selected.modified ? "已保存修改，可按原流程重新送审" : "待修改" }}
        </span>
        <AICheckScoreButton :question-id="selected.question.id" />
      </div>

      <div v-if="revisionReason(selected)" class="reason-box">
        <label>专家退修意见</label>
        <p>{{ revisionReason(selected) }}</p>
      </div>

      <div class="version-strip" :class="{ ready: selected.modified }" role="status" aria-live="polite">
        <span>送审版本 <strong>v{{ selected.task.question_version }}</strong></span>
        <span class="version-arrow" aria-hidden="true">→</span>
        <span>当前版本 <strong>v{{ selected.question.version }}</strong></span>
        <b v-if="selected.modified">已产生新版本，可重新送审</b>
        <em v-else>尚未产生新版本，修改内容后版本号会自动递增</em>
      </div>

      <div class="edit-field">
        <label>题干 *</label>
        <textarea v-model="editForm.clinical_stem" class="edit-textarea"></textarea>
      </div>
      <div class="edit-field">
        <label>选项 *</label>
        <div v-for="(opt, i) in editForm.options" :key="i" class="edit-option-row">
          <span class="opt-label">{{ opt.label }}</span>
          <input v-model="opt.text" class="edit-input" />
        </div>
      </div>
      <div class="edit-field">
        <label>正确答案 *</label>
        <select v-model="editForm.answer" class="edit-select">
          <option v-for="opt in editForm.options" :key="opt.label" :value="opt.label">{{ opt.label }}</option>
        </select>
      </div>
      <div class="edit-field">
        <label>解析</label>
        <textarea v-model="editForm.explanation" class="edit-textarea"></textarea>
      </div>
      <div class="edit-field">
        <label>修改说明（可选）</label>
        <input v-model="editForm.change_reason" class="edit-input" placeholder="简述按意见做了哪些修改" />
      </div>

      <div class="submit-row">
	        <button class="primary-button" type="button" :disabled="saving || submitting || !selectedDirty" @click="saveRevision">
	          {{ saving ? "保存中..." : "保存修改" }}
	        </button>
	        <button class="primary-button secondary" type="button" :disabled="saving || submitting || selectedDirty || !selected.modified" @click="resubmit([selected.question.id])">
	          {{ submitting ? "提交中..." : "按原流程重新送审" }}
	        </button>
	      </div>
	      <p class="submit-hint">必须先保存产生新版本，之后按原流程从第一轮重新审核；人工修改不触发 AI 复检。</p>
    </section>

    <section v-else class="panel empty-panel">
      <p>← 请从左侧选择一道退回修改的题目</p>
    </section>
  </div>

  <!-- 送审格式校验失败弹窗：逐条对照修改后才能提交 -->
  <div v-if="errorDialog" class="error-overlay" role="alertdialog" aria-modal="true" aria-labelledby="revision-error-title">
    <div class="error-modal">
	    <h3 id="revision-error-title">操作未完成</h3>
      <p class="error-message">{{ errorDialog }}</p>
	      <p class="error-hint">请按上方意见修改并保存新版本，再按原流程重新送审。</p>
      <div class="error-actions">
        <button class="primary-button" type="button" @click="errorDialog = ''">知道了，去修改</button>
      </div>
    </div>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
</template>

<style scoped>
.error-overlay {
  position: fixed;
  inset: 0;
  background: rgba(15, 23, 42, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.error-modal {
  background: #fff;
  border-radius: 12px;
  padding: 22px 24px;
  width: min(520px, calc(100vw - 48px));
  box-shadow: 0 18px 48px rgba(15, 23, 42, 0.25);
}

.error-modal h3 {
  margin: 0 0 10px;
  font-size: 16px;
  color: #c54858;
}

.error-message {
  margin: 0 0 10px;
  padding: 10px 12px;
  background: #fff5f5;
  border: 1px solid #f3c8cd;
  border-radius: 8px;
  color: #9b2c3c;
  font-size: 14px;
  line-height: 1.6;
  white-space: pre-wrap;
}

.error-hint {
  margin: 0 0 14px;
  color: #6e7b8f;
  font-size: 13px;
  line-height: 1.6;
}

.error-actions {
  display: flex;
  justify-content: flex-end;
}

.revisions-layout {
  display: grid;
  grid-template-columns: 360px minmax(0, 1fr);
  gap: 16px;
}

.revisions-list-panel {
  max-height: calc(100vh - 180px);
  overflow-y: auto;
}

.filter-row {
  display: flex;
  gap: 8px;
  margin-bottom: 10px;
}

.filter-row select {
  flex: 1;
  height: 32px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 8px;
  font-size: 13px;
}

.revision-list {
  display: grid;
  gap: 8px;
  max-height: calc(100vh - 300px);
  overflow-y: auto;
}

.revision-card {
	position: relative;
  border: 2px solid #dce8f7;
  border-radius: 10px;
  padding: 10px 12px;
  background: #fff;
  cursor: pointer;
  transition: all 0.15s;
}
.revision-checkbox { position: absolute; top: 12px; right: 12px; }
.ready-select-all { margin-left: auto; display: inline-flex; align-items: center; gap: 6px; color: #52647a; font-size: 12px; }

.revision-card:hover {
  border-color: #1385f8;
}

.revision-card.active {
  border-color: #1385f8;
  background: #eff8ff;
}

.revision-card.done {
  border-style: dashed;
  opacity: 0.85;
}

.rc-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.rc-flow {
  font-size: 12px;
  font-weight: 700;
  color: #172033;
  background: #f0f3f7;
  padding: 2px 8px;
  border-radius: 4px;
  max-width: 45%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rc-round {
  font-size: 11px;
  font-weight: 700;
  color: #1385f8;
  background: #eff8ff;
  padding: 2px 6px;
  border-radius: 4px;
  white-space: nowrap;
}

.rc-version {
  font-size: 11px;
  color: #6e7b8f;
  white-space: nowrap;
}

.rc-state {
  margin-left: auto;
  font-size: 11px;
  font-weight: 700;
  padding: 2px 8px;
  border-radius: 4px;
  white-space: nowrap;
}

.rc-state.pending { background: #fdf2e3; color: #c07b22; }
.rc-state.done { background: #f0fff8; color: #087c55; }

.rc-stem {
  font-size: 13px;
  font-weight: 600;
  color: #172033;
  line-height: 1.5;
  margin-bottom: 6px;
}

.rc-reason {
  font-size: 12px;
  color: #c07b22;
  background: #fff8ec;
  border-radius: 6px;
  padding: 6px 8px;
  margin-bottom: 6px;
}

.rc-action {
  text-align: right;
  font-size: 12px;
  font-weight: 700;
  color: #1385f8;
}

.reason-box {
  border: 1px solid #f3d9b0;
  background: #fff8ec;
  border-radius: 8px;
  padding: 10px 12px;
  margin-bottom: 14px;
}

.version-strip {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 14px;
  padding: 8px 10px;
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  background: #f7f9fc;
  color: #53647a;
  font-size: 12px;
}

.version-strip strong {
  color: #172033;
}

.version-strip .version-arrow {
  color: #9aa8ba;
}

.version-strip b {
  color: #087c55;
  font-weight: 700;
}

.version-strip em {
  color: #c07b22;
  font-style: normal;
}

.reason-box label {
  font-size: 11px;
  font-weight: 700;
  color: #c07b22;
}

.reason-box p {
  margin: 4px 0 0;
  font-size: 13px;
  color: #172033;
  line-height: 1.6;
}

.edit-field {
  margin-bottom: 12px;
}

.edit-field label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 4px;
}

.edit-textarea {
  width: 100%;
  min-height: 90px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 8px 10px;
  font-size: 13px;
  line-height: 1.6;
  resize: vertical;
  box-sizing: border-box;
}

.edit-option-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 6px;
}

.opt-label {
  width: 24px;
  height: 24px;
  display: grid;
  place-items: center;
  border-radius: 50%;
  background: #e8f0f8;
  color: #1385f8;
  font-size: 12px;
  font-weight: 700;
}

.edit-input {
  flex: 1;
  height: 34px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 10px;
  font-size: 13px;
}

.edit-select {
  height: 34px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 10px;
  font-size: 13px;
}

.submit-row {
  display: flex;
  justify-content: flex-end;
}

.primary-button {
  height: 36px;
  border: 0;
  border-radius: 7px;
  background: #1385f8;
  color: #fff;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  padding: 0 20px;
}

.primary-button:hover { background: #0571dc; }
.primary-button:disabled { opacity: 0.6; cursor: not-allowed; }

.submit-hint {
  margin: 8px 0 0;
  text-align: right;
  font-size: 12px;
  color: #9aa5b4;
}

.empty {
  text-align: center;
  color: #6e7b8f;
  padding: 40px;
  line-height: 2;
}

.empty-hint {
  font-size: 12px;
  color: #9aa5b4;
}

.loading {
  text-align: center;
  color: #6e7b8f;
  padding: 30px;
}

.empty-panel {
  display: grid;
  place-items: center;
  color: #6e7b8f;
}
</style>
