<script setup>
import { ref, computed, onMounted, onUnmounted } from "vue";
import { api } from "../api.js";

const toast = ref("");
const logs = ref([]);
const loading = ref(false);
const users = ref([]); // 用于把 actor 的 user ID 映射为可读用户名
const page = ref(1);
const pageSize = 200;
const total = ref(0);
const totalPages = computed(() => Math.max(1, Math.ceil(total.value / pageSize)));

// 筛选条件：行为下拉（含动态统计）+ 操作人 + 题目ID，可任意组合
const filterAction = ref("");
const filterActor = ref("");
const filterQuestion = ref("");
const actionOptions = ref([]); // [{ action, count }] 来自后端实际日志统计
const selectedLog = ref(null); // 点击行后弹窗展示的完整日志

// 全量行为目录：覆盖存量与新增的所有操作行为；未知行为回退显示原始代码。
const ACTION_LABELS = {
  // 题目生命周期
  create: "生成入库",
  update: "保存修改",
  delete: "删除题目",
  restore: "恢复历史版本",
  archive: "归档淘汰",
  publish: "审核定稿",
  unpublish: "撤回定稿",
  // 审核流程
  review: "轮内审核",
  final_approved: "决断通过",
  final_rejected: "决断驳回",
  final_revision_required: "决断退改",
  submit: "送审",
  resubmit: "重送审核",
  submit_bank: "题库批量送审",
  flow_create: "创建审核流程",
  flow_update: "修改审核流程",
  flow_delete: "删除审核流程",
  flow_revoke: "撤销送审",
  // 生成与批量
  generate: "单题生成",
  generate_batch: "批量生成",
  batch_retry: "批量重跑",
  batch_import: "导入批量结果",
  import: "批量导入",
  export: "导出",
  // 分享
  question_share_request: "申请入全局库",
  question_share_approve: "分享通过",
  question_share_reject: "分享驳回",
  // AI 服务
  ai_check_override: "AI 检查干预",
  ai_provider_create: "新增 AI 配置",
  ai_provider_update: "修改 AI 配置",
  ai_provider_delete: "删除 AI 配置",
  ai_provider_activate: "启用 AI 配置",
  // 专家库
  expert_create: "新增专家",
  expert_update: "更新专家",
  expert_delete: "删除专家",
  // 用户与角色
  user_create: "创建账号",
  user_register: "自主注册",
  user_update: "修改账号",
  user_update_permissions: "调整权限",
  user_reset_password: "重置密码",
  user_delete: "删除账号",
  role_create: "创建角色",
  role_update: "修改角色",
  role_delete: "删除角色",
  // 题库
  bank_create: "创建题库",
  bank_update: "修改题库",
  bank_delete: "删除题库",
  bank_collect: "题库归纳",
  // 知识点
  knowledge_create: "新增知识点",
  knowledge_update: "修改知识点",
  knowledge_delete: "删除知识点",
  knowledge_import: "导入知识点",
  knowledge_export: "导出知识点",
  knowledge_version_create: "创建大纲版本",
  knowledge_version_publish: "启用大纲版本",
  knowledge_version_delete: "删除大纲版本",
  // 认证
  auth_login: "登录成功",
  auth_login_failed: "登录失败",
  auth_rate_limited: "登录限流",
  auth_switch_role: "切换身份",
  auth_change_password: "修改密码",
  auth_update_profile: "更新资料",
};

// actor 显示名：user ID → 昵称/用户名
function actorName(actor) {
  if (!actor) return "-";
  const u = users.value.find((x) => x.id === actor);
  if (u) return `${u.display_name || u.username} (${u.username})`;
  return actor;
}

async function loadUsers() {
  try {
    const data = await api.listUsers();
    users.value = data.users || [];
  } catch (e) {
    console.error(e);
  }
}

async function loadActionOptions() {
  try {
    const data = await api.auditLogActions();
    actionOptions.value = data.actions || [];
  } catch (e) {
    console.error(e);
  }
}

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadLogs() {
  loading.value = true;
  try {
    const filter = {};
    if (filterAction.value) filter.action = filterAction.value;
    if (filterActor.value.trim()) filter.actor = filterActor.value.trim();
    if (filterQuestion.value.trim()) filter.question = filterQuestion.value.trim();
    const data = await api.auditLogs(page.value, pageSize, filter);
    logs.value = data.logs || [];
    total.value = data.total ?? logs.value.length;
  } catch (e) {
    showToast("加载失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

function doFilter() {
  page.value = 1;
  loadLogs();
}

function turnPage(delta) {
  const next = page.value + delta;
  if (next < 1 || next > totalPages.value) return;
  page.value = next;
  loadLogs();
}

function clearFilter() {
  filterAction.value = "";
  filterActor.value = "";
  filterQuestion.value = "";
  page.value = 1;
  loadLogs();
}

function actionText(action) {
  return ACTION_LABELS[action] || action;
}

function actionOptionLabel(stat) {
  const label = ACTION_LABELS[stat.action] || stat.action;
  return `${label}（${stat.count}）`;
}

function actionClass(action) {
  if (["delete", "flow_delete", "expert_delete", "user_delete", "role_delete", "bank_delete",
    "knowledge_delete", "knowledge_version_delete", "ai_provider_delete",
    "auth_login_failed", "auth_rate_limited", "final_rejected", "question_share_reject"].includes(action)) return "action-delete";
  if (["create", "generate", "generate_batch", "import", "export", "batch_import", "publish",
    "expert_create", "user_create", "user_register", "role_create", "bank_create",
    "flow_create", "knowledge_create", "knowledge_version_create", "ai_provider_create",
    "question_share_approve", "final_approved"].includes(action)) return "action-create";
  if (["review", "submit", "resubmit", "submit_bank", "question_share_request",
    "final_revision_required", "restore", "batch_retry"].includes(action)) return "action-review";
  if (["update", "restore", "expert_update", "auth_login", "auth_switch_role",
    "auth_change_password", "auth_update_profile", "user_update", "user_update_permissions",
    "user_reset_password", "role_update", "ai_provider_update", "ai_provider_activate",
    "ai_check_override"].includes(action)) return "action-update";
  if (action.startsWith("knowledge_") || action.startsWith("bank_") || action.startsWith("flow_")) return "action-flow";
  if (["unpublish", "archive"].includes(action)) return "action-archive";
  return "";
}

function formatTime(t) {
  if (!t) return "";
  const d = new Date(t);
  return d.toLocaleString("zh-CN", { hour12: false });
}

function openDetail(log) {
  selectedLog.value = log;
}

function closeDetail() {
  selectedLog.value = null;
}

function onKeydown(e) {
  if (e.key === "Escape") closeDetail();
}

async function copyText(text) {
  if (!text) return;
  try {
    await navigator.clipboard.writeText(text);
    showToast("已复制");
  } catch (e) {
    showToast("复制失败，请手动选择复制");
  }
}

onMounted(() => {
  loadLogs();
  loadUsers();
  loadActionOptions();
  window.addEventListener("keydown", onKeydown);
});

onUnmounted(() => {
  window.removeEventListener("keydown", onKeydown);
});
</script>

<template>
  <div class="audit-layout">
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>操作日志</h2>
        <small>共 {{ total }} 条记录 · 第 {{ page }} / {{ totalPages }} 页 · 点击行查看完整信息</small>
      </div>

      <div class="filter-row">
        <select v-model="filterAction" class="filter-select" @change="doFilter">
          <option value="">全部行为</option>
          <option v-for="opt in actionOptions" :key="opt.action" :value="opt.action">
            {{ actionOptionLabel(opt) }}
          </option>
        </select>
        <input
          v-model="filterActor"
          placeholder="按操作人（用户名）"
          class="filter-input"
          @keyup.enter="doFilter"
        />
        <input
          v-model="filterQuestion"
          placeholder="按题目ID"
          class="filter-input"
          @keyup.enter="doFilter"
        />
        <button class="primary-button" type="button" @click="doFilter">查询</button>
        <button class="ghost-button" type="button" @click="clearFilter">重置</button>
        <button class="ghost-button" type="button" :disabled="page <= 1 || loading" @click="turnPage(-1)">上一页</button>
        <button class="ghost-button" type="button" :disabled="page >= totalPages || loading" @click="turnPage(1)">下一页</button>
      </div>

      <div v-if="loading" class="loading">加载中...</div>
      <table v-else class="audit-table">
        <thead>
          <tr>
            <th>时间</th>
            <th>操作</th>
            <th>操作人</th>
            <th>题目ID</th>
            <th>详情</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="log in logs" :key="log.id" class="log-row" @click="openDetail(log)">
            <td class="time-cell">{{ formatTime(log.created_at) }}</td>
            <td>
              <span class="action-tag" :class="actionClass(log.action)">{{ actionText(log.action) }}</span>
            </td>
            <td class="actor-cell">{{ actorName(log.actor) }}</td>
            <td class="id-cell">{{ log.question_id || "-" }}</td>
            <td class="detail-cell">{{ log.detail }}</td>
          </tr>
          <tr v-if="!logs.length">
            <td colspan="5" class="empty">暂无日志</td>
          </tr>
        </tbody>
      </table>
    </section>

    <div v-if="selectedLog" class="modal-backdrop" @click.self="closeDetail">
      <div class="modal" role="dialog" aria-label="日志详情">
        <div class="modal-head">
          <h3>日志详情</h3>
          <button class="modal-close" type="button" aria-label="关闭" @click="closeDetail">✕</button>
        </div>
        <dl class="modal-body">
          <div class="field">
            <dt>日志ID</dt>
            <dd class="mono">{{ selectedLog.id }}</dd>
          </div>
          <div class="field">
            <dt>时间</dt>
            <dd>{{ formatTime(selectedLog.created_at) }}</dd>
          </div>
          <div class="field">
            <dt>操作行为</dt>
            <dd>
              <span class="action-tag" :class="actionClass(selectedLog.action)">{{ actionText(selectedLog.action) }}</span>
              <span class="action-code mono">{{ selectedLog.action }}</span>
            </dd>
          </div>
          <div class="field">
            <dt>操作人</dt>
            <dd>{{ actorName(selectedLog.actor) }} <span class="action-code mono">{{ selectedLog.actor }}</span></dd>
          </div>
          <div class="field">
            <dt>题目ID</dt>
            <dd class="mono wrap">
              <template v-if="selectedLog.question_id">
                {{ selectedLog.question_id }}
                <button class="copy-button" type="button" @click="copyText(selectedLog.question_id)">复制</button>
              </template>
              <template v-else>-</template>
            </dd>
          </div>
          <div class="field">
            <dt>详情</dt>
            <dd class="wrap detail-full">{{ selectedLog.detail || "-" }}
              <button v-if="selectedLog.detail" class="copy-button" type="button" @click="copyText(selectedLog.detail)">复制</button>
            </dd>
          </div>
        </dl>
      </div>
    </div>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
</template>

<style scoped>
.audit-layout {
  max-width: 100%;
}

.filter-row {
  display: flex;
  gap: 8px;
  margin-bottom: 16px;
  flex-wrap: wrap;
}

.filter-select {
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 10px;
  font-size: 13px;
  max-width: 240px;
}

.filter-input {
  flex: 1;
  min-width: 150px;
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 10px;
  font-size: 13px;
}

.audit-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.audit-table th {
  text-align: left;
  padding: 10px 12px;
  background: #f8fbff;
  color: #6e7b8f;
  font-weight: 600;
  border-bottom: 1px solid #e5ebf3;
}

.audit-table td {
  padding: 10px 12px;
  border-bottom: 1px solid #f0f3f7;
}

.log-row {
  cursor: pointer;
}

.audit-table tr:hover {
  background: #f8fbff;
}

.time-cell {
  font-size: 12px;
  color: #6e7b8f;
  white-space: nowrap;
}

.actor-cell {
  font-weight: 600;
  color: #172033;
}

.id-cell {
  font-family: monospace;
  font-size: 12px;
  color: #6e7b8f;
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.detail-cell {
  color: #435269;
  max-width: 300px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.action-tag {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 600;
}

.action-create { background: #f0fff8; color: #087c55; }
.action-update { background: #eff8ff; color: #0571dc; }
.action-delete { background: #fff0f0; color: #c54858; }
.action-review { background: #fff3e2; color: #dd8a00; }
.action-publish { background: #e9f8ef; color: #199e63; }
.action-flow { background: #f3efff; color: #7a5ae0; }
.action-archive { background: #f2f4f7; color: #5a6b84; }

.action-code {
  margin-left: 8px;
  font-size: 11px;
  color: #8a96a5;
}

.mono {
  font-family: monospace;
}

.wrap {
  overflow-wrap: anywhere;
}

.empty {
  text-align: center;
  color: #6e7b8f;
  padding: 30px !important;
}

.loading {
  text-align: center;
  color: #6e7b8f;
  padding: 30px;
}

.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(15, 23, 42, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
  padding: 20px;
}

.modal {
  background: #fff;
  border-radius: 12px;
  width: min(680px, 100%);
  max-height: 82vh;
  display: flex;
  flex-direction: column;
  box-shadow: 0 20px 50px rgba(15, 23, 42, 0.25);
}

.modal-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 20px 12px;
  border-bottom: 1px solid #eef2f7;
}

.modal-head h3 {
  margin: 0;
  font-size: 16px;
  color: #172033;
}

.modal-close {
  border: 0;
  background: transparent;
  font-size: 16px;
  color: #6e7b8f;
  cursor: pointer;
  padding: 4px 8px;
  border-radius: 6px;
}

.modal-close:hover {
  background: #f2f5fa;
  color: #172033;
}

.modal-body {
  padding: 8px 20px 20px;
  margin: 0;
  overflow-y: auto;
}

.field {
  display: grid;
  grid-template-columns: 84px minmax(0, 1fr);
  gap: 10px;
  padding: 10px 0;
  border-bottom: 1px dashed #f0f3f7;
}

.field:last-child {
  border-bottom: 0;
}

.field dt {
  color: #6e7b8f;
  font-size: 13px;
}

.field dd {
  margin: 0;
  color: #172033;
  font-size: 13px;
}

.detail-full {
  white-space: pre-wrap;
  line-height: 1.6;
}

.copy-button {
  margin-left: 8px;
  border: 1px solid #d9e3ef;
  background: #f8fbff;
  color: #0571dc;
  border-radius: 5px;
  font-size: 12px;
  padding: 2px 8px;
  cursor: pointer;
}

.copy-button:hover {
  background: #eff6ff;
}

@media (max-width: 700px) {
  .filter-row {
    gap: 6px;
  }

  .filter-select,
  .filter-input,
  .filter-row button {
    width: 100%;
    min-width: 0;
    flex: 1 1 100%;
  }

  .audit-table {
    display: block;
    width: 100%;
  }

  .audit-table thead {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    clip-path: inset(50%);
    white-space: nowrap;
  }

  .audit-table tbody {
    display: grid;
    gap: 8px;
  }

  .audit-table tr {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 3px;
    padding: 9px 10px;
    border: 1px solid #e5ebf3;
    border-radius: 8px;
    background: #fff;
  }

  .audit-table td {
    display: grid;
    grid-template-columns: 56px minmax(0, 1fr);
    gap: 8px;
    min-width: 0;
    padding: 4px 0;
    border: 0;
    white-space: normal;
    overflow-wrap: anywhere;
  }

  .audit-table td::before {
    color: #8a96a5;
    font-size: 11px;
  }

  .audit-table td:nth-child(1)::before { content: "时间"; }
  .audit-table td:nth-child(2)::before { content: "操作"; }
  .audit-table td:nth-child(3)::before { content: "操作人"; }
  .audit-table td:nth-child(4)::before { content: "题目ID"; }
  .audit-table td:nth-child(5)::before { content: "详情"; }

  .audit-table td[colspan] {
    display: block;
    text-align: center;
  }

  .audit-table .time-cell,
  .audit-table .id-cell,
  .audit-table .detail-cell {
    max-width: none;
    white-space: normal;
    overflow: visible;
    text-overflow: clip;
  }
}
</style>
