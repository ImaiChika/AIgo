<script setup>
import { ref, computed, onMounted } from "vue";
import { api } from "../api.js";
import QuestionDetailModal from "../components/QuestionDetailModal.vue";

const toast = ref("");
const banks = ref([]);
const professions = ref([]); // 系统已有专业（供选择范围）
const loading = ref(false);
const showCreate = ref(false);
const editingId = ref("");
const selectedBankId = ref(""); // 展开的题库详情
const detailQuestion = ref(null); // 题目详情弹窗数据

const form = ref({ id: "", name: "", description: "", professions: [] });

// 题库详情：库内题目
const bankQuestions = ref([]);
const bankQTotal = ref(0);
const bankQPage = ref(1);
const bankQSearch = ref("");
const bankQLoading = ref(false);

// 添加题目：搜索库外题目（含所有状态，可单个/批量加入）
const addSearch = ref("");
const addProfession = ref(""); // 准确筛选：专业
const addStatus = ref("");     // 准确筛选：状态
const addDifficulty = ref(""); // 准确筛选：难度
const addOutlineCode = ref(""); // 准确筛选：大纲代码前缀
const addResults = ref([]);
const addLoading = ref(false);
const addPage = ref(1);
const addTotal = ref(0);
const addHasMore = ref(false);
const addSelected = ref(new Set());
const addSelectAll = ref(false);

function toggleAddSelect(id) {
  if (addSelected.value.has(id)) addSelected.value.delete(id);
  else addSelected.value.add(id);
}

function toggleAddSelectAll() {
  if (addSelectAll.value) {
    addSelected.value.clear();
    addSelectAll.value = false;
  } else {
    addResults.value.forEach((q) => addSelected.value.add(q.id));
    addSelectAll.value = true;
  }
}

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadBanks() {
  loading.value = true;
  try {
    const data = await api.listBanks();
    banks.value = data.banks || [];
  } catch (e) {
    showToast("加载失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

async function loadProfessions() {
  try {
    const data = await api.listProfessions();
    professions.value = data.professions || [];
  } catch (e) {
    console.error(e);
  }
}

function resetForm() {
  form.value = { id: "", name: "", description: "", professions: [] };
  editingId.value = "";
}

function startCreate() {
  resetForm();
  showCreate.value = true;
}

function startEdit(b) {
  form.value = {
    id: b.id,
    name: b.name,
    description: b.description || "",
    professions: [...(b.professions || [])],
  };
  editingId.value = b.id;
  showCreate.value = true;
}

function toggleProfession(p) {
  const idx = form.value.professions.indexOf(p);
  if (idx >= 0) form.value.professions.splice(idx, 1);
  else form.value.professions.push(p);
}

// 自定义专业输入
function addCustomProfession() {
  const p = customProfession.value.trim();
  if (p && !form.value.professions.includes(p)) {
    form.value.professions.push(p);
  }
  customProfession.value = "";
}

const customProfession = ref("");

async function submitBank() {
  if (!form.value.name.trim()) {
    showToast("题库名称不能为空");
    return;
  }
  try {
    let createdBank = null;
    if (editingId.value) {
      await api.updateBank(editingId.value, form.value);
      showToast("修改成功（已自动重新归纳）");
    } else {
      createdBank = await api.createBank(form.value);
      showToast("创建成功（已自动归纳匹配专业的题目）");
    }
    showCreate.value = false;
    resetForm();
    loadBanks();
    // 创建成功后自动展开详情，方便立即搜索/批量添加题目
    if (createdBank && createdBank.id) {
      selectedBankId.value = createdBank.id;
      bankQPage.value = 1;
      bankQSearch.value = "";
      await loadBankQuestions();
    }
  } catch (e) {
    showToast(`${editingId.value ? "修改" : "创建"}失败: ` + e.message);
  }
}

async function deleteBank(b) {
  if (!confirm(`确定删除题库「${b.name}」？题库中的题目将变为未分类。`)) return;
  try {
    await api.deleteBank(b.id);
    showToast("已删除");
    if (selectedBankId.value === b.id) selectedBankId.value = "";
    loadBanks();
  } catch (e) {
    showToast("删除失败: " + e.message);
  }
}

async function openBank(b) {
  selectedBankId.value = b.id;
  bankQPage.value = 1;
  bankQSearch.value = "";
  await loadBankQuestions();
}

function closeBank() {
  selectedBankId.value = "";
  bankQuestions.value = [];
}

async function loadBankQuestions() {
  if (!selectedBankId.value) return;
  bankQLoading.value = true;
  try {
    const data = await api.listBankQuestions(selectedBankId.value, bankQSearch.value, bankQPage.value, 100);
    bankQuestions.value = bankQPage.value === 1 ? (data.questions || []) : bankQuestions.value.concat(data.questions || []);
    bankQTotal.value = data.total || 0;
  } catch (e) {
    showToast("加载题目失败: " + e.message);
  } finally {
    bankQLoading.value = false;
  }
}

function loadMoreBankQuestions() {
  bankQPage.value += 1;
  loadBankQuestions();
}

// 从本库移出（不影响题目在其他题库的归属）
async function removeFromBank(q) {
  try {
    await api.removeQuestionBank(q.id, selectedBankId.value);
    showToast("已从本库移出");
    loadBankQuestions();
    loadBanks();
  } catch (e) {
    showToast("移出失败: " + e.message);
  }
}

// 加入其他题库（多对多：加入不影响本库/其他库归属）
async function addToOtherBank(q, bankId) {
  try {
    await api.addQuestionBank(q.id, bankId);
    showToast(`已加入「${bankName(bankId)}」`);
    loadBankQuestions();
    loadBanks();
  } catch (e) {
    showToast("加入失败: " + e.message);
  }
}

// 搜索并添加题目到本库（准确筛选：专业/状态/难度/大纲；模糊搜索：关键词）
// 搜索范围为全部题目，包含已审核/已入库等所有状态；左右翻页查看
async function searchAdd(page = 1) {
  if (page === 1 && !addSearch.value.trim() && !addProfession.value && !addStatus.value && !addDifficulty.value && !addOutlineCode.value) return;
  addLoading.value = true;
  try {
    const data = await api.searchQuestions(
      addSearch.value.trim(),
      addStatus.value,
      page, 100,
      "",
      addProfession.value ? [addProfession.value] : [],
      addDifficulty.value,
      addOutlineCode.value
    );
    addResults.value = (data.questions || []).filter((q) => !(q.bank_ids || []).includes(selectedBankId.value));
    addTotal.value = data.total || 0;
    addPage.value = page;
    addHasMore.value = !!data.has_more;
    addSelectAll.value = false; // 翻页后按当前页重新判断
  } catch (e) {
    showToast("搜索失败: " + e.message);
  } finally {
    addLoading.value = false;
  }
}

// 总页数（每页 100）
const addPageCount = computed(() => Math.max(1, Math.ceil(addTotal.value / 100)));

// 翻页（勾选跨页保留：addSelected 为全局集合）
function goAddPage(p) {
  if (p < 1 || p > addPageCount.value) return;
  searchAdd(p);
}

// 可用于显示的页码窗口（最多 10 个）
const addPageWindow = computed(() => {
  const total = addPageCount.value;
  const cur = addPage.value;
  const start = Math.max(1, Math.min(cur - 4, total - 9));
  const end = Math.min(total, start + 9);
  const pages = [];
  for (let i = start; i <= end; i++) pages.push(i);
  return pages;
});

// 全选全部搜索结果（逐页翻页收集勾选，跨页保持）
async function selectAllAddResults() {
  for (let p = 1; p <= addPageCount.value; p++) {
    await searchAdd(p);
    addResults.value.forEach((q) => addSelected.value.add(q.id));
  }
  addSelectAll.value = true;
  showToast(`已全选全部 ${addSelected.value.size} 道结果`);
}

async function addToBank(q) {
  try {
    await api.addQuestionBank(q.id, selectedBankId.value);
    showToast("已加入题库");
    addResults.value = addResults.value.filter((x) => x.id !== q.id);
    addSelected.value.delete(q.id);
    loadBankQuestions();
    loadBanks();
  } catch (e) {
    showToast("加入失败: " + e.message);
  }
}

// 批量加入选中的题目
async function addSelectedToBank() {
  if (!addSelected.value.size) {
    showToast("请先勾选要加入的题目");
    return;
  }
  try {
    const data = await api.moveQuestionsBankBatch(Array.from(addSelected.value), selectedBankId.value);
    showToast(`已批量加入 ${data.moved} 道题${(data.failed || []).length ? `，失败 ${data.failed.length} 道` : ""}`);
    addResults.value = addResults.value.filter((x) => !addSelected.value.has(x.id));
    addSelected.value.clear();
    addSelectAll.value = false;
    loadBankQuestions();
    loadBanks();
  } catch (e) {
    showToast("批量加入失败: " + e.message);
  }
}

function bankById(id) {
  return banks.value.find((b) => b.id === id) || null;
}

// 多归属显示：题目所属的题库名称列表
function bankNames(ids) {
  if (!ids || !ids.length) return "未分类";
  return ids.map((id) => bankById(id)?.name || id).join("、");
}

function bankName(id) {
  return bankById(id)?.name || id;
}

function statusText(status) {
  const map = {
    ai_draft: "AI草稿", auto_checked: "已初评", ai_reviewed: "AI已检查",
    reviewing: "审核中", conflict: "待决断", revision_required: "需修改",
    rejected: "已驳回", approved: "已通过", published: "已入库", archived: "已归档",
  };
  return map[status] || status;
}

// 打开题目详情弹窗
function openDetail(q) {
  detailQuestion.value = q;
}

onMounted(() => {
  loadBanks();
  loadProfessions();
});
</script>

<template>
  <div class="banks-layout">
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>题库管理</h2>
        <small>指定专业范围自动归纳题目，也可手动微调进出</small>
        <button class="primary-button" type="button" @click="showCreate = !showCreate; resetForm()">
          {{ showCreate ? "取消" : "+ 新建题库" }}
        </button>
      </div>

      <!-- 新建/编辑表单 -->
      <div v-if="showCreate" class="create-form">
        <div class="form-row">
          <div class="field">
            <label>题库名称 *</label>
            <input v-model="form.name" placeholder="如：内科题库" />
          </div>
          <div class="field">
            <label>题库 ID（{{ editingId ? "不可修改" : "可选，留空自动生成" }}）</label>
            <input v-model="form.id" placeholder="如：bank-neike" :disabled="!!editingId" />
          </div>
          <div class="field">
            <label>描述</label>
            <input v-model="form.description" placeholder="可选" />
          </div>
        </div>

        <div class="prof-field">
          <label>专业范围（自动归纳规则）：勾选系统已有专业，或输入新专业</label>
          <div class="prof-chips">
            <button
              v-for="p in professions"
              :key="p"
              type="button"
              class="prof-chip"
              :class="{ selected: form.professions.includes(p) }"
              @click="toggleProfession(p)"
            >
              {{ p }}
            </button>
            <input v-model="customProfession" class="prof-input" placeholder="自定义专业，如：呼吸"
              @keyup.enter.prevent="addCustomProfession" />
            <button class="ghost-button" type="button" @click="addCustomProfession">添加专业</button>
          </div>
          <p class="prof-hint">
            创建/保存后，系统自动把「未分类且专业匹配」的题目归纳进本库；
            之后 AI 出题/新建题目也会自动归入第一个匹配的题库。也可点题库卡片上的「重新归纳」手动执行。
          </p>
        </div>

        <div class="form-actions">
          <button class="primary-button" type="button" @click="submitBank">
            {{ editingId ? "保存修改" : "创建题库" }}
          </button>
          <button class="ghost-button" type="button" @click="showCreate = false; resetForm()">取消</button>
        </div>
      </div>

      <!-- 题库列表 -->
      <div v-if="loading" class="loading">加载中...</div>
      <div v-else class="bank-list">
        <div v-for="b in banks" :key="b.id" class="bank-card">
          <div class="bank-header">
            <div class="bank-title" @click="selectedBankId === b.id ? closeBank() : openBank(b)">
              <strong>{{ b.name }}</strong>
              <span class="bank-id">{{ b.id }}</span>
              <span class="bank-count">{{ b.question_count || 0 }} 道题</span>
            </div>
            <div class="bank-actions">
              <button class="edit-btn" type="button" @click="startEdit(b)">编辑</button>
              <button class="delete-btn" type="button" @click="deleteBank(b)" title="删除">×</button>
            </div>
          </div>
          <p v-if="b.description" class="bank-desc">{{ b.description }}</p>
          <div v-if="(b.professions || []).length" class="bank-profs">
            <span class="prof-label">专业范围：</span>
            <span v-for="p in b.professions" :key="p" class="prof-tag">{{ p }}</span>
          </div>
          <p v-else class="bank-desc">未设置专业范围（仅手动归纳）</p>

          <!-- 题库详情：手动微调 -->
          <div v-if="selectedBankId === b.id" class="bank-detail">
            <div class="detail-head">
              <strong>题库内题目（{{ bankQTotal }} 道）</strong>
              <input v-model="bankQSearch" placeholder="在库内搜索..." @keyup.enter="bankQPage = 1; loadBankQuestions()" />
              <button class="ghost-button" type="button" @click="bankQPage = 1; loadBankQuestions()">搜索</button>
            </div>

            <div v-if="bankQLoading" class="loading">加载中...</div>
            <div v-else class="detail-list">
              <div v-for="q in bankQuestions" :key="q.id" class="detail-item">
                <span class="dq-stem" role="button" tabindex="0" title="点击查看题目全部信息" @click="openDetail(q)" @keydown.enter="openDetail(q)">{{ (q.clinical_stem || "").slice(0, 50) }}...</span>
                <span class="dq-meta">{{ q.profession || "无专业" }}</span>
                <span class="dq-status">{{ q.status }}</span>
                <button class="out-btn" type="button" @click="removeFromBank(q)" title="从本库移出（不影响其他库归属）">移出本库</button>
                <select class="move-select" :value="''" @change="addToOtherBank(q, $event.target.value)" title="同时加入其他题库（多对多）">
                  <option value="">加入其他库...</option>
                  <option v-for="other in banks.filter(x => x.id !== b.id)" :key="other.id" :value="other.id">{{ other.name }}</option>
                </select>
              </div>
              <div v-if="!bankQuestions.length" class="empty">该题库暂无题目</div>
              <button v-else-if="bankQuestions.length < bankQTotal" class="ghost-button" type="button" @click="loadMoreBankQuestions">
                加载更多（{{ bankQuestions.length }} / {{ bankQTotal }}）
              </button>
            </div>

            <!-- 手动添加题目（搜索全部题目，含已审核等所有状态，可单个/批量加入） -->
            <div class="add-section">
              <strong>手动添加题目到本库</strong>
              <p class="add-hint">在总题库中搜索（含已审核/已入库等所有状态），勾选后单个或批量加入本库</p>
              <div class="add-row">
                <input v-model="addSearch" placeholder="模糊搜索：题干/答案/ID 关键词" @keyup.enter="searchAdd" />
                <input v-model="addOutlineCode" placeholder="大纲代码（如 110.4.1）" @keyup.enter="searchAdd" />
                <select v-model="addProfession">
                  <option value="">全部专业</option>
                  <option v-for="p in professions" :key="p" :value="p">{{ p }}</option>
                </select>
                <select v-model="addStatus">
                  <option value="">全部状态</option>
                  <option value="ai_draft">AI草稿</option>
                  <option value="auto_checked">已初评</option>
                  <option value="ai_reviewed">AI已检查</option>
                  <option value="reviewing">审核中</option>
                  <option value="conflict">待决断</option>
                  <option value="approved">已通过</option>
                  <option value="rejected">已驳回</option>
                  <option value="revision_required">需修改</option>
                  <option value="published">已入库</option>
                </select>
                <select v-model="addDifficulty">
                  <option value="">全部难度</option>
                  <option value="easy">简单</option>
                  <option value="medium">中等</option>
                  <option value="hard">困难</option>
                </select>
                <button class="ghost-button" type="button" :disabled="addLoading" @click="searchAdd()">
                  {{ addLoading ? "搜索中..." : "搜索" }}
                </button>
                <button class="ghost-button" type="button" @click="addSearch=''; addProfession=''; addStatus=''; addDifficulty=''; addOutlineCode=''; addResults=[]; addTotal=0; addHasMore=false">重置</button>
              </div>
              <div v-if="addResults.length" class="add-toolbar">
                <span class="add-total">共 {{ addTotal }} 道，本页 {{ addResults.length }} 道</span>
                <label class="add-select-all">
                  <input type="checkbox" :checked="addSelectAll" @change="toggleAddSelectAll" />
                  <span>全选本页（{{ addResults.length }} 道）</span>
                </label>
                <button class="ghost-button" type="button" @click="selectAllAddResults" :disabled="addLoading">
                  {{ addLoading ? "加载中..." : "全选全部结果" }}
                </button>
                <span class="add-selected-count" v-if="addSelected.size">已勾选 {{ addSelected.size }} 道</span>
                <button class="batch-add-btn" type="button" :disabled="!addSelected.size" @click="addSelectedToBank">
                  批量加入本库（{{ addSelected.size }}）
                </button>
              </div>
              <div v-if="addResults.length" class="add-results">
                <div v-for="q in addResults" :key="q.id" class="detail-item" :class="{ checked: addSelected.has(q.id) }">
                  <input type="checkbox" :checked="addSelected.has(q.id)" @change="toggleAddSelect(q.id)" class="add-checkbox" />
                  <span class="dq-stem" role="button" tabindex="0" title="点击查看题目全部信息" @click="openDetail(q)" @keydown.enter="openDetail(q)">{{ (q.clinical_stem || "").slice(0, 50) }}...</span>
                  <span class="dq-meta">{{ q.profession || "无专业" }} ｜ {{ statusText(q.status) }} ｜ 所属：{{ bankNames(q.bank_ids) }}</span>
                  <button class="in-btn" type="button" @click="addToBank(q)">加入</button>
                </div>
              </div>
              <div v-else-if="addTotal > 0 && !addLoading" class="empty">本页无匹配（或题目均已在本库）</div>
              <div v-else-if="addTotal === 0 && !addLoading" class="empty">无匹配结果</div>

              <!-- 左右翻页 -->
              <div v-if="addPageCount > 1" class="pagination">
                <button class="page-btn" type="button" :disabled="addPage <= 1 || addLoading" @click="goAddPage(addPage - 1)">‹ 上一页</button>
                <button
                  v-for="p in addPageWindow"
                  :key="p"
                  type="button"
                  class="page-btn"
                  :class="{ active: p === addPage }"
                  :disabled="addLoading"
                  @click="goAddPage(p)"
                >{{ p }}</button>
                <button class="page-btn" type="button" :disabled="addPage >= addPageCount || addLoading" @click="goAddPage(addPage + 1)">下一页 ›</button>
                <span class="page-info">第 {{ addPage }} / {{ addPageCount }} 页</span>
              </div>
            </div>
          </div>
        </div>
        <div v-if="!banks.length" class="empty">
          暂无题库。创建题库时指定专业范围（如「呼吸」「消化」），系统会自动把匹配的题目归纳进库；
          也可以在题库详情里手动把题目移入/移出。
        </div>
      </div>
    </section>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>

  <!-- 题目详情弹窗 -->
  <QuestionDetailModal v-if="detailQuestion" :question="detailQuestion" :bank-name="bankById" @close="detailQuestion = null" />
</template>

<style scoped>
.banks-layout {
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
  grid-template-columns: 1fr 1fr 1fr;
  gap: 12px;
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

.prof-field {
  margin-top: 12px;
}

.prof-field label {
  display: block;
  font-size: 12px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 6px;
}

.prof-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  align-items: center;
}

.prof-chip {
  padding: 4px 12px;
  border: 1px solid #e5ebf3;
  border-radius: 14px;
  background: #fff;
  font-size: 12px;
  cursor: pointer;
}

.prof-chip:hover {
  border-color: #1385f8;
}

.prof-chip.selected {
  background: #1385f8;
  border-color: #1385f8;
  color: #fff;
}

.prof-input {
  height: 30px;
  width: 180px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 8px;
  font-size: 12px;
}

.prof-hint {
  margin: 8px 0 0;
  font-size: 12px;
  color: #9aa5b4;
  line-height: 1.6;
}

.form-actions {
  display: flex;
  gap: 10px;
  margin-top: 12px;
}

.bank-list {
  display: grid;
  gap: 12px;
}

.bank-card {
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  padding: 16px;
  background: #fff;
}

.bank-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.bank-title {
  cursor: pointer;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.bank-title:hover strong {
  color: #1385f8;
}

.bank-title strong {
  font-size: 15px;
  color: #172033;
}

.bank-id {
  font-size: 11px;
  color: #6e7b8f;
  font-family: monospace;
}

.bank-count {
  font-size: 12px;
  color: #1385f8;
  background: #eff8ff;
  border-radius: 4px;
  padding: 2px 8px;
  font-weight: 600;
}

.bank-actions {
  display: flex;
  gap: 8px;
}

.collect-btn {
  height: 28px;
  padding: 0 12px;
  border: 1px solid #dce8f7;
  border-radius: 6px;
  background: #fff;
  color: #199e63;
  font-size: 13px;
  cursor: pointer;
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

.bank-desc {
  margin: 6px 0 0;
  font-size: 13px;
  color: #6e7b8f;
}

.bank-profs {
  margin-top: 6px;
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  align-items: center;
}

.prof-label {
  font-size: 12px;
  color: #6e7b8f;
}

.prof-tag {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 4px;
  background: #f0f3f7;
  color: #3a4658;
}

.bank-detail {
  margin-top: 12px;
  border-top: 1px solid #e5ebf3;
  padding-top: 12px;
}

.detail-head {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 8px;
}

.detail-head strong {
  font-size: 13px;
  color: #172033;
  white-space: nowrap;
}

.detail-head input {
  flex: 1;
  height: 30px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 8px;
  font-size: 12px;
}

.detail-list {
  display: grid;
  gap: 6px;
  max-height: 320px;
  overflow-y: auto;
}

.detail-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  border: 1px solid #f0f3f7;
  border-radius: 6px;
  background: #f8fbff;
  font-size: 12px;
}

.dq-stem {
  flex: 1;
  color: #172033;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
}

.dq-stem:hover {
  color: #1385f8;
  text-decoration: underline;
}

.dq-meta {
  color: #6e7b8f;
  white-space: nowrap;
}

.dq-status {
  font-size: 11px;
  font-weight: 600;
  color: #6e7b8f;
  white-space: nowrap;
}

.out-btn {
  padding: 3px 8px;
  border: 1px solid #f3c2c2;
  border-radius: 4px;
  background: #fff;
  color: #c54858;
  font-size: 12px;
  cursor: pointer;
  white-space: nowrap;
}

.in-btn {
  padding: 3px 10px;
  border: 1px solid #bdecd9;
  border-radius: 4px;
  background: #fff;
  color: #087c55;
  font-size: 12px;
  cursor: pointer;
  white-space: nowrap;
}

.move-select {
  height: 26px;
  border: 1px solid #e5ebf3;
  border-radius: 4px;
  font-size: 12px;
}

.add-section {
  margin-top: 12px;
}

.add-section > strong {
  font-size: 13px;
  color: #172033;
}

.add-hint {
  margin: 4px 0 8px;
  font-size: 12px;
  color: #6e7b8f;
}

.add-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  margin: 8px 0;
  flex-wrap: wrap;
}

.add-select-all {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: #3a4658;
  cursor: pointer;
}

.add-selected-count {
  font-size: 12px;
  color: #1385f8;
  font-weight: 600;
}

.add-total {
  font-size: 12px;
  color: #6e7b8f;
}

.pagination {
  display: flex;
  align-items: center;
  gap: 4px;
  margin-top: 10px;
  flex-wrap: wrap;
  justify-content: center;
}

.page-btn {
  min-width: 30px;
  height: 28px;
  padding: 0 8px;
  border: 1px solid #e5ebf3;
  border-radius: 5px;
  background: #fff;
  color: #3a4658;
  font-size: 12px;
  cursor: pointer;
}

.page-btn:hover:not(:disabled) {
  border-color: #1385f8;
  color: #1385f8;
}

.page-btn.active {
  background: #1385f8;
  border-color: #1385f8;
  color: #fff;
  font-weight: 600;
}

.page-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.page-info {
  font-size: 12px;
  color: #6e7b8f;
  margin-left: 6px;
}

.batch-add-btn {
  height: 30px;
  padding: 0 14px;
  border: 0;
  border-radius: 6px;
  background: #1385f8;
  color: #fff;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
}

.batch-add-btn:hover {
  background: #0571dc;
}

.batch-add-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.add-checkbox {
  cursor: pointer;
  flex-shrink: 0;
}

.detail-item.checked {
  background: #eff8ff;
  border-color: #1385f8;
}

.add-row {
  display: flex;
  gap: 8px;
  margin-top: 6px;
}

.add-row input {
  flex: 1;
  height: 30px;
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 0 8px;
  font-size: 12px;
}

.add-results {
  margin-top: 8px;
  display: grid;
  gap: 6px;
}

.empty {
  text-align: center;
  color: #6e7b8f;
  padding: 24px;
  font-size: 13px;
  line-height: 1.8;
}

.loading {
  text-align: center;
  color: #6e7b8f;
  padding: 30px;
}
</style>
