<script setup>
import { computed, onMounted, ref } from "vue";
import { api } from "../api.js";

const providers = ref([]);
const loading = ref(false);
const saving = ref(false);
const notice = ref("");
const error = ref("");
const editingId = ref("");
const showForm = ref(false);

const emptyForm = () => ({
  name: "",
  deployment: "cloud",
  base_url: "https://dashscope.aliyuncs.com/compatible-mode/v1",
  api_key: "",
  clear_api_key: false,
  generation_model: "qwen3.5-flash",
  check_model: "qwen3.5-flash",
  batch_api_key: "",
  clear_batch_api_key: false,
  batch_base_url: "https://dashscope.aliyuncs.com/compatible-mode/v1",
  batch_model: "qwen3.5-flash",
  active: true,
});
const form = ref(emptyForm());

const activeProvider = computed(() => providers.value.find((item) => item.active) || null);

function showNotice(message) {
  notice.value = message;
  error.value = "";
  window.clearTimeout(showNotice.timer);
  showNotice.timer = window.setTimeout(() => { notice.value = ""; }, 3500);
}

function showError(message) {
  error.value = message;
  notice.value = "";
}

async function loadProviders() {
  loading.value = true;
  try {
    const data = await api.listAIProviders();
    providers.value = data.providers || [];
  } catch (e) {
    showError("读取配置失败：" + e.message);
  } finally {
    loading.value = false;
  }
}

function startCreate() {
  editingId.value = "";
  form.value = emptyForm();
  showForm.value = true;
  error.value = "";
}

function startEdit(provider) {
  editingId.value = provider.id;
  form.value = {
    name: provider.name,
    deployment: provider.deployment,
    base_url: provider.base_url,
    api_key: "",
    clear_api_key: false,
    generation_model: provider.generation_model,
    check_model: provider.check_model || provider.generation_model,
    batch_api_key: "",
    clear_batch_api_key: false,
    batch_base_url: provider.batch_base_url || provider.base_url,
    batch_model: provider.batch_model || provider.generation_model,
    active: provider.active,
  };
  showForm.value = true;
  error.value = "";
}

function closeForm() {
  if (saving.value) return;
  showForm.value = false;
  editingId.value = "";
}

async function saveProvider() {
  if (saving.value) return;
  if (!form.value.name.trim() || !form.value.base_url.trim() || !form.value.generation_model.trim()) {
    showError("请填写配置名称、API 地址和生成模型");
    return;
  }
  if (form.value.deployment === "cloud" && !editingId.value && !form.value.api_key.trim()) {
    showError("新增云端配置时必须填写 API Key");
    return;
  }
  saving.value = true;
  try {
    const payload = {
      name: form.value.name.trim(),
      deployment: form.value.deployment,
      base_url: form.value.base_url.trim(),
      api_key: form.value.api_key.trim(),
      clear_api_key: form.value.clear_api_key,
      generation_model: form.value.generation_model.trim(),
      check_model: (form.value.check_model || form.value.generation_model).trim(),
      batch_api_key: form.value.batch_api_key.trim(),
      clear_batch_api_key: form.value.clear_batch_api_key,
      batch_base_url: form.value.batch_base_url.trim(),
      batch_model: (form.value.batch_model || form.value.generation_model).trim(),
      active: form.value.active,
    };
    if (editingId.value) {
      await api.updateAIProvider(editingId.value, payload);
      showNotice("配置已更新");
    } else {
      await api.createAIProvider(payload);
      showNotice("配置已保存并生效");
    }
    closeForm();
    await loadProviders();
  } catch (e) {
    showError("保存失败：" + e.message);
  } finally {
    saving.value = false;
  }
}

async function activateProvider(provider) {
  if (provider.active || saving.value) return;
  saving.value = true;
  try {
    await api.activateAIProvider(provider.id);
    showNotice(`已切换到「${provider.name}」`);
    await loadProviders();
  } catch (e) {
    showError("切换失败：" + e.message);
  } finally {
    saving.value = false;
  }
}

async function deleteProvider(provider) {
  if (saving.value) return;
  const warning = provider.active
    ? `「${provider.name}」正在使用中，删除后将回退到环境变量配置；如果没有环境变量，AI 出题会暂时不可用。确定删除吗？`
    : `确定删除配置「${provider.name}」吗？`;
  if (!window.confirm(warning)) return;
  saving.value = true;
  try {
    await api.deleteAIProvider(provider.id);
    showNotice("配置已删除");
    await loadProviders();
  } catch (e) {
    showError("删除失败：" + e.message);
  } finally {
    saving.value = false;
  }
}

function useSameModel() {
  form.value.check_model = form.value.generation_model;
}

onMounted(loadProviders);
</script>

<template>
  <div class="ai-config-page">
    <div class="ai-config-intro">
      <div>
        <p class="eyebrow">系统管理 / 运行配置</p>
        <h2>AI 服务配置</h2>
        <p class="intro-copy">配置实时 AI 出题与 AI 质量检查使用的 OpenAI-compatible 服务。API Key 只在后端加密保存，前端只显示脱敏结果。</p>
      </div>
      <button class="primary-button" type="button" @click="startCreate">＋ 新增配置</button>
    </div>

    <div v-if="notice" class="ai-notice success">{{ notice }}</div>
    <div v-if="error" class="ai-notice error">{{ error }}</div>

    <section class="ai-status-card">
      <div class="status-label">当前生效配置</div>
      <div v-if="activeProvider" class="status-main">
        <div>
          <strong>{{ activeProvider.name }}</strong>
          <span>{{ activeProvider.base_url }}</span>
        </div>
        <div class="status-meta">
          <span class="status-pill active">已启用</span>
          <span>出题：{{ activeProvider.generation_model }}</span>
          <span>检查：{{ activeProvider.check_model }}</span>
          <span>批量：{{ activeProvider.batch_model || activeProvider.generation_model }}</span>
        </div>
      </div>
      <div v-else class="status-empty">
        <strong>暂无数据库配置</strong>
        <span>系统会兼容读取启动环境变量；建议在这里保存一份配置后再部署。</span>
      </div>
    </section>

    <section class="ai-provider-list panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h3>配置列表</h3>
        <small>{{ providers.length }} 项 · 只有一项可以生效</small>
      </div>
      <div v-if="loading" class="loading-row">正在读取配置…</div>
      <div v-else-if="!providers.length" class="empty-row">还没有保存的服务配置，点击右上角新增。</div>
      <div v-else class="provider-table-wrap">
        <table class="provider-table">
          <thead>
            <tr><th>名称</th><th>API 地址</th><th>模型</th><th>凭证</th><th>状态</th><th>操作</th></tr>
          </thead>
          <tbody>
            <tr v-for="provider in providers" :key="provider.id">
              <td><strong>{{ provider.name }}</strong><small>{{ provider.deployment === 'local' ? '本地 / 网关' : '云端' }}</small></td>
              <td class="url-cell" :title="provider.base_url">{{ provider.base_url }}</td>
              <td><span>出题：{{ provider.generation_model }}</span><small>检查：{{ provider.check_model }}</small><small>批量：{{ provider.batch_model || provider.generation_model }}</small></td>
              <td><span :class="provider.api_key_configured ? 'key-ok' : 'key-missing'">实时：{{ provider.api_key_configured ? provider.api_key_masked : '未配置' }}</span><small>批量：{{ provider.batch_api_key_configured ? provider.batch_api_key_masked : '跟随实时密钥' }}</small></td>
              <td><span class="status-pill" :class="provider.active ? 'active' : 'inactive'">{{ provider.active ? '使用中' : '未启用' }}</span></td>
              <td class="actions-cell">
                <button v-if="!provider.active" class="text-button" type="button" :disabled="saving" @click="activateProvider(provider)">启用</button>
                <button class="text-button" type="button" :disabled="saving" @click="startEdit(provider)">编辑</button>
                <button class="text-button danger" type="button" :disabled="saving" @click="deleteProvider(provider)">删除</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <section class="ai-config-note">
      <strong>使用说明</strong>
      <p>生成模型和检查模型默认填写相同值；如果检查模型留空，后端会自动使用生成模型。保存或切换配置后，新生成和新进入队列的 AI 检查会使用新配置，无需重启服务。</p>
      <div class="batch-config-summary"><span>CLI 批量兼容配置</span><strong>Web 端使用本地单题 API 队列</strong><small>批量地址、批量 API Key 和批量模型仅供 CLI 兼容命令使用；Web 批量逐题调用实时生成服务，不使用 Files/Batches。</small></div>
      <p>当前配置仅超级管理员可查看和修改。Web 批量任务逐题调用单题 API；CLI 的旧批量命令仍保留独立兼容执行器。</p>
    </section>

    <div v-if="showForm" class="modal-overlay" @click.self="closeForm">
      <form class="ai-form-card" @submit.prevent="saveProvider">
        <div class="form-title-row">
          <div><p class="eyebrow">{{ editingId ? '编辑配置' : '新增配置' }}</p><h3>{{ editingId ? '修改 AI 服务' : '添加 AI 服务' }}</h3></div>
          <button class="modal-close" type="button" aria-label="关闭" @click="closeForm">×</button>
        </div>
        <div class="form-grid">
          <label class="field full-field"><span>配置名称 <b>*</b></span><input v-model="form.name" placeholder="如：百炼主账号" maxlength="80" /></label>
          <label class="field"><span>服务类型</span><select v-model="form.deployment"><option value="cloud">云端 / 需要 API Key</option><option value="local">本地或内网网关</option></select></label>
          <label class="field"><span>API 地址 <b>*</b></span><input v-model="form.base_url" placeholder="https://.../v1" /></label>
          <label class="field full-field"><span>API Key <em v-if="editingId">留空保留当前密钥</em><b v-else>*</b></span><input v-model="form.api_key" type="password" autocomplete="new-password" :placeholder="editingId ? '留空表示不替换已保存密钥' : '粘贴服务密钥'" :disabled="form.clear_api_key" /></label>
          <label v-if="editingId" class="clear-key-check"><input v-model="form.clear_api_key" type="checkbox" /> 清空已保存密钥（本次输入不会保存）</label>
          <label class="field"><span>AI 生成模型 <b>*</b></span><input v-model="form.generation_model" placeholder="qwen3.5-flash" /></label>
          <label class="field"><span>AI 检查模型 <b>*</b></span><input v-model="form.check_model" placeholder="默认与生成模型相同" /><button class="inline-link" type="button" @click="useSameModel">使用生成模型</button></label>
          <div class="full-field batch-form-section">
            <div class="batch-form-title">CLI 批量兼容配置 <small>Web 端使用本地单题 API 队列</small></div>
            <div class="batch-form-grid">
              <label class="field"><span>批量 API 地址</span><input v-model="form.batch_base_url" placeholder="默认与 API 地址相同" /></label>
              <label class="field"><span>批量模型</span><input v-model="form.batch_model" placeholder="默认与生成模型相同" /></label>
              <label class="field full-field"><span>批量 API Key <em v-if="editingId">留空保留当前密钥</em></span><input v-model="form.batch_api_key" type="password" autocomplete="new-password" :placeholder="editingId ? '留空表示不替换已保存密钥' : '可选；默认使用实时 API Key'" :disabled="form.clear_batch_api_key" /></label>
              <label v-if="editingId" class="clear-key-check"><input v-model="form.clear_batch_api_key" type="checkbox" /> 清空批量 API Key</label>
            </div>
          </div>
        </div>
        <label class="active-check"><input v-model="form.active" type="checkbox" /> 保存后立即启用此配置</label>
        <p class="form-hint">地址只接受 http/https 完整地址，例如百炼兼容接口通常以 <code>/v1</code> 结尾。API Key 不会回显到页面。</p>
        <div class="form-actions"><button class="ghost-button" type="button" :disabled="saving" @click="closeForm">取消</button><button class="primary-button" type="submit" :disabled="saving">{{ saving ? '保存中…' : '保存配置' }}</button></div>
      </form>
    </div>
  </div>
</template>

<style scoped>
.ai-config-page { display: grid; gap: 16px; width: 100%; max-width: 1220px; min-width: 0; }
.ai-config-intro { display: flex; align-items: flex-end; justify-content: space-between; gap: 20px; }
.ai-config-intro h2 { margin: 3px 0 7px; font-size: 23px; color: #172033; }
.eyebrow { margin: 0; color: #7c8da3; font-size: 11px; letter-spacing: .08em; }
.intro-copy { max-width: 700px; margin: 0; color: #6e7b8f; font-size: 13px; line-height: 1.65; }
.ai-notice { border-radius: 7px; padding: 10px 13px; font-size: 13px; }
.ai-notice.success { border: 1px solid #c5eadb; background: #f1fbf7; color: #16724f; }
.ai-notice.error { border: 1px solid #f0c9cf; background: #fff6f7; color: #a23b4b; }
.ai-status-card { border: 1px solid #cadff1; border-left: 4px solid #1385f8; border-radius: 8px; padding: 15px 17px; background: #f8fbff; }
.status-label { margin-bottom: 8px; color: #6e7b8f; font-size: 12px; }
.status-main { display: flex; align-items: center; justify-content: space-between; gap: 20px; }
.status-main strong, .status-empty strong { display: block; color: #172033; font-size: 15px; }
.status-main span:not(.status-pill), .status-empty span { display: block; margin-top: 4px; color: #6e7b8f; font-size: 12px; }
.status-meta { display: flex; align-items: center; gap: 14px; color: #52657c; font-size: 12px; text-align: right; }
.status-meta span:not(.status-pill) { white-space: nowrap; }
.status-pill { display: inline-flex; align-items: center; min-height: 24px; padding: 0 8px; border: 1px solid #d8e2ec; border-radius: 12px; color: #68788c; background: #fff; font-size: 11px; white-space: nowrap; }
.status-pill.active { border-color: #bce5d5; color: #17734f; background: #f1fbf7; }
.status-pill.inactive { color: #78889a; background: #f9fbfd; }
.section-heading { margin-bottom: 10px; }
.ai-provider-list, .provider-table-wrap { min-width: 0; max-width: 100%; }
.provider-table-wrap { overflow-x: auto; }
.provider-table { width: 100%; border-collapse: collapse; min-width: 820px; }
.provider-table th { padding: 10px 9px; border-bottom: 1px solid #e5ebf3; color: #7d8ca0; font-size: 11px; font-weight: 600; text-align: left; white-space: nowrap; }
.provider-table td { padding: 13px 9px; border-bottom: 1px solid #edf1f5; color: #52657c; font-size: 12px; vertical-align: middle; }
.provider-table tr:last-child td { border-bottom: 0; }
.provider-table td strong { display: block; color: #172033; font-size: 13px; }
.provider-table td small { display: block; margin-top: 4px; color: #8a98a9; font-size: 11px; }
.url-cell { max-width: 245px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.key-ok { color: #17734f; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 11px; }
.key-missing { color: #b14c58; }
.actions-cell { white-space: nowrap; }
.actions-cell .text-button { margin-right: 12px; font-size: 12px; font-weight: 500; }
.text-button.danger { color: #b14c58; }
.empty-row, .loading-row { padding: 30px 10px; color: #7c8da3; font-size: 13px; text-align: center; }
.ai-config-note { border: 1px solid #e7edf4; border-radius: 8px; padding: 14px 16px; color: #68798d; background: #fff; font-size: 12px; line-height: 1.65; }
.ai-config-note strong { color: #44566e; }
.ai-config-note p { margin: 5px 0 0; }
.batch-config-summary { display: grid; gap: 3px; margin-top: 12px; border: 1px solid #dce8f7; border-left: 3px solid #12b981; border-radius: 6px; padding: 9px 11px; background: #f6fcf9; }
.batch-config-summary span { color: #6e7b8f; font-size: 11px; }
.batch-config-summary strong { color: #16724f; font-size: 12px; }
.batch-config-summary small { color: #718197; font-size: 11px; line-height: 1.55; }
.modal-overlay { position: fixed; inset: 0; z-index: 100; display: grid; place-items: center; padding: 20px; background: rgba(18, 35, 55, .42); }
.ai-form-card { width: min(650px, 100%); max-height: calc(100vh - 40px); overflow-y: auto; border: 1px solid #dce6f0; border-radius: 10px; padding: 22px; background: #fff; box-shadow: 0 22px 50px rgba(18, 35, 55, .18); }
.form-title-row { display: flex; align-items: flex-start; justify-content: space-between; margin-bottom: 19px; }
.form-title-row h3 { margin: 4px 0 0; color: #172033; font-size: 18px; }
.modal-close { width: 30px; height: 30px; border: 1px solid #e1e8f0; border-radius: 5px; color: #75869a; background: #fff; font-size: 20px; line-height: 1; }
.form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
.field { position: relative; display: grid; gap: 6px; }
.field span { color: #53657b; font-size: 12px; }
.field b { color: #c54858; font-weight: 600; }
.field em { margin-left: 4px; color: #8b9aab; font-style: normal; font-size: 11px; }
.field input, .field select { width: 100%; min-height: 38px; border: 1px solid #d7e1ec; border-radius: 6px; padding: 0 10px; color: #172033; background: #fff; outline: none; font-size: 13px; }
.field input:focus, .field select:focus { border-color: #68aee7; box-shadow: 0 0 0 3px rgba(19, 133, 248, .09); }
.full-field { grid-column: 1 / -1; }
.batch-form-section { border: 1px solid #dce8f7; border-left: 3px solid #12b981; border-radius: 7px; padding: 11px 12px; background: #f7fcfa; }
.batch-form-title { margin-bottom: 10px; color: #16724f; font-size: 12px; font-weight: 700; }
.batch-form-title small { margin-left: 5px; color: #718197; font-size: 11px; font-weight: 400; }
.batch-form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.clear-key-check, .active-check { display: flex; align-items: center; gap: 7px; margin-top: 10px; color: #65778c; font-size: 12px; }
.clear-key-check { color: #a23b4b; }
.inline-link { position: absolute; right: 5px; bottom: 5px; border: 0; padding: 4px 6px; color: #1472c4; background: #fff; font-size: 11px; }
.form-hint { margin: 13px 0 0; color: #8a98a9; font-size: 11px; line-height: 1.6; }
.form-hint code { color: #52657c; }
.form-actions { display: flex; justify-content: flex-end; gap: 9px; margin-top: 20px; padding-top: 15px; border-top: 1px solid #edf1f5; }
@media (max-width: 720px) {
  .ai-config-intro, .status-main { align-items: stretch; flex-direction: column; }
  .ai-config-intro .primary-button { align-self: flex-start; }
  .status-meta { align-items: flex-start; flex-wrap: wrap; text-align: left; }
}
@media (max-width: 560px) { .form-grid, .batch-form-grid { grid-template-columns: 1fr; } .full-field { grid-column: auto; } .ai-form-card { padding: 17px; } }
</style>
