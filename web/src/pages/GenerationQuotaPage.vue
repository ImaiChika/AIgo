<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { api } from "../api.js";

const fields = [
  { key: "single_max_questions", label: "单次单题生成", unit: "道", min: 1, max: 20, help: "老师每次点击「生成」可提交的题数。" },
  { key: "batch_max_questions", label: "批量任务总量", unit: "道", min: 1, max: 500, help: "按「要点数 × 每点题数」计算，超过上限整单拒绝。" },
  { key: "single_active_per_user", label: "每人未完成单题任务", unit: "个", min: 1, max: 10, help: "等待中与执行中的单题任务合计。" },
  { key: "batch_active_per_user", label: "每人活跃批量任务", unit: "个", min: 1, max: 5, help: "进行中的批量任务合计；失败项重跑也受此限制。" },
  { key: "global_pending_questions", label: "全站队列占用题数", unit: "道", min: 20, max: 5000, help: "单题预留、批量待生成、批量导入预留与首次 AI 检查共用，同题跨阶段交叠时去重。" },
];
const quota = ref({});
const savedQuota = ref({});
const usage = ref({});
const loading = ref(true);
const refreshing = ref(false);
const saving = ref(false);
const error = ref("");
const refreshError = ref("");
const notice = ref("");
const lastRefreshAt = ref(0);
const configChangedWhileEditing = ref(false);
const dirty = computed(() => fields.some(field => String(quota.value[field.key] ?? "") !== String(savedQuota.value[field.key] ?? "")));
const lastRefreshText = computed(() => lastRefreshAt.value ? new Date(lastRefreshAt.value).toLocaleTimeString("zh-CN", { hour12: false }) : "尚未更新");
let pollTimer = null;
let disposed = false;
let requestTicket = 0;

async function load({ initial = false, force = false } = {}) {
  if (disposed || saving.value || refreshing.value || (!force && document.hidden)) return;
  const ticket = ++requestTicket;
  if (initial) loading.value = true;
  refreshing.value = true;
  refreshError.value = "";
  try {
    const data = await api.generationQuota();
    if (disposed || ticket !== requestTicket) return;
    const wasDirty = dirty.value;
    if (wasDirty && fields.some(field => Number(savedQuota.value[field.key]) !== Number(data.quota?.[field.key]))) {
      configChangedWhileEditing.value = true;
    }
    savedQuota.value = { ...data.quota };
    if (!wasDirty) quota.value = { ...data.quota };
    usage.value = data.usage || {};
    lastRefreshAt.value = Date.now();
    if (initial) error.value = "";
  } catch (e) {
    if (!disposed && ticket === requestTicket) {
      if (initial) error.value = "读取配额失败：" + e.message;
      else refreshError.value = "实时用量暂未更新：" + e.message;
    }
  } finally {
    refreshing.value = false;
    if (initial) loading.value = false;
  }
}

function resetDraft() {
  quota.value = { ...savedQuota.value };
  configChangedWhileEditing.value = false;
  error.value = "";
}

async function save() {
  if (saving.value) return;
  error.value = "";
  notice.value = "";
  const next = {};
  for (const field of fields) {
    const value = Number(quota.value[field.key]);
    if (!Number.isInteger(value) || value < field.min || value > field.max) {
      error.value = `${field.label}应为 ${field.min}–${field.max} 的整数`;
      return;
    }
    next[field.key] = value;
  }
  if (next.global_pending_questions < Math.max(next.single_max_questions, next.batch_max_questions)) {
    error.value = "全站队列占用题数不能小于单次单题或批量上限";
    return;
  }
  // 丢弃先前轮询的响应，防止保存后的新值被旧快照覆盖。
  requestTicket += 1;
  saving.value = true;
  try {
    const data = await api.saveGenerationQuota(next);
    quota.value = { ...data.quota };
    savedQuota.value = { ...data.quota };
    usage.value = data.usage || {};
    lastRefreshAt.value = Date.now();
    configChangedWhileEditing.value = false;
    notice.value = "配额已保存，新提交的任务立即按新值检查。";
  } catch (e) { error.value = e.message; }
  finally { saving.value = false; }
}
function onVisibilityChange() {
  if (!document.hidden) load();
}
onMounted(() => {
  load({ initial: true, force: true });
  pollTimer = window.setInterval(() => load(), 5000);
  document.addEventListener("visibilitychange", onVisibilityChange);
});
onBeforeUnmount(() => {
  disposed = true;
  requestTicket += 1;
  window.clearInterval(pollTimer);
  document.removeEventListener("visibilitychange", onVisibilityChange);
});
</script>

<template>
  <div class="quota-page">
    <header class="quota-header">
      <div><p class="quota-eyebrow">系统管理 / 运行配置</p><h2>生成任务配额</h2>
        <p>控制新任务进入队列的速度和规模。已接收的任务会继续执行。</p></div>
      <button type="button" class="quota-refresh" :disabled="loading || refreshing" @click="load({ force: true })">{{ refreshing ? "更新中…" : "立即刷新" }}</button>
    </header>

    <p v-if="error" class="quota-message error" role="alert">{{ error }}</p>
    <p v-if="refreshError" class="quota-message error" role="alert">{{ refreshError }}；上次成功更新：{{ lastRefreshText }}</p>
    <p v-if="notice" class="quota-message success" role="status">{{ notice }}</p>
    <p v-if="configChangedWhileEditing" class="quota-message warning" role="status">其他管理员修改了配额。当前输入已保留，请核对后保存，或载入最新配置。</p>
    <div v-if="loading" class="quota-panel">正在读取配额…</div>
    <template v-else>
      <div class="quota-live-meta"><span class="quota-live-dot"></span> 页面可见时每 5 秒自动更新 · 上次更新 {{ lastRefreshText }}</div>
      <section class="quota-usage" aria-label="当前用量">
        <div><span>全站队列占用题数</span><strong>{{ usage.pending_question_units ?? 0 }}<small> / {{ savedQuota.global_pending_questions }}</small></strong></div>
        <div><span>活跃单题任务</span><strong>{{ usage.active_single_runs ?? 0 }}</strong></div>
        <div><span>活跃批量任务</span><strong>{{ usage.active_batch_jobs ?? 0 }}</strong></div>
        <div><span>批量导入进行中</span><strong>{{ usage.active_batch_imports ?? 0 }}</strong></div>
        <div><span>待处理 AI 首检</span><strong>{{ usage.active_ai_check_tasks ?? 0 }}</strong></div>
        <div><span>导入待首检预留</span><strong>{{ usage.batch_import_reserved_questions ?? 0 }}</strong></div>
      </section>
      <p class="quota-breakdown">占用明细：单题预留 {{ usage.single_pending_questions ?? 0 }} 道 + 批量待生成 {{ usage.batch_pending_questions ?? 0 }} 道 + 导入待首检预留 {{ usage.batch_import_reserved_questions ?? 0 }} 道 + 首检 {{ usage.active_ai_check_tasks ?? 0 }} 道 − 同题阶段交叠 {{ usage.overlap_questions ?? 0 }} 道 = {{ usage.pending_question_units ?? 0 }} 道</p>
      <section class="quota-panel">
        <div class="quota-panel-heading"><h3>准入上限</h3><p>各项为全站统一规则，提交超额时老师会看到具体拒绝原因。</p></div>
        <div class="quota-grid">
          <label v-for="field in fields" :key="field.key" class="quota-field">
            <span class="quota-label">{{ field.label }}</span>
            <span class="quota-input-wrap"><input v-model.number="quota[field.key]" type="number" step="1" :min="field.min" :max="field.max" :aria-label="field.label" /><span>{{ field.unit }}</span></span>
            <small>{{ field.help }}可设 {{ field.min }}–{{ field.max }}。</small>
          </label>
        </div>
        <div class="quota-actions"><button v-if="dirty" type="button" class="quota-refresh" @click="resetDraft">载入最新配置</button><button type="button" class="primary-button" :disabled="saving" @click="save">{{ saving ? "保存中…" : "保存并立即生效" }}</button></div>
      </section>
      <p class="quota-footnote">只计生成、导入预留与首次 AI 检查的运行工作量；批量结果完成但尚未发起导入、审核、退修、驳回、发布和首检最终故障均不占用配额。导入会先确认余量，额度不足时提示稍后重试；已入队首检不会被取消。</p>
    </template>
  </div>
</template>

<style scoped>
.quota-page{max-width:1080px;margin:0 auto;padding:12px 4px 32px;color:#253742}
.quota-header{display:flex;justify-content:space-between;align-items:flex-start;gap:24px;margin-bottom:24px}
.quota-eyebrow{font-size:12px;letter-spacing:.08em;color:#577d87;margin:0 0 8px}
h2{font-size:26px;margin:0 0 9px}h3{font-size:18px;margin:0 0 5px}.quota-header p,.quota-panel-heading p{color:#657782;margin:0;line-height:1.6}
.quota-refresh{border:1px solid #b7cbd0;border-radius:8px;background:white;padding:9px 14px;color:#244e57;white-space:nowrap;cursor:pointer}
.quota-usage{display:grid;grid-template-columns:repeat(3,1fr);gap:14px;margin-bottom:18px}.quota-usage>div,.quota-panel{background:white;border:1px solid #dce6e8;border-radius:12px;box-shadow:0 4px 18px rgba(31,70,77,.05)}
.quota-live-meta{font-size:13px;color:#51717a;margin:-9px 0 13px;display:flex;align-items:center;gap:7px}.quota-live-dot{width:7px;height:7px;border-radius:50%;background:#3aa27b;display:inline-block}.quota-breakdown{margin:-5px 0 18px;color:#516b75;font-size:13px;line-height:1.6}
.quota-usage>div{padding:18px 20px}.quota-usage span{display:block;color:#687d85;font-size:13px;margin-bottom:7px}.quota-usage strong{font-size:26px}.quota-usage small{font-size:15px;color:#7a8b91;font-weight:400}
.quota-panel{padding:24px}.quota-panel-heading{margin-bottom:22px}.quota-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:17px}.quota-field{border:1px solid #e4ecee;border-radius:9px;padding:15px;display:flex;flex-direction:column;gap:8px}.quota-label{font-weight:650}.quota-input-wrap{display:flex;align-items:center;gap:10px}.quota-input-wrap input{width:150px;max-width:75%;border:1px solid #aebfc4;border-radius:7px;padding:9px 11px;font:inherit}.quota-field small{color:#697f87;line-height:1.5}.quota-actions{display:flex;justify-content:flex-end;gap:10px;margin-top:22px}.quota-message{padding:10px 14px;border-radius:8px;margin:0 0 16px}.quota-message.error{background:#fff0ed;color:#9c3d2e}.quota-message.success{background:#ecf7f0;color:#246740}.quota-message.warning{background:#fff7e7;color:#876126}.quota-footnote{color:#72858b;font-size:13px;margin-top:14px;line-height:1.6}
@media(max-width:700px){.quota-header{flex-direction:column}.quota-usage,.quota-grid{grid-template-columns:1fr}.quota-panel{padding:17px}}
</style>
