<script setup>
// AI 检查评分按钮：黑白简约小按钮，点击弹窗展示该题最近一次 AI 质量检查结果。
// 数据来自 GET /ai-check/result/{questionId}（登录即可）；404 表示暂无检查记录。
import { ref } from "vue";
import { api } from "../api.js";

const props = defineProps({
  questionId: { type: String, required: true },
});

const open = ref(false);
const loading = ref(false);
const result = ref(null);
const notFound = ref(false);
const errorMsg = ref("");

const verdictText = (v) => ({ pass: "检查通过", issues_found: "发现问题", reject: "检查驳回" }[v] || v || "-");

async function openModal() {
  open.value = true;
  loading.value = true;
  result.value = null;
  notFound.value = false;
  errorMsg.value = "";
  try {
    result.value = await api.aiCheckResult(props.questionId);
  } catch (e) {
    if (e && e.status === 404) notFound.value = true;
    else errorMsg.value = (e && e.message) || "加载失败";
  } finally {
    loading.value = false;
  }
}

function closeModal() {
  open.value = false;
}
</script>

<template>
  <button class="ai-score-btn" type="button" title="查看 AI 检查评分" @click.stop="openModal">AI 检查评分</button>

  <teleport to="body">
    <div v-if="open" class="ai-score-overlay" @click.self="closeModal">
      <div class="ai-score-modal" role="dialog" aria-label="AI 检查评分">
        <div class="asm-head">
          <span class="asm-title">AI 检查评分</span>
          <button class="asm-close" type="button" aria-label="关闭" @click="closeModal">×</button>
        </div>

        <div v-if="loading" class="asm-body asm-hint">加载中…</div>
        <div v-else-if="notFound" class="asm-body asm-hint">暂无 AI 检查记录</div>
        <div v-else-if="errorMsg" class="asm-body asm-hint">加载失败：{{ errorMsg }}</div>
        <div v-else-if="result" class="asm-body">
          <div class="asm-verdict">{{ verdictText(result.verdict) }}</div>

          <div class="asm-scores">
            <div class="asm-score"><b>{{ result.scores?.scientific ?? "-" }}</b><span>科学性</span></div>
            <div class="asm-score"><b>{{ result.scores?.logic ?? "-" }}</b><span>逻辑性</span></div>
            <div class="asm-score"><b>{{ result.scores?.a2_fit ?? "-" }}</b><span>A2 格式</span></div>
            <div class="asm-score"><b>{{ result.scores?.answer ?? "-" }}</b><span>答案准确</span></div>
          </div>

          <ul v-if="(result.issues || []).length" class="asm-issues">
            <li v-for="(issue, i) in result.issues" :key="i" :class="issue.severity">
              <b>{{ issue.severity === "error" ? "错误" : issue.severity === "warning" ? "警告" : "提示" }}</b>
              {{ issue.field ? `[${issue.field}] ` : "" }}{{ issue.message }}
            </li>
          </ul>

          <p v-if="result.suggestion" class="asm-suggestion">{{ result.suggestion }}</p>

        </div>
      </div>
    </div>
  </teleport>
</template>

<style scoped>
.ai-score-btn {
  padding: 4px 10px;
  font-size: 12px;
  line-height: 1.6;
  color: #536b82;
  background: #fff;
  border: 1px solid #d5e0eb;
  border-radius: 6px;
  cursor: pointer;
  white-space: nowrap;
  transition: background 0.15s, border-color 0.15s, color 0.15s;
}

.ai-score-btn:hover {
  background: #f3f9ff;
  border-color: #8fc2ed;
  color: #0571dc;
}

.asm-close {
  padding: 0 6px;
  font-size: 18px;
  line-height: 1.2;
  color: #111;
  background: none;
  border: none;
  cursor: pointer;
}

.asm-close:hover {
  opacity: 0.6;
}

.ai-score-overlay {
  position: fixed;
  inset: 0;
  z-index: 400;
  display: grid;
  place-items: center;
  background: rgba(0, 0, 0, 0.4);
}

.ai-score-modal {
  display: flex;
  flex-direction: column;
  width: min(430px, 92vw);
  max-height: 80vh;
  overflow-y: auto;
  background: #fff;
  border: 1px solid #111;
  border-radius: 3px;
}

.asm-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 16px;
  border-bottom: 1px solid #111;
}

.asm-title {
  font-size: 14px;
  font-weight: 600;
  letter-spacing: 0.05em;
}

.asm-body {
  padding: 14px 16px;
}

.asm-hint {
  color: #555;
  font-size: 13px;
}

.asm-verdict {
  display: inline-block;
  padding: 2px 10px;
  margin-bottom: 12px;
  font-size: 12px;
  font-weight: 600;
  color: #111;
  border: 1px solid #111;
}

.asm-scores {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 8px;
  margin-bottom: 12px;
}

.asm-score {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
  padding: 8px 4px;
  border: 1px solid #ddd;
  border-radius: 2px;
}

.asm-score b {
  font-size: 20px;
  font-weight: 700;
  color: #111;
}

.asm-score span {
  font-size: 11px;
  color: #555;
}

.asm-issues {
  margin: 0 0 10px;
  padding-left: 18px;
  font-size: 12px;
  line-height: 1.7;
  color: #222;
}

.asm-issues li.error b,
.asm-issues li.warning b {
  font-weight: 700;
}

.asm-issues li.error b::after {
  content: " ■";
}

.asm-issues li.warning b::after {
  content: " ▲";
}

.asm-suggestion {
  margin: 0 0 10px;
  font-size: 12px;
  line-height: 1.7;
  color: #333;
}

.asm-suggestion::before {
  content: "建议：";
  font-weight: 600;
}

</style>
