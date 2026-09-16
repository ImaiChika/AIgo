<script setup>
import { ref, computed, watch, onBeforeUnmount } from "vue";
import { api } from "../api.js";
import { hasPerm } from "../auth.js";

const props = defineProps({
  question: { type: Object, default: null },
});
const emit = defineEmits(["close", "refresh"]);

const aiResult = ref(null);
const aiTask = ref(null); // 检查任务状态快照（排队/执行中/耗尽）
const aiChecking = ref(false);
const overridden = ref(false); // 本会话内已强制通过
let taskTimer = null;
// AI 检查自动执行、不设独立权限点：重新检查按题目编辑权限，强制通过与送审一致按用户管理权限
const canRecheck = computed(() => hasPerm("question:edit"));
const canForcePass = computed(() => hasPerm("user:manage"));

const effectiveStatus = computed(() => (overridden.value ? "ai_reviewed" : props.question?.status));

watch(
  () => props.question,
  async (q) => {
    aiResult.value = null;
    aiTask.value = null;
    overridden.value = false;
    stopTaskPolling();
    if (q && q.id) {
      try {
        aiResult.value = await api.aiCheckResult(q.id);
      } catch (e) {
        aiResult.value = null; // 404 = 尚无检查结果
      }
      await refreshAITask(q.id);
    }
  },
  { immediate: true }
);

onBeforeUnmount(stopTaskPolling);

function stopTaskPolling() {
  if (taskTimer) {
    clearInterval(taskTimer);
    taskTimer = null;
  }
}

// 查询单题检查任务状态；排队/执行中时每 5 秒轮询，完成后刷新检查结果
async function refreshAITask(questionId) {
  try {
    const data = await api.aiCheckProgress([questionId]);
    aiTask.value = (data.items || [])[0] || null;
  } catch (e) {
    aiTask.value = null;
    return;
  }
  stopTaskPolling();
  const status = aiTask.value?.task_status;
  if (status === "pending" || status === "running") {
    taskTimer = setInterval(async () => {
      if (!props.question) return stopTaskPolling();
      try {
        const data = await api.aiCheckProgress([props.question.id]);
        aiTask.value = (data.items || [])[0] || null;
        const st = aiTask.value?.task_status;
        if (st !== "pending" && st !== "running") {
          stopTaskPolling();
          try {
            aiResult.value = await api.aiCheckResult(props.question.id);
          } catch (e) { /* 保持无结果 */ }
        }
      } catch (e) { /* 下一轮重试 */ }
    }, 5000);
  }
}

async function recheckAI() {
  if (!props.question || aiChecking.value) return;
  aiChecking.value = true;
  try {
    await api.aiCheckAsync([props.question.id]);
    await refreshAITask(props.question.id);
  } catch (e) {
    console.warn("AI 检查提交失败", e);
  } finally {
    aiChecking.value = false;
  }
}

// 强制通过：跳过 AI 检查门禁，把草稿直接置为已检查（后端写审计留痕）
async function forcePassAI() {
  if (!props.question || overridden.value) return;
  if (!confirm("确定跳过 AI 检查、强制将该题置为「已检查」？该操作会记录到审计日志。")) return;
  aiChecking.value = true;
  try {
    await api.aiCheckOverride(props.question.id);
    overridden.value = true;
    stopTaskPolling();
    emit("refresh");
  } catch (e) {
    console.warn("强制通过失败", e);
  } finally {
    aiChecking.value = false;
  }
}

const aiVerdictText = computed(() => {
  const map = { pass: "检查通过", issues_found: "发现问题", reject: "检查驳回" };
  return map[aiResult.value?.verdict] || aiResult.value?.verdict;
});

const aiVerdictClass = computed(() => {
  const v = aiResult.value?.verdict;
  if (v === "pass") return "ai-pass";
  if (v === "reject") return "ai-reject";
  return "ai-warn";
});

// 检查结果记录的是检查时的题目版本，题目编辑后结果视为过期
const aiStale = computed(() => props.question && aiResult.value && props.question.version > (aiResult.value.question_version || 0));

// 任务状态：排队中 / 执行中 / 重试耗尽（最终失败）
const aiTaskRunning = computed(() => aiTask.value?.task_status === "pending" || aiTask.value?.task_status === "running");
const aiTaskExhausted = computed(() => aiTask.value?.task_status === "exhausted");
const aiTaskLabel = computed(() => {
  if (aiTask.value?.task_status === "running") return `AI 检查中（第 ${aiTask.value.attempts || 1}/${aiTask.value.max_attempts || 3} 次尝试）…`;
  return "AI 检查排队中…";
});

const aiScores = computed(() => {
  const s = aiResult.value?.scores;
  if (!s) return [];
  return [
    { label: "科学性", value: s.scientific },
    { label: "逻辑性", value: s.logic },
    { label: "A2 格式", value: s.a2_fit },
    { label: "答案准确", value: s.answer },
  ].filter((x) => typeof x.value === "number");
});

const statusText = (status) => {
  const map = {
    ai_draft: "草稿", auto_checked: "已初评", ai_reviewed: "已检查",
    reviewing: "审核中", conflict: "待决断", revision_required: "需修改",
    rejected: "已驳回", published: "已通过", archived: "已归档",
  };
  return map[status] || status;
};

const statusClass = (status) => {
  if (status === "published" || status === "ai_reviewed") return "q-status-good";
  if (status === "rejected") return "q-status-bad";
  if (status === "reviewing") return "q-status-active";
  if (status === "revision_required") return "q-status-warn";
  if (status === "conflict") return "q-status-conflict";
  return "";
};

function difficultyText(d) {
  const map = { easy: "简单", medium: "中等", hard: "困难" };
  if (map[d]) return map[d];
  if (d && !isNaN(Number(d))) {
    const v = Number(d);
    if (v <= 0.6) return "简单";
    if (v <= 0.8) return "中等";
    return "困难";
  }
  return d || "-";
}
</script>

<template>
  <div v-if="question" class="q-modal-overlay" @click.self="emit('close')">
    <div class="q-modal-card">
      <div class="q-modal-head">
        <h3>题目详情</h3>
        <button class="q-close-btn" type="button" @click="emit('close')">×</button>
      </div>

      <div class="q-params">
        <span v-if="question.knowledge_points?.[0]?.version_name" class="q-param"><label>大纲版本</label>{{ question.knowledge_points[0].version_name }}</span>
        <span v-if="question.outline_code" class="q-param"><label>大纲代码</label>{{ question.outline_code }}</span>
        <span v-if="question.profession" class="q-param"><label>专业</label>{{ question.profession }}</span>
        <span v-if="question.system" class="q-param"><label>系统</label>{{ question.system }}</span>
        <span v-if="question.difficulty" class="q-param"><label>难度</label>{{ difficultyText(question.difficulty) }}</span>
        <span v-if="question.cognitive_level" class="q-param"><label>认知层次</label>{{ question.cognitive_level }}</span>
        <span class="q-param"><label>状态</label><b :class="statusClass(effectiveStatus)">{{ statusText(effectiveStatus) }}</b></span>
        <span class="q-param"><label>版本</label>v{{ question.version }}</span>
      </div>

      <div v-if="question.exam_points" class="q-block">
        <label>考核要点</label>
        <p>{{ question.exam_points }}</p>
      </div>

      <div class="q-block">
        <label>题干</label>
        <p class="q-stem">{{ question.clinical_stem }}</p>
      </div>

      <div class="q-block">
        <label>选项</label>
        <div v-for="opt in question.options || []" :key="opt.label" class="q-option" :class="{ correct: opt.label === question.answer }">
          <span class="q-opt-label">{{ opt.label }}</span>
          <span>{{ opt.text }}</span>
          <span v-if="opt.label === question.answer" class="q-correct-mark">✓ 正确答案</span>
        </div>
      </div>

      <div class="q-block">
        <label>解析</label>
        <p v-if="question.explanation">{{ question.explanation }}</p>
        <p v-else class="q-none">（无解析）</p>
      </div>

      <div v-if="(question.knowledge_points || []).length" class="q-block">
        <label>考试大纲要点</label>
        <div class="q-kps">
          <span v-for="kp in question.knowledge_points" :key="kp.id" class="q-kp">
            {{ kp.topic }}<span v-if="kp.outline_code" class="q-kp-code">（{{ kp.outline_code }}）</span>
          </span>
        </div>
      </div>

      <div v-if="(question.source_refs || []).length" class="q-block">
        <label>来源引用</label>
        <div v-for="s in question.source_refs" :key="s.title" class="q-source">
          {{ s.title }}<span v-if="s.note"> - {{ s.note }}</span>
        </div>
      </div>

      <div v-if="aiResult || aiTaskRunning || aiTaskExhausted" class="q-block q-ai">
        <div class="q-ai-head">
          <label>AI 检查结果</label>
          <span v-if="aiResult" class="q-ai-verdict" :class="aiVerdictClass">
            {{ aiVerdictText }}<template v-if="aiStale">（内容已修改，结果待复检）</template>
          </span>
          <span v-else-if="aiTaskRunning" class="q-ai-verdict ai-warn">{{ aiTaskLabel }}</span>
          <span v-else-if="aiTaskExhausted" class="q-ai-verdict ai-reject">检查异常（已重试 {{ aiTask.attempts }} 次）</span>
          <button v-if="canRecheck && (aiTaskExhausted || aiStale)" class="q-ai-recheck" type="button" :disabled="aiChecking" @click="recheckAI">
            {{ aiChecking ? "提交中…" : "重新检查" }}
          </button>
          <button v-if="canForcePass && !overridden && (question.status === 'ai_draft' || question.status === 'auto_checked')" class="q-ai-recheck q-ai-force" type="button" :disabled="aiChecking" @click="forcePassAI">
            强制通过
          </button>
        </div>
        <p v-if="aiTaskExhausted && aiTask.last_error" class="q-ai-error">失败原因：{{ aiTask.last_error }}</p>
        <template v-if="aiResult">
          <div v-if="aiScores.length" class="q-ai-scores">
            <div v-for="s in aiScores" :key="s.label" class="q-ai-score">
              <b :class="s.value >= 70 ? 'ai-pass' : s.value >= 60 ? 'ai-warn' : 'ai-reject'">{{ s.value }}</b>
              <span>{{ s.label }}</span>
            </div>
          </div>
          <ul v-if="(aiResult.issues || []).length" class="q-ai-issues">
            <li v-for="(issue, i) in aiResult.issues" :key="i" :class="issue.severity">
              <b>{{ issue.severity === "error" ? "错误" : issue.severity === "warning" ? "警告" : "提示" }}</b>
              {{ issue.field ? `[${issue.field}] ` : "" }}{{ issue.message }}
            </li>
          </ul>
          <p v-if="aiResult.suggestion" class="q-ai-suggestion">建议：{{ aiResult.suggestion }}</p>
          <p class="q-ai-time">检查时间：{{ aiResult.created_at ? new Date(aiResult.created_at).toLocaleString() : "-" }}<span v-if="aiResult.model"> · 模型：{{ aiResult.model }}</span></p>
        </template>
      </div>

      <div class="q-block q-meta">
        <label>元信息</label>
        <p>ID：{{ question.id }}</p>
        <p>创建：{{ question.created_at ? new Date(question.created_at).toLocaleString() : "-" }}</p>
        <p>更新：{{ question.updated_at ? new Date(question.updated_at).toLocaleString() : "-" }}</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.q-modal-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.45);
  display: grid;
  place-items: center;
  z-index: 300;
}

.q-modal-card {
  width: min(760px, 92vw);
  max-height: 88vh;
  overflow-y: auto;
  background: #fff;
  border-radius: 12px;
  padding: 22px 26px;
  box-shadow: 0 24px 60px rgba(0, 0, 0, 0.25);
}

.q-modal-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.q-modal-head h3 {
  margin: 0;
  font-size: 17px;
  color: #172033;
}

.q-close-btn {
  width: 30px;
  height: 30px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #6e7b8f;
  font-size: 18px;
  cursor: pointer;
}

.q-close-btn:hover {
  background: #fff0f0;
  color: #c54858;
}

.q-params {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 20px;
  padding: 10px 12px;
  background: #f8fbff;
  border: 1px solid #dce8f7;
  border-radius: 8px;
  margin-bottom: 14px;
}

.q-param {
  font-size: 12px;
  color: #3a4658;
}

.q-param label {
  color: #6e7b8f;
  margin-right: 6px;
}

.q-status-good { color: #087c55; }
.q-status-bad { color: #c54858; }
.q-status-active { color: #0571dc; }
.q-status-warn { color: #c07b22; }
.q-status-conflict { color: #3a4658; }

.q-block {
  margin-bottom: 14px;
}

.q-block label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 6px;
}

.q-stem {
  font-size: 14px;
  line-height: 1.7;
  color: #172033;
  margin: 0;
}

.q-option {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 0;
  font-size: 13px;
  color: #3a4658;
}

.q-option.correct {
  color: #087c55;
  font-weight: 600;
}

.q-opt-label {
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

.q-correct-mark {
  font-size: 12px;
  color: #087c55;
}

.q-none {
  color: #9aa5b4;
  font-size: 13px;
  margin: 0;
}

.q-kps {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.q-kp {
  font-size: 12px;
  padding: 3px 10px;
  border-radius: 4px;
  background: #eff8ff;
  color: #0571dc;
}

.q-kp-code {
  color: #6e7b8f;
  font-size: 11px;
}

.q-source {
  font-size: 12px;
  color: #3a4658;
  padding: 2px 0;
}

.q-meta p {
  margin: 2px 0;
  font-size: 12px;
  color: #6e7b8f;
}

.q-ai {
  border: 1px solid #dce8f7;
  background: #f8fbff;
  border-radius: 8px;
  padding: 10px 12px;
}

.q-ai-head {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 8px;
}

.q-ai-head label {
  margin-bottom: 0;
}

.q-ai-verdict {
  font-size: 12px;
  font-weight: 700;
  padding: 2px 8px;
  border-radius: 4px;
}

.q-ai-verdict.ai-pass { background: #f0fff8; color: #087c55; }
.q-ai-verdict.ai-warn { background: #fff8ec; color: #c07b22; }
.q-ai-verdict.ai-reject { background: #fff0f0; color: #c54858; }

.q-ai-recheck {
  margin-left: auto;
  font-size: 12px;
  padding: 3px 10px;
  border: 1px solid #c9dcf3;
  border-radius: 6px;
  background: #fff;
  color: #0571dc;
  cursor: pointer;
}

.q-ai-recheck:disabled { color: #9aa5b4; cursor: default; }
.q-ai-recheck:hover:not(:disabled) { background: #eff8ff; }

.q-ai-force {
  color: #c07b22;
  border-color: #f3d9b0;
}
.q-ai-force:hover:not(:disabled) { background: #fff8ec; }

.q-ai-error {
  margin: 0 0 8px;
  font-size: 12px;
  color: #c54858;
}

.q-ai-scores {
  display: flex;
  gap: 14px;
  margin-bottom: 8px;
}

.q-ai-score {
  display: flex;
  align-items: baseline;
  gap: 4px;
  font-size: 12px;
  color: #6e7b8f;
}

.q-ai-score b { font-size: 16px; }
.q-ai-score b.ai-pass { color: #087c55; }
.q-ai-score b.ai-warn { color: #c07b22; }
.q-ai-score b.ai-reject { color: #c54858; }

.q-ai-issues {
  margin: 0 0 8px;
  padding-left: 18px;
  font-size: 12px;
  color: #3a4658;
}

.q-ai-issues li { margin-bottom: 3px; }
.q-ai-issues li b { margin-right: 4px; }
.q-ai-issues li.error > b { color: #c54858; }
.q-ai-issues li.warning > b { color: #c07b22; }
.q-ai-issues li.info > b { color: #0571dc; }

.q-ai-suggestion {
  margin: 0 0 6px;
  font-size: 12px;
  color: #3a4658;
}

.q-ai-time {
  margin: 0;
  font-size: 11px;
  color: #9aa5b4;
}
</style>
