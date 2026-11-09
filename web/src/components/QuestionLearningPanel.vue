<script setup>
import HelpHint from "./HelpHint.vue";
import { computed, ref, watch, onMounted, onBeforeUnmount, inject } from 'vue';
import { routerKey } from 'vue-router';
import { api } from '../api.js';
import { currentUser, getToken } from '../auth.js';
import { refreshSessionIdentity } from '../sessionIdentity.js';
import { useLiveRefresh } from '../useLiveRefresh.js';
import { learningCapabilities, learningScope, createLearningRequestGate, learningFields, displayLearningValue, candidateStyles, sourceExpectation, validExperienceScope, selectedExperienceWarnings, learningIntentKey } from '../personalLearning.js';
import ExperienceScopeEditor from './ExperienceScopeEditor.vue';
const props = defineProps({ question: { type: Object, default: null }, dirty: Boolean });
const caps = computed(() => learningCapabilities(currentUser.value?.permissions));
const identity = computed(() => learningScope(currentUser.value, getToken()));
const allowed = computed(() => Boolean(getToken() && props.question?.owner_id === currentUser.value?.id && caps.value.history));
const sourceKey = computed(() => JSON.stringify([identity.value, props.question?.id, props.question?.version, props.question?.library_revision, props.question?.review_revision]));
const open = ref(false), comparison = ref(null), jobs = ref([]), currentJob = ref(null), drafts = ref([]), note = ref('');
const budgetHint = computed(() => {
 const job = currentJob.value || {};
 const total = job.execution_budget_seconds || 120;
 const attempt = job.attempt_timeout_seconds || 120;
 return `单次模型分析最多${attempt}秒，最多2次尝试共用${total}秒执行窗口。单次超时后，剩余时间充足才会自动重试；首次排队不占执行窗口，可离页后返回查看。`;
});
const importTarget=ref('rules');
const asExpression=ref(false);
watch(importTarget,()=>{asExpression.value=false;});
const loading = ref(false), submitting = ref(false), importing = ref(false), error = ref(''), notice = ref(''), preview = ref(false), resultStale = ref(false), medicalConfirmed = ref(false);
const readGate = createLearningRequestGate(), jobGate = createLearningRequestGate(), actionGate = createLearningRequestGate();
let disposed = false, timer = null;
const router = inject(routerKey, null);
let removeRouteGuard;
const candidateBaseline = ref('');
const candidateDirty = computed(() => {
 if (!candidateBaseline.value) return false;
 const baseline=JSON.parse(candidateBaseline.value);
 return drafts.value.some(c=>(!c.importedExperienceID || c.importConflict) && JSON.stringify(c)!==JSON.stringify(baseline.find(b=>b.id===c.id)));
});
function allowLeaving() { return !candidateDirty.value || confirm('候选选择或编辑尚未导入个人知识库，离开会丢失这些未保存内容。确定放弃吗？'); }
defineExpose({ allowLeaving, hasUnsavedWork: () => candidateDirty.value });
function beforeUnload(event) { if (candidateDirty.value) { event.preventDefault(); event.returnValue = ''; } }
const hasSelection = computed(() => drafts.value.some(c => c.selected));
const selectedItems = computed(() => drafts.value.filter(c => c.selected).map(c => ({ candidate_id: c.id, title: c.title.trim(), scope: c.scope, rules: c.rules.filter(r => r.selected).map(({ selected, ...rule }) => ({ ...rule, text: rule.text.trim() })) })));
const warnings = computed(() => selectedExperienceWarnings(selectedItems.value));
const hasMedicalChanges = computed(() => selectedItems.value.some(c => c.rules.some(r => r.medical_change)));
const invalidSelection = computed(() => !selectedItems.value.length || selectedItems.value.some(c => !c.title || !validExperienceScope(c.scope) || !c.rules.length || c.rules.some(r => !r.text)));
const comparisonStale = computed(() => comparison.value && (comparison.value.current?.version !== props.question?.version || Object.entries(sourceExpectation(props.question)).some(([key,value]) => comparison.value.expectation?.[key] !== value)));
const analysisBlocked = computed(() => props.dirty ? '有未保存的题目修改，请先保存或撤销；对比只展示当前已保存版。' : !caps.value.analyze ? '当前身份没有 AI 总结权限。' : comparisonStale.value ? '题目状态已变化，请刷新题目后再比较。' : !comparison.value?.original ? '缺少可核验的原始 v1，不能发起分析。' : !comparison.value?.changes?.length ? '没有实际内容差异，无需 AI 分析。' : comparison.value?.blocked_reason || (!comparison.value?.can_analyze ? '当前题目暂不满足分析条件。' : ''));
const jobIsOld = computed(() => currentJob.value && (currentJob.value.source_current === false || currentJob.value.current_version !== props.question?.version));
const importBlocked = computed(() => props.dirty || !caps.value.save || resultStale.value || drafts.value.some(c=>c.importConflict) || jobIsOld.value || currentJob.value?.can_import !== true || currentJob.value?.status !== 'succeeded');
function stopPolling() { clearTimeout(timer); timer = null; }
function clear() { asExpression.value=false;stopPolling(); readGate.invalidate(); jobGate.invalidate(); actionGate.invalidate(); comparison.value = null; jobs.value = []; currentJob.value = null; drafts.value = []; candidateBaseline.value = ''; note.value = ''; error.value = ''; notice.value = ''; preview.value = false; resultStale.value = false; medicalConfirmed.value = false; loading.value = false; submitting.value = false; importing.value = false; }
watch(sourceKey, (next, previous) => {
  const before = JSON.parse(previous || '[]'), after = JSON.parse(next);
  if (before[0] === after[0] && before[1] && before[1] === after[1] && allowed.value && open.value) {
    stopPolling(); readGate.invalidate(); jobGate.invalidate(); actionGate.invalidate();
    resultStale.value = true; preview.value = false; medicalConfirmed.value = false;
    loading.value = false; submitting.value = false; importing.value = false;
    notice.value = '源题已变化，已保留当前候选编辑供核对；请刷新保存版对比，旧状态下不能继续导入。';
    return;
  }
  clear(); open.value = false;
}, { flush: 'sync' });
watch(() => props.dirty, () => { preview.value = false; medicalConfirmed.value = false; });
function setJob(job, explicit = false) {
  if (!job) return;
  const previous = currentJob.value;
  if (previous?.id === job.id && previous.revision !== job.revision && candidateDirty.value && !explicit) resultStale.value = true;
  else if (explicit || previous?.id !== job.id || previous.revision !== job.revision) {
    drafts.value = (job.candidates || []).map(c => ({ ...JSON.parse(JSON.stringify(c)), importedExperienceID:job.imported_experiences?.[c.id] || '', selected: false, rules: (c.rules || []).map(r => ({ ...r, selected: true })) }));
    candidateBaseline.value = JSON.stringify(drafts.value);
    preview.value = false; resultStale.value = false; medicalConfirmed.value = false;
  }
  for (const draft of drafts.value) {
    const imported=job.imported_experiences?.[draft.id];
    if (imported && !draft.importedExperienceID) {
      const baseline=JSON.parse(candidateBaseline.value || '[]').find(c=>c.id===draft.id);
      if (JSON.stringify(draft)!==JSON.stringify(baseline)) {
        draft.importConflict=true;
        notice.value='另一窗口已导入此候选。本窗口未保存的选择或文字已保留，不能当作已保存版本；可复制到已有经验修订，或明确放弃本地副本。';
      }
    }
    if (imported) { draft.importedExperienceID=imported;draft.selected=false; }
  }
  currentJob.value = job;
  jobs.value = [job, ...jobs.value.filter(j => j.id !== job.id)];
  stopPolling(); if (['pending', 'running'].includes(job.status)) timer = setTimeout(refreshJob, 2500);
}
function discardImportedConflict(draft) {
  if (!draft.importConflict || !confirm('确定放弃这条候选在本窗口尚未保存的选择和文字吗？已导入的经验不会改变。')) return;
  const original=currentJob.value?.candidates?.find(c=>c.id===draft.id);if (!original) return;
  Object.assign(draft,JSON.parse(JSON.stringify(original)),{importedExperienceID:draft.importedExperienceID,importConflict:false,selected:false,rules:original.rules.map(r=>({...r,selected:true}))});
}
async function load() {
  if (!allowed.value || disposed) return;
  const ticket = readGate.start(sourceKey.value); loading.value = true; error.value = '';
  try {
    await refreshSessionIdentity(); if (disposed || !readGate.accepts(ticket, sourceKey.value) || !allowed.value) return;
    const result = await api.learningComparison(props.question.id);
    if (disposed || !readGate.accepts(ticket, sourceKey.value)) return;
    comparison.value = result;
    if (result.current?.version !== props.question.version) { error.value = '题目已保存为新版本，请刷新题目后再比较。'; return; }
    const history = caps.value.analyze || caps.value.save ? await api.questionLearningAnalyses(props.question.id) : { jobs: [] };
    if (!disposed && readGate.accepts(ticket, sourceKey.value)) {
      jobs.value = history.jobs || [];
      const selected = jobs.value.find(j => j.id === currentJob.value?.id) || jobs.value[0];
      if (selected) setJob(selected, selected.id !== currentJob.value?.id);
      resultStale.value = selected ? selected.source_current === false : false;
    }
  } catch(e) { if (!disposed && readGate.accepts(ticket, sourceKey.value)) { error.value = e.message; if ([401, 403].includes(e.status)) { clear(); open.value = false; await refreshSessionIdentity().catch(()=>{}); } } }
  finally { if (!disposed && readGate.accepts(ticket, sourceKey.value)) loading.value = false; }
}
function toggle() { if (importing.value || open.value && !allowLeaving()) return; open.value = !open.value; if (open.value) load(); else clear(); }
async function refreshJob() {
  if (!open.value || disposed || !allowed.value || !currentJob.value || document.hidden) return;
  const id = currentJob.value.id, ticket = jobGate.start(`${sourceKey.value}:${id}`);
  try { const data = await api.learningAnalysis(id); if (!disposed && open.value && jobGate.accepts(ticket, `${sourceKey.value}:${currentJob.value?.id}`)) setJob(data.job); }
  catch(e) { if (!disposed && jobGate.accepts(ticket, `${sourceKey.value}:${currentJob.value?.id}`)) { if ([401,403].includes(e.status)) {clear();open.value=false;await refreshSessionIdentity().catch(()=>{});} else error.value = e.message; } }
}
async function analyze() {
  if (!allowed.value || analysisBlocked.value || loading.value || submitting.value || importing.value) return;
  if (['pending', 'running'].includes(currentJob.value?.status)) return;
  if (!allowLeaving()) return;
  const ticket = actionGate.start(sourceKey.value), input = { ...sourceExpectation(props.question), note: note.value.trim() };
  input.request_key = learningIntentKey(currentUser.value.id, currentUser.value.role, 'analyze', [props.question.id, input, currentJob.value?.status === 'failed' ? currentJob.value.id : null]);
  submitting.value = true; error.value = ''; notice.value = '';
  try {
    await refreshSessionIdentity(); if (disposed || !actionGate.accepts(ticket, sourceKey.value) || analysisBlocked.value) return;
    const data = await api.createLearningAnalysis(props.question.id, input);
    if (!disposed && actionGate.accepts(ticket, sourceKey.value)) { setJob(data.job, true); notice.value = '已提交一次 AI 总结；题目、首检和入库状态不变。'; }
  } catch(e) { if (!disposed && actionGate.accepts(ticket, sourceKey.value)) { error.value = e.message; if (e.status === 409) resultStale.value = true; if ([401,403].includes(e.status)) { clear(); open.value=false; await refreshSessionIdentity().catch(()=>{}); } } }
  finally { if (!disposed && actionGate.accepts(ticket, sourceKey.value)) submitting.value = false; }
}
async function selectJob(id) {
  if (submitting.value || importing.value || id !== currentJob.value?.id && !allowLeaving()) return;
  stopPolling(); jobGate.invalidate(); const job = jobs.value.find(j => j.id === id); if (job) { setJob(job, true); await refreshJob(); }
}
function showPreview() { if (invalidSelection.value || importBlocked.value || importing.value) return; preview.value = true; medicalConfirmed.value = false; }
async function save(activate) {
  if (!preview.value || invalidSelection.value || importBlocked.value || importing.value || activate && hasMedicalChanges.value && !medicalConfirmed.value) return;
  const id = currentJob.value.id, ticket = actionGate.start(sourceKey.value);
  const input = { ...sourceExpectation(props.question), expected_revision: currentJob.value.revision, items: selectedItems.value.map(item => ({ ...JSON.parse(JSON.stringify(item)), status: activate ? 'active' : 'draft', confirm_medical_changes: medicalConfirmed.value,...(importTarget.value==='cards'&&asExpression.value?{as_expression:true}:{}) })) };
  input.import_key = learningIntentKey(currentUser.value.id, currentUser.value.role, importTarget.value==='cards'?'knowledge-card':'import', [id, input]);
  importing.value = true; error.value = ''; notice.value = '';
  try {
    await refreshSessionIdentity(); if (disposed || !actionGate.accepts(ticket, sourceKey.value) || importBlocked.value) return;
    const response = importTarget.value==='cards'?await api.learningKnowledgeCards(id,input):await api.importLearningAnalysis(id, input);
    const data=importTarget.value==='cards'?{experiences:response.cards}:response;
    if (!disposed && actionGate.accepts(ticket, sourceKey.value)) {
      if (!Array.isArray(data.experiences) || data.experiences.length!==input.items.length || data.experiences.some(c=>!c.id)) throw new Error('保存结果缺少经验记录，请保持当前编辑并重试核验。');
      if (input.items.some((item,i)=>item.as_expression&&data.experiences[i].content?.expression?.instructions!==item.rules.map(rule=>rule.text).join('\n'))) throw new Error('服务未保留表达要求，请保留编辑并更新后端后重试。');
      for (let i=0;i<input.items.length;i++) {const draft=drafts.value.find(c=>c.id===input.items[i].candidate_id);if(draft){draft.importedExperienceID=data.experiences.find(c=>c.source?.candidate_id===draft.id)?.id || data.experiences[i].id;draft.selected=false;}}
      asExpression.value=false;notice.value = importTarget.value==='cards'?`已保存 ${data.experiences.length} 条个人知识卡${activate?'并启用；勾选后可在知识库工作流采用':'草稿；启用并勾选后可在知识库工作流采用'}。`:`已保存 ${data.experiences.length} 条个人经验${activate?'并启用，将影响本人后续新题':'草稿；启用前不会用于生成'}。`; preview.value = false;
    }
  } catch(e) { if (!disposed && actionGate.accepts(ticket, sourceKey.value)) { error.value = e.message; if (e.status === 409) resultStale.value = true; if ([401,403].includes(e.status)) { clear(); open.value=false; await refreshSessionIdentity().catch(()=>{}); } } }
  finally { if (!disposed && actionGate.accepts(ticket, sourceKey.value)) importing.value = false; }
}
useLiveRefresh(async () => { if (!open.value || !allowed.value || importing.value || submitting.value) return; await refreshSessionIdentity().catch(() => {}); if (open.value && allowed.value) await refreshJob(); });
onMounted(() => { window.addEventListener('beforeunload', beforeUnload); if (router) removeRouteGuard = router.beforeEach(() => allowLeaving()); });
onBeforeUnmount(() => { disposed = true; clear(); window.removeEventListener('beforeunload', beforeUnload); removeRouteGuard?.(); });
</script>
<template>
<section v-if="allowed" class="question-learning-panel">
  <button class="ghost-button" type="button" :aria-expanded="open" :disabled="importing" @click="toggle">{{open?'收起版本比较与经验':'比较原始版 / 总结经验'}}</button>
  <div v-if="open" class="learning-content">
    <p v-if="candidateDirty" class="learning-help" role="status">候选选择或编辑尚未保存；导入个人知识库后才会保留，勾选不会自动启用。</p>
    <div class="ui-help-heading"><h3>原始 AI 版与当前保存版</h3><HelpHint label="AI 总结说明">先核对确定性差异，再按需主动调用 AI。保存、确认入库和导出不会自动学习或再次首检。</HelpHint></div>
    <p v-if="loading" role="status">正在读取版本与分析记录…</p>
    <p v-if="error" class="learning-error" role="alert">{{error}}</p><p v-if="notice" class="learning-notice" role="status">{{notice}}</p>
    <template v-if="comparison">
      <div class="learning-version-labels"><strong>原始版 v{{comparison.original?.version || 1}}</strong><strong>当前保存版 v{{comparison.current?.version}}</strong></div>
      <p v-if="!comparison.original">缺少可核验的 v1，不能把其他历史版本当作原始 AI 版。</p>
      <article v-for="(change,index) in comparison.changes||[]" :key="change.field+index" class="learning-field-change"><h4>{{learningFields[change.field] || change.field}}</h4><div class="learning-diff"><div><small>原始版 v1</small><pre>{{displayLearningValue(change.before)}}</pre></div><div><small>当前保存版 v{{comparison.current?.version}}</small><pre>{{displayLearningValue(change.after)}}</pre></div></div></article>
      <p v-if="!comparison.changes?.length">当前保存内容与原始版没有实际差异。</p>
      <p v-if="analysisBlocked" class="learning-warning" role="status">{{analysisBlocked}}</p>
      <label v-if="caps.analyze" class="learning-label">修改说明（可选）<textarea v-model="note" aria-label="学习修改说明" rows="2" maxlength="1000" :disabled="submitting||importing"/></label>
      <div class="learning-actions"><button v-if="caps.analyze" class="primary-button" type="button" :disabled="loading||submitting||importing||!!analysisBlocked||['pending','running'].includes(currentJob?.status)" @click="analyze">{{submitting?'提交中…':'AI总结修改经验'}}</button><button class="ghost-button" type="button" :disabled="loading||submitting||importing" @click="load">重新读取版本</button></div>
    </template>
    <section v-if="jobs.length" class="learning-job"><label class="learning-label">分析记录<select aria-label="选择学习分析记录" :value="currentJob?.id" :disabled="submitting||importing" @change="selectJob($event.target.value)"><option v-for="j in jobs" :key="j.id" :value="j.id">v{{j.original_version}} → v{{j.current_version}} · {{({pending:'等待执行',running:'分析中',succeeded:'分析完成',failed:'分析失败'})[j.status] || j.status}}</option></select></label>
      <p v-if="['pending','running'].includes(currentJob?.status)" role="status">{{currentJob.status==='pending'?(currentJob.attempts?'等待自动重试':'等待学习分析'):'正在学习分析'}} · 已开始 {{currentJob.attempts||0}} 次（最多 {{currentJob.max_attempts||2}} 次）。可离页后返回查看。<HelpHint label="分析执行预算说明">{{budgetHint}}</HelpHint></p>
      <p v-if="currentJob?.status==='failed'" class="learning-error" role="alert">{{currentJob.error || '分析失败。'}} 本次已结束，不会再自动重试；如需继续，可主动重新发起分析。题目与首检保持原结果。</p>
      <details v-if="currentJob?.attempt_history?.length"><summary>查看分析尝试记录</summary><ul><li v-for="attempt in currentJob.attempt_history" :key="attempt.attempt">第 {{attempt.attempt}} 次 · {{({pending:'已安排有界重试',succeeded:'已完成',failed:'已结束'})[attempt.status]||attempt.status}}<template v-if="attempt.error_kind"> · {{({protocol_error:'候选结构不符合要求',attempt_timeout:'本次模型分析超时',timeout:'分析时间预算耗尽',permission_revoked:'提交身份已撤权',source_changed:'源题已变化',model_error:'模型调用失败'})[attempt.error_kind]||attempt.error_kind}}</template></li></ul></details>
      <p v-if="jobIsOld||resultStale" class="learning-warning" role="alert">此分析不是当前可导入的最新版，题目或候选已变化；旧结果只作历史记录，请重新核对。</p>
      <p v-else-if="currentJob?.blocked_reason" class="learning-warning" role="status">{{currentJob.blocked_reason}}</p>
      <div v-if="currentJob?.status==='succeeded'" class="ui-help-heading"><h4>候选经验</h4><HelpHint label="候选选择说明">三版是同一次调用的不同表达。自主勾选候选和其中规则，再编辑与预览；选择本身不会调用模型。涉及医学变化的建议须自行核对，改答案不代表答案已获认证。</HelpHint></div>
      <fieldset :disabled="importing||resultStale" class="learning-candidates"><article v-for="c in drafts" :key="c.id" class="learning-candidate"><label class="candidate-choice"><input type="checkbox" :aria-label="`选择候选 ${c.id}`" v-model="c.selected" :disabled="!!c.importedExperienceID" @change="preview=false"/>{{candidateStyles[c.style] || c.style}}</label>
        <p v-if="c.importedExperienceID" class="learning-help">此候选已导入个人知识库；后续请到“个人知识库”修订已有记录。</p><p v-if="c.importConflict" class="learning-warning" role="alert">本地副本尚未保存，可复制文字到已有经验修订。<button class="ghost-button" type="button" @click="discardImportedConflict(c)">放弃本地副本</button></p><label class="learning-label">经验标题<input v-model="c.title" :aria-label="`候选标题 ${c.id}`" maxlength="100" :disabled="!!c.importedExperienceID&&!c.importConflict" :readonly="!!c.importConflict" @input="preview=false"/></label>
        <ExperienceScopeEditor v-model="c.scope" :points="question.knowledge_points||[]" :disabled="importing||resultStale||!!c.importedExperienceID" :label="`候选${c.id}适用范围`" @update:modelValue="preview=false"/>
        <div v-for="r in c.rules" :key="r.id" class="learning-rule"><label class="rule-choice"><input type="checkbox" v-model="r.selected" :disabled="!!c.importedExperienceID" :aria-label="`选择规则 ${r.id}`" @change="preview=false"/>采用此规则 <span v-if="r.medical_change" class="medical-change">涉及医学变化 · 待核对</span></label><textarea v-model="r.text" :aria-label="`规则 ${r.id}`" rows="3" maxlength="600" :disabled="!!c.importedExperienceID&&!c.importConflict" :readonly="!!c.importConflict" @input="preview=false"/><p v-if="r.reason">理由：{{r.reason}}</p><details v-if="r.before||r.after"><summary>查看对应字段与前后内容</summary><small>{{learningFields[r.field] || r.field}}</small><p>修改前：{{r.before||'（空）'}}</p><p>修改后：{{r.after||'（空）'}}</p></details></div>
      </article></fieldset>
      <p v-if="hasSelection&&invalidSelection" class="learning-warning">所选候选必须有标题、有效适用范围和至少一条非空规则。</p>
      <button v-if="drafts.length&&caps.save" class="ghost-button" type="button" :disabled="invalidSelection||importBlocked||importing" @click="showPreview">预览所选经验</button>
      <p v-if="drafts.length&&!caps.save" class="learning-help">当前身份只可查看总结，没有保存个人经验的权限。</p>
    </section>
    <section v-if="preview" class="learning-import-preview"><label>保存位置<select v-model="importTarget" aria-label="候选保存位置" :disabled="importing"><option value="rules">命题规则（原个人经验）</option><option value="cards">个人知识卡</option></select></label><HelpHint v-if="importTarget==='cards'" label="候选转知识卡说明">仅整理所选规则，按来源题考点关联，不另调模型。三版属于同源表达，建议核对合并重复内容；保存后可在个人知识库的“知识卡”模块编辑、选择采用或申请共享。</HelpHint><label v-if="importTarget==='cards'&&comparison?.expression_cards_supported" class="learning-confirm"><input type="checkbox" v-model="asExpression" aria-label="将候选作为表达要求保存" :disabled="importing"/>作为表达要求保存<HelpHint label="候选表达用途说明">所选文字用于表达要求，仍须本人启用并勾选，按考点进入知识库工作流。医学事实、题目目标和输出协议优先，不另调用模型。</HelpHint></label><h4>核对将保存的 {{selectedItems.length}} 条个人经验</h4><p v-for="warning in warnings" :key="warning" class="learning-warning" role="status">{{warning}}</p><HelpHint label="个人经验适用范围">以下规则只作为本人命题参考；表达或医学冲突仍需人工核对。</HelpHint><article v-for="item in selectedItems" :key="item.candidate_id"><strong>{{item.title}}</strong><ul><li v-for="r in item.rules" :key="r.id">{{r.text}}</li></ul></article><label v-if="hasMedicalChanges" class="medical-confirm"><input v-model="medicalConfirmed" type="checkbox" aria-label="已核对医学变化" :disabled="importing"/>我已核对涉及医学变化的规则及适用前提；它们不代表共享医学结论。</label><div class="learning-actions"><button class="ghost-button" type="button" :disabled="importing||importBlocked" @click="save(false)">保存草稿</button><button class="primary-button" type="button" :disabled="importing||importBlocked||(hasMedicalChanges&&!medicalConfirmed)" @click="save(true)">保存并启用</button><button class="ghost-button" type="button" :disabled="importing" @click="preview=false">返回编辑</button></div></section>
    <RouterLink to="/my-experiences" class="learning-library-link">管理个人知识库</RouterLink>
  </div>
</section>
</template>
<style scoped>
.question-learning-panel{margin:18px 0;min-width:0;color:#425a73;font-size:13px}.learning-content{margin-top:12px;padding:16px;border:1px solid #dfe8f3;border-radius:8px;background:#fbfcff;min-width:0}.learning-content h3{font-size:15px;margin:0 0 10px}.learning-content h4{font-size:13px;margin:8px 0}.learning-content p,.learning-content li{line-height:1.8;overflow-wrap:anywhere}.learning-help,.learning-content small{color:#748396}.learning-version-labels,.learning-diff{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:12px}.learning-field-change{border-top:1px solid #e5ebf3;margin-top:14px;padding-top:8px}.learning-diff>div{min-width:0;padding:10px;border:1px solid #e6ecf4;background:#fff;border-radius:6px}.learning-diff pre{font:inherit;white-space:pre-wrap;overflow-wrap:anywhere;margin:6px 0}.learning-label{display:flex;flex-direction:column;gap:7px;margin:14px 0}.learning-content input:not([type=checkbox]),.learning-content textarea,.learning-content select{max-width:100%;min-width:0;width:100%;box-sizing:border-box;border:1px solid #d6deea;border-radius:5px;padding:8px;font:inherit;color:inherit;background:#fff}.learning-content textarea{resize:vertical;line-height:1.7}.learning-actions{display:flex;flex-wrap:wrap;gap:9px;margin:14px 0}.learning-warning{color:#946515;background:#fff7e8;padding:8px;border-radius:5px}.learning-error{color:#b91c1c}.learning-notice{color:#166534}.learning-job{border-top:1px solid #e4eaf2;margin-top:18px;padding-top:8px}.learning-candidates{margin:0;padding:0;border:0;min-width:0}.learning-candidate{border:1px solid #dde6f1;border-radius:7px;padding:14px;margin:14px 0;background:#fff;min-width:0}.candidate-choice,.rule-choice,.medical-confirm{display:flex;align-items:flex-start;gap:8px;flex-wrap:wrap;line-height:1.8}.candidate-choice{font-weight:600;font-size:14px}.candidate-choice input,.rule-choice input,.medical-confirm input{margin-top:5px}.learning-rule{border-top:1px solid #e8edf5;margin-top:14px;padding-top:12px;min-width:0}.learning-rule textarea{margin-top:8px}.learning-rule p{margin:6px 0}.learning-rule summary{cursor:pointer;color:#37648c}.medical-change{font-size:12px;color:#9b6311}.learning-import-preview{border:1px solid #a8c5e1;border-radius:7px;padding:14px;margin-top:16px;background:#f7fbff}.learning-import-preview ul{padding-left:20px}.learning-library-link{display:inline-block;margin-top:16px;color:#2d689d}@media(max-width:700px){.learning-content{padding:12px}.learning-diff{grid-template-columns:minmax(0,1fr)}.learning-version-labels{display:flex;flex-wrap:wrap;gap:8px}.learning-version-labels strong+strong::before{content:'→ ';font-weight:400}.learning-candidate{padding:10px}.learning-actions>*{max-width:100%}}
</style>
