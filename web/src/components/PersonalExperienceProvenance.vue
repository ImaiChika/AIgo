<script setup>
import HelpHint from "./HelpHint.vue";
import { computed, ref, watch, onBeforeUnmount } from 'vue';
import { api } from '../api.js';
import { currentUser, getToken } from '../auth.js';
import { refreshSessionIdentity } from '../sessionIdentity.js';
import { learningCapabilities, learningScope, createLearningRequestGate, scopeLabel } from '../personalLearning.js';
const props = defineProps({ question: { type: Object, default: null } });
const identity = computed(() => learningScope(currentUser.value, getToken()));
const allowed = computed(() => Boolean(getToken() && props.question?.owner_id === currentUser.value?.id && learningCapabilities(currentUser.value?.permissions).history));
const key = computed(() => `${identity.value}:${props.question?.id}`);
const open = ref(false), busy = ref(false), snapshot = ref(null), error = ref(''); const gate = createLearningRequestGate(); let disposed = false;
watch(key, () => { gate.invalidate(); open.value = false; snapshot.value = null; busy.value = false; error.value = ''; }, { flush: 'sync' });
async function toggle() {
  if (!allowed.value || busy.value) return;
  open.value = !open.value; if (!open.value) { gate.invalidate(); return; }
  const ticket = gate.start(key.value); busy.value = true; error.value = '';
  try { await refreshSessionIdentity(); if (disposed || !gate.accepts(ticket, key.value) || !allowed.value) return; const data = await api.questionPersonalExperiences(props.question.id); if (!disposed && gate.accepts(ticket, key.value)) snapshot.value = data.snapshot; }
  catch(e) { if (!disposed && gate.accepts(ticket, key.value)) error.value = e.message; }
  finally { if (!disposed && gate.accepts(ticket, key.value)) busy.value = false; }
}
onBeforeUnmount(() => { disposed = true; gate.invalidate(); });
</script>
<template>
<section v-if="allowed" class="personal-experience-provenance"><button class="ghost-button" type="button" :aria-expanded="open" :disabled="busy" @click="toggle">{{open?'收起采用的个人经验':'查看采用的个人经验'}}</button><div v-if="open"><p v-if="busy">正在读取冻结采用记录…</p><p v-if="error" class="experience-provenance-error" role="alert">{{error}}</p><p v-if="!busy&&!error&&!snapshot?.entries?.length">本题首次生成未采用个人经验。</p><template v-if="snapshot"><HelpHint label="采用版本说明">以下是首次生成时冻结的个人经验，修订或停用后仍保留当时版本；引用不代表医学正确性或稳定提质。</HelpHint><article v-for="item in snapshot.entries||[]" :key="item.id"><strong>{{item.title}} · 版本 {{item.revision}}</strong><small>{{scopeLabel(item.scope)}} · {{item.match_reason}}</small><ul><li v-for="r in item.rules||[]" :key="r.id">{{r.text}}<span v-if="r.medical_change">（涉及医学变化）</span></li></ul></article><p v-if="snapshot.excluded_count">另有 {{snapshot.excluded_count}} 条匹配经验受数量或篇幅限制未纳入。</p></template></div></section>
</template>
<style scoped>
.personal-experience-provenance{margin:14px 0;min-width:0;font-size:13px;color:#425a73}.personal-experience-provenance article{padding:12px;margin:12px 0;background:#f6f9fd;border-radius:6px}.personal-experience-provenance p,.personal-experience-provenance li{line-height:1.8;overflow-wrap:anywhere}.personal-experience-provenance small{display:block;margin-top:6px;line-height:1.8;color:#748396}.personal-experience-provenance ul{padding-left:20px}.experience-provenance-note{color:#748396}.experience-provenance-error{color:#b91c1c}
</style>
