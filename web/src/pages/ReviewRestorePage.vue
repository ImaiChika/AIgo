<script setup>
import HelpHint from "../components/HelpHint.vue";
import { ref, computed, watch, nextTick, onMounted, onBeforeUnmount } from 'vue';
import { api } from '../api.js';
import { hasPerm, currentUser } from '../auth.js';
import { notifyNavigationWorkChanged } from '../navigation.js';
import { useLiveRefresh } from '../useLiveRefresh.js';
import CompactPager from '../components/CompactPager.vue';
import ReviewCommentCard from '../components/ReviewCommentCard.vue';

const items = ref([]), total = ref(0), page = ref(1), pageSize = 20;
const options = ref({ flows: [], owners: [] });
const flowId = ref(''), ownerId = ref(''), status = ref(''), keyword = ref('');
const selected = ref(new Set()), detail = ref(null), preview = ref(null);
const loading = ref(false), previewBusy = ref(false), executing = ref(false);
const error = ref(''), notice = ref(''), reason = ref(''), confirmationLabel = ref('');
const dialog = ref(null), reasonInput = ref(null), pendingRequest = ref(null), detailPanel = ref(null);
const allowed = computed(() => hasPerm('review:restore'));
const pageCount = computed(() => Math.max(1, Math.ceil(total.value / pageSize)));
const busy = computed(() => executing.value || previewBusy.value);
const selectable = computed(() => items.value.filter(x => x.eligible));
const allSelected = computed(() => selectable.value.length > 0 && selectable.value.every(x => selected.value.has(x.task.id)));
const selectedFlowCount = computed(() => options.value.flows.find(f => f.id === flowId.value)?.count || 0);
let listTicket = 0, detailTicket = 0, previewTicket = 0, alive = true, previousFocus;
let renderedPageKey = '';
const identity = () => `${currentUser.value?.id}:${currentUser.value?.role}`;
const filter = () => ({ flow_id: flowId.value, owner_id: ownerId.value, status: status.value, keyword: keyword.value.trim(), page: page.value, page_size: pageSize });
const stateText = s => ({ reviewing: '轮审中', conflict: '待最终决断', revision_required: '需修改', archived: '历史批次', published: '已通过', rejected: '已驳回' })[s] || s || '异常任务';
const timeText = time => time && !String(time).startsWith('0001') ? new Date(time).toLocaleString('zh-CN', { hour12: false }) : '未记录';
const personName = id => detail.value?.people?.[id] || id;
const detailHistory = computed(() => detail.value?.history?.length ? detail.value.history : detail.value ? [detail.value.task] : []);
const taskRecords = id => (detail.value?.records || []).filter(r => r.task_id === id);

function clearSensitive() {
  ++listTicket; ++detailTicket; ++previewTicket;
  items.value = []; total.value = 0; options.value = { flows: [], owners: [] };
  detail.value = null; selected.value = new Set(); preview.value = null; pendingRequest.value = null; reason.value = '';
  renderedPageKey = '';
}
function failure(e) {
  if (!alive || e.staleSession) return;
  error.value = e.message || '请求失败，请稍后重试';
  if (e.status === 403) clearSensitive();
}
async function loadOptions() {
  if (!alive || !allowed.value) return;
  const actor = identity();
  try { const data = await api.restoreOptions(); if (alive && allowed.value && actor === identity()) options.value = data; } catch (e) { failure(e); }
}
async function loadDetail(id, silent = false) {
  const ticket = ++detailTicket, actor = identity();
  if (!silent) detail.value = null;
  try {
    const data = await api.restoreCandidate(id);
    if (alive && allowed.value && actor === identity() && ticket === detailTicket) {
      detail.value = data;
      if (!silent && window.innerWidth <= 1150) { await nextTick(); detailPanel.value?.scrollIntoView?.({ behavior: 'smooth', block: 'start' }); }
    }
  } catch (e) {
    if (ticket !== detailTicket) return;
    if (e.status === 404) { detail.value = null; notice.value = '该任务已退出在途审核，列表已更新。'; }
    else failure(e);
  }
}
async function load(silent = false) {
  if (!alive || !allowed.value || executing.value) return;
  const ticket = ++listTicket, actor = identity();
  const requested = filter(), requestKey = JSON.stringify(requested);
  if (!silent) { loading.value = true; error.value = ''; }
  try {
    const data = await api.restoreCandidates(requested);
    if (!alive || !allowed.value || ticket !== listTicket || actor !== identity()) return;
    total.value = data.total || 0;
    if (page.value > pageCount.value) { page.value = pageCount.value; return load(silent); }
    const previousPage = renderedPageKey === requestKey ? items.value : [];
    renderedPageKey = requestKey;
    items.value = data.items || [];
    const gone = new Set(previousPage.filter(x => !items.value.some(y => y.task.id === x.task.id && y.eligible)).map(x => x.task.id));
    selected.value = new Set([...selected.value].filter(id => !gone.has(id)));
    if (detail.value?.task.id) await loadDetail(detail.value.task.id, true);
  } catch (e) { if (ticket === listTicket) failure(e); }
  finally { if (ticket === listTicket) loading.value = false; }
}
function search() { page.value = 1; selected.value = new Set(); detail.value = null; ++detailTicket; load(); }
function goPage(next) { if (busy.value || loading.value) return; page.value = next; load(); }
function toggle(id) { const set = new Set(selected.value); set.has(id) ? set.delete(id) : set.add(id); selected.value = set; }
function toggleAll() { if (loading.value || busy.value) return; const set = new Set(selected.value), remove = allSelected.value; selectable.value.forEach(x => remove ? set.delete(x.task.id) : set.add(x.task.id)); selected.value = set; }
async function prepare(params, label) {
  if (!allowed.value || busy.value) return;
  previewBusy.value = true; error.value = ''; notice.value = '';
  const ticket = ++previewTicket, actor = identity();
  previousFocus = document.activeElement;
  try {
    const data = await api.previewReviewRestore(params);
    if (!alive || !allowed.value || ticket !== previewTicket || actor !== identity()) return;
    reason.value = ''; pendingRequest.value = null; confirmationLabel.value = label; preview.value = data;
    await nextTick(); dialog.value?.showModal(); reasonInput.value?.focus();
  } catch (e) { failure(e); await load(true); }
  finally { if (ticket === previewTicket) previewBusy.value = false; }
}
function closeConfirmation() { if (executing.value) return; preview.value = null; pendingRequest.value = null; previousFocus?.focus(); }
async function execute() {
  if (!allowed.value || !preview.value || executing.value || !reason.value.trim()) return;
  pendingRequest.value ||= { operation_id: `restore-${globalThis.crypto.randomUUID()}`, preview_token: preview.value.token, reason: reason.value.trim() };
  executing.value = true; error.value = '';
  try {
    const result = await api.restoreReview(pendingRequest.value);
    if (!alive || !allowed.value) return;
    preview.value = null; pendingRequest.value = null; selected.value = new Set(); detail.value = null;
    notice.value = `已还原 ${result.restored} 道题，清除 ${result.deleted_tasks} 个送审批次、${result.deleted_records} 条审核记录和 ${result.deleted_logs} 条旧审核日志。原作者可在“新题修改与送审”重新处理。`;
    notifyNavigationWorkChanged();
  } catch (e) {
    failure(e);
    if ([400, 403, 409].includes(e.status)) { preview.value = null; pendingRequest.value = null; selected.value = new Set(); }
    // 网络中断可能发生在提交成功后，保留相同操作ID和原请求供安全重试。
  } finally {
    executing.value = false;
    await Promise.allSettled([load(true), loadOptions()]);
  }
}
watch(allowed, value => { if (!value) clearSensitive(); });
watch([() => currentUser.value?.id, () => currentUser.value?.role], () => { clearSensitive(); if (allowed.value) { load(); loadOptions(); } });
useLiveRefresh(async () => { if (allowed.value && !busy.value) await Promise.allSettled([load(true), loadOptions()]); });
onMounted(() => { if (allowed.value) { load(); loadOptions(); } });
onBeforeUnmount(() => { alive = false; clearSensitive(); });
</script>

<template>
  <div class="restore-workspace">
    <section class="panel restore-intro">
      <div><div class="section-heading"><span class="dot blue"></span><h2>审核还原</h2><small>在途审核管理</small></div><HelpHint label="审核还原说明">撤销仍在轮审或待决断的审核安排，题目回到原出题人的新题页。题目内容、版本与首次 AI 检查结果保留。</HelpHint></div>
      <span class="restore-limit">每次最多 1000 道</span>
    </section>
    <p v-if="!allowed" class="restore-message" role="alert">当前身份没有审核还原权限。</p>
    <template v-else>
      <div v-if="error" class="restore-message error" role="alert">{{ error }}</div>
      <div v-if="notice" class="restore-message success" role="status">{{ notice }}</div>
      <section class="panel restore-controls">
        <form class="restore-filters" @submit.prevent="search">
          <label>审核流程<select v-model="flowId" aria-label="按审核流程筛选" :disabled="busy" @change="search"><option value="">全部流程</option><option v-for="f in options.flows" :key="f.id" :value="f.id">{{ f.name }}（{{ f.count }}）</option></select></label>
          <label>出题人<select v-model="ownerId" aria-label="按出题人筛选" :disabled="busy" @change="search"><option value="">全部出题人</option><option v-for="owner in options.owners" :key="owner.id" :value="owner.id">{{ owner.name }}</option></select></label>
          <label>审核进度<select v-model="status" aria-label="按审核进度筛选" :disabled="busy" @change="search"><option value="">全部进度</option><option value="reviewing">轮审中</option><option value="conflict">待最终决断</option></select></label>
          <label class="restore-search">题目关键词<input v-model="keyword" aria-label="题目关键词" placeholder="题干、知识点或题目 ID" :disabled="busy" /></label><button class="ghost-button" :disabled="busy || loading">查询</button>
        </form>
        <div class="restore-batchbar"><span>共 {{ total }} 道在途题 · 已选 {{ selected.size }} 道（跨页保留）</span><div><button class="ghost-button" :disabled="busy || !selected.size" @click="prepare({task_ids:[...selected]}, '还原所选题目')">还原所选 {{ selected.size }} 道</button><button class="primary-button" :disabled="busy || !flowId || !selectedFlowCount" @click="prepare({flow_id:flowId}, '还原此流程全部可还原题目（跨所有分页，不受其他筛选条件限制）')">还原此流程全部</button></div></div>
      </section>
      <div class="restore-columns">
        <section class="panel restore-list">
          <div class="restore-list-heading"><label><input type="checkbox" :checked="allSelected" :disabled="busy || loading || !selectable.length" @change="toggleAll" /> 全选本页可还原题</label><button class="text-button" :disabled="busy" @click="load();loadOptions()">刷新</button></div>
          <div v-if="loading" class="loading">正在读取在途任务…</div>
          <div v-else-if="!items.length" class="empty">当前范围没有在途审核题目。</div>
          <article v-for="item in items" :key="item.question.id" class="restore-row" :class="{active:detail?.question.id===item.question.id}">
            <input type="checkbox" :checked="selected.has(item.task.id)" :disabled="busy || loading || !item.eligible" :aria-label="`选择题目 ${item.question.id}`" @change="toggle(item.task.id)" />
            <div class="restore-row-body"><div class="restore-row-meta"><span class="restore-state" :class="item.task.status">{{ stateText(item.task.status) }}</span><span>{{ item.flow.name || '未关联流程' }} · 第 {{ item.task.current_round || '—' }} 轮</span><span>v{{ item.question.version }}</span></div><p class="restore-stem">{{ item.question.clinical_stem }}</p><div class="restore-row-foot"><span>{{ item.creator_name }}（{{ item.creator_username }}）</span><button class="text-button" :disabled="busy || !item.task.id" @click="loadDetail(item.task.id)">查看详情</button></div><p v-if="!item.eligible" class="restore-ineligible">不可还原：{{ item.reason }}</p></div>
          </article>
          <CompactPager :page="page" :total-pages="pageCount" :disabled="busy || loading" @change="goPage" />
        </section>
        <section v-if="detail" ref="detailPanel" class="panel restore-detail">
          <div class="section-heading"><h2>任务详情</h2><button class="primary-button" :disabled="busy || !detail.eligible" @click="prepare({task_ids:[detail.task.id]}, '还原当前题')">还原当前题</button></div>
          <p v-if="!detail.eligible" class="restore-ineligible">{{ detail.reason }}</p>
          <dl class="restore-facts"><div><dt>出题人</dt><dd>{{ detail.creator_name }}（{{ detail.creator_username }}）</dd></div><div><dt>题目 / 版本</dt><dd>{{ detail.question.id }} / v{{ detail.question.version }}</dd></div><div><dt>生成时间</dt><dd>{{ timeText(detail.question.created_at) }}</dd></div><div><dt>本次送审建立</dt><dd>{{ timeText(detail.task.created_at) }}</dd></div><div><dt>最近审核流转</dt><dd>{{ timeText(detail.task.updated_at) }}</dd></div></dl>
          <h3>题目内容</h3><p class="restore-full-stem">{{ detail.question.clinical_stem }}</p><p v-for="option in detail.question.options" :key="option.label">{{ option.label }}. {{ option.text }}</p><p><strong>正确答案：</strong>{{ detail.question.answer }}</p><p v-if="detail.question.explanation" class="restore-full-stem">解析：{{ detail.question.explanation }}</p><div class="restore-knowledge"><span v-for="kp in detail.question.knowledge_points" :key="kp.id || kp.outline_code">{{ kp.outline_code }} {{ kp.topic }}</span><span v-if="!detail.question.knowledge_points?.length">未关联知识点</span></div>
          <h3>流程与专家分配</h3><p>{{ detail.flow.name || detail.task.flow_id }} · {{ stateText(detail.task.status) }}</p><ol class="restore-rounds"><li v-for="round in detail.flow.rounds" :key="round.round_number" :class="{current:round.round_number===detail.task.current_round}"><strong>第 {{ round.round_number }} 轮 {{ round.name }}</strong><span>{{ (round.expert_ids || []).map(personName).join('、') }}</span><small>{{ detail.flow.vote_rule==='veto' || !round.required_count ? '全部同意' : `${round.required_count} 人同意` }}后通过</small></li></ol><p>最终把关：{{ detail.task.final_reviewer_ids?.length ? detail.task.final_reviewer_ids.map(personName).join('、') : '任一有最终决断权限的把关人' }}</p>
          <h3>全部人工审核记录（{{ detail.record_count }} 条）</h3><p class="restore-muted">还原将清除本题所有人工送审批次及其意见，包括此前保留的退修历史。</p><div v-for="task in detailHistory" :key="task.id" class="restore-history"><h4>第 {{ task.attempt || 1 }} 次送审 · {{ task.flow_name || task.flow_id }} · v{{ task.question_version }} · {{ stateText(task.status) }}</h4><div v-for="record in taskRecords(task.id)" :key="record.id"><small>第 {{ record.round_number }} 轮 · {{ timeText(record.created_at) }}</small><ReviewCommentCard :record="record" :fallback-name="personName(record.expert_id)" /></div><p v-if="!taskRecords(task.id).length" class="restore-muted">暂无人工审核意见。</p></div>
        </section>
        <section v-else class="panel restore-placeholder"><span>查看审核安排与记录</span></section>
      </div>
    </template>
    <dialog v-if="preview && allowed" ref="dialog" class="restore-confirm" aria-labelledby="restore-confirm-title" @cancel.prevent="closeConfirmation">
      <h2 id="restore-confirm-title">确认审核还原</h2><p>{{ confirmationLabel }}</p><p class="restore-confirm-count">{{ preview.count }} <small>道题目</small></p><p>清除 {{ preview.task_count }} 个送审批次、{{ preview.record_count }} 条意见，以及这些题目的旧送审、投票和决断操作日志。</p><div class="restore-owners"><span v-for="(count,name) in preview.owners" :key="name">{{ name }}：{{ count }} 道</span></div><p class="restore-ineligible">全部人工审核历史会被删除。当前内容、版本和首次 AI 检查保留；题目回到原出题人的“新题修改与送审”，可重新选择流程。</p><label>还原原因<textarea ref="reasonInput" v-model="reason" maxlength="500" rows="3" :disabled="executing || !!pendingRequest" placeholder="简述为什么需要重新安排审核" /></label><p v-if="pendingRequest && !executing" class="restore-muted">上次请求未确认结果。重试将使用同一次操作，不会重复清理后来新建的任务。</p><p v-if="error" class="restore-ineligible" role="alert">{{ error }}</p><div class="restore-confirm-actions"><button class="ghost-button" :disabled="executing" @click="closeConfirmation">取消</button><button class="primary-button" :disabled="executing || !reason.trim()" @click="execute">{{ executing ? '还原中…' : pendingRequest ? '重试本次还原' : '确认还原' }}</button></div>
    </dialog>
  </div>
</template>

<style scoped>
.restore-workspace{display:grid;gap:16px;color:#25364b}.restore-intro{display:flex;justify-content:space-between;gap:22px;align-items:center}.restore-intro p,.restore-muted,.restore-placeholder p{font-size:12px;color:#6b7d91;line-height:1.8}.restore-limit{white-space:nowrap;border:1px solid #dbe8f4;padding:7px 11px;border-radius:5px;font-size:11px;color:#396e9c}.restore-filters{display:flex;align-items:end;gap:12px;flex-wrap:wrap}.restore-filters label{display:grid;gap:6px;font-size:11px;color:#65798f;flex:1;min-width:145px}.restore-filters input,.restore-filters select,.restore-confirm textarea{width:100%;min-height:36px;border:1px solid #d8e2ed;border-radius:5px;background:white;padding:8px;font:inherit;color:#25364b}.restore-filters .restore-search{flex:1.5}.restore-batchbar{display:flex;align-items:center;justify-content:space-between;gap:12px;border-top:1px solid #e7edf4;padding-top:14px;margin-top:16px;font-size:12px;color:#61778b}.restore-batchbar>div{display:flex;gap:8px}.restore-columns{display:grid;grid-template-columns:minmax(320px,.9fr) minmax(400px,1.1fr);gap:16px;align-items:start}.restore-list-heading{display:flex;justify-content:space-between;align-items:center;font-size:12px;padding-bottom:12px}.restore-row{display:flex;gap:12px;border-top:1px solid #e6edf4;padding:17px 8px;align-items:start}.restore-row.active{background:#f0f7fe;border-left:3px solid #1680dc;padding-left:5px}.restore-row>input{margin-top:5px;flex-shrink:0}.restore-row-body{min-width:0;flex:1}.restore-row-meta,.restore-row-foot{display:flex;align-items:center;gap:8px;flex-wrap:wrap;color:#718198;font-size:11px}.restore-row-foot{justify-content:space-between}.restore-state{padding:3px 7px;border-radius:4px;background:#e9f3fd;color:#1b72b6}.restore-state.conflict{background:#fff2dc;color:#a06412}.restore-stem{font-size:13px;line-height:1.8;display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;overflow:hidden;margin:10px 0}.restore-ineligible{font-size:12px;color:#a64e31;line-height:1.8;white-space:pre-wrap}.restore-detail .section-heading{justify-content:space-between}.restore-detail h3{font-size:13px;border-top:1px solid #e8edf3;padding-top:17px;margin:22px 0 12px}.restore-detail p{font-size:13px;line-height:1.85;overflow-wrap:anywhere}.restore-full-stem{white-space:pre-wrap}.restore-facts{font-size:11px;display:grid;gap:9px;margin:16px 0}.restore-facts>div{display:grid;grid-template-columns:95px 1fr;gap:10px}.restore-facts dt{color:#8190a2}.restore-facts dd{margin:0;overflow-wrap:anywhere}.restore-knowledge,.restore-owners{display:flex;flex-wrap:wrap;gap:7px;font-size:11px}.restore-knowledge span,.restore-owners span{background:#f1f5f9;padding:5px 8px;border-radius:4px}.restore-rounds{padding:0;list-style:none;display:grid;gap:8px}.restore-rounds li{display:grid;gap:5px;padding:11px;border-left:2px solid #dbe5ee;background:#f7f9fc;font-size:12px}.restore-rounds li.current{border-color:#2587d8;background:#eff7ff}.restore-rounds small{color:#718198}.restore-history{border:1px solid #e5ecf3;border-radius:5px;padding:12px;margin-bottom:10px}.restore-history h4{font-size:12px;font-weight:500;margin:0 0 12px}.restore-history>div>small{display:block;color:#8b96a4;margin:12px 0 6px;font-size:10px}.restore-placeholder{min-height:260px;display:flex;flex-direction:column;justify-content:center;align-items:center;text-align:center;padding:42px}.restore-placeholder>span{font-size:15px;font-weight:600;color:#5c7793}.restore-message{margin:0;padding:12px 16px;border:1px solid #dae5ef;background:#f3f7fb;font-size:12px;border-radius:5px;line-height:1.7}.restore-message.error{color:#ad482d;background:#fff4ee;border-color:#f2d9cd}.restore-message.success{color:#23734e;background:#eef8f3;border-color:#d0e8db}.restore-confirm{width:min(570px,calc(100vw - 32px));max-height:90vh;overflow:auto;border:1px solid #d7e2ed;border-radius:9px;padding:26px;color:#25364b;box-shadow:0 22px 70px #18355030}.restore-confirm::backdrop{background:#102c4c65}.restore-confirm h2{font-size:18px;margin:0 0 15px}.restore-confirm p{font-size:12px;line-height:1.9}.restore-confirm .restore-confirm-count{font-size:34px;color:#176fbb;margin:12px 0}.restore-confirm-count small{font-size:13px;color:#65798f}.restore-confirm>label{display:grid;gap:8px;font-size:12px;margin-top:15px}.restore-confirm textarea{resize:vertical}.restore-confirm-actions{display:flex;justify-content:end;gap:10px;margin-top:20px}.text-button{background:none;border:0;color:#1b73b9;font-size:12px;padding:4px;cursor:pointer}.restore-workspace button:disabled{opacity:.5;cursor:not-allowed}.restore-workspace .empty p{font-size:12px;line-height:1.8}.restore-workspace :focus-visible{outline:2px solid #318ad4;outline-offset:2px}@media(max-width:1150px){.restore-columns{grid-template-columns:1fr}.restore-placeholder{display:none}}@media(max-width:600px){.restore-intro,.restore-batchbar{align-items:start;flex-direction:column}.restore-batchbar>div{width:100%;flex-wrap:wrap}.restore-filters label{min-width:120px}.restore-intro .section-heading{flex-wrap:wrap}.restore-facts>div{grid-template-columns:83px 1fr}.restore-confirm{padding:20px}}
</style>
