<script setup>
import { ref, onMounted } from "vue";
import { api } from "../api.js";

const toast = ref("");
const logs = ref([]);
const loading = ref(false);
const filterType = ref("all");
const filterValue = ref("");
const users = ref([]); // 用于把 actor 的 user ID 映射为可读用户名

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

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  toast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadLogs() {
  loading.value = true;
  try {
    let data;
    if (filterType.value === "question" && filterValue.value) {
      data = await api.auditLogsByQuestion(filterValue.value);
    } else if (filterType.value === "actor" && filterValue.value) {
      data = await api.auditLogsByActor(filterValue.value);
    } else {
      data = await api.auditLogs(200);
    }
    logs.value = data.logs || [];
  } catch (e) {
    showToast("加载失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

function doFilter() {
  loadLogs();
}

function clearFilter() {
  filterType.value = "all";
  filterValue.value = "";
  loadLogs();
}

function actionText(action) {
  const map = {
    create: "创建",
    update: "修改",
    delete: "删除",
    review: "审核",
    publish: "发布",
    import: "导入",
    export: "导出",
    submit: "提交审核",
    resubmit: "重新提交",
    flow_create: "建流程",
    flow_update: "改流程",
    flow_delete: "删流程",
    expert_create: "建专家",
    expert_update: "改专家",
    expert_delete: "删专家",
    user_create: "建账号",
    user_register: "自主注册",
    auth_login_failed: "登录失败",
    auth_rate_limited: "登录限速",
  };
  return map[action] || action;
}

function actionClass(action) {
  if (action === "create" || action === "import" || action === "export" || action === "expert_create" || action === "user_create") return "action-create";
  if (action === "update" || action === "flow_update" || action === "expert_update") return "action-update";
  if (action === "delete" || action === "flow_delete" || action === "expert_delete" || action === "auth_login_failed" || action === "auth_rate_limited") return "action-delete";
  if (action === "review" || action === "submit" || action === "resubmit") return "action-review";
  if (action === "publish") return "action-publish";
  if (action === "flow_create") return "action-flow";
  return "";
}

function formatTime(t) {
  if (!t) return "";
  const d = new Date(t);
  return d.toLocaleString("zh-CN");
}

onMounted(() => {
  loadLogs();
  loadUsers();
});
</script>

<template>
  <div class="audit-layout">
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>操作日志</h2>
        <small>{{ logs.length }} 条记录</small>
      </div>

      <div class="filter-row">
        <select v-model="filterType" class="filter-select">
          <option value="all">全部日志</option>
          <option value="question">按题目ID</option>
          <option value="actor">按操作人</option>
        </select>
        <input
          v-if="filterType !== 'all'"
          v-model="filterValue"
          :placeholder="filterType === 'question' ? '输入题目ID' : '输入用户名'"
          class="filter-input"
          @keyup.enter="doFilter"
        />
        <button class="primary-button" type="button" @click="doFilter">查询</button>
        <button class="ghost-button" type="button" @click="clearFilter">重置</button>
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
          <tr v-for="log in logs" :key="log.id">
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
</style>
