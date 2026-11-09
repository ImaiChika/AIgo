<script setup>
import {permissionVisible} from "../permissionPresentation.js";
import {questionReviewUi,questionReviewPathVisible,reviewRoleVisible,presentedWorkRoleName} from "../questionReviewUi.js";
import { computed, onMounted, onBeforeUnmount, ref, watch } from "vue";
import { useRouter } from "vue-router";
import { api } from "../api.js";
import { canAccessQuestionArrangement, canAccessPersonalBank } from "../navigation.js";
import { useLiveRefresh } from "../useLiveRefresh.js";
import { currentUser, hasPerm, hasAccountPerm, identitiesWithPerm, roleName, setAuth, getToken } from "../auth.js";

const router = useRouter();
const loading = ref(false);
const partialError = ref(false);
const switchBusy = ref(false);
const switchError = ref("");
let loadTicket = 0;

const counts = ref({
  review: 0,
  decisions: 0,
  revisions: 0,
  shareApprovals: 0,
  normal: 0,
  deleted: 0,
  myPendingShares: 0,
  generated: 0,
  reviewed: 0,
  newQuestions: 0,
  crossReview: 0,
  crossDecisions: 0,
  crossRevisions: 0,
  crossShareApprovals: 0,
});

// 当前身份待办：与现有行为一致，按当前身份权限显示，可直接进入处理。
const todoEntries = computed(() => [
  { key: "newQuestions", label: "待整理题目", count: counts.value.newQuestions, to: "/new-questions", perm: "question:view" },
  { key: "review", label: "待我审核", count: counts.value.review, to: "/review", perm: "review:do" },
  { key: "decisions", label: "待我决断", count: counts.value.decisions, to: "/review-decisions", perm: "review:final" },
  { key: "revisions", label: "待我修改", count: counts.value.revisions, to: "/my-revisions", perm: "question:edit" },
  { key: "shareApprovals", label: "分享审批", count: counts.value.shareApprovals, to: "/share-requests", perm: "question:share_review" },
].filter(item=>questionReviewPathVisible(item.to)).filter((item) => item.key === "newQuestions" ? canAccessQuestionArrangement(hasPerm) : hasPerm(item.perm)));

// 跨身份待办：任务归属账号本身，当前身份处理不了、但账号其他身份可以时，
// 在此提示并引导切换身份（按权限点与各身份有效权限判定，不依赖内置角色 ID）。
// 只显示确有任务的项；点击"切换处理"确认后切换身份并直达对应页面。
const crossIdentityEntries = computed(() => [
  { key: "crossNewQuestions", label: "待整理题目", count: counts.value.newQuestions, to: "/new-questions", perm: "question:view" },
  { key: "crossReview", label: "待我审核", count: counts.value.crossReview, to: "/review", perm: "review:do" },
  { key: "crossDecisions", label: "待我决断", count: counts.value.crossDecisions, to: "/review-decisions", perm: "review:final" },
  { key: "crossRevisions", label: "待我修改", count: counts.value.crossRevisions, to: "/my-revisions", perm: "question:edit" },
  { key: "crossShareApprovals", label: "分享审批", count: counts.value.crossShareApprovals, to: "/share-requests", perm: "question:share_review" },
].filter((item) => {
  if(!questionReviewPathVisible(item.to))return false;
  if (!item.count) return false;
  if (item.key === "crossNewQuestions") return !canAccessQuestionArrangement(hasPerm) && targetIdentities(item).length > 0;
  return !hasPerm(item.perm) && hasAccountPerm(item.perm);
}));

function targetIdentities(item) {
  return identitiesWithPerm(item.perm).filter(identity=>reviewRoleVisible({id:identity.id,permissions:currentUser.value?.role_permissions?.[identity.id]||[]})).filter(identity => item.key !== "crossNewQuestions" || canAccessQuestionArrangement(p => (currentUser.value?.role_permissions?.[identity.id] || []).includes(p)));
}
async function switchIdentityAndOpen(item) {
  if (switchBusy.value || !questionReviewPathVisible(item.to)) return;
  const targets = targetIdentities(item);
  if (!targets.length) return;
  const target = targets[0];
  if (!window.confirm(`「${item.label}」需要在【${target.name}】身份下处理，是否切换工作身份？`)) return;
  switchBusy.value = true;
  switchError.value = "";
  try {
    const data = await api.switchRole(target.id);
    setAuth(data.token, data.user);
    const failure = await router.push(item.to);
    if (failure && failure.name === "NavigationFailure") {
      // 路由守卫拦截（如权限未同步）时留在仪表盘并提示，不中断使用。
      switchError.value = "身份已切换，但页面跳转被拦截，请刷新后重试";
    }
  } catch (e) {
    switchError.value = e.message || "身份切换失败";
  } finally {
    switchBusy.value = false;
  }
}

const bankEntries = computed(() => hasPerm("question:view") ? [
  { key: "normal", label: "普通题库", count: counts.value.normal, tone: "green" },
  { key: "deleted", label: "删除题库", count: counts.value.deleted, tone: "gray" },
] : [
  { key: "formal", label: "正式题库（历史授权）", count: counts.value.legacyFormal, tone: "green", perm: "question:view_formal" },
  { key: "eliminated", label: "淘汰题库（历史授权）", count: counts.value.legacyEliminated, tone: "gray", perm: "question:view_eliminated" },
].filter(item => hasPerm(item.perm)));

const todoTotal = computed(() => todoEntries.value.reduce((sum, item) => sum + item.count, 0));
const permissionCount = computed(() => currentUser.value?.permissions?.filter(permissionVisible).length || 0);

async function loadDashboard(silent = false) {
  const ticket = ++loadTicket;
  if (!silent) loading.value = true;
  partialError.value = false;
  if (!silent) counts.value = {
    review: 0,
    decisions: 0,
    revisions: 0,
    shareApprovals: 0,
    normal: 0,
    deleted: 0,
    legacyFormal: 0,
    legacyEliminated: 0,
    myPendingShares: 0,
    generated: 0,
    reviewed: 0,
    newQuestions: 0,
    crossReview: 0,
    crossDecisions: 0,
    crossRevisions: 0,
    crossShareApprovals: 0,
  };

  const jobs = [];
  const addJob = (request, apply) => jobs.push(
    request().then((data) => {
      if (ticket === loadTicket) apply(data);
    }).catch(() => {
      if (ticket === loadTicket) partialError.value = true;
    }),
  );

  let summary;
  addJob(() => api.mySummary(questionReviewUi.enabled() ? "" : "authoring"), (data) => {
    summary=data;
    counts.value.generated = data.generated_count || 0;
    counts.value.reviewed = data.reviewed_count || 0;
    counts.value.newQuestions = data.new_questions_count || 0;
    // 账号级待办计数：后端身份无关，供"其他身份待办"引导（当前身份的
    // 专页计数仍按原逻辑单独拉取，保持既有口径不变）。
    counts.value.crossReview = data.pending_review_count || 0;
    counts.value.crossDecisions = data.pending_decision_count || 0;
    counts.value.crossRevisions = data.pending_revision_count || 0;
    counts.value.crossShareApprovals = data.pending_share_approval_count || 0;
    if (hasPerm("review:do")) counts.value.review = data.pending_review_count || 0;
    if (hasPerm("review:final")) counts.value.decisions = data.pending_decision_count || 0;
    if (hasPerm("question:edit")) counts.value.revisions = data.pending_revision_count || 0;
    if (hasPerm("question:share_review")) counts.value.shareApprovals = data.pending_share_approval_count || 0;
  });

  if (questionReviewUi.enabled() && hasPerm("question:share")) {
    addJob(() => api.listQuestionShares("mine"), (data) => {
      counts.value.myPendingShares = (data.items || []).filter((item) => item.request?.status === "pending").length;
    });
  }
  if (!hasPerm("question:view")) {
    for (const entry of [{ key: "legacyFormal", tier: "formal", perm: "question:view_formal" }, { key: "legacyEliminated", tier: "eliminated", perm: "question:view_eliminated" }]) {
      if (hasPerm(entry.perm)) addJob(() => api.listQuestions(1, 1, "", entry.tier, "personal", ""), data => { counts.value[entry.key] = data.total || 0; });
    }
  }

  await Promise.all(jobs);
  if(ticket!==loadTicket)return;
  // Current servers return both counts in the summary. Older servers/mock
  // adapters retain the previous fallback, without ever loading list bodies
  // merely to count when the summary already has authoritative zero values.
  if(hasPerm("question:view")){
    const fallback=[];
    for(const entry of [{key:"normal",library:"active"},{key:"deleted",library:"deleted"}]){
      const count=summary?.[`library_${entry.library}_count`];
      if(typeof count==='number')counts.value[entry.key]=count;
      else fallback.push(api.listQuestions(1,1,"","","personal",entry.library).then(data=>{if(ticket===loadTicket)counts.value[entry.key]=data.total||0;}).catch(()=>{if(ticket===loadTicket)partialError.value=true;}));
    }
    await Promise.all(fallback);
  }
  if (ticket === loadTicket) loading.value = false;
}

useLiveRefresh(() => loadDashboard(true));
onBeforeUnmount(() => ++loadTicket);
onMounted(() => loadDashboard());
watch(() => `${getToken()}:${currentUser.value?.id || ""}:${currentUser.value?.role || ""}:${(currentUser.value?.permissions || []).join(",")}`, () => loadDashboard());
</script>

<template>
  <div class="my-dashboard">
    <section class="identity-card">
      <div class="identity-avatar">{{ (currentUser?.display_name || currentUser?.username || "用")[0] }}</div>
      <div class="identity-main">
        <h2>{{ currentUser?.display_name || currentUser?.username }}</h2>
        <div class="identity-meta">
          <span>@{{ currentUser?.username }}</span>
          <span class="role-pill">{{ presentedWorkRoleName(currentUser?.role,roleName(currentUser?.role),currentUser?.role_permissions?.[currentUser?.role]||[]) }}</span>
          <span>{{ permissionCount ? `${permissionCount} 项权限` : "待分配权限" }}</span>
        </div>
      </div>
      <RouterLink class="settings-link" to="/settings">个人设置</RouterLink>
    </section>

    <div class="dashboard-grid">
      <section class="panel todo-panel">
        <div class="section-heading">
          <span class="dot blue"></span>
          <h2>任务提醒</h2>
          <small>{{ todoTotal }} 项</small>
          <button class="refresh-button" type="button" :disabled="loading" @click="loadDashboard()">
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

        <!-- 其他身份待办：任务归属账号，当前身份处理不了时提示切换身份（按权限点判定） -->
        <template v-if="crossIdentityEntries.length">
          <div class="cross-identity-heading">
            <span>其他身份待办</span>
            <small>任务属于本账号，需切换到对应身份处理</small>
          </div>
          <p v-if="switchError" class="cross-switch-error" role="alert">{{ switchError }}</p>
          <div class="todo-list cross-identity-list">
            <div v-for="item in crossIdentityEntries" :key="item.key" class="todo-row cross" :data-kind="item.key">
              <span class="todo-mark" :class="{ active: item.count > 0 }"></span>
              <span>{{ item.label }}</span>
              <strong class="active">{{ item.count }}</strong>
              <button class="cross-switch-button" type="button" :disabled="switchBusy" @click="switchIdentityAndOpen(item)">
                {{ switchBusy ? "切换中..." : `切换到【${targetIdentities(item)[0]?.name || "对应身份"}】处理` }}
              </button>
            </div>
          </div>
        </template>
      </section>

      <section class="panel bank-panel">
        <div class="section-heading">
          <span class="dot teal"></span>
          <h2>我的题库</h2>
          <small v-if="questionReviewUi.enabled() && hasPerm('question:share') && counts.myPendingShares">分享待审 {{ counts.myPendingShares }}</small>
        </div>

        <div v-if="canAccessPersonalBank(hasPerm)" class="bank-metrics">
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

    <section class="panel achievement-panel">
      <div class="section-heading"><span class="dot teal"></span><h2>我的累计数据</h2><small>当前账号</small></div>
      <div class="achievement-grid">
        <div class="achievement-card generated"><span>我已出题</span><strong>{{ loading ? "—" : counts.generated }}</strong><small>累计生成题目</small></div>
        <div v-if="questionReviewUi.enabled()" class="achievement-card reviewed"><span>我已审核</span><strong>{{ loading ? "—" : counts.reviewed }}</strong><small>累计提交审核意见</small></div>
        <RouterLink v-if="canAccessQuestionArrangement(hasPerm)" class="achievement-card pending" to="/new-questions"><span>待整理题目</span><strong>{{ loading ? "—" : counts.newQuestions }}</strong><small>进入题目整理页 →</small></RouterLink>
      </div>
    </section>

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

.cross-identity-heading {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px dashed #dfe8e4;
}

.cross-identity-heading span {
  font-size: 13px;
  font-weight: 600;
  color: #4a5d55;
}

.cross-identity-heading small {
  font-size: 11px;
  color: #93a39b;
}

.cross-identity-list .todo-row.cross {
  grid-template-columns: 8px minmax(0, 1fr) auto auto;
}

.cross-switch-button {
  justify-self: end;
  border: 1px solid #cfe0d8;
  border-radius: 6px;
  background: #f2f9f5;
  color: #1d7a4f;
  padding: 4px 10px;
  font-size: 12px;
  cursor: pointer;
  white-space: nowrap;
}

.cross-switch-button:hover:not(:disabled) {
  background: #e4f4ea;
  border-color: #9fcdb6;
}

.cross-switch-button:disabled {
  opacity: 0.55;
  cursor: default;
}

.cross-switch-error {
  margin: 8px 0 0;
  padding: 8px 10px;
  border-radius: 6px;
  background: #fff0f0;
  color: #c54858;
  font-size: 12px;
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

.achievement-panel { border-color: #d7e8e0; background: linear-gradient(135deg, #ffffff, #f5fbf8); }
.achievement-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; }
.achievement-card { display: grid; gap: 7px; min-width: 0; padding: 14px; border: 1px solid #e0e9e4; border-radius: 8px; color: #3d5149; background: rgba(255, 255, 255, .85); text-decoration: none; }
.achievement-card:hover { border-color: #9fc4b2; box-shadow: 0 5px 16px rgba(35, 61, 51, .08); }
.achievement-card span { font-size: 12px; font-weight: 650; }
.achievement-card strong { color: #234b3b; font-size: 28px; line-height: 1; }
.achievement-card small { color: #87958e; font-size: 11px; }
.achievement-card.generated { border-top: 3px solid #4c8a70; }
.achievement-card.reviewed { border-top: 3px solid #5485ae; }
.achievement-card.pending { border-top: 3px solid #d58b32; }

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

  .achievement-grid {
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
