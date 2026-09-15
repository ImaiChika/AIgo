<script setup>
import { ref, onMounted } from "vue";
import { api } from "../api.js";
import { currentUser, permissionName } from "../auth.js";

const toast = ref("");
const users = ref([]);
const roles = ref([]);
const banks = ref([]);
const permGroups = ref([]);
const loading = ref(false);
const showCreate = ref(false);
const expandedRow = ref(""); // 展开权限矩阵的用户ID

const newUser = ref({
  username: "",
  password: "",
  display_name: "",
  role: "",
  roles: [],
  permissions: [],
  bank_ids: [],
});

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadAll() {
  loading.value = true;
  try {
    const [userData, roleData, bankData, permData] = await Promise.all([
      api.listUsers(),
      api.listRoles(),
      api.listBanks(false),
      api.listPermissions(),
    ]);
    users.value = userData.users || [];
    roles.value = roleData.roles || [];
    banks.value = bankData.banks || [];
    // 按分组整理权限点
    const groups = {};
    for (const p of permData.permissions || []) {
      if (!groups[p.group]) groups[p.group] = [];
      groups[p.group].push(p);
    }
    permGroups.value = Object.keys(groups).map((g) => ({ group: g, perms: groups[g] }));
  } catch (e) {
    showToast("加载失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

function roleOptions() {
  // 超级管理员是系统唯一身份，只能由启动引导产生，不能作为用户管理中的可分配角色。
  const available = roles.value.filter((r) => r.id !== "super_admin" &&
    (currentUser.value?.role === "super_admin" || r.id !== "admin"));
  return [{ id: "", name: "无角色" }, ...available];
}

function roleIDs(u) {
  return (u?.roles && u.roles.length) ? u.roles : (u?.role ? [u.role] : []);
}

function roleOptionsFor(u) {
  if (roleIDs(u).includes("super_admin")) return roleOptions();
  const options = roleOptions();
  const missing = roleIDs(u).filter((id) => !options.some((r) => r.id === id));
  if (!missing.length) return options;
  const current = roles.value.filter((r) => missing.includes(r.id));
  return [...current, ...options];
}

function isProtectedUser(u) {
  const assigned = roleIDs(u);
  return u?.id === currentUser.value?.id || assigned.includes("super_admin") ||
    (assigned.includes("admin") && currentUser.value?.role !== "super_admin");
}

function canAssignPermission(u, permission) {
  return !isProtectedUser(u) &&
    (currentUser.value?.role === "super_admin" || permission.code !== "role:manage");
}

const creating = ref(false); // 创建用户在途守卫，防重复提交

async function createUser() {
  if (creating.value) return;
  creating.value = true;
  try {
    await api.createUser(newUser.value);
    showToast("创建成功");
    showCreate.value = false;
    newUser.value = { username: "", password: "", display_name: "", role: "", roles: [], permissions: [], bank_ids: [] };
    loadAll();
  } catch (e) {
    showToast("创建失败: " + e.message);
  } finally {
    creating.value = false;
  }
}

// 勾选/取消权限（立即保存整行）
async function togglePerm(u, code) {
  if (!canAssignPermission(u, { code })) return;
  const perms = new Set(u.direct_permissions || []);
  if (perms.has(code)) perms.delete(code);
  else perms.add(code);
  await saveUser(u, { permissions: Array.from(perms) });
}

async function toggleBank(u, bankId) {
  if (isProtectedUser(u)) return;
  const banksArr = new Set(u.bank_ids || []);
  if (banksArr.has(bankId)) banksArr.delete(bankId);
  else banksArr.add(bankId);
  await saveUser(u, { bank_ids: Array.from(banksArr) });
}

async function changeRole(u, role) {
  if (isProtectedUser(u)) return;
  await saveUser(u, { role, roles: role ? [role] : [] });
}

async function changeRoles(u, event) {
  if (isProtectedUser(u)) return;
  const selected = [...(event.target.selectedOptions || [])].map((option) => option.value).filter(Boolean);
  const role = selected.includes(u.role) ? u.role : (selected[0] || "");
  await saveUser(u, { role, roles: selected });
}

async function toggleEnabled(u) {
  if (isProtectedUser(u)) return;
  await saveUser(u, { enabled: !u.enabled });
}

async function saveUser(u, patch) {
  try {
    const updated = await api.updateUser(u.id, {
      display_name: u.display_name,
      role: u.role,
      roles: roleIDs(u),
      permissions: u.direct_permissions,
      bank_ids: u.bank_ids,
      ...patch,
    });
    Object.assign(u, updated);
    showToast(`已保存 ${u.username} 的权限`);
  } catch (e) {
    showToast("保存失败: " + e.message);
  }
}

async function deleteUser(u) {
  if (isProtectedUser(u)) return;
  if (!confirm(`确定删除用户「${u.username}」？删除后该账号将无法登录，历史审核记录保留。`)) return;
  try {
    await api.deleteUser(u.id);
    users.value = users.value.filter((item) => item.id !== u.id);
    if (expandedRow.value === u.id) expandedRow.value = "";
    showToast(`已删除 ${u.username}`);
  } catch (e) {
    showToast("删除失败: " + e.message);
  }
}

// 用户直接分配的权限（区别于角色模板权限）
function directPerms(u) {
  return u.direct_permissions || [];
}

onMounted(loadAll);
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
      <p class="permission-summary">
        超级管理员仅保留一名；管理员负责业务管理。命题教师和审题老师是互斥岗位，但同一账号可以同时挂载两个岗位并在左下角切换当前身份。命题教师负责出题和提交审核，审题老师只处理分配到的审核任务；“单题出题”和“批量推理”仍是两个独立权限。
      </p>

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
            <label>角色模板</label>
            <select v-model="newUser.roles" multiple size="2" @change="newUser.role = newUser.roles[0] || ''">
              <option v-for="r in roleOptions()" :key="r.id" :value="r.id">{{ r.name }}</option>
            </select>
          </div>
        </div>
        <button class="primary-button" type="button" :disabled="creating" @click="createUser">{{ creating ? "创建中..." : "确认创建" }}</button>
        <span class="form-hint">创建后可在下方列表中继续勾选具体权限与题库范围</span>
      </div>

      <div v-if="loading" class="loading">加载中...</div>
      <table v-else class="users-table">
        <thead>
          <tr>
            <th>用户名</th>
            <th>显示名</th>
            <th>角色模板</th>
            <th>状态</th>
            <th>权限</th>
            <th>创建时间</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="u in users" :key="u.id">
            <tr>
              <td class="username-cell">
                {{ u.username }}
                <span v-if="!u.role && !(u.direct_permissions || []).length" class="new-tag">新注册</span>
              </td>
              <td>{{ u.display_name }}</td>
              <td>
                <span v-if="roleIDs(u).includes('super_admin')" class="protected-role">超级管理员（系统唯一）</span>
                <select v-else multiple size="2" :value="roleIDs(u)" :disabled="isProtectedUser(u)" @change="changeRoles(u, $event)">
                  <option v-for="r in roleOptionsFor(u)" :key="r.id" :value="r.id">{{ r.name }}</option>
                </select>
              </td>
              <td>
                <button class="toggle-btn" :class="u.enabled ? 'on' : 'off'" type="button" :disabled="isProtectedUser(u)" @click="toggleEnabled(u)">
                  {{ u.enabled ? "启用" : "禁用" }}
                </button>
              </td>
              <td>
                <span class="perm-count">{{ (u.permissions || []).length }} 项有效权限</span>
              </td>
              <td class="time-cell">{{ new Date(u.created_at).toLocaleString() }}</td>
              <td>
                <button class="expand-btn" type="button" @click="expandedRow = expandedRow === u.id ? '' : u.id">
                  {{ expandedRow === u.id ? "收起权限" : "分配权限" }}
                </button>
                <button v-if="!isProtectedUser(u)" class="delete-user-btn" type="button" @click="deleteUser(u)">删除</button>
              </td>
            </tr>
            <!-- 权限矩阵行：一列列权限打勾分配 -->
            <tr v-if="expandedRow === u.id" class="perm-matrix-row">
              <td colspan="7">
                <div class="perm-matrix">
                  <div v-for="g in permGroups" :key="g.group" class="perm-group">
                    <div class="perm-group-title">{{ g.group }}权限</div>
                    <div class="perm-checks">
                      <label v-for="p in g.perms" :key="p.code" class="perm-check" :class="{ scoped: p.bank_scope }">
                        <input
                          type="checkbox"
                          :checked="directPerms(u).includes(p.code)"
                          :disabled="!canAssignPermission(u, p)"
                          @change="togglePerm(u, p.code)"
                        />
                        {{ permissionName(p) }}
                        <span v-if="p.bank_scope" class="scope-mark" title="可按题库限定范围">库</span>
                      </label>
                    </div>
                  </div>

                  <!-- 题库范围 -->
                  <div class="perm-group bank-scope">
                    <div class="perm-group-title">
                      分类子题库范围
                      <span class="scope-hint">（不选表示全部；对角色权限和直接权限都生效）</span>
                    </div>
                    <div class="bank-chips">
                      <button
                        v-for="b in banks" :key="b.id"
                        type="button"
                        class="bank-chip"
                        :class="{ selected: (u.bank_ids || []).includes(b.id) }"
                        :disabled="isProtectedUser(u)"
                        @click="toggleBank(u, b.id)"
                      >
                        {{ b.name }}
                      </button>
                      <span v-if="!banks.length" class="no-bank">暂无题库，请先在「题库管理」创建（如内科、外科）</span>
                    </div>
                  </div>

                  <p class="matrix-note">
                    角色决定“能做什么”，题库范围决定“可在哪些专业子题库做”；审题与最终决断的题目内容按任务分配开放，不需要题库查看权限。
                  </p>
                </div>
              </td>
            </tr>
          </template>
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

.permission-summary {
  margin: 0 0 16px;
  padding: 10px 12px;
  border-radius: 6px;
  background: #eff8ff;
  color: #49627d;
  font-size: 12px;
  line-height: 1.6;
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
  box-sizing: border-box;
}

.form-hint {
  margin-left: 12px;
  font-size: 12px;
  color: #9aa5b4;
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
  white-space: nowrap;
}

.users-table td {
  padding: 10px 12px;
  border-bottom: 1px solid #f0f3f7;
}

.username-cell {
  font-weight: 600;
  color: #172033;
}

.new-tag {
  display: inline-block;
  margin-left: 6px;
  padding: 1px 6px;
  border-radius: 3px;
  background: #fdf2e3;
  color: #c07b22;
  font-size: 11px;
  font-weight: 600;
}

.time-cell {
  font-size: 12px;
  color: #6e7b8f;
}

.users-table select {
  height: 30px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 8px;
  font-size: 13px;
}

.protected-role {
  color: #6e7b8f;
  font-size: 13px;
  white-space: nowrap;
}

.toggle-btn {
  padding: 3px 10px;
  border-radius: 4px;
  border: 1px solid transparent;
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
}

.toggle-btn.on {
  background: #f0fff8;
  color: #087c55;
  border-color: #bdecd9;
}

.toggle-btn.off {
  background: #fff0f0;
  color: #c54858;
  border-color: #f3c2c2;
}

.toggle-btn:disabled,
.users-table select:disabled,
.perm-check input:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}

.perm-count {
  font-size: 12px;
  color: #1385f8;
  font-weight: 600;
}

.expand-btn {
  padding: 4px 10px;
  border: 1px solid #dce8f7;
  border-radius: 6px;
  background: #fff;
  color: #1385f8;
  font-size: 12px;
  cursor: pointer;
}

.expand-btn:hover {
  background: #eff8ff;
}

.delete-user-btn {
  margin-left: 6px;
  padding: 4px 10px;
  border: 1px solid #f3c2c2;
  border-radius: 6px;
  background: #fff;
  color: #c54858;
  font-size: 12px;
  cursor: pointer;
}

.delete-user-btn:hover {
  background: #fff0f0;
}

.perm-matrix-row td {
  background: #f8fbff;
  padding: 16px;
}

.perm-matrix {
  display: grid;
  gap: 14px;
}

.perm-group-title {
  font-size: 13px;
  font-weight: 700;
  color: #172033;
  margin-bottom: 8px;
}

.perm-checks {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 18px;
}

.perm-check {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  font-size: 13px;
  color: #3a4658;
  cursor: pointer;
  padding: 4px 8px;
  border: 1px solid #e5ebf3;
  border-radius: 5px;
  background: #fff;
}

.perm-check:hover {
  border-color: #1385f8;
}

.perm-check input {
  cursor: pointer;
}

.scope-mark {
  font-size: 10px;
  color: #c07b22;
  background: #fdf2e3;
  border-radius: 3px;
  padding: 0 4px;
}

.bank-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.bank-chip {
  padding: 5px 14px;
  border: 1px solid #e5ebf3;
  border-radius: 16px;
  background: #fff;
  font-size: 12px;
  cursor: pointer;
}

.bank-chip:hover {
  border-color: #1385f8;
}

.bank-chip.selected {
  background: #1385f8;
  color: #fff;
  border-color: #1385f8;
}

.bank-chip:disabled {
  cursor: not-allowed;
  opacity: 0.6;
}

.no-bank {
  font-size: 12px;
  color: #c07b22;
}

.scope-hint {
  font-size: 11px;
  color: #9aa5b4;
  font-weight: 400;
}

.matrix-note {
  margin: 0;
  font-size: 12px;
  color: #6e7b8f;
  background: #eff8ff;
  border-radius: 6px;
  padding: 8px 12px;
}

.loading {
  text-align: center;
  color: #6e7b8f;
  padding: 30px;
}

@media (max-width: 700px) {
  .permission-summary {
    overflow-wrap: anywhere;
  }

  .create-form .form-row {
    grid-template-columns: minmax(0, 1fr);
  }

  .form-hint {
    display: block;
    margin: 8px 0 0;
  }

  .users-table {
    display: block;
    width: 100%;
  }

  .users-table thead {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    clip-path: inset(50%);
    white-space: nowrap;
  }

  .users-table tbody {
    display: grid;
    gap: 8px;
  }

  .users-table tr {
    display: grid;
    grid-template-columns: minmax(0, 1fr);
    gap: 3px;
    padding: 9px 10px;
    border: 1px solid #e5ebf3;
    border-radius: 8px;
    background: #fff;
  }

  .users-table td {
    display: grid;
    grid-template-columns: 64px minmax(0, 1fr);
    gap: 8px;
    min-width: 0;
    padding: 4px 0;
    border: 0;
    overflow-wrap: anywhere;
  }

  .users-table td::before {
    color: #8a96a5;
    font-size: 11px;
  }

  .users-table td:nth-child(1)::before { content: "用户名"; }
  .users-table td:nth-child(2)::before { content: "显示名"; }
  .users-table td:nth-child(3)::before { content: "角色"; }
  .users-table td:nth-child(4)::before { content: "状态"; }
  .users-table td:nth-child(5)::before { content: "权限"; }
  .users-table td:nth-child(6)::before { content: "创建时间"; }
  .users-table td:nth-child(7)::before { content: "操作"; }

  .users-table td:last-child {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
  }

  .users-table td:last-child::before {
    flex: 0 0 64px;
  }

  .users-table td[colspan] {
    display: block;
  }

  .users-table select {
    min-width: 0;
    max-width: 100%;
  }

  .perm-matrix-row td {
    display: block;
    padding: 10px 0;
  }

  .perm-matrix-row td::before {
    content: none !important;
  }

  .perm-checks {
    gap: 6px;
  }

  .perm-check {
    min-width: 0;
    overflow-wrap: anywhere;
  }
}
</style>
