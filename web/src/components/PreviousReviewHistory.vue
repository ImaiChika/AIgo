<script setup>
import HelpHint from "./HelpHint.vue";
import { ref, watch, onBeforeUnmount } from 'vue';
import { api } from '../api.js';
import { useLiveRefresh } from '../useLiveRefresh.js';
import ReviewCommentCard from './ReviewCommentCard.vue';
const props = defineProps({ questionId: { type: String, required: true } });
const items = ref([]);
let ticket = 0;
async function load() {
 const current = ++ticket, id = props.questionId;
 if (!id) { items.value = []; return; }
 try { const data = await api.reviewHistory(id); if (current === ticket && id === props.questionId) items.value = (data.items || []).filter(x => x.superseded); }
 catch { if (current === ticket) items.value = []; }
}
watch(() => props.questionId, () => { items.value = []; load(); }, { immediate: true });
useLiveRefresh(load);
onBeforeUnmount(() => ++ticket);
</script>
<template>
 <details v-if="items.length" class="previous-review-history">
  <summary>历史送审与退修原因（{{ items.length }} 个批次）</summary>
  <section v-for="item in items" :key="item.task_id"><div class="ui-help-heading"><h4>第 {{ item.attempt }} 次送审 · {{ item.flow_name }} · v{{ item.question_version }}</h4><HelpHint label="历史送审记录说明">本批次已由新的送审任务接替，仍保留原流程和有权查看的历史意见。</HelpHint></div><div v-for="record in item.records" :key="record.id"><small>第 {{ record.round_number }} 轮</small><ReviewCommentCard :record="record" compact /></div><p v-if="!item.records.length" class="history-note">没有当前身份可查看的历史意见。</p></section>
 </details>
</template>
<style scoped>
.previous-review-history{margin:18px 0;padding:13px 0;border-top:1px solid #e3ebf3;font-size:12px}.previous-review-history summary{cursor:pointer;color:#3775aa;font-weight:600}.previous-review-history section{padding:10px 0}.previous-review-history h4{font-size:12px;font-weight:500}.history-note,.previous-review-history small{font-size:11px;color:#7b899a;line-height:1.8}.previous-review-history small{display:block;margin:10px 0 6px}
</style>
