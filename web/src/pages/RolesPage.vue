<script setup>
import { ref, onMounted } from "vue";
import { api } from "../api.js";
import { permissionName } from "../auth.js";

const toast = ref("");
const roles = ref([]);
const permGroups = ref([]);
const permNameMap = ref({});
const loading = ref(false);
const showCreate = ref(false);
const editingId = ref("");
// 分类子题库管理已从当前产品流程撤下；保留后端权限仅用于兼容历史角色数据。
const hiddenPermissionCodes = new Set(["bank:manage", "question:create"]);

const form = ref({
  name: "",
  description: "",
  permissions: [],
});

function permName(code) {
  return permNameMap.value[code] || code;
}

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadAll() {
  loading.value = true;
  try {
    const [roleData, permData] = await Promise.all([api.listRoles(), api.listPermissions()]);
    roles.value = roleData.roles || [];
    const groups = {};
    const nameMap = {};
    for (const p of permData.permissions || []) {
      if (hiddenPermissionCodes.has(p.code)) continue;
      nameMap[p.code] = permissionName(p);
      if (!groups[p.group]) groups[p.group] = [];
      groups[p.group].push(p);
    }
    permNameMap.value = nameMap;
    permGroups.value = Object.keys(groups).map((g) => ({ group: g, perms: groups[g] }));
  } catch (e) {
    showToast("加载失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

function resetForm() {
  form.value = { name: "", description: "", permissions: [] };
  editingId.value = "";
}

function startCreate() {
  resetForm();
  showCreate.value = true;
}

function startEdit(r) {
  form.value = {
    name: r.name,
    description: r.description || "",
    permissions: (r.permissions || []).filter((code) => !hiddenPermissionCodes.has(code)),
  };
  editingId.value = r.id;
  showCreate.value = true;
}

function togglePerm(code) {
  const idx = form.value.permissions.indexOf(code);
  if (idx >= 0) form.value.permissions.splice(idx, 1);
  else form.value.permissions.push(code);
}

const savingRole = ref(false); // 角色表单在途守卫，防重复提交

async function submitRole() {
  if (savingRole.value) return;
  if (!form.value.name.trim()) {
    showToast("角色名称不能为空");
    return;
  }
  savingRole.value = true;
  try {
    if (editingId.value) {
      await api.updateRole(editingId.value, form.value);
      showToast("修改成功");
    } else {
      await api.createRole(form.value);
      showToast("创建成功");
    }
    showCreate.value = false;
    resetForm();
    loadAll();
  } catch (e) {
    showToast(`${editingId.value ? "修改" : "创建"}失败: ` + e.message);
  } finally {
    savingRole.value = false;
  }
}

const deletingRoleId = ref("");

async function deleteRole(r) {
  if (!confirm(`确定删除角色「${r.name}」？`)) return;
  if (deletingRoleId.value) return;
  deletingRoleId.value = r.id;
  try {
    await api.deleteRole(r.id);
    showToast("已删除");
    loadAll();
  } catch (e) {
    showToast("删除失败: " + e.message);
  } finally {
    deletingRoleId.value = "";
  }
}

onMounted(loadAll);
</script>

<template>
  <div class="roles-layout">
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>角色管理</h2>
        <small>{{ roles.length }} 个角色模板</small>
        <button class="primary-button" type="button" @click="showCreate = !showCreate; resetForm()">
          {{ showCreate ? "取消" : "+ 新建角色" }}
        </button>
      </div>

      <!-- 创建/编辑表单 -->
      <div v-if="showCreate" class="create-form">
        <div class="form-row">
          <div class="field">
            <label>角色名称 *</label>
            <input v-model="form.name" placeholder="如：内科审题老师" />
          </div>
          <div class="field">
            <label>描述</label>
            <input v-model="form.description" placeholder="可选" />
          </div>
        </div>

        <div class="perm-picker">
          <h3>角色权限</h3>
        <p class="perm-picker-hint">命题教师模板默认同时拥有“单题出题”和“批量推理”；自定义命题角色勾选单题出题时会自动补齐批量推理，管理员型角色仍按需配置。</p>
          <div v-for="g in permGroups" :key="g.group" class="perm-group">
            <div class="perm-group-title">{{ g.group }}权限</div>
            <div class="perm-checks">
              <label v-for="p in g.perms" :key="p.code" class="perm-check">
                <input type="checkbox" :checked="form.permissions.includes(p.code)" @change="togglePerm(p.code)" />
                {{ p.name }}
              </label>
            </div>
          </div>
        </div>

        <div class="form-actions">
          <button class="primary-button" type="button" :disabled="savingRole" @click="submitRole">
            {{ savingRole ? "保存中..." : (editingId ? "保存修改" : "创建角色") }}
          </button>
          <button class="ghost-button" type="button" @click="showCreate = false; resetForm()">取消</button>
        </div>
      </div>

      <!-- 角色列表 -->
      <div v-if="loading" class="loading">加载中...</div>
      <div v-else class="role-list">
        <div v-for="r in roles" :key="r.id" class="role-card">
          <div class="role-header">
            <div>
              <strong>{{ r.name }}</strong>
            </div>
	        <span v-if="r.is_builtin" class="builtin-tag">内置固定</span>
	        <div v-else class="role-actions">
              <button class="edit-btn" type="button" @click="startEdit(r)">编辑</button>
              <button class="delete-btn" type="button" :disabled="deletingRoleId === r.id" @click="deleteRole(r)" title="删除">×</button>
            </div>
          </div>
          <p v-if="r.description" class="role-desc">{{ r.description }}</p>
          <div class="role-perms">
            <span v-for="p in (r.permissions || []).filter((code) => !hiddenPermissionCodes.has(code))" :key="p" class="perm-tag">{{ permName(p) }}</span>
            <span v-if="!(r.permissions || []).filter((code) => !hiddenPermissionCodes.has(code)).length" class="no-perm">无权限</span>
          </div>
        </div>
        <div v-if="!roles.length" class="empty">暂无角色，点击「新建角色」创建</div>
      </div>
    </section>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
</template>

<style scoped>
.roles-layout {
  max-width: 100%;
}

.create-form {
  padding: 20px;
  background: #f8fbff;
  border: 1px solid #dce8f7;
  border-radius: 8px;
  margin-bottom: 16px;
}

.form-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
  margin-bottom: 16px;
}

.field label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 4px;
}

.field input {
  width: 100%;
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 10px;
  font-size: 13px;
  box-sizing: border-box;
}

.perm-picker h3 {
  font-size: 14px;
  color: #172033;
  margin: 0 0 12px;
}

.perm-picker-hint {
  margin: -4px 0 14px;
  color: #6e7b8f;
  font-size: 12px;
  line-height: 1.6;
}

.perm-group {
  margin-bottom: 12px;
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

.form-actions {
  display: flex;
  gap: 10px;
  margin-top: 16px;
}

.role-list {
  display: grid;
  gap: 12px;
}

.role-card {
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  padding: 16px;
  background: #fff;
}

.role-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.role-header strong {
  font-size: 15px;
  color: #172033;
}

.role-actions {
  display: flex;
  gap: 8px;
}
.builtin-tag { color: #718197; font-size: 11px; border: 1px solid #dbe4ed; border-radius: 999px; padding: 3px 8px; }

.edit-btn {
  height: 28px;
  padding: 0 12px;
  border: 1px solid #dce8f7;
  border-radius: 6px;
  background: #fff;
  color: #1385f8;
  font-size: 13px;
  cursor: pointer;
}

.delete-btn {
  width: 28px;
  height: 28px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #c54858;
  font-size: 18px;
  cursor: pointer;
}

.role-desc {
  margin: 6px 0 0;
  font-size: 13px;
  color: #6e7b8f;
}

.role-perms {
  margin-top: 10px;
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.perm-tag {
  padding: 2px 8px;
  border-radius: 4px;
  background: #eff8ff;
  color: #0571dc;
  font-size: 12px;
}

.no-perm {
  font-size: 12px;
  color: #9aa5b4;
}

.empty {
  text-align: center;
  color: #6e7b8f;
  padding: 40px;
}

.loading {
  text-align: center;
  color: #6e7b8f;
  padding: 30px;
}
</style>
