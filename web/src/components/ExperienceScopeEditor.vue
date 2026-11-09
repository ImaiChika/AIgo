<script setup>
import { computed, ref, watch, onBeforeUnmount } from 'vue';
import { api } from '../api.js';
import { currentUser, getToken } from '../auth.js';
import { learningScope } from '../personalLearning.js';
const props = defineProps({ modelValue: { type: Object, required: true }, points: { type: Array, default: () => [] }, disabled: Boolean, label: { type: String, default: '适用范围' } });
const emit = defineEmits(['update:modelValue']);
const open = ref(false), versions = ref([]), version = ref(''), query = ref(''), results = ref([]), busy = ref(false), error = ref('');
const known = ref({});
const identity = computed(() => learningScope(currentUser.value, getToken()));
let ticket = 0, disposed = false;
watch(() => props.points, value => { for (const p of value) known.value[p.id] = p.topic || p.outline_code; }, { immediate: true });
watch(identity, () => { ++ticket; open.value = false; versions.value = []; results.value = []; known.value = {}; busy.value = false; error.value = ''; });
function changeKind(kind) {
  if (props.disabled) return;
  emit('update:modelValue', kind === 'point' ? { kind, point_ids: props.points.map(p => p.id) } : kind === 'profession' ? { kind, profession: props.modelValue.profession || props.points[0]?.subject || '' } : { kind });
}
function togglePoint(point) {
  if (props.disabled) return;
  known.value[point.id] = point.topic || point.outline_code;
  const ids = new Set(props.modelValue.point_ids || []); ids.has(point.id) ? ids.delete(point.id) : ids.add(point.id);
  emit('update:modelValue', { kind: 'point', point_ids: [...ids] });
}
async function choosePoints() {
  if (props.disabled || busy.value) return;
  open.value = !open.value; if (!open.value) { ++ticket; return; }
  const request = ++ticket, session = identity.value; busy.value = true; error.value = '';
  try { const data = await api.kpVersions(); if (!disposed && request === ticket && session === identity.value) { versions.value = (data.versions || []).filter(v => v.status === 'published'); version.value = versions.value.some(v => v.id === version.value) ? version.value : data.default_version_id || versions.value[0]?.id || ''; } }
  catch(e) { if (!disposed && request === ticket && session === identity.value) error.value = e.message; }
  finally { if (!disposed && request === ticket && session === identity.value) busy.value = false; }
}
async function search() {
  if (props.disabled || !version.value || busy.value) return;
  const request = ++ticket, session = identity.value, selectedVersion = version.value; busy.value = true; error.value = '';
  try { const data = await api.searchKPFiltered({ version_id: selectedVersion, q: query.value, page_size: 20 }); if (!disposed && request === ticket && session === identity.value && selectedVersion === version.value) results.value = data.points || []; }
  catch(e) { if (!disposed && request === ticket && session === identity.value) error.value = e.message; }
  finally { if (!disposed && request === ticket && session === identity.value) busy.value = false; }
}
watch(version, () => { ++ticket; results.value = []; busy.value = false; });
onBeforeUnmount(() => { disposed = true; ++ticket; });
</script>
<template>
<div class="experience-scope-editor">
  <label>{{ label }}<select :aria-label="label" :disabled="disabled" :value="modelValue.kind" @change="changeKind($event.target.value)"><option value="point">指定知识点</option><option value="profession">指定专业</option><option value="all">本人全部临床题</option></select></label>
  <label v-if="modelValue.kind==='profession'">适用专业<input :aria-label="`${label}专业`" :disabled="disabled" :value="modelValue.profession" maxlength="100" placeholder="例如：内科" @input="emit('update:modelValue',{kind:'profession',profession:$event.target.value})"/></label>
  <template v-if="modelValue.kind==='point'">
    <div class="scope-points"><span v-for="(id,index) in modelValue.point_ids||[]" :key="id">{{ known[id] || `已绑定知识点 ${index+1}` }}<button type="button" :disabled="disabled" :aria-label="`移除知识点 ${index+1}`" @click="togglePoint({id,topic:known[id]})">×</button></span></div>
    <button class="text-button" type="button" :disabled="disabled||busy" @click="choosePoints">{{ open?'收起知识点选择':'调整知识点' }}</button>
    <div v-if="open" class="scope-picker"><select v-model="version" aria-label="经验适用大纲版本" :disabled="disabled"><option value="">请选择大纲</option><option v-for="v in versions" :key="v.id" :value="v.id">{{v.name}}</option></select><input v-model="query" aria-label="搜索经验适用知识点" placeholder="知识点名称或代码" :disabled="disabled" @keydown.enter.prevent="search"/><button class="ghost-button" type="button" :disabled="disabled||busy||!version" @click="search">搜索知识点</button><label v-for="p in results" :key="p.id" class="scope-point-option"><input type="checkbox" :checked="modelValue.point_ids?.includes(p.id)" :disabled="disabled" @change="togglePoint(p)"/>{{p.outline_code}} · {{p.topic}}</label><small>每次最多20项，请按名称缩小范围。</small></div>
  </template>
  <p v-if="error" role="alert">{{error}}</p>
</div>
</template>
<style scoped>
.experience-scope-editor{min-width:0}.experience-scope-editor label{display:flex;flex-direction:column;gap:6px;margin:10px 0;font-size:13px}.experience-scope-editor input,.experience-scope-editor select{box-sizing:border-box;width:100%;max-width:100%;min-width:0;padding:8px;border:1px solid #d6deea;border-radius:5px;font:inherit;background:#fff;color:inherit}.scope-points{display:flex;flex-wrap:wrap;gap:6px}.scope-points span{padding:4px 8px;background:#edf4fc;border-radius:5px;font-size:12px;overflow-wrap:anywhere}.scope-points button{border:0;background:transparent;color:#567493;cursor:pointer}.scope-picker{display:flex;flex-wrap:wrap;gap:7px;padding:10px;border:1px solid #e3eaf2;border-radius:6px;margin-top:8px}.scope-picker>input{flex:1;min-width:120px}.scope-picker>select{flex-basis:100%}.scope-picker .scope-point-option{flex-direction:row;align-items:center;flex-basis:100%;margin:3px 0}.scope-point-option input{width:auto}.scope-picker small{display:block;color:#748396}.experience-scope-editor p{color:#b91c1c;overflow-wrap:anywhere}
</style>
