<script setup>
import { computed } from "vue";
import ReviewCommentCard from "./ReviewCommentCard.vue";

const props = defineProps({
  records: { type: Array, default: () => [] },
  reviewerNames: { type: Object, default: () => ({}) },
  compact: { type: Boolean, default: false },
});

// 决断记录（轮外把关/撤回修改）按 ID 前缀识别：不与轮内专家评语混排，
// 单独成区放在对应送审批次的轮次之后——决断是全部轮次外的最后一道。
function isDecisionRecord(record) {
  const id = record?.id || "";
  return id.startsWith("rec-final-") || id.startsWith("rec-unpublish-");
}

const groups = computed(() => {
  const grouped = new Map();
  for (const record of props.records || []) {
    if (isDecisionRecord(record)) continue;
    const attempt = record.attempt || 1;
    const round = record.round_number || 1;
    const key = `${attempt}:${round}`;
    if (!grouped.has(key)) grouped.set(key, { attempt, round, records: [] });
    grouped.get(key).records.push(record);
  }
  const rounds = [...grouped.values()]
    .sort((a, b) => a.attempt - b.attempt || a.round - b.round)
    .map((group) => ({
      ...group,
      decision: false,
      records: group.records.sort((a, b) => new Date(a.created_at) - new Date(b.created_at)),
    }));
  // 每个批次的决断区接在该批次轮次之后
  const byAttempt = new Map();
  for (const record of props.records || []) {
    if (!isDecisionRecord(record)) continue;
    const attempt = record.attempt || 1;
    if (!byAttempt.has(attempt)) byAttempt.set(attempt, []);
    byAttempt.get(attempt).push(record);
  }
  const result = [];
  for (const group of rounds) {
    result.push(group);
    if (group.round === Math.max(...rounds.filter((r) => r.attempt === group.attempt).map((r) => r.round))) {
      const decisions = byAttempt.get(group.attempt);
      if (decisions?.length) {
        result.push({
          attempt: group.attempt,
          round: 0,
          decision: true,
          records: decisions.sort((a, b) => new Date(a.created_at) - new Date(b.created_at)),
        });
        byAttempt.delete(group.attempt);
      }
    }
  }
  // 没有轮内记录、只有决断的批次（例如撤回修改后尚未重送）
  for (const [attempt, decisions] of byAttempt) {
    result.push({
      attempt,
      round: 0,
      decision: true,
      records: decisions.sort((a, b) => new Date(a.created_at) - new Date(b.created_at)),
    });
  }
  return result;
});

const attempts = computed(() => new Set((props.records || []).map((record) => record.attempt || 1)).size);
</script>

<template>
  <div v-if="groups.length" class="review-history">
    <section v-for="group in groups" :key="`${group.attempt}:${group.round}`" class="history-group">
      <div class="history-heading" :class="{ 'decision-heading': group.decision }">
        <span v-if="attempts > 1">第 {{ group.attempt }} 次送审</span>
        <strong>{{ group.decision ? "最终决断" : `第 ${group.round} 轮` }}</strong>
        <small>{{ group.records.length }} 条</small>
      </div>
      <div class="history-grid">
        <ReviewCommentCard
          v-for="record in group.records"
          :key="record.id"
          :record="record"
          :fallback-name="reviewerNames[record.expert_id] || record.expert_id"
          :compact="compact"
        />
      </div>
    </section>
  </div>
  <p v-else class="history-empty">暂无审核记录</p>
</template>

<style scoped>
.review-history {
  display: grid;
  gap: 12px;
  min-width: 0;
}

.history-group {
  min-width: 0;
}

.history-heading {
  display: flex;
  align-items: center;
  gap: 7px;
  margin-bottom: 6px;
  color: #778496;
  font-size: 11px;
}

.history-heading strong {
  color: #426783;
  font-size: 12px;
}

.history-heading.decision-heading strong {
  color: #8a5ae0;
}

.history-heading small {
  margin-left: auto;
  color: #9aa5b4;
}

.history-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 8px;
  align-items: start;
}

.history-empty {
  margin: 0;
  color: #8a96a5;
  font-size: 12px;
}

@media (max-width: 620px) {
  .history-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
