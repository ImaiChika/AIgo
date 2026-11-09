<script setup>
import HelpHint from "../components/HelpHint.vue";
import { computed, ref, watch, onMounted, onBeforeUnmount, nextTick } from 'vue';
import { onBeforeRouteLeave } from 'vue-router';
import { api } from '../api.js';
import { currentUser, getToken } from '../auth.js';
import { refreshSessionIdentity } from '../sessionIdentity.js';
import { useLiveRefresh } from '../useLiveRefresh.js';
import { learningCapabilities, learningScope, createLearningRequestGate, experienceStates, scopeLabel, validExperienceScope, selectedExperienceWarnings, experienceSourceLabel, manualKnowledgeRules, learningRequestKey } from '../personalLearning.js';
import { readPersonalKnowledgeDraft, savePersonalKnowledgeDraft } from '../personalKnowledgeDraft.js';
import ExperienceScopeEditor from '../components/ExperienceScopeEditor.vue';
const caps = computed(() => learningCapabilities(currentUser.value?.permissions));
const identity = computed(() => learningScope(currentUser.value, getToken()));
const rows = ref([]), total = ref(0), offset = ref(0), status = ref(''), query = ref(''), appliedQuery = ref(''), loading = ref(false), busy = ref(false), error = ref(''), message = ref('');
const editor = ref(null), editorBase = ref(null), stale = ref(false), medicalConfirmed = ref(false), historyRows = ref([]), historyID = ref(''), historyBusy = ref(false), selectedRevision = ref(0), activation = ref(null);
const readGate = createLearningRequestGate(), historyGate = createLearningRequestGate(), actionGate = createLearningRequestGate(); let disposed = false;
const creator = ref(null);
const messageID=ref(''), messageIndex=ref(0), messageTitle=ref('');let messageTimer;
function clearMessage(){clearTimeout(messageTimer);messageTimer=undefined;message.value='';messageID.value='';}
function showMessage(text,id='',index=0,title=''){clearMessage();message.value=text;messageID.value=id;messageIndex.value=index;messageTitle.value=title;messageTimer=setTimeout(clearMessage,4000);}
const displayRows=computed(()=>{const result=[...rows.value];if(message.value&&messageID.value&&!result.some(c=>c.id===messageID.value))result.splice(Math.min(messageIndex.value,result.length),0,{id:messageID.value,title:messageTitle.value,noticeOnly:true});return result;});

const operationDialog = ref(null), dialogHeading = ref(null), historyTitle = ref('');
const dialogKind = computed(() => creator.value ? 'create' : editor.value ? 'edit' : activation.value ? 'activate' : historyID.value ? 'history' : '');
const dialogTitle = computed(() => ({create:'新增命题规则',edit:'修订命题规则',activate:'启用命题规则',history:'命题规则历史'})[dialogKind.value] || '命题规则');
watch(dialogKind, async kind => {
  if (!kind) { operationDialog.value?.close(); return; }
  await nextTick();
  if (!disposed && canView.value && dialogKind.value && operationDialog.value && !operationDialog.value.open) {
    operationDialog.value.showModal(); dialogHeading.value?.focus();
  }
});
function closeHistory() { historyGate.invalidate(); historyID.value=''; historyTitle.value=''; historyRows.value=[]; selectedRevision.value=0; historyBusy.value=false; }
function closeOperation() {
  if (busy.value) return;
  if (creator.value) closeCreator();
  else if (editor.value) closeEditor();
  else if (activation.value) { activation.value=null; error.value=''; }
  else { closeHistory(); error.value=''; }
}
const canView = computed(() => Boolean(getToken() && caps.value.libraryView));
const creatorDirty = computed(() => Boolean(creator.value && (creator.value.pending || creator.value.title.trim() || creator.value.content.trim() || creator.value.scope.kind !== 'all' || creator.value.medical)));
const dirty = computed(() => creatorDirty.value || editor.value && JSON.stringify(editor.value) !== JSON.stringify(editorBase.value));
const creatorSize = computed(() => Array.from(creator.value?.content.trim() || '').length);
const validCreator = computed(() => creator.value?.title.trim() && Array.from(creator.value.title.trim()).length <= 100 && creatorSize.value > 0 && creatorSize.value <= 2000 && validExperienceScope(creator.value.scope));
watch(creator, value => { if (value && !disposed && caps.value.manage) savePersonalKnowledgeDraft(currentUser.value, value); }, { deep: true });
const medical = computed(() => (editor.value?.rules || activation.value?.rules || []).some(r => r.medical_change));
function needsMedicalConfirmation(card, fromEditor = false) { return card?.rules?.some(r => r.medical_change) && (!card.medical_confirmed || fromEditor && JSON.stringify([card.scope, card.rules]) !== JSON.stringify([editorBase.value?.scope, editorBase.value?.rules])); }
const medicalNeedsConfirmation = computed(() => needsMedicalConfirmation(editor.value || activation.value, !!editor.value));
const validEditor = computed(() => editor.value?.title?.trim() && validExperienceScope(editor.value.scope) && editor.value.rules?.length && editor.value.rules.every(r => r.text.trim()));
const warnings = computed(() => selectedExperienceWarnings(editor.value ? [editor.value] : []));
const historyVersion = computed(() => historyRows.value.find(c => c.revision === Number(selectedRevision.value)) || historyRows.value[0]);
function clear() { creator.value = null; readGate.invalidate(); historyGate.invalidate(); historyTitle.value = ''; actionGate.invalidate(); rows.value = []; total.value = 0; offset.value = 0; editor.value = null; editorBase.value = null; historyRows.value = []; historyID.value = ''; selectedRevision.value = 0; activation.value = null; medicalConfirmed.value = false; stale.value = false; error.value = ''; clearMessage(); loading.value = false; busy.value = false; historyBusy.value = false; }
watch(identity, (next, previous) => { const old = JSON.parse(previous || '[]'); if (old[0]) savePersonalKnowledgeDraft({ id: old[0], role: old[1] }, null); clear(); if (canView.value) load(); }, { flush: 'sync' });
async function load({ background = false } = {}) {
  if (!canView.value || disposed || background && (loading.value || busy.value)) return;
  const ticket = readGate.start(identity.value);
  // Live checks keep the existing layout and controls stable, including empty lists.
  // Explicit navigation/search still shows loading feedback.
  if (!background) { loading.value = true; error.value = ''; }
  try { const data = await api.personalExperiences({ status: status.value, q: appliedQuery.value, limit: 20, offset: offset.value }); if (!disposed && readGate.accepts(ticket, identity.value)) { error.value = ''; rows.value = data.experiences || []; total.value = data.total ?? rows.value.length; if (editor.value) { const current = rows.value.find(c => c.id === editor.value.id); if (current && current.revision !== editorBase.value.revision) stale.value = true; } } }
  catch(e) { if (!disposed && readGate.accepts(ticket, identity.value)) { error.value = e.message; if ([401, 403].includes(e.status)) { discardCreatorDraft(); clear(); await refreshSessionIdentity(); } } }
  finally { if (!disposed && readGate.accepts(ticket, identity.value)) loading.value = false; }
}
defineExpose({allowLeaving:()=>!busy.value&&canLeaveEditor()});
function canLeaveEditor() { return !dirty.value || confirm('个人经验有未保存修改，确定放弃并继续吗？'); }
function edit(card) {
  if (!caps.value.manage || busy.value || !canLeaveEditor()) return;
  discardCreatorDraft(); closeHistory();
  editor.value = JSON.parse(JSON.stringify(card)); editorBase.value = JSON.parse(JSON.stringify(card)); stale.value = false; medicalConfirmed.value = false; activation.value = null; error.value = ''; clearMessage();
}
function closeEditor() { if (!busy.value && canLeaveEditor()) { editor.value = null; editorBase.value = null; stale.value = false; error.value = ''; } }
async function showHistory(card) {
  if (busy.value || !canView.value || !canLeaveEditor()) return;
  clearMessage(); discardCreatorDraft(); editor.value=null; editorBase.value=null; activation.value=null;
  const ticket = historyGate.start(`${identity.value}:${card.id}`); historyID.value = card.id; historyTitle.value = card.title; historyRows.value = []; historyBusy.value = true; error.value = '';
  try { const data = await api.personalExperienceHistory(card.id); if (!disposed && historyGate.accepts(ticket, `${identity.value}:${historyID.value}`)) { historyRows.value = data.versions || []; selectedRevision.value = historyRows.value[0]?.revision || 0; } }
  catch(e) { if (!disposed && historyGate.accepts(ticket, `${identity.value}:${historyID.value}`)) { error.value = e.message; if ([401,403].includes(e.status)) { discardCreatorDraft(); clear(); await refreshSessionIdentity().catch(()=>{}); } } }
  finally { if (!disposed && historyGate.accepts(ticket, `${identity.value}:${historyID.value}`)) historyBusy.value = false; }
}
function prepareActivation(card) { if (!caps.value.manage || busy.value || !canLeaveEditor()) return; discardCreatorDraft(); closeHistory(); activation.value = card; editor.value = null; editorBase.value = null; medicalConfirmed.value = false; error.value = ''; }
async function update(card, nextStatus, fromEditor = false) {
  if (!caps.value.manage || busy.value || fromEditor && (!validEditor.value || stale.value)) return;
  if (nextStatus === 'active' && needsMedicalConfirmation(card, fromEditor) && !medicalConfirmed.value) return;
  const resultIndex=Math.max(0,rows.value.findIndex(c=>c.id===card.id));
  const input = { expected_revision: fromEditor ? editorBase.value.revision : card.revision, title: card.title.trim(), scope: JSON.parse(JSON.stringify(card.scope)), rules: card.rules.map(r => ({ ...r, text: r.text.trim() })), status: nextStatus, confirm_medical_changes: medicalConfirmed.value };
  const ticket = actionGate.start(identity.value); busy.value = true; error.value = ''; clearMessage();
  try { await refreshSessionIdentity(); if (disposed || !actionGate.accepts(ticket, identity.value) || !caps.value.manage) return; await api.savePersonalExperience(card.id, input); if (!disposed && actionGate.accepts(ticket, identity.value)) { editor.value = null; editorBase.value = null; activation.value = null; const success=nextStatus==='active'?'已启用':nextStatus==='archived'?'已归档':card.status==='archived'?'已恢复为草稿':fromEditor?'已保存草稿':'已停用'; historyGate.invalidate(); historyID.value = ''; historyRows.value = []; await load(); if(!disposed&&actionGate.accepts(ticket,identity.value))showMessage(success,card.id,resultIndex,card.title); } }
  catch(e) { if (!disposed && actionGate.accepts(ticket, identity.value)) { error.value = e.message; if (e.status === 409) { stale.value = true; activation.value = null; } if ([401, 403].includes(e.status)) { discardCreatorDraft(); clear(); await refreshSessionIdentity(); } } }
  finally { if (!disposed && actionGate.accepts(ticket, identity.value)) busy.value = false; }
}
async function refresh(background = false) { const silent = background === true; if (busy.value || disposed || silent && loading.value) return; if(!silent)clearMessage(); const scope = identity.value; await refreshSessionIdentity().catch(() => {}); if (scope === identity.value && canView.value) await load({ background: silent }); }
watch(status, () => { clearMessage(); offset.value = 0; load(); });
function go(delta) { if (busy.value || loading.value || !canLeaveEditor()) return; clearMessage(); discardCreatorDraft(); editor.value = null; editorBase.value = null; offset.value = Math.max(0, offset.value + delta * 20); load(); }
function search() { if (busy.value || !canLeaveEditor()) return; clearMessage(); discardCreatorDraft(); editor.value = null; editorBase.value = null; appliedQuery.value = query.value.trim(); offset.value = 0; load(); }
function discardCreatorDraft() { creator.value = null; savePersonalKnowledgeDraft(currentUser.value, null); }
function newPersonalKnowledge() {
  if (!caps.value.manage || busy.value || !canLeaveEditor()) return;
  closeHistory(); editor.value = null; editorBase.value = null; activation.value = null;
  creator.value = { createKey: learningRequestKey(), title: '', content: '', scope: { kind: 'all' }, medical: false, confirmed: false, pending: null };
  error.value = ''; clearMessage();
}
function closeCreator() {
  if (busy.value || !canLeaveEditor()) return;
  creator.value = null; savePersonalKnowledgeDraft(currentUser.value, null); error.value = '';
}
async function createKnowledge(nextStatus) {
  if (!caps.value.manage || busy.value || !creator.value) return;
  if (!creator.value.pending && (!validCreator.value || nextStatus === 'active' && creator.value.medical && !creator.value.confirmed)) return;
  const ticket = actionGate.start(identity.value); busy.value = true; error.value = ''; clearMessage();
  try {
    await refreshSessionIdentity();
    if (disposed || !actionGate.accepts(ticket, identity.value) || !caps.value.manage || !creator.value) return;
    creator.value.pending ||= { create_key: creator.value.createKey, title: creator.value.title.trim(), scope: JSON.parse(JSON.stringify(creator.value.scope)), rules: manualKnowledgeRules(creator.value.content, creator.value.medical), status: nextStatus, confirm_medical_changes: creator.value.confirmed };
    const data = await api.createPersonalExperience(JSON.parse(JSON.stringify(creator.value.pending)));
    if (disposed || !actionGate.accepts(ticket, identity.value)) return;
    if (!data?.id || !Number.isInteger(data.revision) || data.revision < 1) throw new Error('保存结果缺少知识记录，请重试核对同一请求。');
    const active = creator.value.pending.status === 'active';
    creator.value = null; savePersonalKnowledgeDraft(currentUser.value, null);
    await load();
    if(!disposed&&actionGate.accepts(ticket,identity.value))showMessage(active?'个人知识已保存并启用':'个人知识草稿已保存');
  } catch(e) {
    if (!disposed && actionGate.accepts(ticket, identity.value)) {
      error.value = e.message;
      if (e.status === 400 && creator.value) creator.value.pending = null;
      if ([401,403].includes(e.status)) { clear(); savePersonalKnowledgeDraft(currentUser.value, null); await refreshSessionIdentity().catch(() => {}); }
    }
  } finally { if (!disposed && actionGate.accepts(ticket, identity.value)) busy.value = false; }
}
function beforeUnload(event) { if (dirty.value) { event.preventDefault(); event.returnValue = ''; } }
onBeforeRouteLeave(() => { if (!getToken()) return true; if (!canLeaveEditor()) return false; savePersonalKnowledgeDraft(currentUser.value, null); return true; });
onMounted(async () => { window.addEventListener('beforeunload', beforeUnload); await refresh(); if (!disposed && caps.value.manage && !dialogKind.value) creator.value = readPersonalKnowledgeDraft(currentUser.value); }); useLiveRefresh(() => refresh(true));
onBeforeUnmount(() => { disposed = true; clearMessage(); operationDialog.value?.close(); readGate.invalidate(); historyGate.invalidate(); actionGate.invalidate(); window.removeEventListener('beforeunload', beforeUnload); });
</script>
<template>
<section v-if="canView" class="personal-experiences-page panel"><div class="experience-page-heading"><div><div class="ui-help-heading"><h2>个人知识库</h2><HelpHint label="个人知识库说明">仅本人可见的命题要求与经验。支持手工维护，也可从题目修改中主动总结；保存、修订与启停不调用模型。</HelpHint></div></div><div class="experience-actions"><button v-if="caps.manage" class="primary-button" type="button" :disabled="busy" @click="newPersonalKnowledge">新增个人知识</button><small v-if="message&&!messageID" class="experience-success" role="status">{{message}}</small><RouterLink v-if="caps.view" class="ghost-button" to="/new-questions">返回题目整理</RouterLink></div></div>
  <form class="experience-page-filters" @submit.prevent="search"><label>经验状态<select v-model="status" aria-label="筛选经验状态" :disabled="busy"><option value="">全部状态</option><option v-for="(name,key) in experienceStates" :key="key" :value="key">{{name}}</option></select></label><label>标题关键词<input v-model="query" aria-label="搜索个人经验" maxlength="120"/></label><button class="ghost-button" :disabled="loading||busy">查询</button><button class="ghost-button" type="button" :disabled="loading||busy" @click="refresh">刷新</button><small>{{total}} 条个人经验</small></form>
  <p v-if="error&&!dialogKind" class="experience-error" role="alert">{{error}}</p><p v-if="loading" role="status">读取中…</p><p v-else-if="!rows.length&&!messageID" class="experience-empty">暂无{{status==='active'?'已启用':''}}个人知识。可手工新增，或从本人题目修改中主动总结。</p>
  <template v-for="card in displayRows" :key="card.id"><p v-if="card.noticeOnly" class="experience-action-result experience-success" role="status">「{{card.title}}」{{message}}</p><article v-else class="personal-experience-card"><div class="experience-card-heading"><h3>{{card.title}}</h3><span :class="['experience-state',card.status]">{{experienceStates[card.status] || card.status}}</span><small>版本 {{card.revision}}</small></div><p>{{scopeLabel(card.scope)}}</p><ul><li v-for="r in card.rules||[]" :key="r.id">{{r.text}}<span v-if="r.medical_change" class="experience-medical"> · 医学变化需核对</span></li></ul><p class="experience-source">来源：{{experienceSourceLabel(card)}}；来源与历史保留。</p><div class="experience-actions"><button class="ghost-button" type="button" :disabled="busy" @click="showHistory(card)">查看历史</button><button v-if="caps.manage&&card.status==='archived'" class="ghost-button" type="button" :disabled="busy" @click="update(card,'draft')">恢复为草稿</button><template v-if="caps.manage&&card.status!=='archived'"><button class="ghost-button" type="button" :disabled="busy" @click="edit(card)">修订</button><button v-if="card.status==='active'" class="ghost-button" type="button" :disabled="busy" @click="update(card,'draft')">停用</button><button v-else class="ghost-button" type="button" :disabled="busy" @click="prepareActivation(card)">启用</button><button class="text-button" type="button" :disabled="busy" @click="update(card,'archived')">归档</button></template><small v-if="message&&messageID===card.id" class="experience-success" role="status">{{message}}</small></div></article></template>
  <div v-if="total>20" class="experience-pager"><button class="ghost-button" type="button" :disabled="!offset||loading||busy" @click="go(-1)">上一页</button><span>{{Math.floor(offset/20)+1}} / {{Math.max(1,Math.ceil(total/20))}}</span><button class="ghost-button" type="button" :disabled="offset+20>=total||loading||busy" @click="go(1)">下一页</button></div>
  <Teleport to="body"><dialog v-if="dialogKind" ref="operationDialog" class="personal-experiences-page experience-dialog" aria-labelledby="personal-rule-dialog-title" @cancel.prevent="closeOperation">
    <header class="experience-dialog-heading"><h2 id="personal-rule-dialog-title" ref="dialogHeading" tabindex="-1">{{dialogTitle}}</h2><HelpHint label="新增个人知识说明">写下希望后续命题遵循的表达要求或经验，仅对本人可见。选择适用范围后，可先存草稿或直接启用。</HelpHint><button class="ghost-button experience-dialog-close" type="button" aria-label="关闭命题规则窗口" :disabled="busy" @click="closeOperation">×</button></header>
    <div class="experience-dialog-body"><p v-if="error" class="experience-error" role="alert">{{error}}</p>
  <section v-if="creator&&caps.manage" class="experience-editor personal-knowledge-create">

    <fieldset :disabled="busy||!!creator.pending"><label>知识标题<input v-model="creator.title" aria-label="知识标题" maxlength="100"/></label><ExperienceScopeEditor v-model="creator.scope" label="知识适用范围"/><label>命题要求或经验<textarea v-model="creator.content" aria-label="命题要求或经验" rows="5" maxlength="4000" placeholder="例如：解析按选项分段，先说明正确项依据，再分别解释各干扰项。"/></label><small :class="{'experience-error':creatorSize>2000}">{{creatorSize}} / 2000 字</small><label class="experience-check"><input v-model="creator.medical" type="checkbox" aria-label="包含医学判断变化"/>包含医学判断或答案相关要求，需要核对适用前提</label><label v-if="creator.medical" class="experience-check"><input v-model="creator.confirmed" type="checkbox" aria-label="已核对手工知识医学变化"/>我已核对涉及医学变化的要求及适用前提。</label></fieldset>
    <p v-if="creator.pending" role="status">上次保存结果尚未确认，内容和请求已保留。请重试核对；放弃本地草稿不会撤销服务端可能已完成的保存。</p>
    <div class="experience-actions"><button class="ghost-button" type="button" :disabled="busy" @click="closeCreator">放弃本地草稿</button><button v-if="creator.pending" class="primary-button" type="button" :disabled="busy" @click="createKnowledge(creator.pending.status)">重试核对保存</button><template v-else><button class="ghost-button" type="button" :disabled="busy||!validCreator" @click="createKnowledge('draft')">保存草稿</button><button class="primary-button" type="button" :disabled="busy||!validCreator||(creator.medical&&!creator.confirmed)" @click="createKnowledge('active')">保存并启用</button></template></div>
  </section>
  <section v-else-if="editor&&caps.manage" class="experience-editor"><div class="ui-help-heading"><h3>基于版本 {{editorBase.revision}}</h3><HelpHint label="个人知识修订说明">保存草稿会停止后续新任务采用；选择“保存并启用”才将修订用于之后新题。</HelpHint></div><p v-if="stale" class="experience-error" role="alert">经验已被另一窗口修改，请保留自己的文字并重新核对最新版本，不能覆盖旧修订。</p><fieldset :disabled="busy||stale"><label>经验标题<input v-model="editor.title" aria-label="经验标题" maxlength="100"/></label><ExperienceScopeEditor v-model="editor.scope" label="经验适用范围"/><label v-for="r in editor.rules" :key="r.id">经验规则<span v-if="r.medical_change" class="experience-medical">涉及医学变化 · 需核对</span><textarea v-model="r.text" :aria-label="`修订规则 ${r.id}`" rows="3" maxlength="600"/></label><label v-if="medicalNeedsConfirmation" class="experience-check"><input v-model="medicalConfirmed" type="checkbox" aria-label="核对修订的医学变化"/>我已核对这些医学变化及适用前提。</label></fieldset><p v-for="warning in warnings" :key="warning" class="experience-medical" role="status">{{warning}}</p><div class="experience-actions"><button class="ghost-button" type="button" :disabled="busy||stale||!validEditor" @click="update(editor,'draft',true)">保存为草稿</button><button class="primary-button" type="button" :disabled="busy||stale||!validEditor||(medicalNeedsConfirmation&&!medicalConfirmed)" @click="update(editor,'active',true)">保存并启用</button><button class="ghost-button" type="button" :disabled="busy" @click="closeEditor">取消修订</button></div></section>
  <section v-else-if="activation" class="experience-activation"><div class="ui-help-heading"><h3>启用「{{activation.title}}」</h3><HelpHint label="个人知识启用说明">该经验将按适用范围影响本人后续新题；已提交任务继续使用原冻结版本。</HelpHint></div><label v-if="medicalNeedsConfirmation" class="experience-check"><input v-model="medicalConfirmed" type="checkbox" aria-label="核对待启用医学变化" :disabled="busy"/>我已核对涉及医学变化的规则和适用前提。</label><div class="experience-actions"><button class="primary-button" type="button" :disabled="busy||(medicalNeedsConfirmation&&!medicalConfirmed)" @click="update(activation,'active')">确认启用</button><button class="ghost-button" type="button" :disabled="busy" @click="activation=null">取消</button></div></section>
  <section v-else-if="historyID" class="experience-history"><h3>{{historyTitle}}</h3><p v-if="historyBusy" role="status">正在读取历史…</p><template v-if="historyVersion"><label>查看版本<select v-model="selectedRevision" aria-label="查看经验历史版本"><option v-for="c in historyRows" :key="c.revision" :value="c.revision">版本 {{c.revision}} · {{experienceStates[c.status]}}</option></select></label><h4>{{historyVersion.title}}</h4><p>{{scopeLabel(historyVersion.scope)}}</p><ul><li v-for="r in historyVersion.rules" :key="r.id">{{r.text}}</li></ul><small>来源：{{experienceSourceLabel(historyVersion)}}</small></template><p v-else-if="!historyBusy&&!error" class="experience-empty">暂无历史版本。</p><div class="experience-actions"><button class="ghost-button" type="button" @click="closeOperation">关闭历史</button></div></section>
    </div>
  </dialog></Teleport>
</section>
<section v-else class="panel personal-experiences-page"><p>当前身份没有查看个人经验的权限，请切换到已授权的工作身份。</p></section>
</template>
<style scoped>
.personal-experiences-page{max-width:1100px;padding:24px;min-width:0;color:#425a73}.experience-page-heading,.experience-card-heading,.experience-actions,.experience-page-filters,.experience-pager{display:flex;align-items:center;gap:12px;flex-wrap:wrap}.experience-page-heading{justify-content:space-between;align-items:flex-start}.experience-page-heading h2{font-size:20px;margin:0}.personal-experiences-page h3{font-size:15px;margin:0}.personal-experiences-page p,.personal-experiences-page li{line-height:1.8;overflow-wrap:anywhere}.experience-page-heading p,.experience-source,.personal-experiences-page small{color:#748396;font-size:13px}.experience-page-heading p{max-width:760px}.personal-experiences-page label{display:flex;flex-direction:column;gap:7px;font-size:13px;margin:12px 0}.personal-experiences-page select,.personal-experiences-page input:not([type=checkbox]),.personal-experiences-page textarea{padding:8px 10px;border:1px solid #d6deea;border-radius:5px;background:#fff;color:inherit;font:inherit;max-width:100%;box-sizing:border-box;min-width:0}.personal-experiences-page textarea{width:100%;line-height:1.8;resize:vertical}.personal-experience-card{border-top:1px solid #e5ecf4;padding:20px 0;min-width:0}.personal-experience-card ul{padding-left:20px;font-size:14px}.experience-state{border-radius:4px;background:#f0f3f8;padding:3px 8px;font-size:12px}.experience-state.active{background:#eaf7ee;color:#166534}.experience-card-heading h3{overflow-wrap:anywhere}.experience-actions{margin-top:14px;gap:8px}.experience-source{font-size:12px}.experience-editor,.experience-history,.experience-activation{padding:18px;border:1px solid #dce6f1;border-radius:7px;background:#f8fbff;margin-top:18px;min-width:0}.experience-editor fieldset{border:0;padding:0;min-width:0;margin:0}.experience-editor input{width:100%}.experience-editor .experience-check,.experience-activation .experience-check{flex-direction:row;align-items:flex-start;line-height:1.8}.experience-check input{width:auto;margin-top:6px}.experience-error{color:#b91c1c}.experience-success{color:#166534}.experience-medical{color:#946515;font-size:12px}.experience-empty{color:#748396;text-align:center;padding:28px 0}.experience-pager{justify-content:flex-end;margin-top:10px}.experience-history ul{padding-left:20px}@media(max-width:700px){.personal-experiences-page{padding:16px}.experience-page-heading{display:block}.experience-page-heading>a{display:inline-block;margin-bottom:8px}.experience-editor,.experience-history,.experience-activation{padding:12px}.experience-actions button{max-width:100%}}
.experience-dialog{box-sizing:border-box;width:760px;max-width:calc(100vw - 28px);max-height:calc(100dvh - 40px);padding:0;margin:auto;border:1px solid #dbe5ef;border-radius:9px;color:#425a73;background:#fff;box-shadow:0 18px 70px #16345138;overflow:hidden}.experience-dialog[open]{display:flex;flex-direction:column}.experience-dialog::backdrop{background:#172d485c}.experience-dialog-heading{display:flex;align-items:center;justify-content:space-between;gap:16px;padding:18px 24px;border-bottom:1px solid #e4ebf3;flex:none}.experience-dialog-heading h2{margin:0;font-size:18px;line-height:1.5}.experience-dialog-heading h2:focus{outline:none}.experience-dialog-close{width:36px;height:36px;flex:none;padding:0;font-size:20px}.experience-dialog-body{min-height:0;padding:20px 24px;overflow:auto;overscroll-behavior:contain}.experience-dialog .experience-editor,.experience-dialog .experience-history,.experience-dialog .experience-activation{padding:0;margin:0;border:0;background:transparent}.experience-dialog .experience-history ul{white-space:pre-wrap}.experience-dialog .experience-actions{padding-top:16px;border-top:1px solid #e4ebf3;margin-top:20px}.experience-dialog .experience-empty{padding:16px 0}@media(max-width:600px){.experience-dialog{padding:0;max-height:calc(100dvh - 28px)}.experience-dialog-heading{padding:14px 16px}.experience-dialog-body{padding:16px}}
.experience-page-filters{display:grid;grid-template-columns:minmax(140px,180px) minmax(180px,1fr) auto auto auto;align-items:end;margin:20px 0;gap:12px}.experience-page-filters label{margin:0}.experience-page-filters :is(select,input,button){box-sizing:border-box;height:40px;min-height:40px}.experience-page-filters :is(select,input){width:100%}.experience-page-filters small{align-self:end;line-height:40px;white-space:nowrap}.experience-actions .experience-success{font-size:12px;color:#166534;line-height:1.6}.experience-action-result{font-size:12px;padding:12px 0;margin:0;border-top:1px solid #e5ecf4}@media(max-width:700px){.experience-page-filters{grid-template-columns:1fr 1fr}.experience-page-filters small{grid-column:1/-1;line-height:1.6}.experience-page-filters button{width:100%}}
</style>
