<script setup>
import { ref, onMounted } from "vue";
import { api } from "../api.js";

const toast = ref("");
const flows = ref([]);
const experts = ref([]);
const loading = ref(false);
const showCreate = ref(false);

const form = ref({
  id: "",
  name: "",
  description: "",
  rounds: [
    { round_number: 1, name: "命题教师初审", expert_ids: [], required_count: 1, can_modify: true, pass_condition: "" },
  ],
});

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  toast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadFlows() {
  loading.value = true;
  try {
    const data = await api.listFlows();
    flows.value = data.flows || [];
  } catch (e) {
    showToast("加载失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

async function loadExperts() {
  try {
    const data = await api.listExperts();
    experts.value = data.experts || [];
  } catch (e) {
    console.error(e);
  }
}

function resetForm() {
  form.value = {
    id: "",
    name: "",
    description: "",
    rounds: [
      { round_number: 1, name: "命题教师初审", expert_ids: [], required_count: 1, can_modify: true, pass_condition: "" },
    ],
  };
}

function addRound() {
  const num = form.value.rounds.length + 1;
  form.value.rounds.push({
    round_number: num,
    name: `第${num}轮审核`,
    expert_ids: [],
    required_count: 1,
    can_modify: false,
    pass_condition: "",
  });
}

function removeRound(index) {
  if (form.value.rounds.length <= 1) {
    showToast("至少需要一轮审核");
    return;
  }
  form.value.rounds.splice(index, 1);
  // 重新编号
  form.value.rounds.forEach((r, i) => { r.round_number = i + 1; });
}

function toggleExpert(roundIndex, expertId) {
  const round = form.value.rounds[roundIndex];
  const idx = round.expert_ids.indexOf(expertId);
  if (idx >= 0) {
    round.expert_ids.splice(idx, 1);
  } else {
    round.expert_ids.push(expertId);
  }
  // required_count 不能超过专家数
  if (round.required_count > round.expert_ids.length) {
    round.required_count = round.expert_ids.length;
  }
}

async function submitFlow() {
  if (!form.value.name.trim()) {
    showToast("流程名称不能为空");
    return;
  }
  // 校验每轮
  for (const round of form.value.rounds) {
    if (round.expert_ids.length === 0) {
      showToast(`第${round.round_number}轮没有选择审核人`);
      return;
    }
    if (round.required_count < 1) {
      round.required_count = 1;
    }
    if (round.required_count > round.expert_ids.length) {
      round.required_count = round.expert_ids.length;
    }
  }

  const flowId = form.value.id || `flow-${Date.now()}`;
  const flow = {
    id: flowId,
    name: form.value.name,
    description: form.value.description,
    subject: "临床医学",
    rounds: form.value.rounds,
  };

  try {
    await api.createFlow(flow);
    showToast("创建成功");
    showCreate.value = false;
    resetForm();
    loadFlows();
  } catch (e) {
    showToast("创建失败: " + e.message);
  }
}

async function deleteFlow(flow) {
  if (!confirm(`确定删除流程：${flow.name}？`)) return;
  try {
    await api.deleteFlow(flow.id);
    showToast("已删除");
    flows.value = flows.value.filter((f) => f.id !== flow.id);
  } catch (e) {
    showToast("删除失败: " + e.message);
  }
}

function expertName(id) {
  const e = experts.value.find((x) => x.id === id);
  return e ? e.name : id;
}

onMounted(() => {
  loadFlows();
  loadExperts();
});
</script>

<template>
  <div class="flow-layout">
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>审核流程配置</h2>
        <small>{{ flows.length }} 个流程</small>
        <button class="primary-button" type="button" @click="showCreate = !showCreate; resetForm()">
          {{ showCreate ? "取消" : "+ 新建流程" }}
        </button>
      </div>

      <!-- 创建表单 -->
      <div v-if="showCreate" class="create-form">
        <div class="form-row">
          <div class="field">
            <label>流程名称 *</label>
            <input v-model="form.name" placeholder="如：执医A2试题三轮审核" />
          </div>
          <div class="field">
            <label>描述</label>
            <input v-model="form.description" placeholder="可选" />
          </div>
          <div class="field">
            <label>流程ID（自动生成）</label>
            <input v-model="form.id" placeholder="留空自动生成" />
          </div>
        </div>

        <!-- 轮次配置 -->
        <div class="rounds-section">
          <h3>审核轮次配置</h3>
          <div v-for="(round, ri) in form.rounds" :key="ri" class="round-card">
            <div class="round-header">
              <strong>第 {{ round.round_number }} 轮</strong>
              <input v-model="round.name" class="round-name-input" placeholder="轮次名称" />
              <button class="remove-btn" type="button" @click="removeRound(ri)" title="删除此轮">×</button>
            </div>

            <div class="round-config">
              <div class="config-item">
                <label>需要通过人数：</label>
                <input v-model.number="round.required_count" type="number" min="1" :max="round.expert_ids.length || 1" class="count-input" />
                <span class="config-hint">/ {{ round.expert_ids.length }} 人</span>
              </div>
              <div class="config-item">
                <label>允许修改题目：</label>
                <select v-model="round.can_modify" class="select-input">
                  <option :value="true">是</option>
                  <option :value="false">否</option>
                </select>
              </div>
              <div class="config-item full">
                <label>通过条件说明：</label>
                <input v-model="round.pass_condition" placeholder="如：检查题干、答案、解析" />
              </div>
            </div>

            <div class="expert-select">
              <label>选择审核人：</label>
              <div class="expert-chips">
                <button
                  v-for="e in experts"
                  :key="e.id"
                  type="button"
                  class="expert-chip"
                  :class="{ selected: round.expert_ids.includes(e.id) }"
                  @click="toggleExpert(ri, e.id)"
                >
                  {{ e.name }} ({{ e.department }})
                </button>
                <span v-if="!experts.length" class="no-expert">暂无专家，请先在专家库添加</span>
              </div>
            </div>
          </div>

          <button class="ghost-button" type="button" @click="addRound">+ 添加一轮</button>
        </div>

        <div class="form-actions">
          <button class="primary-button" type="button" @click="submitFlow">创建流程</button>
          <button class="ghost-button" type="button" @click="showCreate = false; resetForm()">取消</button>
        </div>
      </div>

      <!-- 流程列表 -->
      <div v-if="loading" class="loading">加载中...</div>
      <div v-else class="flow-list">
        <div v-for="flow in flows" :key="flow.id" class="flow-card">
          <div class="flow-header">
            <div>
              <strong>{{ flow.name }}</strong>
              <span class="flow-id">{{ flow.id }}</span>
            </div>
            <button class="delete-btn" type="button" @click="deleteFlow(flow)" title="删除">×</button>
          </div>
          <p v-if="flow.description" class="flow-desc">{{ flow.description }}</p>
          <div class="flow-rounds">
            <div v-for="round in flow.rounds" :key="round.round_number" class="flow-round">
              <span class="round-num">第{{ round.round_number }}轮</span>
              <span class="round-name">{{ round.name }}</span>
              <span class="round-experts">
                {{ round.expert_ids.map(id => expertName(id)).join(", ") || "未分配" }}
              </span>
              <span class="round-req">
                需 {{ round.required_count || round.expert_ids.length }}/{{ round.expert_ids.length }} 人通过
              </span>
            </div>
          </div>
        </div>
        <div v-if="!flows.length" class="empty">暂无审核流程，点击上方「新建流程」创建</div>
      </div>
    </section>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
</template>

<style scoped>
.flow-layout {
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
  grid-template-columns: 1fr 1fr 1fr;
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
}

.rounds-section h3 {
  font-size: 14px;
  color: #172033;
  margin: 0 0 12px;
}

.round-card {
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  padding: 14px;
  margin-bottom: 12px;
  background: #fff;
}

.round-header {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 12px;
}

.round-header strong {
  white-space: nowrap;
  color: #1385f8;
}

.round-name-input {
  flex: 1;
  height: 32px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 8px;
  font-size: 13px;
}

.remove-btn {
  width: 28px;
  height: 28px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  background: #fff;
  color: #c54858;
  font-size: 18px;
  cursor: pointer;
}

.remove-btn:hover {
  background: #fff0f0;
}

.round-config {
  display: flex;
  gap: 16px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}

.config-item {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
}

.config-item.full {
  width: 100%;
}

.config-item label {
  color: #6e7b8f;
  white-space: nowrap;
}

.count-input {
  width: 50px;
  height: 30px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  text-align: center;
  font-size: 13px;
}

.select-input {
  height: 30px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 8px;
  font-size: 13px;
}

.config-hint {
  color: #6e7b8f;
  font-size: 12px;
}

.config-item.full input {
  flex: 1;
  height: 30px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 8px;
  font-size: 13px;
}

.expert-select label {
  display: block;
  font-size: 12px;
  color: #6e7b8f;
  margin-bottom: 6px;
}

.expert-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.expert-chip {
  padding: 4px 10px;
  border: 1px solid #e5ebf3;
  border-radius: 16px;
  background: #fff;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.15s;
}

.expert-chip:hover {
  border-color: #1385f8;
}

.expert-chip.selected {
  background: #1385f8;
  color: #fff;
  border-color: #1385f8;
}

.no-expert {
  color: #c54858;
  font-size: 12px;
}

.form-actions {
  display: flex;
  gap: 10px;
  margin-top: 16px;
}

.flow-list {
  display: grid;
  gap: 12px;
}

.flow-card {
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  padding: 16px;
  background: #fff;
}

.flow-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}

.flow-header strong {
  font-size: 15px;
  color: #172033;
}

.flow-id {
  margin-left: 8px;
  font-size: 11px;
  color: #6e7b8f;
  font-family: monospace;
}

.flow-desc {
  margin: 6px 0 0;
  font-size: 13px;
  color: #6e7b8f;
}

.flow-rounds {
  margin-top: 12px;
  display: grid;
  gap: 6px;
}

.flow-round {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  background: #f8fbff;
  border-radius: 6px;
  font-size: 13px;
}

.round-num {
  font-weight: 700;
  color: #1385f8;
  white-space: nowrap;
}

.round-name {
  color: #172033;
  font-weight: 600;
}

.round-experts {
  flex: 1;
  color: #6e7b8f;
}

.round-req {
  font-size: 12px;
  color: #6e7b8f;
  background: #eff8ff;
  padding: 2px 8px;
  border-radius: 4px;
  white-space: nowrap;
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

.delete-btn:hover {
  background: #fff0f0;
  border-color: #c54858;
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
