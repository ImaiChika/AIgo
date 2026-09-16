<script setup>
// 专家结构化评语卡：态度 + 专家信息 + 分栏评语，供多专家横向对比。
const props = defineProps({
  record: { type: Object, required: true }, // 审核记录
  fallbackName: { type: String, default: "" }, // record.expert_name 为空时的显示名
  compact: { type: Boolean, default: false }, // 紧凑模式（结果页用）
});

const sections = [
  { key: "stem", label: "题干" },
  { key: "options", label: "选项" },
  { key: "answer", label: "答案与解析" },
  { key: "other", label: "其他" },
];

const attitudeMap = {
  approved: { text: "通过", cls: "at-ok" },
  rejected: { text: "驳回", cls: "at-no" },
  revision_required: { text: "需修改", cls: "at-rev" },
};

const attitude = attitudeMap[props.record.review_status] || { text: props.record.review_status, cls: "" };

const storedName = (props.record.expert_name || "").trim();
const displayName = storedName && !storedName.startsWith("user-")
  ? storedName
  : props.fallbackName || storedName || props.record.expert_id;

const attemptTag = props.record.attempt > 1 ? `第${props.record.attempt}批` : "";
const isFinal = (props.record.id || "").startsWith("rec-final-");

function sectionValue(key) {
  return (props.record.comment && props.record.comment[key] || "").trim();
}

const hasStructured = sections.some((s) => sectionValue(s.key));

function formatTime(ts) {
  if (!ts) return "";
  const d = new Date(ts);
  return `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}
</script>

<template>
  <div class="rc-card" :class="[compact ? 'compact' : '', record.review_status]">
    <div class="rc-head">
      <span class="rc-name">{{ displayName }}</span>
      <span v-if="isFinal" class="rc-final">最终决断</span>
      <span class="rc-attitude" :class="attitude.cls">{{ attitude.text }}</span>
      <span v-if="attemptTag" class="rc-attempt">{{ attemptTag }}</span>
      <span class="rc-time">{{ formatTime(record.created_at) }}</span>
    </div>
    <div v-if="hasStructured" class="rc-body">
      <div v-for="s in sections" :key="s.key" class="rc-section" :class="{ filled: sectionValue(s.key) }">
        <span class="rc-label">{{ s.label }}</span>
        <p>{{ sectionValue(s.key) || "未填写" }}</p>
      </div>
    </div>
    <div v-else-if="record.opinion" class="rc-legacy">
      <span class="rc-label">意见</span>
      <p>{{ record.opinion }}</p>
    </div>
    <div v-else class="rc-none">未填写评语</div>
  </div>
</template>

<style scoped>
.rc-card {
  border: 1px solid #e5ebf3;
  border-top: 2px solid #c6d2e2;
  border-radius: 8px;
  background: #fff;
  padding: 11px 12px;
  min-width: 0;
}

.rc-card.approved { border-top-color: #2fae7c; }
.rc-card.rejected { border-top-color: #d95d6e; }
.rc-card.revision_required { border-top-color: #e0a23c; }

.rc-head {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}

.rc-name {
  font-size: 13px;
  font-weight: 700;
  color: #172033;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rc-attitude {
  font-size: 11px;
  font-weight: 700;
  padding: 0;
  white-space: nowrap;
}

.rc-final {
  padding: 1px 5px;
  border: 1px solid #ded3f4;
  border-radius: 4px;
  color: #7652bb;
  background: transparent;
  font-size: 10px;
  font-weight: 700;
  white-space: nowrap;
}

.at-ok { color: #087c55; }
.at-no { color: #c54858; }
.at-rev { color: #c07b22; }

.rc-attempt {
  font-size: 10px;
  font-weight: 700;
  padding: 0;
  color: #6e7b8f;
  white-space: nowrap;
}

.rc-time {
  margin-left: auto;
  font-size: 11px;
  color: #9aa5b4;
  white-space: nowrap;
}

.rc-body {
  display: grid;
  gap: 6px;
}

.rc-section {
  border: 1px solid #eef2f7;
  border-radius: 6px;
  padding: 5px 8px;
  background: #fbfcfe;
}

.rc-section.filled {
  background: #fff;
  border-color: #e0e8f2;
}

.rc-label {
  display: inline-block;
  font-size: 10px;
  font-weight: 700;
  color: #8a95a6;
  margin-bottom: 2px;
}

.rc-section p,
.rc-legacy p {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  color: #3a4658;
  white-space: pre-wrap;
  word-break: break-word;
}

.rc-section:not(.filled) p {
  color: #b3bdcb;
}

.rc-legacy {
  border: 1px solid #eef2f7;
  border-radius: 6px;
  padding: 5px 8px;
  background: #fafbfd;
}

.rc-none {
  font-size: 12px;
  color: #b3bdcb;
}

/* 紧凑模式 */
.rc-card.compact {
  padding: 8px 10px;
}

.rc-card.compact .rc-body {
  grid-template-columns: 1fr 1fr;
}
</style>
