<script setup>
import { ref, computed, onMounted } from "vue";
import { api } from "../api.js";
import { hasPerm } from "../auth.js";

const stats = ref(null);
const loading = ref(false);
const error = ref("");
const canViewGlobal = computed(() => hasPerm("question:view_global"));
const statsScope = ref(canViewGlobal.value ? "global" : "personal");
// 后端仅授权“全局数据 + 用户管理权限”时才返回 user_count；缺省即隐藏卡片。
const hasUserCount = computed(() => stats.value?.user_count != null);

const statusNames = {
  ai_draft: "草稿",
  auto_checked: "已初评",
  ai_reviewed: "已检查",
  reviewing: "审核中",
  conflict: "待决断",
  revision_required: "需修改",
  rejected: "已驳回",
  published: "已通过",
  archived: "已归档",
};

// 题库分层（与后端 domain.QuestionTier 一致）
const tierNames = {
  formal: "正式题库",
  working: "待审核题库",
  eliminated: "淘汰题库",
};

const verdictNames = {
  pass: "检查通过",
  issues_found: "发现问题",
  reject: "建议淘汰",
};

const taskStatusNames = {
  pending: "待执行",
  running: "执行中",
  succeeded: "已完成",
  done: "已完成",
  failed: "待重试",
  exhausted: "最终失败",
};

function tierName(t) {
  return tierNames[t] || t;
}

function statusName(s) {
  return statusNames[s] || s;
}

function displayKnowledgeCategory(name) {
  // 知识点目录的缺省分类与题库归属不是同一概念。
  return name === "未分类" ? "未设置分类" : name;
}

async function loadStats(scope = statsScope.value) {
  statsScope.value = scope;
  loading.value = true;
  error.value = "";
  try {
    stats.value = await api.stats(scope);
  } catch (e) {
    error.value = e.message;
  } finally {
    loading.value = false;
  }
}

// 计算条形图宽度
function barWidth(count, total) {
  if (!total) return "0%";
  return Math.max(2, Math.round((count / total) * 100)) + "%";
}

// ===== 派生数据 =====

// 题库分层计数（后端按查看权限返回，无权层不出现）
const tierCounts = computed(() => stats.value?.tier_counts || {});

// 审核流程现状（漏斗各环节，按状态归组）
const funnelSteps = computed(() => {
  const sd = stats.value?.status_distribution || {};
  const tc = tierCounts.value;
  const pendingSubmit = (sd.ai_draft || 0) + (sd.auto_checked || 0) + (sd.ai_reviewed || 0);
  const steps = [
    { label: "待送审（草稿/已检查）", count: pendingSubmit, cls: "blue" },
    { label: "审核中", count: sd.reviewing || 0, cls: "teal" },
    { label: "待决断", count: sd.conflict || 0, cls: "orange" },
    { label: "退回待修改", count: sd.revision_required || 0, cls: "orange" },
  ];
  // 正式/淘汰层仅在有查看权限时由后端返回
  if ("formal" in tc) steps.push({ label: "已定稿（正式题库）", count: tc.formal, cls: "green" });
  if ("eliminated" in tc) steps.push({ label: "已淘汰（驳回/归档）", count: tc.eliminated, cls: "red" });
  return steps;
});

// 全周期定稿转化率：正式定稿 ÷（待审核 + 正式 + 淘汰 + AI检查淘汰）
const finalizeRate = computed(() => {
  const tc = tierCounts.value;
  const discarded = stats.value?.ai_check?.discarded || 0;
  const total = (tc.working || 0) + (tc.formal || 0) + (tc.eliminated || 0) + discarded;
  if (!total || !("formal" in tc)) return null;
  return ((tc.formal / total) * 100).toFixed(1) + "%";
});

// 近90天每日新增（补齐空缺日期，前端日历日为准）
const trendList = computed(() => {
  const trend = stats.value?.creation_trend || {};
  const days = [];
  const now = new Date();
  for (let i = 89; i >= 0; i--) {
    const d = new Date(now);
    d.setDate(now.getDate() - i);
    const key = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
    days.push({ date: key, label: `${d.getMonth() + 1}/${d.getDate()}`, count: trend[key] || 0 });
  }
  return days;
});

const trendMax = computed(() => Math.max(1, ...trendList.value.map((d) => d.count)));
const trendTotal = computed(() => trendList.value.reduce((sum, d) => sum + d.count, 0));

// 知识点覆盖率
const coveragePct = computed(() => {
  const total = stats.value?.kp_version?.total;
  if (!total) return null;
  return (((stats.value?.kp_covered || 0) / total) * 100).toFixed(1) + "%";
});

// AI 检查通过率（通过 / 全部检查结论 + 淘汰留档）。
// 淘汰题的检查结果行随题目删除级联清除，仅剩 ai_check_discards 档案；
// 分母必须并入 discarded，否则未通过检查的题不进分母，通过率虚高。
const aiPassRate = computed(() => {
  const v = stats.value?.ai_check?.verdicts || {};
  const discarded = stats.value?.ai_check?.discarded || 0;
  const total = (v.pass || 0) + (v.issues_found || 0) + (v.reject || 0) + discarded;
  if (!total) return null;
  return (((v.pass || 0) / total) * 100).toFixed(1) + "%";
});

onMounted(loadStats);
</script>

<template>
  <div class="stats-layout">
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>数据统计</h2>
        <div class="heading-actions">
          <div v-if="canViewGlobal" class="stats-scope-tabs" aria-label="统计范围">
            <button type="button" :class="{ active: statsScope === 'global' }" @click="loadStats('global')">全局数据</button>
            <button type="button" :class="{ active: statsScope === 'personal' }" @click="loadStats('personal')">我的数据</button>
          </div>
          <button class="ghost-button" type="button" @click="loadStats()">刷新</button>
        </div>
      </div>

      <div v-if="loading" class="loading">加载中...</div>
      <div v-else-if="error" class="error-box">加载失败: {{ error }}</div>
      <div v-else-if="stats" class="stats-content">
        <!-- 概览卡片：用户数是系统级指标，仅后端在“全局数据+用户管理权限”时返回；
             字段缺省时隐藏卡片，避免把“无权查看”显示成“用户数 0” -->
        <div class="cards-row" :class="hasUserCount ? 'six' : 'five'">
          <div class="stat-card">
            <strong>{{ stats.question_count }}</strong>
            <span>待审核题库</span>
          </div>
          <div class="stat-card">
            <strong>{{ "formal" in tierCounts ? tierCounts.formal : "-" }}</strong>
            <span>正式题库</span>
          </div>
          <div class="stat-card">
            <strong>{{ stats.knowledge_count }}</strong>
            <span>大纲要点数</span>
            <em v-if="stats.kp_version?.version_name" class="card-sub">{{ stats.kp_version.version_name }}</em>
          </div>
          <div class="stat-card">
            <strong>{{ coveragePct ?? "-" }}</strong>
            <span>大纲要点覆盖率</span>
            <em class="card-sub">已出题 {{ stats.kp_covered ?? 0 }} / {{ stats.kp_version?.total ?? 0 }}</em>
          </div>
          <div class="stat-card">
            <strong>{{ stats.ai_check?.discarded ?? 0 }}</strong>
            <span>AI 检查淘汰</span>
          </div>
          <div v-if="hasUserCount" class="stat-card">
            <strong>{{ stats.user_count }}</strong>
            <span>用户数</span>
          </div>
        </div>

        <!-- 近90天生成趋势 -->
        <div class="trend-card">
          <h3>近 90 天生成趋势 <small class="muted">共 {{ trendTotal }} 题</small></h3>
          <div class="trend-chart" role="img" :aria-label="`近 90 天共生成 ${trendTotal} 题`">
            <div class="trend-plot">
              <div v-for="d in trendList" :key="d.date" class="trend-col" :title="`${d.date}：${d.count} 题`">
                <div v-if="d.count > 0" class="trend-bar" :style="{ height: Math.max(3, Math.round((d.count / trendMax) * 68)) + 'px' }"></div>
              </div>
            </div>
            <div class="trend-labels" aria-hidden="true">
              <span
                v-for="(d, i) in trendList"
                :key="`label-${d.date}`"
                class="trend-label"
                :class="{ visible: i % 15 === 0 || i === trendList.length - 1 }"
              >{{ d.label }}</span>
            </div>
          </div>
        </div>

        <!-- 审核流程现状 + AI 质量检查 -->
        <div class="charts-grid">
          <div class="chart-card">
            <h3>
              审核流程现状
              <small v-if="finalizeRate" class="muted">定稿转化率 {{ finalizeRate }}</small>
            </h3>
            <div v-for="step in funnelSteps" :key="step.label" class="bar-row">
              <span class="bar-label wide">{{ step.label }}</span>
              <div class="bar-track">
                <div class="bar-fill" :class="step.cls" :style="{ width: barWidth(step.count, Math.max(1, ...funnelSteps.map((s) => s.count))) }"></div>
              </div>
              <span class="bar-count">{{ step.count }}</span>
            </div>
            <p v-if="!funnelSteps.length" class="empty-hint">暂无数据</p>
          </div>

          <div class="chart-card">
            <h3>
              AI 质量检查
              <small v-if="aiPassRate" class="muted">通过率 {{ aiPassRate }}</small>
            </h3>
            <div v-for="(count, verdict) in stats.ai_check?.verdicts || {}" :key="verdict" class="bar-row">
              <span class="bar-label wide">{{ verdictNames[verdict] || verdict }}</span>
              <div class="bar-track">
                <div class="bar-fill teal" :style="{ width: barWidth(count, Math.max(1, Object.values(stats.ai_check.verdicts).reduce((a, b) => a + b, 0))) }"></div>
              </div>
              <span class="bar-count">{{ count }}</span>
            </div>
            <p v-if="!Object.keys(stats.ai_check?.verdicts || {}).length" class="empty-hint">暂无检查记录</p>
            <div v-if="Object.keys(stats.ai_check?.tasks || {}).length" class="chip-row">
              <span v-for="(count, t) in stats.ai_check.tasks" :key="t" class="chip">
                {{ taskStatusNames[t] || t }} {{ count }}
              </span>
            </div>
          </div>
        </div>

        <div class="charts-grid">
          <!-- 题库分层 -->
          <div class="chart-card">
            <h3>题库分层</h3>
            <div v-for="(label, tier) in tierNames" :key="tier" class="bar-row">
              <template v-if="tier in tierCounts">
                <span class="bar-label">{{ label }}</span>
                <div class="bar-track">
                  <div class="bar-fill blue" :style="{ width: barWidth(tierCounts[tier], Math.max(1, ...Object.values(tierCounts))) }"></div>
                </div>
                <span class="bar-count">{{ tierCounts[tier] }}</span>
              </template>
            </div>
            <p v-if="!Object.keys(tierCounts).length" class="empty-hint">暂无可见题库</p>
          </div>

          <!-- 审核状态分布 -->
          <div class="chart-card">
            <h3>审核状态分布（待审核题库）</h3>
            <div v-for="(count, status) in stats.status_distribution || {}" :key="status" class="bar-row">
              <span class="bar-label">{{ statusName(status) }}</span>
              <div class="bar-track">
                <div class="bar-fill green" :style="{ width: barWidth(count, stats.question_count) }"></div>
              </div>
              <span class="bar-count">{{ count }}</span>
            </div>
            <p v-if="!Object.keys(stats.status_distribution || {}).length" class="empty-hint">暂无数据</p>
          </div>

          <!-- 难度分布 -->
          <div class="chart-card">
            <h3>难度分布</h3>
            <div v-for="(count, diff) in stats.difficulty_distribution || {}" :key="diff" class="bar-row">
              <span class="bar-label">{{ diff }}</span>
              <div class="bar-track">
                <div class="bar-fill orange" :style="{ width: barWidth(count, stats.question_count) }"></div>
              </div>
              <span class="bar-count">{{ count }}</span>
            </div>
            <p v-if="!Object.keys(stats.difficulty_distribution || {}).length" class="empty-hint">暂无数据</p>
          </div>

          <!-- 专业分布 -->
          <div class="chart-card">
            <h3>专业分布</h3>
            <div v-for="(count, profession) in stats.profession_distribution || {}" :key="profession" class="bar-row">
              <span class="bar-label">{{ profession }}</span>
              <div class="bar-track">
                <div class="bar-fill teal" :style="{ width: barWidth(count, Math.max(1, ...Object.values(stats.profession_distribution || { x: 1 }))) }"></div>
              </div>
              <span class="bar-count">{{ count }}</span>
            </div>
            <p v-if="!Object.keys(stats.profession_distribution || {}).length" class="empty-hint">暂无数据</p>
          </div>

          <!-- 大纲分类分布 -->
          <div class="chart-card">
            <h3>大纲分类分布</h3>
            <div v-for="(count, cat) in stats.knowledge_categories || {}" :key="cat" class="bar-row">
              <span class="bar-label">{{ displayKnowledgeCategory(cat) }}</span>
              <div class="bar-track">
                <div class="bar-fill orange" :style="{ width: barWidth(count, stats.knowledge_count) }"></div>
              </div>
              <span class="bar-count">{{ count }}</span>
            </div>
            <p v-if="!Object.keys(stats.knowledge_categories || {}).length" class="empty-hint">暂无数据</p>
          </div>
        </div>

        <!-- 附加信息行 -->
        <div class="extra-row">
          <div class="chart-card" v-if="Object.keys(stats.kp_versions_total || {}).length">
            <h3>各大纲版本要点总数</h3>
            <div class="chip-row">
              <span v-for="(count, name) in stats.kp_versions_total" :key="name" class="chip" :class="{ active: name === stats.kp_version?.version_name }">
                {{ name }}：{{ count }}
              </span>
            </div>
          </div>
          <div class="chart-card" v-if="Object.keys(stats.batch_jobs || {}).length">
            <h3>批量推理任务（近 200 个）</h3>
            <div class="chip-row">
              <span class="chip">总计 {{ stats.batch_jobs.total || 0 }}</span>
              <span v-for="(count, st) in stats.batch_jobs" :key="st" v-show="st !== 'total'" class="chip">
                {{ st }} {{ count }}
              </span>
            </div>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.stats-layout {
  max-width: 100%;
}

.heading-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-left: auto;
}

.stats-scope-tabs {
  display: inline-flex;
  padding: 3px;
  border: 1px solid #dce8f7;
  border-radius: 7px;
  background: #f7faff;
}

.stats-scope-tabs button {
  border: 0;
  border-radius: 5px;
  background: transparent;
  color: #6e7b8f;
  padding: 6px 10px;
  font-size: 11px;
  cursor: pointer;
}

.stats-scope-tabs button.active {
  background: #e6f3ff;
  color: #0d6ecd;
  font-weight: 600;
}

.loading {
  text-align: center;
  color: #6e7b8f;
  padding: 40px;
}

.error-box {
  padding: 12px 16px;
  background: #fff0f0;
  color: #c54858;
  border-radius: 6px;
  font-size: 13px;
}

.cards-row {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 16px;
  margin-bottom: 20px;
}

.cards-row.six {
  grid-template-columns: repeat(6, 1fr);
}

.cards-row.five {
  grid-template-columns: repeat(5, 1fr);
}

@media (max-width: 1100px) {
  .cards-row.six,
  .cards-row.five {
    grid-template-columns: repeat(3, 1fr);
  }
}

.stat-card {
  padding: 20px 12px;
  background: #f8fbff;
  border: 1px solid #dce8f7;
  border-radius: 10px;
  text-align: center;
}

.stat-card strong {
  display: block;
  font-size: 28px;
  color: #1385f8;
}

.stat-card span {
  font-size: 13px;
  color: #6e7b8f;
}

.card-sub {
  display: block;
  font-style: normal;
  font-size: 11px;
  color: #9aa5b4;
  margin-top: 2px;
}

.trend-card {
  border: 1px solid #e5ebf3;
  border-radius: 10px;
  padding: 16px;
  background: #fff;
  margin-bottom: 16px;
}

.trend-card h3 {
  margin: 0 0 12px;
  font-size: 14px;
  color: #172033;
}

.trend-chart {
  display: grid;
  grid-template-rows: 72px 22px;
  height: 94px;
  overflow: hidden;
}

.trend-plot,
.trend-labels {
  display: grid;
  grid-template-columns: repeat(90, minmax(2px, 1fr));
  column-gap: 3px;
}

.trend-plot {
  align-items: end;
  border-bottom: 1px solid #dce8f7;
}

.trend-col {
  display: flex;
  align-items: flex-end;
  justify-content: flex-end;
  height: 100%;
  min-width: 0;
}

.trend-bar {
  width: 100%;
  min-height: 3px;
  background: #1385f8;
  border-radius: 3px 3px 0 0;
  opacity: 0.88;
}

.trend-labels {
  align-items: start;
}

.trend-label {
  visibility: hidden;
  justify-self: center;
  font-size: 9px;
  color: #9aa5b4;
  margin-top: 5px;
  white-space: nowrap;
}

.trend-label.visible {
  visibility: visible;
}

.trend-label:first-child {
  justify-self: start;
}

.trend-label:last-child {
  justify-self: end;
}

.charts-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
  margin-bottom: 16px;
}

.extra-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}

.chart-card {
  border: 1px solid #e5ebf3;
  border-radius: 10px;
  padding: 16px;
  background: #fff;
}

.chart-card h3 {
  margin: 0 0 14px;
  font-size: 14px;
  color: #172033;
}

.muted {
  font-size: 12px;
  color: #9aa5b4;
  font-weight: normal;
  margin-left: 6px;
}

.bar-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}

.bar-label {
  width: 90px;
  font-size: 12px;
  color: #3a4658;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.bar-label.wide {
  width: 150px;
}

.bar-track {
  flex: 1;
  height: 14px;
  background: #f0f3f7;
  border-radius: 7px;
  overflow: hidden;
}

.bar-fill {
  height: 100%;
  border-radius: 7px;
  transition: width 0.3s;
}

.bar-fill.blue { background: #1385f8; }
.bar-fill.green { background: #199e63; }
.bar-fill.orange { background: #dd8a00; }
.bar-fill.teal { background: #0f9b8e; }
.bar-fill.red { background: #c54858; }

.bar-count {
  width: 36px;
  text-align: right;
  font-size: 12px;
  font-weight: 600;
  color: #6e7b8f;
}

.chip-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.chip {
  padding: 4px 10px;
  background: #f0f5fb;
  border: 1px solid #dce8f7;
  border-radius: 12px;
  font-size: 12px;
  color: #3a4658;
}

.chip.active {
  background: #e6f3ff;
  border-color: #1385f8;
  color: #0d6ecd;
  font-weight: 600;
}

.empty-hint {
  font-size: 12px;
  color: #9aa5b4;
  text-align: center;
  margin: 10px 0 0;
}

@media (max-width: 700px) {
  .stats-content,
  .chart-card,
  .trend-card {
    min-width: 0;
  }

  .cards-row,
  .cards-row.six {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 8px;
  }

  .stat-card {
    min-width: 0;
    padding: 13px 7px;
  }

  .charts-grid,
  .extra-row {
    grid-template-columns: minmax(0, 1fr);
    gap: 10px;
  }

  .chart-card,
  .trend-card {
    padding: 12px;
  }

  .chart-card h3 {
    overflow-wrap: anywhere;
  }

  .bar-row {
    min-width: 0;
    gap: 6px;
  }

  .bar-label,
  .bar-label.wide {
    width: 80px;
    flex: 0 1 80px;
    min-width: 0;
    white-space: normal;
    overflow-wrap: anywhere;
  }

  .bar-track {
    min-width: 0;
  }

  .bar-count {
    width: 30px;
    flex: 0 0 30px;
  }

  .trend-card h3 {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
}
</style>
