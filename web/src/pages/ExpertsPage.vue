<script setup>
import { ref, onMounted } from "vue";
import { api } from "../api.js";

const toast = ref("");
const experts = ref([]);
const loading = ref(false);
const showCreate = ref(false);
const editingId = ref(null);

const form = ref({
  name: "",
  department: "",
  title: "",
  specialties: "",
});

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  toast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadExperts() {
  loading.value = true;
  try {
    const data = await api.listExperts();
    experts.value = data.experts || [];
  } catch (e) {
    showToast("加载失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

function resetForm() {
  form.value = { name: "", department: "", title: "", specialties: "" };
  editingId.value = null;
}

function startCreate() {
  resetForm();
  showCreate.value = true;
}

function startEdit(e) {
  form.value = {
    name: e.name,
    department: e.department,
    title: e.title,
    specialties: (e.specialties || []).join("、"),
  };
  editingId.value = e.id;
  showCreate.value = true;
}

async function submitForm() {
  if (!form.value.name.trim()) {
    showToast("专家姓名不能为空");
    return;
  }
  const specialties = form.value.specialties
    .split(/[,，、\s]+/)
    .map((s) => s.trim())
    .filter(Boolean);

  try {
    if (editingId.value) {
      await api.updateExpert(editingId.value, {
        name: form.value.name,
        department: form.value.department,
        title: form.value.title,
        specialties,
      });
      showToast("更新成功");
    } else {
      await api.createExpert({
        name: form.value.name,
        department: form.value.department,
        title: form.value.title,
        specialties,
      });
      showToast("添加成功");
    }
    showCreate.value = false;
    resetForm();
    loadExperts();
  } catch (e) {
    showToast("操作失败: " + e.message);
  }
}

async function toggleEnabled(e) {
  try {
    await api.updateExpert(e.id, { enabled: !e.enabled });
    e.enabled = !e.enabled;
    showToast(e.enabled ? "已启用" : "已停用");
  } catch (e2) {
    showToast("操作失败: " + e2.message);
  }
}

async function deleteExpert(e) {
  if (!confirm(`确定删除专家：${e.name}？`)) return;
  try {
    await api.deleteExpert(e.id);
    showToast("已删除");
    experts.value = experts.value.filter((item) => item.id !== e.id);
  } catch (e2) {
    showToast("删除失败: " + e2.message);
  }
}

onMounted(loadExperts);
</script>

<template>
  <div class="experts-layout">
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>专家库</h2>
        <small>{{ experts.length }} 位专家</small>
        <button class="primary-button" type="button" @click="startCreate">+ 添加专家</button>
      </div>

      <!-- 表单 -->
      <div v-if="showCreate" class="create-form">
        <div class="form-row">
          <div class="field">
            <label>姓名 *</label>
            <input v-model="form.name" placeholder="专家姓名" />
          </div>
          <div class="field">
            <label>科室/专业</label>
            <input v-model="form.department" placeholder="如：呼吸内科" />
          </div>
          <div class="field">
            <label>职称</label>
            <input v-model="form.title" placeholder="如：主任医师" />
          </div>
          <div class="field">
            <label>擅长知识点（顿号分隔）</label>
            <input v-model="form.specialties" placeholder="如：肺炎、哮喘、COPD" />
          </div>
        </div>
        <div class="form-actions">
          <button class="primary-button" type="button" @click="submitForm">
            {{ editingId ? "保存修改" : "确认添加" }}
          </button>
          <button class="ghost-button" type="button" @click="showCreate = false; resetForm()">取消</button>
        </div>
      </div>

      <!-- 列表 -->
      <div v-if="loading" class="loading">加载中...</div>
      <table v-else class="experts-table">
        <thead>
          <tr>
            <th>ID</th>
            <th>姓名</th>
            <th>科室</th>
            <th>职称</th>
            <th>擅长</th>
            <th>状态</th>
            <th>操作</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in experts" :key="e.id">
            <td class="id-cell">{{ e.id }}</td>
            <td class="name-cell">{{ e.name }}</td>
            <td>{{ e.department || "-" }}</td>
            <td>{{ e.title || "-" }}</td>
            <td class="spec-cell">{{ (e.specialties || []).join("、") || "-" }}</td>
            <td>
              <span
                class="status-tag"
                :class="e.enabled ? 'status-on' : 'status-off'"
                @click="toggleEnabled(e)"
              >
                {{ e.enabled ? "启用" : "停用" }}
              </span>
            </td>
            <td class="action-cell">
              <button class="edit-btn" type="button" @click="startEdit(e)" title="编辑">✎</button>
              <button class="delete-btn" type="button" @click="deleteExpert(e)" title="删除">×</button>
            </td>
          </tr>
          <tr v-if="!experts.length">
            <td colspan="7" class="empty">暂无专家，点击上方「添加专家」</td>
          </tr>
        </tbody>
      </table>
    </section>
  </div>
</template>

<style scoped>
.experts-layout {
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

.field input {
  width: 100%;
  height: 36px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 10px;
  font-size: 13px;
}

.form-actions {
  display: flex;
  gap: 10px;
}

.experts-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 14px;
}

.experts-table th {
  text-align: left;
  padding: 10px 12px;
  background: #f8fbff;
  color: #6e7b8f;
  font-weight: 600;
  border-bottom: 1px solid #e5ebf3;
}

.experts-table td {
  padding: 10px 12px;
  border-bottom: 1px solid #f0f3f7;
}

.experts-table tr:hover {
  background: #f8fbff;
}

.id-cell {
  color: #6e7b8f;
  font-size: 12px;
  font-family: monospace;
}

.name-cell {
  font-weight: 600;
  color: #172033;
}

.spec-cell {
  font-size: 12px;
  color: #6e7b8f;
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.status-tag {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
  user-select: none;
}

.status-on {
  background: #f0fff8;
  color: #087c55;
}

.status-off {
  background: #f0f3f7;
  color: #6e7b8f;
}

.action-cell {
  display: flex;
  gap: 6px;
}

.edit-btn,
.delete-btn {
  width: 28px;
  height: 28px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  font-size: 16px;
  cursor: pointer;
  display: grid;
  place-items: center;
}

.edit-btn {
  color: #0571dc;
}

.edit-btn:hover {
  background: #eff8ff;
}

.delete-btn {
  color: #c54858;
}

.delete-btn:hover {
  background: #fff0f0;
  border-color: #c54858;
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
</style>
