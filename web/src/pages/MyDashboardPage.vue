<script setup>
import { computed, onMounted, ref } from "vue";
import { api } from "../api.js";
import { currentUser, hasPerm, roleName } from "../auth.js";

const loading = ref(false);
const partialError = ref(false);
let loadTicket = 0;

const counts = ref({
  review: 0,
  decisions: 0,
  revisions: 0,
  shareApprovals: 0,
  formal: 0,
  working: 0,
  eliminated: 0,
  myPendingShares: 0,
});

const todoEntries = computed(() => [
  { key: "review", label: "待我审核", count: counts.value.review, to: "/review", visible: hasPerm("review:do") },
  { key: "decisions", label: "待我决断", count: counts.value.decisions, to: "/review-decisions", visible: hasPerm("review:final") },
  { key: "revisions", label: "待我修改", count: counts.value.revisions, to: "/my-revisions", visible: hasPerm("question:edit") },
  { key: "shareApprovals", label: "分享审批", count: counts.value.shareApprovals, to: "/share-requests", visible: hasPerm("question:share_review") },
].filter((item) => item.visible));

const bankEntries = computed(() => [
  { key: "formal", label: "正式题库", count: counts.value.formal, tone: "green" },
  { key: "working", label: "待审核题库", count: counts.value.working, tone: "blue" },
  { key: "eliminated", label: "淘汰题库", count: counts.value.eliminated, tone: "gray" },
]);

const todoTotal = computed(() => todoEntries.value.reduce((sum, item) => sum + item.count, 0));
const permissionCount = computed(() => currentUser.value?.permissions?.length || 0);

async function loadDashboard() {
  const ticket = ++loadTicket;
  loading.value = true;
  partialError.value = false;
  counts.value = {
    review: 0,
    decisions: 0,
    revisions: 0,
    shareApprovals: 0,
    formal: 0,
    working: 0,
    eliminated: 0,
    myPendingShares: 0,
  };

  const jobs = [];
  const addJob = (request, apply) => jobs.push(
    request().then((data) => {
      if (ticket === loadTicket) apply(data);
    }).catch(() => {
      if (ticket === loadTicket) partialError.value = true;
    }),
  );

  if (hasPerm("review:do")) {
    addJob(api.myTasks, (data) => { counts.value.review = data.total ?? (data.tasks || []).length; });
  }
  if (hasPerm("review:final")) {
    addJob(api.myDecisions, (data) => { counts.value.decisions = data.total ?? (data.tasks || []).length; });
  }
  if (hasPerm("question:edit")) {
    addJob(api.myRevisions, (data) => { counts.value.revisions = data.total ?? (data.items || []).length; });
  }
  if (hasPerm("question:share_review")) {
    addJob(() => api.listQuestionShares("pending"), (data) => {
      counts.value.shareApprovals = data.total ?? (data.items || []).length;
    });
  }
  if (hasPerm("question:share")) {
    addJob(() => api.listQuestionShares("mine"), (data) => {
      counts.value.myPendingShares = (data.items || []).filter((item) => item.request?.status === "pending").length;
    });
  }
  if (hasPerm("question:view")) {
    for (const tier of ["formal", "working", "eliminated"]) {
      addJob(() => api.listQuestions(1, 1, "", tier, "personal"), (data) => {
        counts.value[tier] = data.total || 0;
      });
    }
  }

  await Promise.all(jobs);
  if (ticket === loadTicket) loading.value = false;
}

onMounted(loadDashboard);
</script>

<template>
  <div class="my-dashboard">
    <section class="identity-card">
      <div class="identity-avatar">{{ (currentUser?.display_name || currentUser?.username || "用")[0] }}</div>
      <div class="identity-main">
        <h2>{{ currentUser?.display_name || currentUser?.username }}</h2>
        <div class="identity-meta">
          <span>@{{ currentUser?.username }}</span>
          <span class="role-pill">{{ roleName(currentUser?.role) }}</span>
          <span>{{ permissionCount ? `${permissionCount} 项权限` : "待分配权限" }}</span>
        </div>
      </div>
      <RouterLink class="settings-link" to="/settings">个人设置</RouterLink>
    </section>

    <div class="dashboard-grid">
      <section class="panel todo-panel">
        <div class="section-heading">
          <span class="dot blue"></span>
          <h2>我的待办</h2>
          <small>{{ todoTotal }} 项</small>
          <button class="refresh-button" type="button" :disabled="loading" @click="loadDashboard">
            {{ loading ? "刷新中" : "刷新" }}
          </button>
        </div>

        <div v-if="loading && !todoEntries.length" class="compact-empty">加载中</div>
        <div v-else-if="todoEntries.length" class="todo-list">
          <RouterLink v-for="item in todoEntries" :key="item.key" class="todo-row" :data-kind="item.key" :to="item.to">
            <span class="todo-mark" :class="{ active: item.count > 0 }"></span>
            <span>{{ item.label }}</span>
            <strong :class="{ active: item.count > 0 }">{{ item.count }}</strong>
            <span class="row-arrow">→</span>
          </RouterLink>
        </div>
        <div v-else class="compact-empty">暂无待办</div>
      </section>

      <section class="panel bank-panel">
        <div class="section-heading">
          <span class="dot teal"></span>
          <h2>我的题库</h2>
          <small v-if="hasPerm('question:share') && counts.myPendingShares">分享待审 {{ counts.myPendingShares }}</small>
        </div>

        <div v-if="hasPerm('question:view')" class="bank-metrics">
          <RouterLink
            v-for="item in bankEntries"
            :key="item.key"
            class="metric-card"
            :class="item.tone"
            :data-kind="item.key"
            :to="{ path: '/bank', query: { scope: 'personal', tier: item.key } }"
          >
            <span>{{ item.label }}</span>
            <strong>{{ loading ? "—" : item.count }}</strong>
          </RouterLink>
        </div>
        <div v-else class="compact-empty">暂无题库权限</div>
      </section>
    </div>

    <p v-if="partialError" class="load-notice" role="status">部分数据暂不可用</p>
  </div>
</template>

<style scoped>
.my-dashboard {
  display: grid;
  gap: 16px;
  min-width: 0;
}

.identity-card {
  display: flex;
  align-items: center;
  gap: 14px;
  min-width: 0;
  padding: 18px 20px;
  border: 1px solid #dfe8e4;
  border-radius: 9px;
  background: #fff;
  box-shadow: var(--shadow);
}

.identity-avatar {
  display: grid;
  width: 46px;
  height: 46px;
  flex: 0 0 46px;
  place-items: center;
  border-radius: 9px;
  color: #fff;
  background: #315f51;
  font-size: 19px;
  font-weight: 700;
}

.identity-main {
  flex: 1;
  min-width: 0;
}

.identity-main h2 {
  margin: 0 0 6px;
  overflow: hidden;
  color: #20372f;
  font-size: 18px;
  font-weight: 650;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.identity-meta {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 12px;
  color: #78867f;
  font-size: 11px;
}

.role-pill {
  padding: 2px 7px;
  border-radius: 4px;
  color: #315f51;
  background: #eef5f1;
  font-weight: 600;
}

.settings-link {
  flex: 0 0 auto;
  padding: 7px 11px;
  border: 1px solid #dce5e1;
  border-radius: 6px;
  color: #49665b;
  background: #fff;
  font-size: 12px;
  text-decoration: none;
}

.settings-link:hover {
  border-color: #8ba99d;
  background: #f7faf8;
}

.dashboard-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.15fr) minmax(0, .85fr);
  gap: 16px;
}

.todo-panel,
.bank-panel {
  min-width: 0;
}

.refresh-button {
  min-height: 28px;
  margin-left: 4px;
  padding: 0 9px;
  border: 1px solid #dce5e1;
  border-radius: 5px;
  color: #5a6d65;
  background: #fff;
  font-size: 11px;
}

.refresh-button:disabled {
  opacity: .55;
}

.todo-list {
  display: grid;
  gap: 2px;
}

.todo-row {
  display: grid;
  grid-template-columns: 8px minmax(0, 1fr) auto 18px;
  align-items: center;
  gap: 10px;
  min-width: 0;
  padding: 12px 8px;
  border-bottom: 1px solid #eef2f0;
  color: #2d3f39;
  font-size: 13px;
  text-decoration: none;
}

.todo-row:last-child {
  border-bottom: 0;
}

.todo-row:hover {
  border-radius: 6px;
  background: #f7faf8;
}

.todo-mark {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #cbd4d0;
}

.todo-mark.active {
  background: #d58b32;
}

.todo-row strong {
  color: #7a8781;
  font-size: 15px;
}

.todo-row strong.active {
  color: #b66e1d;
}

.row-arrow {
  color: #a0aaa5;
}

.bank-metrics {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
}

.metric-card {
  display: grid;
  gap: 11px;
  min-width: 0;
  padding: 13px 11px;
  border: 1px solid #e4e9e7;
  border-top: 3px solid #9ca9a4;
  border-radius: 7px;
  color: #4f5f59;
  background: #fbfcfb;
  text-decoration: none;
}

.metric-card:hover {
  background: #fff;
  box-shadow: 0 5px 16px rgba(35, 61, 51, .08);
}

.metric-card.green { border-top-color: #4c8a70; }
.metric-card.blue { border-top-color: #5485ae; }
.metric-card.gray { border-top-color: #89918d; }

.metric-card span {
  overflow: hidden;
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.metric-card strong {
  color: #263b33;
  font-size: 25px;
  font-weight: 650;
}

.compact-empty {
  display: grid;
  min-height: 112px;
  place-items: center;
  color: #909c97;
  font-size: 12px;
}

.load-notice {
  margin: -4px 0 0;
  color: #b26d24;
  font-size: 12px;
}

@media (max-width: 800px) {
  .dashboard-grid {
    grid-template-columns: minmax(0, 1fr);
  }
}

@media (max-width: 560px) {
  .identity-card {
    align-items: flex-start;
    padding: 14px;
  }

  .settings-link {
    padding: 6px 8px;
  }

  .bank-metrics {
    grid-template-columns: minmax(0, 1fr);
  }

  .metric-card {
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    gap: 8px;
    border-top-width: 1px;
    border-left: 3px solid #9ca9a4;
  }

  .metric-card.green { border-left-color: #4c8a70; }
  .metric-card.blue { border-left-color: #5485ae; }
  .metric-card.gray { border-left-color: #89918d; }

  .metric-card strong {
    font-size: 21px;
  }
}
</style>
