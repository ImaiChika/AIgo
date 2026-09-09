<script setup>
import { ref, onMounted } from "vue";
import { api } from "../api.js";
import AICheckScoreButton from "../components/AICheckScoreButton.vue";

const toast = ref("");
const items = ref([]);
const loading = ref(false);
const selected = ref(null); // 当前编辑的条目
const editForm = ref({ clinical_stem: "", options: [], answer: "", explanation: "", change_reason: "" });
const saving = ref(false);
const statusFilter = ref(""); // ""=全部 / pending=待修改 / done=已提交修改

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

async function submitRevision() {
  if (!selected.value) return;
  if (!confirm("确认按退修意见修改完成？提交后将回库等待管理员重新送审（无需 AI 检查）。")) return;
  saving.value = true;
  try {
    const data = await api.updateQuestion(selected.value.question.id, {
      clinical_stem: editForm.value.clinical_stem,
      options: editForm.value.options.map((o) => ({ label: o.label, text: o.text })),
      answer: editForm.value.answer,
      explanation: editForm.value.explanation,
      change_reason: editForm.value.change_reason || "按审核退修意见修改",
    });
    showToast("修改已提交，等待管理员重新送审");
    await load();
    // 更新选中条目为最新题目
    const found = items.value.find((it) => it.task.id === selected.value?.task.id);
    if (found) {
      found.question = data;
      selected.value = found;
    }
  } catch (e) {
    showToast("提交修改失败: " + e.message);
  } finally {
    saving.value = false;
  }
}

onMounted(load);
</script>

<template>
  <div class="revisions-layout">
    <!-- 左侧：退回给我的题目列表 -->
    <section class="panel revisions-list-panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>待我修改</h2>
        <small>{{ items.length }} 项</small>
      </div>

      <div class="filter-row">
        <select v-model="statusFilter" @change="applyFilter">
          <option value="">全部</option>
          <option value="pending">待修改</option>
          <option value="done">已提交修改</option>
        </select>
        <button class="ghost-button compact-button" type="button" @click="load">刷新</button>
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
          <div class="rc-head">
            <span class="rc-flow">{{ item.task.flow_id }}</span>
            <span class="rc-round">第 {{ item.task.current_round }} 轮</span>
            <span class="rc-state" :class="item.modified ? 'done' : 'pending'">
              {{ item.modified ? "已提交修改" : "待修改" }}
            </span>
          </div>
          <div class="rc-stem">{{ (item.question.clinical_stem || "").slice(0, 55) }}{{ (item.question.clinical_stem || "").length > 55 ? "..." : "" }}</div>
          <div v-if="revisionReason(item)" class="rc-reason">退修意见：{{ revisionReason(item).slice(0, 60) }}{{ revisionReason(item).length > 60 ? "..." : "" }}</div>
          <div class="rc-action">{{ item.modified ? "查看 / 继续修改 →" : "去修改 →" }}</div>
        </div>
      </div>
    </section>

    <!-- 右侧：修改工作台 -->
    <section class="panel" v-if="selected">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>按退修意见修改</h2>
        <span class="rc-state" :class="selected.modified ? 'done' : 'pending'">
          {{ selected.modified ? "已提交修改，等待管理员重新送审" : "待修改" }}
        </span>
        <AICheckScoreButton :question-id="selected.question.id" />
      </div>

      <div v-if="revisionReason(selected)" class="reason-box">
        <label>专家退修意见</label>
        <p>{{ revisionReason(selected) }}</p>
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
        <button class="primary-button" type="button" :disabled="saving" @click="submitRevision">
          {{ saving ? "提交中..." : "提交修改" }}
        </button>
      </div>
      <p class="submit-hint">提交修改后题目回库等待管理员重新送审，无需 AI 检查。</p>
    </section>

    <section v-else class="panel empty-panel">
      <p>← 请从左侧选择一道退回修改的题目</p>
    </section>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
</template>

<style scoped>
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
  border: 2px solid #dce8f7;
  border-radius: 10px;
  padding: 10px 12px;
  background: #fff;
  cursor: pointer;
  transition: all 0.15s;
}

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
