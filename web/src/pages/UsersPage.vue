<script setup>
import { ref, onMounted } from "vue";
import { api } from "../api.js";

const toast = ref("");
const users = ref([]);
const loading = ref(false);
const showCreate = ref(false);

const newUser = ref({
  username: "",
  password: "",
  display_name: "",
  role: "teacher",
});

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadUsers() {
  loading.value = true;
  try {
    const data = await api.listUsers();
    users.value = data.users || [];
  } catch (e) {
    showToast("加载失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

async function createUser() {
  try {
    await api.createUser(newUser.value);
    showToast("创建成功");
    showCreate.value = false;
    newUser.value = { username: "", password: "", display_name: "", role: "teacher" };
    loadUsers();
  } catch (e) {
    showToast("创建失败: " + e.message);
  }
}

function roleText(role) {
  const map = { admin: "管理员", expert: "专家", teacher: "命题教师" };
  return map[role] || role;
}

function roleClass(role) {
  if (role === "admin") return "role-admin";
  if (role === "expert") return "role-expert";
  return "role-teacher";
}

onMounted(loadUsers);
</script>

<template>
  <div class="users-layout">
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>用户管理</h2>
        <small>{{ users.length }} 个用户</small>
        <button class="primary-button" type="button" @click="showCreate = !showCreate">
          {{ showCreate ? "取消" : "新建用户" }}
        </button>
      </div>

      <!-- 新建用户表单 -->
      <div v-if="showCreate" class="create-form">
        <div class="form-row">
          <div class="field">
            <label>用户名</label>
            <input v-model="newUser.username" placeholder="登录用户名" />
          </div>
          <div class="field">
            <label>密码</label>
            <input v-model="newUser.password" type="password" placeholder="至少8位" />
          </div>
          <div class="field">
            <label>显示名</label>
            <input v-model="newUser.display_name" placeholder="真实姓名" />
          </div>
          <div class="field">
            <label>角色</label>
            <select v-model="newUser.role">
              <option value="teacher">命题教师</option>
              <option value="expert">专家</option>
              <option value="admin">管理员</option>
            </select>
          </div>
        </div>
        <button class="primary-button" type="button" @click="createUser">确认创建</button>
      </div>

      <!-- 用户列表 -->
      <div v-if="loading" class="loading">加载中...</div>
      <table v-else class="users-table">
        <thead>
          <tr>
            <th>用户名</th>
            <th>显示名</th>
            <th>角色</th>
            <th>状态</th>
            <th>创建时间</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in users" :key="u.id">
            <td class="username-cell">{{ u.username }}</td>
            <td>{{ u.display_name }}</td>
            <td>
              <span class="role-tag" :class="roleClass(u.role)">{{ roleText(u.role) }}</span>
            </td>
            <td>
              <span :class="u.enabled ? 'status-on' : 'status-off'">
                {{ u.enabled ? "启用" : "禁用" }}
              </span>
            </td>
            <td class="time-cell">{{ new Date(u.created_at).toLocaleString() }}</td>
          </tr>
        </tbody>
      </table>
    </section>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
</template>

<style scoped>
.users-layout {
  max-width: 100%;
}

.create-form {
  padding: 16px;
  background: #f8fbff;
  border: 1px solid #dce8f7;
  border-radius: 8px;
  margin-bottom: 16px;
}

.form-row {
  display: grid;
  grid-template-columns: 1fr 1fr 1fr 1fr;
  gap: 12px;
  margin-bottom: 12px;
}

.field label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 4px;
}

.field input,
.field select {
  width: 100%;
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 10px;
  font-size: 13px;
}

.users-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 14px;
}

.users-table th {
  text-align: left;
  padding: 10px 12px;
  background: #f8fbff;
  color: #6e7b8f;
  font-weight: 600;
  border-bottom: 1px solid #e5ebf3;
}

.users-table td {
  padding: 10px 12px;
  border-bottom: 1px solid #f0f3f7;
}

.username-cell {
  font-weight: 600;
  color: #172033;
}

.time-cell {
  font-size: 12px;
  color: #6e7b8f;
}

.role-tag {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 600;
}

.role-admin {
  background: #fff3e2;
  color: #dd8a00;
}

.role-expert {
  background: #e9f8ef;
  color: #199e63;
}

.role-teacher {
  background: #eff8ff;
  color: #0571dc;
}

.status-on {
  color: #087c55;
  font-weight: 600;
}

.status-off {
  color: #c54858;
  font-weight: 600;
}

.loading {
  text-align: center;
  color: #6e7b8f;
  padding: 30px;
}
</style>
