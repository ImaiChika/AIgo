<script setup>
import { ref, onMounted } from "vue";
import { api } from "../api.js";
import { currentUser, permissionName } from "../auth.js";

const toast = ref("");
const users = ref([]);
const roles = ref([]);
const permGroups = ref([]);
// 分类子题库管理已从当前产品流程撤下；历史权限只在后端保留兼容。
const hiddenPermissionCodes = new Set(["bank:manage", "question:create"]);
const loading = ref(false);
const showCreate = ref(false);
const expandedRow = ref(""); // 展开权限矩阵的用户ID
const rolePickerUserId = ref("");
const passwordGranted = ref(null); // 刚重置成功的 {username, password}，仅展示一次

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
    const [userData, roleData, permData] = await Promise.all([
      api.listUsers(),
      api.listRoles(),
      api.listPermissions(),
    ]);
    users.value = userData.users || [];
    roles.value = roleData.roles || [];
    // 按分组整理权限点
    const groups = {};
    for (const p of permData.permissions || []) {
      if (hiddenPermissionCodes.has(p.code)) continue;
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

async function changeRole(u, role) {
  if (isProtectedUser(u)) return;
  const assigned = roleIDs(u);
  // 在已经挂载的身份间切换默认身份时保留附加身份；选择一个全新模板时才替换。
  const next = role ? (assigned.includes(role) ? assigned : [role]) : [];
  await saveUser(u, { role, roles: next });
}

function toggleRolePicker(u) {
  rolePickerUserId.value = rolePickerUserId.value === u.id ? "" : u.id;
}

async function toggleRoleAssignment(u, roleID) {
  if (isProtectedUser(u)) return;
  const assigned = new Set(roleIDs(u));
  if (assigned.has(roleID)) assigned.delete(roleID);
  else assigned.add(roleID);
  const next = [...assigned];
  const role = next.includes(u.role) ? u.role : (next[0] || "");
  await saveUser(u, { role, roles: next });
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
	if (!confirm(`确定删除用户「${u.username}」？仅当该账号没有个人题目、生成/批量任务、分享申请或审核流程引用时才能删除；否则请改为停用。`)) return;
  try {
    await api.deleteUser(u.id);
    users.value = users.value.filter((item) => item.id !== u.id);
    if (expandedRow.value === u.id) expandedRow.value = "";
    showToast(`已删除 ${u.username}`);
  } catch (e) {
    showToast("删除失败: " + e.message);
  }
}

// 重置密码：默认重置为 12345678，管理员可在弹窗中改选其他至少 8 位的口令。
const resetTarget = ref(null);
const resetFormPassword = ref("12345678");

function openResetDialog(u) {
  if (isProtectedUser(u)) return;
  resetTarget.value = u;
  resetFormPassword.value = "12345678";
}

async function confirmResetPassword() {
  const target = resetTarget.value;
  if (!target) return;
  const password = resetFormPassword.value.trim();
  if (password.length < 8) {
    showToast("密码至少 8 位");
    return;
  }
  try {
    const data = await api.resetUserPassword(target.id, { new_password: password });
    passwordGranted.value = { username: data.username, password: data.new_password };
    showToast(`已重置 ${target.username} 的密码`);
    resetTarget.value = null;
  } catch (e) {
    showToast("重置失败: " + e.message);
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
	      超级管理员仅保留一名；管理员负责业务管理。命题教师与审题老师模板权限互斥，同一账号可挂载两种身份并在左下角切换。命题教师负责单题/批量出题和提交审核，审题老师只处理分配任务。直接勾选的权限属于账号级覆盖，会在所有身份下生效；双身份老师通常不要直接勾选出题或审题权限，以免绕过身份隔离。
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
            <select v-model="newUser.role" @change="newUser.roles = newUser.role ? [newUser.role] : []">
              <option v-for="r in roleOptions()" :key="r.id" :value="r.id">{{ r.name }}</option>
            </select>
          </div>
        </div>
        <button class="primary-button" type="button" :disabled="creating" @click="createUser">{{ creating ? "创建中..." : "确认创建" }}</button>
        <span class="form-hint">创建后可在下方列表中继续勾选具体权限</span>
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
                <div v-else class="role-picker-cell">
                  <select :value="u.role" :disabled="isProtectedUser(u)" @change="changeRole(u, $event.target.value)">
                    <option v-for="r in roleOptionsFor(u)" :key="r.id" :value="r.id">{{ r.name }}</option>
                  </select>
                  <button v-if="roleOptionsFor(u).length > 2" class="role-more-button" type="button" :disabled="isProtectedUser(u)" @click="toggleRolePicker(u)">附加身份</button>
                  <div v-if="rolePickerUserId === u.id" class="role-popover">
                    <label v-for="r in roleOptionsFor(u).filter((item) => item.id)" :key="r.id">
                      <input type="checkbox" :checked="roleIDs(u).includes(r.id)" @change="toggleRoleAssignment(u, r.id)" />
                      {{ r.name }}
                    </label>
                  </div>
                </div>
              </td>
              <td>
                <button class="toggle-btn" :class="u.enabled ? 'on' : 'off'" type="button" :disabled="isProtectedUser(u)" @click="toggleEnabled(u)">
                  {{ u.enabled ? "启用" : "禁用" }}
                </button>
              </td>
              <td>
	            <span class="perm-count" title="该数字是账号全部身份与直接权限的并集；登录后当前身份显示的权限数可能更少">账号合计 {{ (u.permissions || []).length }} 项</span>
              </td>
              <td class="time-cell">{{ new Date(u.created_at).toLocaleString() }}</td>
              <td>
                <button class="expand-btn" type="button" @click="expandedRow = expandedRow === u.id ? '' : u.id">
                  {{ expandedRow === u.id ? "收起权限" : "分配权限" }}
                </button>
                <button v-if="!isProtectedUser(u)" class="expand-btn" type="button" @click="openResetDialog(u)">重置密码</button>
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
                      <label v-for="p in g.perms" :key="p.code" class="perm-check">
                        <input
                          type="checkbox"
                          :checked="directPerms(u).includes(p.code)"
                          :disabled="!canAssignPermission(u, p)"
                          @change="togglePerm(u, p.code)"
                        />
                        {{ permissionName(p) }}
                      </label>
                    </div>
                  </div>

                  <p class="matrix-note">
	              角色模板决定当前身份能力；这里直接勾选的是账号级覆盖权限，会对该账号所有身份生效。审题与最终决断的题目内容仍按审核任务分配开放。
                  </p>
                </div>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </section>

    <div v-if="resetTarget" class="modal-overlay" @click.self="resetTarget = null">
      <div class="password-granted-card" role="dialog" aria-label="重置密码">
        <div class="form-title-row">
          <div><p class="eyebrow">重置密码</p><h3>重置「{{ resetTarget.username }}」的登录密码</h3></div>
          <button class="modal-close" type="button" aria-label="关闭" @click="resetTarget = null">×</button>
        </div>
        <label class="reset-password-field">
          新密码
          <input v-model="resetFormPassword" type="text" autocomplete="off" spellcheck="false" />
        </label>
        <p class="granted-note">默认重置为 12345678，可直接修改为其他至少 8 位的口令；该账号已登录的会话在 token 过期前仍有效，请提醒本人首次登录后及时修改。</p>
        <div class="reset-dialog-actions">
          <button class="ghost-button" type="button" @click="resetTarget = null">取消</button>
          <button class="primary-button" type="button" @click="confirmResetPassword">确认重置</button>
        </div>
      </div>
    </div>
    <div v-if="passwordGranted" class="modal-overlay" @click.self="passwordGranted = null">
      <div class="password-granted-card" role="dialog" aria-label="新密码">
        <div class="form-title-row">
          <div><p class="eyebrow">重置成功</p><h3>{{ passwordGranted.username }} 的新密码</h3></div>
          <button class="modal-close" type="button" aria-label="关闭" @click="passwordGranted = null">×</button>
        </div>
        <p class="granted-password">{{ passwordGranted.password }}</p>
        <p class="granted-note">请立即把该初始口令告知本人，并提醒首次使用后尽快在「个人设置」中修改；此口令不会再次显示。</p>
        <button class="primary-button" type="button" @click="passwordGranted = null">我已保存</button>
      </div>
    </div>
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

.role-picker-cell {
  position: relative;
  display: grid;
  gap: 4px;
  min-width: 118px;
}

.role-more-button {
  width: fit-content;
  padding: 2px 6px;
  border: 1px solid #dce8f7;
  border-radius: 4px;
  background: #f8fbff;
  color: #1385f8;
  font-size: 11px;
  cursor: pointer;
}

.role-popover {
  position: absolute;
  z-index: 20;
  top: calc(100% + 4px);
  left: 0;
  display: grid;
  gap: 7px;
  min-width: 150px;
  padding: 9px 10px;
  border: 1px solid #dce8f7;
  border-radius: 7px;
  background: #fff;
  box-shadow: 0 10px 24px rgba(30, 60, 90, .16);
  color: #3a4658;
  font-size: 12px;
}

.role-popover label {
  display: flex;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
  cursor: pointer;
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

<style scoped>
/* 重置密码成功后的一次性口令展示 */
.modal-overlay {
  position: fixed;
  inset: 0;
  z-index: 1100;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(15, 32, 55, 0.45);
}
.form-title-row {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
}
.form-title-row .eyebrow {
  margin: 0 0 2px;
  color: #6e7b8f;
  font-size: 11px;
}
.form-title-row h3 {
  margin: 0;
  font-size: 17px;
}
.modal-close {
  border: none;
  background: transparent;
  font-size: 18px;
  line-height: 1;
  cursor: pointer;
  color: #6e7b8f;
  padding: 2px 6px;
}
.reset-password-field {
  display: grid;
  gap: 6px;
  font-size: 13px;
  color: #3a4658;
  margin: 4px 0 2px;
}

.reset-password-field input {
  height: 36px;
  padding: 0 10px;
  border: 1px solid #cdd9ea;
  border-radius: 6px;
  font: inherit;
  font-size: 14px;
  letter-spacing: 0.5px;
}

.reset-dialog-actions {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 14px;
}

.password-granted-card {
  background: #fff;
  border-radius: 10px;
  padding: 18px 20px;
  width: min(460px, calc(100vw - 40px));
  box-shadow: 0 12px 32px rgba(0, 0, 0, 0.18);
}
.granted-password {
  margin: 10px 0;
  padding: 10px 12px;
  border: 1px dashed #9db7d8;
  border-radius: 6px;
  background: #f4f8fd;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 15px;
  letter-spacing: 1px;
  word-break: break-all;
  user-select: all;
}
.granted-note {
  margin: 0 0 14px;
  color: #718197;
  font-size: 12px;
  line-height: 1.6;
}
</style>
