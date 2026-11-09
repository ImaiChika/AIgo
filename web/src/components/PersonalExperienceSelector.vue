<script setup>
import HelpHint from './HelpHint.vue';
import { computed, ref, watch, onMounted, onActivated, onDeactivated, onBeforeUnmount } from 'vue';
import { api } from '../api.js';
import { currentUser, getToken } from '../auth.js';
import { refreshSessionIdentity } from '../sessionIdentity.js';
import { learningCapabilities, learningScope, createLearningRequestGate } from '../personalLearning.js';
const props = defineProps({ modelValue: { type: Boolean, default: true } });
const emit = defineEmits(['update:modelValue']);
const caps = computed(() => learningCapabilities(currentUser.value?.permissions));
const identity = computed(() => learningScope(currentUser.value, getToken()));
const count = ref(null), busy = ref(false), error = ref('');
const gate = createLearningRequestGate(); let disposed = false, active = true;
async function load() {
  if (disposed || !active || !caps.value.use || !caps.value.libraryView || !getToken()) return;
  const ticket = gate.start(identity.value); busy.value = true; error.value = '';
  try { await refreshSessionIdentity(); if (disposed || !active || !gate.accepts(ticket, identity.value) || !caps.value.use) return; const data = await api.personalExperiences({ status: 'active', limit: 1 }); if (!disposed && gate.accepts(ticket, identity.value)) count.value = data.total ?? data.experiences?.length ?? 0; }
  catch(e) { if (!disposed && gate.accepts(ticket, identity.value)) error.value = e.message; }
  finally { if (!disposed && gate.accepts(ticket, identity.value)) busy.value = false; }
}
watch(identity, () => { gate.invalidate(); count.value = null; error.value = ''; busy.value = false; if (caps.value.use) load(); });
onMounted(load); onActivated(() => { active = true; load(); });
onDeactivated(() => { active = false; gate.invalidate(); busy.value = false; });
onBeforeUnmount(() => { disposed = true; gate.invalidate(); });
</script>
<template>
<section v-if="caps.use" class="personal-experience-selector"><div class="experience-selector-heading"><label><input type="checkbox" aria-label="应用命题规则" :checked="modelValue" @change="emit('update:modelValue',$event.target.checked)"/>应用命题规则</label><RouterLink v-if="caps.libraryView" to="/my-experiences">管理个人知识库</RouterLink><HelpHint label="命题规则采用说明">仅控制“命题规则”模块，按本人的启用状态、考点和专业匹配，直接生成和知识库模式均可采用。知识卡须另行勾选，在知识库工作流中采用。实际版本以任务记录为准，修改或停用只影响之后的新任务。</HelpHint></div><small v-if="busy">核对启用经验中…</small><small v-else-if="count===0">暂无启用条目</small><small v-else-if="count!=null">已启用 {{count}} 条</small><p v-if="error" class="experience-selector-error" role="status">读取经验数量失败：{{error}} <button class="text-button" type="button" @click="load">重试</button></p></section>
</template>
<style scoped>
.personal-experience-selector{margin:14px 0;padding:13px;border:1px solid #dce6ef;border-radius:7px;background:#f8fbfe;color:#425a73;font-size:13px;min-width:0}.experience-selector-heading{display:flex;align-items:center;justify-content:space-between;gap:10px;flex-wrap:nowrap}.experience-selector-heading label{flex:1;min-width:0;display:flex;align-items:center;gap:8px;font-weight:600}.experience-selector-heading a{color:#37678f;white-space:nowrap;font-size:12px}.personal-experience-selector p,.personal-experience-selector small{line-height:1.8;overflow-wrap:anywhere}.personal-experience-selector p{margin:9px 0}.personal-experience-selector small{color:#748396}.experience-selector-error{color:#9b6311}
</style>
