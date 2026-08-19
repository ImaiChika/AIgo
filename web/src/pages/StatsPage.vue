<script setup>
import { ref, onMounted } from "vue";
import { api } from "../api.js";

const stats = ref(null);
const loading = ref(false);
const error = ref("");

const statusNames = {
  ai_draft: "AI草稿",
  auto_checked: "已初评",
  ai_reviewed: "AI已检查",
  reviewing: "审核中",
  conflict: "待决断",
  revision_required: "需修改",
  rejected: "已驳回",
  approved: "已通过",
  published: "已入库",
  archived: "已归档",
};

function statusName(s) {
  return statusNames[s] || s;
}

async function loadStats() {
  loading.value = true;
  error.value = "";
  try {
    stats.value = await api.stats();
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

onMounted(loadStats);
</script>

<template>
  <div class="stats-layout">
    <section class="panel">
      <div class="section-heading">
        <span class="dot blue"></span>
        <h2>统计分析</h2>
        <button class="ghost-button" type="button" @click="loadStats">刷新</button>
      </div>

      <div v-if="loading" class="loading">加载中...</div>
      <div v-else-if="error" class="error-box">加载失败: {{ error }}</div>
      <div v-else-if="stats" class="stats-content">
        <!-- 概览卡片 -->
        <div class="cards-row">
          <div class="stat-card">
            <strong>{{ stats.question_count }}</strong>
            <span>题目总数</span>
          </div>
          <div class="stat-card">
            <strong>{{ stats.knowledge_count }}</strong>
            <span>知识点数</span>
          </div>
          <div class="stat-card">
            <strong>{{ stats.user_count ?? "-" }}</strong>
            <span>用户数</span>
          </div>
        </div>

        <div class="charts-grid">
          <!-- 题库分布 -->
          <div class="chart-card">
            <h3>题库分布</h3>
            <div v-for="(count, name) in stats.bank_distribution || {}" :key="name" class="bar-row">
              <span class="bar-label">{{ name }}</span>
              <div class="bar-track">
                <div class="bar-fill blue" :style="{ width: barWidth(count, stats.question_count) }"></div>
              </div>
              <span class="bar-count">{{ count }}</span>
            </div>
            <p v-if="!Object.keys(stats.bank_distribution || {}).length" class="empty-hint">暂无数据</p>
          </div>

          <!-- 状态分布 -->
          <div class="chart-card">
            <h3>审核状态分布</h3>
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
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.stats-layout {
  max-width: 100%;
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

.stat-card {
  padding: 24px;
  background: #f8fbff;
  border: 1px solid #dce8f7;
  border-radius: 10px;
  text-align: center;
}

.stat-card strong {
  display: block;
  font-size: 32px;
  color: #1385f8;
}

.stat-card span {
  font-size: 13px;
  color: #6e7b8f;
}

.charts-grid {
  display: grid;
  grid-template-columns: 1fr 1fr 1fr;
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

.bar-count {
  width: 36px;
  text-align: right;
  font-size: 12px;
  font-weight: 600;
  color: #6e7b8f;
}

.empty-hint {
  font-size: 12px;
  color: #9aa5b4;
  text-align: center;
  margin: 10px 0 0;
}
</style>
