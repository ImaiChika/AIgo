<script setup>
import { onMounted, onBeforeUnmount, ref } from 'vue';
import { api } from '../api.js';
import HelpHint from './HelpHint.vue';
const form = ref({ generation_temperature: 0.4, check_temperature: 0.2, revision: 0 });
const loading = ref(true), loaded = ref(false), saving = ref(false), error = ref(''), notice = ref('');
let disposed = false, timer;
async function load() {
  loading.value = true; error.value = ''; notice.value = '';
  try { const data = await api.getModelSampling(); if (!disposed) { form.value = data; loaded.value = true; } }
  catch (e) { if (!disposed) { loaded.value = false; error.value = '读取调用参数失败：' + e.message; } }
  finally { if (!disposed) loading.value = false; }
}
async function reload() {
  if (saving.value || loading.value) return;
  if (loaded.value && !window.confirm('重新读取会替换当前未保存的参数，是否继续？')) return;
  await load();
}
async function save() {
  if (saving.value || loading.value || !loaded.value) return;
  error.value = ''; notice.value = ''; window.clearTimeout(timer);
  if (![form.value.generation_temperature, form.value.check_temperature].every(v => Number.isFinite(v) && v >= 0 && v <= 2)) {
    error.value = '温度须在0到2之间'; return;
  }
  saving.value = true;
  try { const data = await api.saveModelSampling({ ...form.value }); if (!disposed) { form.value = data; notice.value = '调用参数已保存'; timer = window.setTimeout(() => { notice.value = ''; }, 3500); } }
  catch (e) { if (!disposed) error.value = '保存调用参数失败：' + e.message; }
  finally { if (!disposed) saving.value = false; }
}
onMounted(load);
onBeforeUnmount(() => { disposed = true; window.clearTimeout(timer); });
</script>
<template>
  <section class="sampling-settings panel" aria-label="出题与首检调用参数">
    <div class="sampling-heading"><h3>出题与首检参数</h3><HelpHint label="调用参数说明">温度影响采样方式，不关闭思考，也不改变题目格式、知识依据校验、首检标准或重试预算。服务提供方可能调整实际温度。AI总结仍在独立标签页配置。</HelpHint></div>
    <p v-if="loading" class="loading-note">正在读取调用参数…</p>
    <form v-else @submit.prevent="save">
      <div v-if="loaded" class="sampling-grid">
        <label class="sampling-card"><span class="field-title">AI出题<HelpHint label="出题温度说明">默认0.4，同时用于单题和批量。新受理任务冻结此值；已受理任务及其重试继续使用原值。</HelpHint></span><small>单题 · 批量</small><input v-model.number="form.generation_temperature" aria-label="出题温度" type="number" min="0" max="2" step="0.1" :disabled="saving" /></label>
        <label class="sampling-card"><span class="field-title">AI首检<HelpHint label="首检温度说明">默认0.2。新入队的首次检查冻结此值；已有检查任务及其重试不变。独立首检服务同样使用这里的温度。</HelpHint></span><small>首次质量检查</small><input v-model.number="form.check_temperature" aria-label="首检温度" type="number" min="0" max="2" step="0.1" :disabled="saving" /></label>
      </div>
      <p v-if="error" class="sampling-error" role="alert">{{ error }}</p>
      <div class="sampling-footer"><span v-if="notice" class="sampling-notice" role="status">{{ notice }}</span><button class="ghost-button" type="button" :disabled="saving" @click="reload">重新读取</button><button v-if="loaded" class="primary-button" type="submit" :disabled="saving">{{ saving ? '保存中…' : '保存调用参数' }}</button></div>
    </form>
  </section>
</template>
<style scoped>
.sampling-settings{max-width:820px;padding:22px;min-width:0}.sampling-heading{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:22px}.sampling-heading h3{margin:0;color:#172033;font-size:18px}.loading-note{font-size:13px;color:#65778c}.sampling-grid{display:grid;grid-template-columns:1fr 1fr;gap:16px}.sampling-card{display:grid;gap:10px;padding:18px;border:1px solid #dce6f0;border-radius:8px;background:#f8fbff;min-width:0}.field-title{display:flex;align-items:center;justify-content:space-between;gap:8px;color:#253d57;font-size:15px;font-weight:600}.sampling-card small{color:#65778c;font-size:12px}.sampling-card input{box-sizing:border-box;width:100%;min-width:0;height:40px;border:1px solid #d7e1ec;border-radius:6px;padding:0 11px;background:#fff;color:#172033;font-size:14px}.sampling-card input:focus-visible{outline:2px solid #8bbcf3;outline-offset:2px}.sampling-footer{display:flex;justify-content:flex-end;align-items:center;gap:12px;margin-top:22px;padding-top:18px;border-top:1px solid #edf1f5;flex-wrap:wrap}.sampling-notice{color:#17734f;font-size:12px}.sampling-error{color:#a23b4b;background:#fff6f7;border:1px solid #f0c9cf;border-radius:6px;padding:10px 12px;font-size:13px;overflow-wrap:anywhere}button:disabled{opacity:.6;cursor:wait}@media(max-width:560px){.sampling-settings{padding:16px}.sampling-grid{grid-template-columns:1fr}.sampling-footer button{min-height:40px}}
</style>
