<script setup>
import { ref, onMounted, computed } from "vue";
import { api } from "../api.js";

const toast = ref("");
const items = ref([]);
const stats = ref(null);
const total = ref(0);
const hasMore = ref(false);
const loading = ref(false);

const filterStatus = ref("");
const filterBank = ref("");
const searchQuery = ref("");
const page = ref(1);
const pageSize = 50;
const expandedId = ref(""); // 展开查看专家评语的题目ID
const banks = ref([]);
const experts = ref([]);

const statusMeta = {
  pending: { label: "未提交审核", cls: "s-pending" },
  reviewing: { label: "审核中", cls: "s-reviewing" },
  conflict: { label: "待决断", cls: "s-conflict" },
  revision_required: { label: "需修改", cls: "s-revision" },
  rejected: { label: "已驳回", cls: "s-rejected" },
  approved: { label: "已通过", cls: "s-approved" },
  published: { label: "已入库", cls: "s-published" },
};

function statusText(s) {
  return statusMeta[s]?.label || s;
}
function statusClass(s) {
  return statusMeta[s]?.cls || "";
}

// 问题题目快速筛选（需修改/已驳回/待决断）
const problemFilters = ["revision_required", "rejected", "conflict"];

function showToast(msg) {
  toast.value = msg;
  window.clearTimeout(showToast.timer);
  showToast.timer = window.setTimeout(() => { toast.value = ""; }, 3000);
}

async function loadResults(resetPage = true) {
  loading.value = true;
  if (resetPage) page.value = 1;
  try {
    const data = await api.reviewResults({
      final_status: filterStatus.value,
      bank_id: filterBank.value,
      q: searchQuery.value,
      page: page.value,
      page_size: pageSize,
    });
    items.value = page.value === 1 ? (data.items || []) : items.value.concat(data.items || []);
    total.value = data.total || 0;
    hasMore.value = !!data.has_more;
    stats.value = data.stats || null;
  } catch (e) {
    showToast("加载失败: " + e.message);
  } finally {
    loading.value = false;
  }
}

function loadMore() {
  page.value += 1;
  loadResults(false);
}

function doSearch() {
  expandedId.value = "";
  loadResults();
}

function clearFilters() {
  filterStatus.value = "";
  filterBank.value = "";
  searchQuery.value = "";
  doSearch();
}

function setStatusFilter(s) {
  filterStatus.value = filterStatus.value === s ? "" : s;
  doSearch();
}

async function loadBanks() {
  try {
    const data = await api.listBanks();
    banks.value = data.banks || [];
  } catch (e) {
    console.error(e);
  }
}

async function loadExperts() {
  try {
    const data = await api.listReviewers();
    experts.value = data.reviewers || [];
  } catch (e) {
    console.error(e);
  }
}

function bankName(id) {
  if (!id) return "未分类";
  const b = banks.value.find((x) => x.id === id);
  return b ? b.name : id;
}

function questionBanks(ids) {
  if (!ids || !ids.length) return "未分类";
  return ids.map(bankName).join("、");
}

function expertName(id) {
  const e = experts.value.find((x) => x.id === id);
  return e ? e.display_name || e.username : id;
}

function difficultyText(d) {
  const map = { easy: "简单", medium: "中等", hard: "困难" };
  if (map[d]) return map[d];
  if (d && !isNaN(Number(d))) {
    const v = Number(d);
    if (v <= 0.6) return "简单";
    if (v <= 0.8) return "中等";
    return "困难";
  }
  return d || "-";
}

// 专家评语（审核记录按轮次分组）
function recordsByRound(item) {
  const map = new Map();
  for (const r of item.records || []) {
    if (!map.has(r.round_number)) map.set(r.round_number, []);
    map.get(r.round_number).push(r);
  }
  return Array.from(map.entries()).sort((a, b) => a[0] - b[0]);
}

function conclusionText(c) {
  const map = {
    approved: "通过",
    rejected: "驳回",
    revision_required: "需修改",
    final_approved: "决断：通过",
    final_rejected: "决断：驳回",
    final_revision_required: "决断：退回修改",
  };
  return map[c] || c;
}

function conclusionClass(c) {
  if (c.startsWith("final_")) return "c-final";
  if (c === "approved") return "c-approved";
  if (c === "rejected") return "c-rejected";
  return "c-revision";
}

onMounted(() => {
  loadResults();
  loadBanks();
  loadExperts();
});
</script>

<template>
  <div class="results-layout">
    <!-- 统计卡片 -->
    <section class="panel" v-if="stats">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>审核结果统计</h2>
        <small>共 {{ stats.total }} 道题</small>
      </div>
      <div class="stats-grid">
        <button type="button" class="stat-card s-pending" :class="{ active: filterStatus === 'pending' }" @click="setStatusFilter('pending')">
          <strong>{{ stats.pending }}</strong><span>未提交审核</span>
        </button>
        <button type="button" class="stat-card s-reviewing" :class="{ active: filterStatus === 'reviewing' }" @click="setStatusFilter('reviewing')">
          <strong>{{ stats.reviewing }}</strong><span>审核中</span>
        </button>
        <button type="button" class="stat-card s-conflict" :class="{ active: filterStatus === 'conflict' }" @click="setStatusFilter('conflict')">
          <strong>{{ stats.conflict }}</strong><span>待决断</span>
        </button>
        <button type="button" class="stat-card s-revision" :class="{ active: filterStatus === 'revision_required' }" @click="setStatusFilter('revision_required')">
          <strong>{{ stats.revision_required }}</strong><span>需修改</span>
        </button>
        <button type="button" class="stat-card s-rejected" :class="{ active: filterStatus === 'rejected' }" @click="setStatusFilter('rejected')">
          <strong>{{ stats.rejected }}</strong><span>已驳回</span>
        </button>
        <button type="button" class="stat-card s-approved" :class="{ active: filterStatus === 'approved' }" @click="setStatusFilter('approved')">
          <strong>{{ stats.approved }}</strong><span>已通过</span>
        </button>
        <button type="button" class="stat-card s-published" :class="{ active: filterStatus === 'published' }" @click="setStatusFilter('published')">
          <strong>{{ stats.published }}</strong><span>已入库</span>
        </button>
      </div>
    </section>

    <!-- 列表 -->
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>题目审核情况</h2>
        <small>共 {{ total }} 道匹配</small>
        <button class="ghost-button" type="button" @click="setStatusFilter(problemFilters.includes(filterStatus) ? filterStatus : 'rejected')">
          {{ problemFilters.includes(filterStatus) ? "取消问题筛选" : "找出问题题目" }}
        </button>
      </div>

      <div class="filter-row">
        <input v-model="searchQuery" placeholder="搜索ID、题干、专业..." @keyup.enter="doSearch" />
        <select v-model="filterStatus" @change="doSearch">
          <option value="">全部状态</option>
          <option value="pending">未提交审核</option>
          <option value="reviewing">审核中</option>
          <option value="conflict">待决断</option>
          <option value="revision_required">需修改</option>
          <option value="rejected">已驳回</option>
          <option value="approved">已通过</option>
          <option value="published">已入库</option>
        </select>
        <select v-model="filterBank" @change="doSearch">
          <option value="">全部题库</option>
          <option value="__unclassified__">未分类</option>
          <option v-for="b in banks" :key="b.id" :value="b.id">{{ b.name }}</option>
        </select>
        <button class="ghost-button" type="button" @click="doSearch">搜索</button>
        <button class="ghost-button" type="button" @click="clearFilters">重置</button>
      </div>

      <div v-if="loading && !items.length" class="loading">加载中...</div>
      <div v-else class="result-list">
        <div v-for="item in items" :key="item.question.id" class="result-card">
          <div class="result-head" @click="expandedId = expandedId === item.question.id ? '' : item.question.id">
            <div class="r-info">
              <span class="r-stem">{{ (item.question.clinical_stem || "").slice(0, 70) }}{{ (item.question.clinical_stem || "").length > 70 ? "..." : "" }}</span>
              <span class="r-meta">
                <span v-if="item.question.profession">专业：{{ item.question.profession }}</span>
                <span>题库：{{ questionBanks(item.question.bank_ids) }}</span>
                <span>难度：{{ item.question.difficulty }}</span>
              </span>
            </div>
            <span class="r-status" :class="statusClass(item.final_status)">{{ statusText(item.final_status) }}</span>
            <span class="r-expand">{{ expandedId === item.question.id ? "收起 ▲" : "查看评语 ▼" }}</span>
          </div>

          <!-- 展开：完整题目详情 + 任务信息 + 专家评语 -->
          <div v-if="expandedId === item.question.id" class="result-detail">
            <!-- 题目完整信息 -->
            <div class="q-detail">
              <div class="q-params">
                <span v-if="item.question.outline_code" class="q-param"><label>大纲代码</label>{{ item.question.outline_code }}</span>
                <span v-if="item.question.profession" class="q-param"><label>专业</label>{{ item.question.profession }}</span>
                <span v-if="item.question.system" class="q-param"><label>系统</label>{{ item.question.system }}</span>
                <span class="q-param"><label>难度</label>{{ difficultyText(item.question.difficulty) }}</span>
                <span v-if="item.question.cognitive_level" class="q-param"><label>认知层次</label>{{ item.question.cognitive_level }}</span>
                <span class="q-param"><label>题库</label>{{ questionBanks(item.question.bank_ids) }}</span>
                <span class="q-param"><label>版本</label>v{{ item.question.version }}</span>
                <span class="q-param"><label>ID</label>{{ item.question.id }}</span>
              </div>
              <div v-if="item.question.exam_points" class="q-sub">
                <label>考核要点</label>{{ item.question.exam_points }}
              </div>
              <div class="q-stem-full">
                <label>题干</label>
                <p>{{ item.question.clinical_stem }}</p>
              </div>
              <div class="q-opts">
                <label>选项</label>
                <div v-for="opt in item.question.options || []" :key="opt.label" class="q-opt" :class="{ correct: opt.label === item.question.answer }">
                  <span class="q-opt-label">{{ opt.label }}</span>
                  <span>{{ opt.text }}</span>
                  <span v-if="opt.label === item.question.answer" class="q-correct">✓ 正确答案</span>
                </div>
              </div>
              <div class="q-sub">
                <label>解析</label>
                <span v-if="item.question.explanation">{{ item.question.explanation }}</span>
                <span v-else class="q-none">（无解析）</span>
              </div>
            </div>

            <div v-if="item.task" class="task-line">
              审核任务：第 {{ item.task.current_round }} 轮 / 共
              {{ item.task.round_results?.length || 1 }} 轮 ｜ 流程：{{ item.task.flow_id }} ｜
              审核人：{{ (item.task.assigned_to || []).map(expertName).join("、") || "自动匹配" }}
              <span v-if="item.task.final_decision" class="final-decision">
                ｜ 最终决断：{{ expertName(item.task.final_decision.expert_id) }} → {{ conclusionText("final_" + item.task.final_decision.conclusion) }}
                <span v-if="item.task.final_decision.opinion">（{{ item.task.final_decision.opinion }}）</span>
              </span>
            </div>
            <div v-else class="task-line none">从未提交审核</div>

            <div v-if="(item.records || []).length" class="rounds">
              <div v-for="[round, recs] in recordsByRound(item)" :key="round" class="round-block">
                <div class="round-title">第 {{ round }} 轮（{{ recs.length }} 条意见）</div>
                <div v-for="rec in recs" :key="rec.id" class="record-item">
                  <span class="rec-expert">{{ expertName(rec.expert_id) }}</span>
                  <span class="rec-conclusion" :class="conclusionClass(rec.review_status)">{{ conclusionText(rec.review_status) }}</span>
                  <span class="rec-opinion">{{ rec.opinion || "（无评语）" }}</span>
                  <span class="rec-time">{{ new Date(rec.created_at).toLocaleString() }}</span>
                </div>
              </div>
            </div>
            <div v-else class="task-line none">暂无专家审核记录</div>
          </div>
        </div>

        <div v-if="!items.length && !loading" class="empty">暂无匹配的题目</div>
        <button v-else-if="hasMore" class="load-more-btn" type="button" @click="loadMore">
          加载更多（已显示 {{ items.length }} / {{ total }}）
        </button>
      </div>
    </section>
  </div>

  <div class="toast" :class="{ show: toast }" role="status" aria-live="polite">{{ toast }}</div>
</template>

<style scoped>
.results-layout {
  display: grid;
  gap: 16px;
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(7, 1fr);
  gap: 10px;
}

.stat-card {
  padding: 14px 8px;
  border-radius: 8px;
  border: 1px solid transparent;
  text-align: center;
  cursor: pointer;
  background: #fff;
  transition: all 0.15s;
}

.stat-card:hover {
  border-color: #1385f8;
}

.stat-card.active {
  border-color: #1385f8;
  box-shadow: 0 0 0 2px rgba(19, 133, 248, 0.15);
}

.stat-card strong {
  display: block;
  font-size: 22px;
}

.stat-card span {
  font-size: 12px;
}

.s-pending strong { color: #6e7b8f; }
.s-reviewing strong { color: #0571dc; }
.s-conflict strong { color: #b93a7c; }
.s-revision strong { color: #c07b22; }
.s-rejected strong { color: #c54858; }
.s-approved strong { color: #087c55; }
.s-published strong { color: #1385f8; }

.filter-row {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}

.filter-row input {
  flex: 1;
  min-width: 200px;
  height: 34px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 10px;
  font-size: 13px;
}

.filter-row select {
  height: 34px;
  border: 1px solid #e5ebf3;
  border-radius: 7px;
  padding: 0 8px;
  font-size: 13px;
}

.result-list {
  display: grid;
  gap: 8px;
}

.result-card {
  border: 1px solid #e5ebf3;
  border-radius: 8px;
  background: #fff;
}

.result-head {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  cursor: pointer;
}

.result-head:hover {
  background: #f8fbff;
}

.r-info {
  flex: 1;
  min-width: 0;
}

.r-stem {
  display: block;
  font-size: 13px;
  color: #172033;
  font-weight: 600;
}

.r-meta {
  display: flex;
  gap: 12px;
  margin-top: 4px;
  font-size: 12px;
  color: #6e7b8f;
}

.r-status {
  font-size: 12px;
  font-weight: 700;
  padding: 3px 10px;
  border-radius: 4px;
  white-space: nowrap;
}

.s-pending { background: #f0f3f7; color: #6e7b8f; }
.s-reviewing { background: #eff8ff; color: #0571dc; }
.s-conflict { background: #fdf0f8; color: #b93a7c; }
.s-revision { background: #fdf2e3; color: #c07b22; }
.s-rejected { background: #fff0f0; color: #c54858; }
.s-approved { background: #f0fff8; color: #087c55; }
.s-published { background: #eff8ff; color: #1385f8; }

.r-expand {
  font-size: 12px;
  color: #1385f8;
  white-space: nowrap;
}

.result-detail {
  border-top: 1px solid #f0f3f7;
  padding: 12px 14px;
  background: #fafcff;
}

/* 题目完整详情 */
.q-detail {
  border: 1px solid #dce8f7;
  border-radius: 8px;
  padding: 12px;
  background: #fff;
  margin-bottom: 10px;
}

.q-params {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 16px;
  margin-bottom: 8px;
}

.q-param {
  font-size: 12px;
  color: #3a4658;
}

.q-param label {
  color: #6e7b8f;
  margin-right: 5px;
}

.q-sub {
  font-size: 13px;
  color: #3a4658;
  margin-bottom: 8px;
}

.q-sub label {
  display: block;
  font-size: 11px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 2px;
}

.q-stem-full {
  margin-bottom: 8px;
}

.q-stem-full label {
  display: block;
  font-size: 11px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 2px;
}

.q-stem-full p {
  margin: 0;
  font-size: 13px;
  line-height: 1.7;
  color: #172033;
}

.q-opts {
  margin-bottom: 8px;
}

.q-opts label {
  display: block;
  font-size: 11px;
  font-weight: 600;
  color: #6e7b8f;
  margin-bottom: 2px;
}

.q-opt {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 3px 0;
  font-size: 13px;
  color: #3a4658;
}

.q-opt.correct {
  color: #087c55;
  font-weight: 600;
}

.q-opt-label {
  width: 20px;
  height: 20px;
  display: grid;
  place-items: center;
  border-radius: 50%;
  background: #e8f0f8;
  color: #1385f8;
  font-size: 11px;
  font-weight: 700;
}

.q-correct {
  font-size: 11px;
  color: #087c55;
}

.q-none {
  color: #9aa5b4;
  font-size: 12px;
}

.task-line {
  font-size: 12px;
  color: #3a4658;
  margin-bottom: 10px;
}

.task-line.none {
  color: #9aa5b4;
}

.final-decision {
  color: #b93a7c;
  font-weight: 600;
}

.rounds {
  display: grid;
  gap: 10px;
}

.round-block {
  border: 1px solid #e5ebf3;
  border-radius: 6px;
  padding: 8px 10px;
  background: #fff;
}

.round-title {
  font-size: 12px;
  font-weight: 700;
  color: #1385f8;
  margin-bottom: 6px;
}

.record-item {
  display: flex;
  gap: 10px;
  align-items: baseline;
  padding: 4px 0;
  font-size: 12px;
  border-bottom: 1px dashed #f0f3f7;
}

.record-item:last-child {
  border-bottom: 0;
}

.rec-expert {
  color: #172033;
  font-weight: 600;
  white-space: nowrap;
}

.rec-conclusion {
  font-weight: 700;
  white-space: nowrap;
}

.c-approved { color: #087c55; }
.c-rejected { color: #c54858; }
.c-revision { color: #c07b22; }
.c-final { color: #b93a7c; }

.rec-opinion {
  flex: 1;
  color: #3a4658;
}

.rec-time {
  color: #9aa5b4;
  white-space: nowrap;
}

.load-more-btn {
  width: 100%;
  padding: 10px;
  border: 1px dashed #dce8f7;
  border-radius: 6px;
  background: #f8fbff;
  color: #1385f8;
  font-size: 13px;
  cursor: pointer;
}

.loading {
  text-align: center;
  color: #6e7b8f;
  padding: 30px;
}

.empty {
  text-align: center;
  color: #6e7b8f;
  padding: 30px;
}
</style>
