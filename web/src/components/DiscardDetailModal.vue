<script setup>
// 淘汰题目详情弹窗：展示 AI 检查淘汰的原题快照、四维评分、问题与建议。
// 数据来自 /ai-check/progress 返回的淘汰明细（discarded=true 的 item），
// 生成页（本次出题）与批量页（任务详情）共用。
import { computed } from "vue";

const props = defineProps({
  // 检查进度接口的淘汰明细项：stem_summary / verdict / scores / issues /
  // suggestion / question（完整快照，旧留档可能没有）/ model / check_completed_at
  item: { type: Object, required: true },
});

const emit = defineEmits(["close"]);

const verdictText = computed(() => (
  props.item.verdict === "reject" ? "不合格（reject）" : "存在问题（issues_found）"
));

const question = computed(() => props.item.question || null);
const scores = computed(() => props.item.scores || null);
const options = computed(() => question.value?.options || []);
const optionLabel = (i) => String.fromCharCode(65 + i);
const severityText = (s) => ({ error: "错误", warning: "警告", info: "提示" }[s] || s);
// 知识点完整层级：优先取题目快照内关联的知识点；旧留档无快照时回退到
// 列表行补充的大纲代码与要点（批量页导入明细提供）。
const kp = computed(() => question.value?.knowledge_points?.[0] || null);
const outlineCode = computed(() => kp.value?.outline_code || props.item.outline_code || "");
const topic = computed(() => kp.value?.topic || props.item.topic || "");
const kpRows = computed(() => {
  if (!kp.value) return [];
  return [
    ["分类", kp.value.category],
    ["专业", kp.value.subject],
    ["单元", kp.value.unit],
    ["细目", kp.value.sub_item],
    ["要点", kp.value.topic],
    ["大纲版本", kp.value.version_name],
  ].filter(([, v]) => v);
});
</script>

<template>
  <teleport to="body">
    <div class="ddm-overlay" @click.self="emit('close')">
      <div class="ddm-modal" role="dialog" aria-label="淘汰题目详情">
        <div class="ddm-head">
          <span class="ddm-title">AI 检查淘汰详情</span>
          <button class="ddm-close" type="button" aria-label="关闭" @click="emit('close')">×</button>
        </div>

        <div class="ddm-body">
          <div class="ddm-verdict-row">
            <span class="ddm-verdict">{{ verdictText }}</span>
            <span v-if="outlineCode" class="ddm-outline-code">{{ outlineCode }}</span>
            <span class="ddm-eliminated">该题未进入题库，已自动淘汰</span>
          </div>

          <!-- 知识点定位：完整大纲层级，老师据此判断哪个考点出的问题 -->
          <div v-if="kpRows.length || topic" class="ddm-kp">
            <p class="ddm-section-title">知识点</p>
            <p v-if="topic && !kpRows.length" class="ddm-kp-topic">{{ topic }}</p>
            <dl v-else class="ddm-kp-list">
              <div v-for="[label, value] in kpRows" :key="label" class="ddm-kp-row">
                <dt>{{ label }}</dt>
                <dd>{{ value }}</dd>
              </div>
            </dl>
          </div>

          <div v-if="scores" class="ddm-scores">
            <div class="ddm-score"><b>{{ scores.scientific ?? "-" }}</b><span>科学性</span></div>
            <div class="ddm-score"><b>{{ scores.logic ?? "-" }}</b><span>逻辑性</span></div>
            <div class="ddm-score"><b>{{ scores.a2_fit ?? "-" }}</b><span>A2 格式</span></div>
            <div class="ddm-score"><b>{{ scores.answer ?? "-" }}</b><span>答案准确</span></div>
          </div>

          <!-- 原题快照：题目已删除，凭淘汰留档还原 -->
          <div v-if="question" class="ddm-question">
            <p class="ddm-section-title">原题（淘汰时快照）</p>
            <p class="ddm-stem">{{ question.clinical_stem }}</p>
            <div class="ddm-options">
              <div
                v-for="(opt, i) in options"
                :key="i"
                class="ddm-option"
                :class="{ correct: optionLabel(i) === question.answer }"
              >
                <span class="ddm-option-label">{{ optionLabel(i) }}</span>
                <span>{{ opt.text }}</span>
                <span v-if="optionLabel(i) === question.answer" class="ddm-correct-mark">✓ 正确答案</span>
              </div>
            </div>
            <div v-if="question.explanation" class="ddm-explanation">
              <label>解析</label>
              <p>{{ question.explanation }}</p>
            </div>
          </div>
          <div v-else class="ddm-legacy">
            <p class="ddm-section-title">原题摘要</p>
            <p class="ddm-stem">{{ item.stem_summary || "暂无题干摘要" }}</p>
            <p class="ddm-legacy-note">该题淘汰时尚未保留完整题目快照（升级前留档），仅展示题干摘要、评分与淘汰原因。</p>
          </div>

          <div v-if="(item.issues || []).length" class="ddm-issues-block">
            <p class="ddm-section-title">淘汰原因</p>
            <ul class="ddm-issues">
              <li v-for="(issue, i) in item.issues" :key="i" :class="issue.severity">
                <b>{{ severityText(issue.severity) }}</b>
                {{ issue.field ? `[${issue.field}] ` : "" }}{{ issue.message }}
              </li>
            </ul>
          </div>

          <p v-if="item.suggestion" class="ddm-suggestion">{{ item.suggestion }}</p>

        </div>
      </div>
    </div>
  </teleport>
</template>

<style scoped>
.ddm-overlay {
  position: fixed;
  inset: 0;
  z-index: 400;
  display: grid;
  place-items: center;
  background: rgba(0, 0, 0, 0.4);
}

.ddm-modal {
  display: flex;
  flex-direction: column;
  width: min(620px, 92vw);
  max-height: 84vh;
  overflow-y: auto;
  background: #fff;
  border: 1px solid #111;
  border-radius: 3px;
}

.ddm-head {
  position: sticky;
  top: 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 16px;
  background: #fff;
  border-bottom: 1px solid #111;
}

.ddm-title {
  font-size: 14px;
  font-weight: 600;
  letter-spacing: 0.05em;
}

.ddm-close {
  padding: 0 6px;
  font-size: 18px;
  line-height: 1.2;
  color: #111;
  background: none;
  border: none;
  cursor: pointer;
}

.ddm-close:hover {
  opacity: 0.6;
}

.ddm-body {
  padding: 14px 16px;
}

.ddm-verdict-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
}

.ddm-verdict {
  padding: 2px 10px;
  font-size: 12px;
  font-weight: 600;
  color: #fff;
  background: #a23b4b;
  border-radius: 2px;
}

.ddm-eliminated {
  font-size: 11px;
  color: #a23b4b;
}

.ddm-outline-code {
  padding: 2px 8px;
  font-family: ui-monospace, Menlo, monospace;
  font-size: 12px;
  font-weight: 700;
  color: #a23b4b;
  background: #ffe9e9;
  border-radius: 2px;
}

.ddm-kp {
  margin-bottom: 14px;
}

.ddm-kp-topic {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
  color: #172033;
}

.ddm-kp-list {
  margin: 0;
  display: grid;
  gap: 3px;
}

.ddm-kp-row {
  display: flex;
  gap: 8px;
  font-size: 12px;
  line-height: 1.6;
}

.ddm-kp-row dt {
  flex: none;
  width: 64px;
  color: #8a97a8;
}

.ddm-kp-row dd {
  margin: 0;
  color: #172033;
}

.ddm-scores {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 8px;
  margin-bottom: 14px;
}

.ddm-score {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 2px;
  padding: 8px 4px;
  border: 1px solid #ddd;
  border-radius: 2px;
}

.ddm-score b {
  font-size: 20px;
  font-weight: 700;
  color: #111;
}

.ddm-score span {
  font-size: 11px;
  color: #555;
}

.ddm-section-title {
  margin: 0 0 6px;
  font-size: 12px;
  font-weight: 600;
  color: #666;
  border-left: 3px solid #111;
  padding-left: 6px;
}

.ddm-question,
.ddm-legacy {
  margin-bottom: 14px;
}

.ddm-stem {
  margin: 0 0 8px;
  font-size: 13px;
  line-height: 1.8;
  color: #172033;
}

.ddm-options {
  display: grid;
  gap: 6px;
  margin-bottom: 8px;
}

.ddm-option {
  display: flex;
  align-items: baseline;
  gap: 8px;
  font-size: 13px;
  color: #3a4658;
}

.ddm-option.correct {
  color: #087c55;
  font-weight: 600;
}

.ddm-option-label {
  flex: none;
  font-weight: 700;
}

.ddm-correct-mark {
  font-size: 12px;
  color: #087c55;
}

.ddm-explanation label {
  font-size: 11px;
  color: #9aa5b4;
}

.ddm-explanation p {
  margin: 4px 0 0;
  font-size: 12px;
  line-height: 1.7;
  color: #3a4658;
}

.ddm-legacy-note {
  margin: 6px 0 0;
  font-size: 11px;
  color: #9aa5b4;
}

.ddm-issues-block {
  margin-bottom: 10px;
}

.ddm-issues {
  margin: 0;
  padding-left: 18px;
  font-size: 12px;
  line-height: 1.7;
  color: #222;
}

.ddm-issues li.error b,
.ddm-issues li.warning b {
  font-weight: 700;
}

.ddm-issues li.error b::after {
  content: " ■";
}

.ddm-issues li.warning b::after {
  content: " ▲";
}

.ddm-suggestion {
  margin: 0 0 10px;
  font-size: 12px;
  line-height: 1.7;
  color: #333;
}

.ddm-suggestion::before {
  content: "AI 建议：";
  font-weight: 600;
}

</style>
