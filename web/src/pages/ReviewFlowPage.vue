<script setup>
import { ref, onMounted } from "vue";
import { api } from "../api.js";

const toast = ref("");
const flows = ref([]);
const reviewers = ref([]); // 有审题权限的用户（流程选审核人用）
const users = ref([]); // 用户账号列表（把关人名映射）
const banks = ref([]); // 题库列表
const submissionBankByFlow = ref({}); // 通用流程每次送审必须明确选择本次分类子题库
const loading = ref(false);
const showCreate = ref(false);
const editingFlowId = ref(""); // 非空表示编辑模式

const form = ref({
  id: "",
  name: "",
  description: "",
  bank_id: "",
  vote_rule: "",
  final_reviewer_ids: [],
  rounds: [
    { round_number: 1, name: "专家组审核", expert_ids: [], required_count: 0, can_modify: false, pass_condition: "" },
  ],
});

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
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

async function loadReviewers() {
  try {
    const data = await api.listReviewers();
    reviewers.value = data.reviewers || [];
  } catch (e) {
    console.error(e);
  }
}

async function loadUsers() {
  try {
    const data = await api.listUsers();
    users.value = data.users || [];
  } catch (e) {
    console.error(e);
  }
}

async function loadBanks() {
  try {
    const data = await api.listBanks();
    banks.value = data.banks || [];
  } catch (e) {
    console.error(e);
  }
}

// 可选的最终把关人：拥有最终把关权限的用户
const finalReviewers = () => users.value.filter((u) => (u.permissions || []).includes("review:final"));

function resetForm() {
  form.value = {
    id: "",
    name: "",
    description: "",
    bank_id: "",
    vote_rule: "",
    final_reviewer_ids: [],
    rounds: [
      { round_number: 1, name: "专家组审核", expert_ids: [], required_count: 0, can_modify: false, pass_condition: "" },
    ],
  };
  editingFlowId.value = "";
}

function addRound() {
  const num = form.value.rounds.length + 1;
  form.value.rounds.push({
    round_number: num,
    name: `第${num}轮审核`,
    expert_ids: [],
    required_count: 0,
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
}

function toggleFinalReviewer(userId) {
  const idx = form.value.final_reviewer_ids.indexOf(userId);
  if (idx >= 0) form.value.final_reviewer_ids.splice(idx, 1);
  else form.value.final_reviewer_ids.push(userId);
}

// 回填表单进入编辑模式
function startEditFlow(flow) {
  editingFlowId.value = flow.id;
  form.value = {
    id: flow.id,
    name: flow.name,
    description: flow.description || "",
    bank_id: flow.bank_id || "",
    vote_rule: flow.vote_rule || "",
    final_reviewer_ids: [...(flow.final_reviewer_ids || [])],
    rounds: (flow.rounds || []).map((r) => ({
      round_number: r.round_number,
      name: r.name,
      expert_ids: [...(r.expert_ids || [])],
      required_count: r.required_count || 0,
      can_modify: !!r.can_modify,
      pass_condition: r.pass_condition || "",
    })),
  };
  showCreate.value = true;
}

function validateForm() {
  if (!form.value.name.trim()) {
    showToast("流程名称不能为空");
    return false;
  }
  return true;
}

const savingFlow = ref(false);

async function submitFlow() {
  if (!validateForm()) return;
  if (savingFlow.value) return; // 在途防重复提交

  const flowId = form.value.id || `flow-${Date.now()}`;
  const flow = {
    id: flowId,
    name: form.value.name,
    description: form.value.description,
    subject: "临床医学",
    bank_id: form.value.bank_id,
    vote_rule: form.value.vote_rule,
    final_reviewer_ids: form.value.final_reviewer_ids,
    rounds: form.value.rounds,
  };

  savingFlow.value = true;
  try {
    if (editingFlowId.value) {
      await api.updateFlow(editingFlowId.value, flow);
      showToast("修改成功");
    } else {
      await api.createFlow(flow);
      showToast("创建成功");
    }
    showCreate.value = false;
    resetForm();
    loadFlows();
  } catch (e) {
    showToast(`${editingFlowId.value ? "修改" : "创建"}失败: ` + e.message);
  } finally {
    savingFlow.value = false;
  }
}

// 撤销流程提交：删除该流程下所有未完成的审核任务，题目恢复提交前状态（管理员防误提交/卡死用）
const revokingFlowId = ref("");

async function revokeFlow(flow) {
  if (!confirm(`撤销「${flow.name}」的提交？\n\n将删除该流程下所有未完成审核的任务（审核中/需修改/待决断），这些题目恢复到提交前状态；\n已审核结束（已通过/已驳回）的题目不受影响。\n\n此操作用于误提交或卡死时撤回，请确认。`)) return;
  revokingFlowId.value = flow.id;
  try {
    const data = await api.revokeFlow(flow.id);
    let msg = `已撤销 ${data.revoked} 个未完成审核任务，恢复题目 ${data.restored} 道`;
    if (data.kept) msg += `，保留终态任务 ${data.kept} 个（审核已结束，不受影响）`;
    showToast(msg);
    loadFlows();
  } catch (e) {
    showToast("撤销失败: " + e.message);
  } finally {
    revokingFlowId.value = "";
  }
}

const deletingFlowId = ref("");

async function deleteFlow(flow) {
  if (!confirm(`确定删除流程：${flow.name}？`)) return;
  if (deletingFlowId.value) return; // 在途防重复提交
  deletingFlowId.value = flow.id;
  try {
    await api.deleteFlow(flow.id);
    showToast("已删除");
    flows.value = flows.value.filter((f) => f.id !== flow.id);
  } catch (e) {
    showToast("删除失败: " + e.message);
  } finally {
    deletingFlowId.value = "";
  }
}

// 批量提交审核：把流程适用题库内所有可提交题目统一提交
const submittingFlowId = ref("");

async function submitFlowBank(flow) {
  const submissionBankId = flow.bank_id || submissionBankByFlow.value[flow.id] || "";
  if (!submissionBankId) {
    showToast("通用流程送审前必须选择本次提交的分类子题库");
    return;
  }
  const bn = bankName(submissionBankId);
  const bank = banks.value.find((b) => b.id === submissionBankId);
  let conflictHint = "";
  if (bank && bank.status_counts) {
    const c = bank.status_counts;
    const reviewing = (c.reviewing || 0) + (c.conflict || 0);
    const finished = (c.published || 0) + (c.archived || 0);
    if (reviewing > 0 || finished > 0) {
      conflictHint = `\n\n${reviewing} 道已在审核中或待决断，将被跳过；${finished} 道已结束，不会重复处理。`;
    }
  }
  if (!confirm(`将「${bn}」中所有已通过 AI 检查、可送审的题目统一提交到流程「${flow.name}」？\n已在审核中、已完成或已驳回的题目会自动跳过。${conflictHint}`)) return;
  submittingFlowId.value = flow.id;
  try {
    const data = await api.submitBankReview(submissionBankId, flow.id);
    const notes = [];
    if (data.skipped_reviewing) notes.push(`已在审核中 ${data.skipped_reviewing}`);
    if (data.skipped_finished) notes.push(`已审核结束 ${data.skipped_finished}`);
    if (data.skipped_not_checked) notes.push(`等待 AI 检查 ${data.skipped_not_checked}`);

    // 失败明细分类：「无需重复提交」属于后端幂等拦截的重复提交，按已提交处理而非失败
    const failed = data.failed || [];
    const duplicates = failed.filter((f) => f.includes("无需重复提交"));
    const realFailed = failed.filter((f) => !f.includes("无需重复提交"));
    const firstReason = (list) => (list[0] || "").split(": ").slice(1).join(": ") || list[0];

    if (data.submitted > 0) {
      let msg = `批量提交完成：成功提交 ${data.submitted} 道`;
      if (notes.length) msg += `，${notes.join("，")}`;
      if (duplicates.length) msg += `，${duplicates.length} 道已提交过，无需重复提交`;
      if (realFailed.length) msg += `，失败 ${realFailed.length} 道（示例原因：${firstReason(realFailed)}）`;
      showToast(msg);
      if (realFailed.length) console.error("批量提交失败列表:", realFailed);
    } else if (duplicates.length && !realFailed.length) {
      // 全部是重复提交：明确提示无需重复操作
      showToast(`题目已成功提交过审核，无需重复提交（${duplicates.length} 道均在审核流程中）`);
    } else if (realFailed.length) {
      showToast(`提交失败 ${realFailed.length} 道。示例失败原因：${firstReason(realFailed)}`);
      console.error("批量提交失败列表:", realFailed);
    } else if (notes.length) {
      // 0 道提交且无失败：题库内题目均处于不可重复提交的状态
      showToast(`无需重复提交：题库「${bn}」内没有新可送的题目（${notes.join("，")}），题目均已提交过或已结束审核。`);
    } else {
      showToast(`题库「${bn}」内没有可提交的题目：共 ${bank?.question_count || 0} 道，请确认题目已归属本库并通过 AI 检查（可点「重新归纳」）。`);
    }
  } catch (e) {
    showToast("批量提交失败: " + e.message);
  } finally {
    submittingFlowId.value = "";
  }
}

function expertName(id) {
  const u = users.value.find((x) => x.id === id);
  if (u) return u.display_name || u.username;
  const r = reviewers.value.find((x) => x.id === id);
  return r ? r.display_name || r.username : id;
}

// 该流程自动匹配到的审题人（轮次未显式指定审核人时，批量提交后由这些人审核）
function matchedReviewers(flow) {
  const firstRound = (flow.rounds || [])[0];
  if (firstRound && (firstRound.expert_ids || []).length) {
    return firstRound.expert_ids.map(expertName).join("、");
  }
  const bankId = flow.bank_id || submissionBankByFlow.value[flow.id] || "";
  if (!bankId) return "送审时选择题库后自动匹配";
  return "按该分类子题库范围自动匹配（直接分配优先）";
}

function bankName(id) {
  if (!id) return "通用（全部题库）";
  const b = banks.value.find((x) => x.id === id);
  return b ? b.name : id;
}

// 题库下拉选项：附带可提交/审核中数量提示
function bankOptionLabel(b) {
  if (!b.status_counts) return `${b.name}（${b.question_count || 0} 题）`;
  const c = b.status_counts;
  const pending = (c.ai_draft || 0) + (c.auto_checked || 0) + (c.ai_reviewed || 0) + (c.revision_required || 0);
  const reviewing = (c.reviewing || 0) + (c.conflict || 0);
  let label = `${b.name}（${b.question_count || 0} 题`;
  if (pending) label += `，待提交 ${pending}`;
  if (reviewing) label += `，审核中 ${reviewing}`;
  label += "）";
  return label;
}

function userName(id) {
  const u = users.value.find((x) => x.id === id);
  return u ? u.display_name || u.username : id;
}

onMounted(() => {
  loadFlows();
  loadReviewers();
  loadUsers();
  loadBanks();
});
</script>

<template>
  <div class="flow-layout">
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>审核流程</h2>
        <small>{{ flows.length }} 个流程</small>
        <button class="primary-button" type="button" @click="showCreate = !showCreate; resetForm()">
          {{ showCreate ? "取消" : "+ 新建流程" }}
        </button>
      </div>

      <!-- 创建/编辑表单 -->
      <div v-if="showCreate" class="create-form">
        <div class="form-heading-row">
          <h3>{{ editingFlowId ? "编辑流程" : "新建流程" }}</h3>
        </div>
        <div class="form-row">
          <div class="field">
            <label>流程名称 *</label>
            <input v-model="form.name" placeholder="如：内科题库审核流程" />
          </div>
          <div class="field">
            <label>描述</label>
            <input v-model="form.description" placeholder="可选" />
          </div>
          <div class="field">
            <label>适用题库（空 = 通用流程）</label>
            <select v-model="form.bank_id">
              <option value="">通用（全部题库）</option>
              <option v-for="b in banks" :key="b.id" :value="b.id">{{ bankOptionLabel(b) }}</option>
            </select>
          </div>
          <div class="field">
            <label>投票规则</label>
            <select v-model="form.vote_rule">
              <option value="">通过票数推进（默认）</option>
              <option value="veto">一票否决（任一驳回直接驳回）</option>
            </select>
          </div>
        </div>
        <p class="rule-hint" v-if="form.vote_rule === 'veto'">
          任一审核人驳回则题目被驳回；提出修改则退回修改；全部通过后进入下一轮。
        </p>
        <p class="rule-hint" v-else>
          达到设定票数后进入下一轮；未达到时转交最终把关人处理。
        </p>

        <!-- 最终把关人 -->
        <div class="final-reviewers">
          <label>最终把关人（不选则由任意具备最终把关权限的用户处理）</label>
          <div class="expert-chips">
            <button
              v-for="u in finalReviewers()"
              :key="u.id"
              type="button"
              class="expert-chip final-chip"
              :class="{ selected: form.final_reviewer_ids.includes(u.id) }"
              @click="toggleFinalReviewer(u.id)"
            >
              {{ u.display_name || u.username }}
            </button>
            <span v-if="!finalReviewers().length" class="no-expert">没有用户拥有「最终把关」权限，请在用户管理中分配</span>
          </div>
        </div>

        <!-- 轮次配置 -->
        <div class="rounds-section">
          <h3>审核轮次</h3>
          <div v-for="(round, ri) in form.rounds" :key="ri" class="round-card">
            <div class="round-header">
              <strong>第 {{ round.round_number }} 轮</strong>
              <input v-model="round.name" class="round-name-input" placeholder="轮次名称" />
              <button class="remove-btn" type="button" @click="removeRound(ri)" title="删除此轮">×</button>
            </div>

            <div class="round-config">
              <div class="config-item">
                <label>通过票数：</label>
                <input v-model.number="round.required_count" type="number" min="1" class="count-input" />
                <span class="config-hint">达到该票数后进入下一轮</span>
              </div>
              <div class="config-item full">
                <label>审核说明：</label>
                <input v-model="round.pass_condition" placeholder="如：检查题干、答案、解析" />
              </div>
            </div>
            <div class="expert-select">
              <label>审核人（不选则按审题权限和题库范围分配）</label>
              <div class="expert-chips">
                <button
                  v-for="r in reviewers"
                  :key="r.id"
                  type="button"
                  class="expert-chip"
                  :class="{ selected: round.expert_ids.includes(r.id) }"
                  @click="toggleExpert(ri, r.id)"
                >
                  {{ r.display_name || r.username }}
                </button>
                <span v-if="!reviewers.length" class="no-expert">没有用户拥有审题权限，请在「用户管理」中分配审题权限</span>
              </div>
              <p class="expert-hint">
                审核范围可在“用户管理”中调整。
              </p>
            </div>
          </div>

          <button class="ghost-button" type="button" @click="addRound">+ 添加一轮</button>
        </div>

        <div class="form-actions">
          <button class="primary-button" type="button" :disabled="savingFlow" @click="submitFlow">
            {{ savingFlow ? "保存中..." : (editingFlowId ? "保存修改" : "创建流程") }}
          </button>
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
            </div>
            <div class="flow-actions">
              <select
                v-if="!flow.bank_id"
                v-model="submissionBankByFlow[flow.id]"
                class="submit-bank-select"
                title="通用流程本次要提交的分类子题库"
              >
                <option value="">选择送审题库...</option>
                <option v-for="b in banks" :key="b.id" :value="b.id">{{ bankOptionLabel(b) }}</option>
              </select>
              <button
                class="submit-bank-btn"
                type="button"
                :disabled="submittingFlowId === flow.id || (!flow.bank_id && !submissionBankByFlow[flow.id])"
                @click="submitFlowBank(flow)"
                title="批量提交该流程适用题库内的所有可提交题目"
              >
                {{ submittingFlowId === flow.id ? "提交中..." : "批量提交审核" }}
              </button>
              <button
                class="revoke-btn"
                type="button"
                :disabled="revokingFlowId === flow.id"
                @click="revokeFlow(flow)"
                title="撤销该流程下所有未完成的审核任务，题目恢复提交前状态"
              >
                {{ revokingFlowId === flow.id ? "撤销中..." : "撤销提交" }}
              </button>
              <button class="edit-btn" type="button" @click="startEditFlow(flow)" title="编辑">编辑</button>
              <button class="delete-btn" type="button" :disabled="deletingFlowId === flow.id" @click="deleteFlow(flow)" title="删除">×</button>
            </div>
          </div>
          <p v-if="flow.description" class="flow-desc">{{ flow.description }}</p>
          <p class="flow-meta">
            适用题库：{{ bankName(flow.bank_id) }}
            <span v-if="flow.vote_rule === 'veto'" class="rule-tag veto">一票否决</span>
            <span v-else class="rule-tag">通过票数推进</span>
            <span v-if="(flow.final_reviewer_ids || []).length">
              ｜ 最终把关：{{ flow.final_reviewer_ids.map(userName).join("、") }}
            </span>
            <span v-else>｜ 最终把关：任意有「最终把关」权限的用户</span>
          </p>
          <p class="flow-meta reviewers-hint">
            审核人：{{ matchedReviewers(flow) }}
          </p>
          <div class="flow-rounds">
            <div v-for="round in flow.rounds" :key="round.round_number" class="flow-round">
              <span class="round-num">第{{ round.round_number }}轮</span>
              <span class="round-name">{{ round.name }}</span>
              <span class="round-experts">
                {{ (round.expert_ids || []).length ? round.expert_ids.map(expertName).join(", ") : "自动匹配审题人" }}
              </span>
              <span class="round-req">
                {{ round.required_count ? `需${round.required_count}票通过` : "全票通过" }}
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

.form-heading-row h3 {
  margin: 0 0 12px;
  font-size: 15px;
  color: #172033;
}

.flow-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.submit-bank-select {
  max-width: 220px;
  height: 28px;
  border: 1px solid #dce8f7;
  border-radius: 6px;
  background: #fff;
  color: #3a4658;
  font-size: 12px;
}

.submit-bank-btn {
  height: 28px;
  padding: 0 12px;
  border: 1px solid #bdecd9;
  border-radius: 6px;
  background: #f0fff8;
  color: #087c55;
  font-size: 13px;
  cursor: pointer;
  white-space: nowrap;
}

.submit-bank-btn:hover {
  background: #dff5ea;
}

.submit-bank-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

.revoke-btn {
  height: 28px;
  padding: 0 12px;
  border: 1px solid #f3c2c2;
  border-radius: 6px;
  background: #fff;
  color: #c54858;
  font-size: 13px;
  cursor: pointer;
  white-space: nowrap;
}

.revoke-btn:hover {
  background: #fff0f0;
  border-color: #c54858;
}

.revoke-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}

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

.edit-btn:hover {
  background: #eff8ff;
  border-color: #1385f8;
}

.no-account-mark {
  display: inline-block;
  margin-left: 4px;
  padding: 0 4px;
  border-radius: 3px;
  background: #fff0f0;
  color: #c54858;
  font-size: 10px;
}

.expert-chip.no-account {
  border-color: #f3c2c2;
}

.expert-hint {
  margin: 8px 0 0;
  font-size: 12px;
  color: #9aa5b4;
  line-height: 1.6;
}

.form-row {
  display: grid;
  grid-template-columns: 1fr 1fr 1fr 1fr;
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

.final-reviewers {
  margin-bottom: 16px;
}

.final-reviewers label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 6px;
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

.final-chip.selected {
  background: #dd8a00;
  border-color: #dd8a00;
  color: #fff;
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

.flow-desc {
  margin: 6px 0 0;
  font-size: 13px;
  color: #6e7b8f;
}

.flow-meta {
  margin: 6px 0 0;
  font-size: 12px;
  color: #6e7b8f;
}

.reviewers-hint {
  background: #eff8ff;
  border-radius: 5px;
  padding: 4px 8px;
  display: inline-block;
  margin-top: 8px;
  color: #0571dc;
}

.rule-tag {
  display: inline-block;
  margin-left: 8px;
  padding: 1px 8px;
  border-radius: 4px;
  background: #eff8ff;
  color: #0571dc;
  font-size: 11px;
  font-weight: 600;
}

.rule-tag.veto {
  background: #fff0f0;
  color: #c54858;
}

.rule-hint {
  margin: 4px 0 12px;
  font-size: 12px;
  color: #6e7b8f;
  background: #f8fbff;
  border-radius: 5px;
  padding: 6px 10px;
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

.delete-btn:disabled,
.remove-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
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
