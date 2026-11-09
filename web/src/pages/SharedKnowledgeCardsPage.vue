<script setup>
import {formatDateTime} from "../dateTime.js";
import {knowledgeRows,completeKnowledgeCards} from '../knowledgeSummaries.js';
import KnowledgeCardEditor from "../components/KnowledgeCardEditor.vue";
import HelpHint from "../components/HelpHint.vue";
import { computed, ref, watch, nextTick, onMounted, onBeforeUnmount } from 'vue';
import { api } from '../api.js';
import { currentUser, getToken } from '../auth.js';
import { refreshSessionIdentity } from '../sessionIdentity.js';
import KnowledgeImportDialog from '../components/KnowledgeImportDialog.vue';
import { knowledgeCapabilities, knowledgeScope, createKnowledgeRequestGate, cardKinds, cardKindLabel, cardStates, dimensionFields, toKnowledgeEditor, fromKnowledgeEditor } from '../knowledgeBaseAccess.js';
const caps = computed(() => knowledgeCapabilities(currentUser.value?.permissions));
const scope = computed(() => knowledgeScope(currentUser.value, getToken()));
const chosenIDs=ref(new Set());
const cards = ref([]), total = ref(0), query = ref(''), kind = ref(''), status = ref(''), offset = ref(0);
const loading = ref(false), busy = ref(false), error = ref(''), message = ref('');
const draft = ref(null), selected = ref(null), versions = ref([]), confirmation = ref(null);
const importOpen = ref(false);
const editor = ref(null), viewer = ref(null), confirmDialog = ref(null);
const gate = createKnowledgeRequestGate(), detailGate = createKnowledgeRequestGate();
let disposed = false, epoch = 0, timer, refreshInFlight;
function clear() { chosenIDs.value=new Set(); importOpen.value=false; cards.value = []; total.value = 0; draft.value = null; selected.value = null; versions.value = []; confirmation.value = null; }
function isCurrent(captured, revision) { return !disposed && captured === scope.value && revision === epoch; }
async function load(reset = false, { background = false } = {}) {
  if (disposed || !caps.value.view || background && (loading.value || busy.value || draft.value || selected.value || confirmation.value || importOpen.value)) return;
  if (reset) offset.value = 0;
  const ticket = gate.start(scope.value);
  if (!background) { loading.value = true; error.value = ''; }
  try {
    const data = await api.knowledgeCards({q: query.value, kind: kind.value, status: status.value, limit: 30, offset: offset.value,view:"summary"});
    if (!disposed && gate.accepts(ticket, scope.value)) { error.value=''; cards.value = knowledgeRows(data); total.value = data.total;if(caps.value.use&&cards.value.some(c=>c.origin_submission_id)){const choices=await api.knowledgeChoices();if(!disposed&&gate.accepts(ticket,scope.value))chosenIDs.value=new Set((choices.cards||[]).map(c=>c.id));} }
  } catch(e) { if (!disposed && gate.accepts(ticket, scope.value)) { error.value=e.message; if ([401,403].includes(e.status)) clear(); } }
  finally { if (!disposed && gate.accepts(ticket,scope.value)) loading.value=false; }
}
watch(scope, () => { epoch++; gate.invalidate(); detailGate.invalidate(); clear(); loading.value=false; busy.value=false; error.value=''; message.value=''; if (caps.value.view) load(true); });
async function showCard(card) {
 if(busy.value||!caps.value.view)return;
  const ticket = detailGate.start(scope.value); error.value='';
  try { const data=await api.knowledgeCardHistory(card.id); if (!disposed && detailGate.accepts(ticket,scope.value)) { versions.value=data.versions; selected.value=data.versions[0]; } }
  catch(e) { if (!disposed && detailGate.accepts(ticket,scope.value)) { error.value=e.message; if ([401,403].includes(e.status)) { clear(); await refreshSessionIdentity(); } } }
}
async function edit(card = null) {
 if(!caps.value.manage||busy.value)return;
 detailGate.invalidate();selected.value=null;confirmation.value=null;error.value='';
 if(!card){draft.value=toKnowledgeEditor();return;}
 const captured=scope.value,revision=epoch,ticket=detailGate.start(scope.value);busy.value=true;
 try{const [full]=await completeKnowledgeCards([card],api.knowledgeCard,()=>isCurrent(captured,revision)&&detailGate.accepts(ticket,scope.value)&&caps.value.manage);if(isCurrent(captured,revision)&&detailGate.accepts(ticket,scope.value)&&caps.value.manage)draft.value=toKnowledgeEditor(full);}
 catch(e){if(isCurrent(captured,revision)){error.value=e.message;if([401,403].includes(e.status)){clear();await refreshSessionIdentity();}}}
 finally{if(isCurrent(captured,revision))busy.value=false;}
}
async function mutate(action, success) {
  if (!caps.value.manage || busy.value) return;
  const captured=scope.value, revision=epoch; busy.value=true; error.value=''; message.value='';
  try { await refreshSessionIdentity(); if (!isCurrent(captured,revision)||!caps.value.manage) return; gate.invalidate(); detailGate.invalidate(); await action(); if (!isCurrent(captured,revision))return; draft.value=null; confirmation.value=null; selected.value=null; message.value=success; await load(); }
  catch(e) { if (isCurrent(captured,revision)) { error.value=e.message; if ([401,403].includes(e.status)) { clear(); await refreshSessionIdentity(); } } }
  finally { if (isCurrent(captured,revision)) busy.value=false; }
}
function save() { const value=fromKnowledgeEditor(draft.value); mutate(()=>api.saveKnowledgeCard(value),'已保存为草稿，人工核对后可启用'); }
function confirmAction(card, action) { error.value=''; confirmation.value={card, action}; }
function perform() { const {card,action}=confirmation.value; mutate(()=>action==='activate'?api.activateKnowledgeCard(card):api.archiveKnowledgeCard(card),action==='activate'?'知识卡已启用':'知识卡已归档，历史版本保留'); }
function closeEditor() { if (!busy.value) { draft.value=null; error.value=''; } }
watch([draft,selected,confirmation], async()=>{ await nextTick(); if (draft.value&&caps.value.manage&&!editor.value?.open)editor.value?.showModal(); if(selected.value&&!viewer.value?.open)viewer.value?.showModal(); if(confirmation.value&&caps.value.manage&&!confirmDialog.value?.open)confirmDialog.value?.showModal(); });
async function refresh(background = false) {
  const silent = background === true;
  if (disposed || silent && loading.value) return;
  if (refreshInFlight) return refreshInFlight;
  const captured = scope.value;
  const request = (async () => {
    try {
      await refreshSessionIdentity();
      if (disposed || captured !== scope.value) return;
      if (!draft.value&&!selected.value&&!confirmation.value&&!importOpen.value&&!busy.value) await load(false, { background: silent });
    } catch(e) { if(!disposed&&captured===scope.value)error.value=e.message; }
  })();
  refreshInFlight = request;
  try { await request; } finally { if (refreshInFlight===request) refreshInFlight=null; }
}
function imported(result){importOpen.value=false;message.value=`已导入 ${result.saved} 张新草稿，人工核对后可启用`;load(true);}
const focusRefresh=()=>refresh(true);
onMounted(()=>{ refresh(); window.addEventListener('focus',focusRefresh); timer=setInterval(()=>{if(!document.hidden)refresh(true);},30000); });
onBeforeUnmount(()=>{disposed=true;epoch++;gate.invalidate();detailGate.invalidate();clearInterval(timer);window.removeEventListener('focus',focusRefresh);});
async function chooseShared(card,value){if(busy.value||!caps.value.use)return;const captured=scope.value,previous=new Set(chosenIDs.value),ids=new Set(chosenIDs.value);if(value)ids.add(card.id);else ids.delete(card.id);chosenIDs.value=ids;busy.value=true;error.value='';try{await api.chooseKnowledgeCard({scope:'shared',id:card.id,selected:value});}catch(e){if(!disposed&&captured===scope.value){chosenIDs.value=previous;error.value=e.message;}}finally{if(!disposed&&captured===scope.value)busy.value=false;}}

</script>

<template>
<section class="panel kb-library">
  <div class="kb-heading"><div class="ui-help-heading"><h2>知识库</h2><HelpHint label="知识卡使用说明">通用命题规范启用后用于新单题和批量（每次按最近维护最多3张）；考点资料用于知识库工作流。编辑后需要重新启用；归档移出可用资料，已提交任务仍保留原版本。{{ !caps.manage ? '当前身份只读。' : '' }}</HelpHint></div><div v-if="caps.manage" class="kb-actions"><button class="ghost-button" :disabled="busy" @click="importOpen=true">批量导入</button><button class="primary-button" :disabled="busy" @click="edit()">新增知识卡</button></div></div>
  <form class="kb-filters" @submit.prevent="load(true)"><input v-model="query" aria-label="搜索知识卡" placeholder="搜索标题" maxlength="120"/><select v-model="kind" aria-label="知识卡类型"><option value="">全部类型</option><option v-for="(name,key) in cardKinds" :key="key" :value="key">{{name}}</option></select><select v-model="status" aria-label="知识卡状态"><option value="">全部状态</option><option v-for="(name,key) in cardStates" :key="key" :value="key">{{name}}</option></select><button class="ghost-button" :disabled="loading">查询</button></form>
  <p v-if="error&&!draft&&!confirmation" class="kb-error" role="alert">{{error}}</p><p v-if="message" class="kb-success" role="status">{{message}}</p>
  <p v-if="loading" role="status">加载中…</p><p v-else-if="!cards.length" class="kb-empty">暂无知识卡</p>
  <article v-for="card in cards" :key="card.id" class="kb-card"><div class="kb-card-top"><button class="kb-title" :disabled="busy" @click="showCard(card)">{{card.title}}</button><span>{{cardKindLabel(card)}}</span><span class="kb-state" :class="card.status">{{cardStates[card.status]}}</span><small>版本 {{card.revision}}</small></div><p class="kb-points">{{card.knowledge_points.map(p=>`${p.version_name || p.version_year} · ${p.topic}`).join('；') || (card.kind==='authoring_guideline'?'全局适用 · 直接生成与知识库工作流':'尚未绑定知识点')}}</p><div v-if="card.origin_submission_id&&caps.use" class="collection-actions"><label class="collection-check"><input type="checkbox" :aria-label="`用于出题 ${card.title}`" :checked="chosenIDs.has(card.id)" :disabled="busy||card.status!=='active'&&!chosenIDs.has(card.id)" @change="chooseShared(card,$event.target.checked)"/>用于我的出题</label></div><div class="kb-card-bottom"><small>{{(card.is_summary?card.source_excerpt:card.source)||'未填写来源'}}</small><div class="kb-actions"><button class="ghost-button" :disabled="busy" @click="showCard(card)">查看与历史</button><template v-if="caps.manage&&card.status!=='archived'"><button class="ghost-button" :disabled="busy" @click="edit(card)">编辑</button><button v-if="card.status==='draft'" class="ghost-button" :disabled="busy" @click="confirmAction(card,'activate')">启用</button><button class="kb-delete" :disabled="busy" @click="confirmAction(card,'archive')">归档</button></template></div></div></article>
  <div class="kb-pager"><span>共 {{total}} 张</span><button class="ghost-button" :disabled="offset===0||loading" @click="offset=Math.max(0,offset-30);load()">上一页</button><button class="ghost-button" :disabled="offset+30>=total||loading" @click="offset+=30;load()">下一页</button></div>
</section>
<Teleport to="body">
<KnowledgeImportDialog v-if="importOpen&&caps.manage" @close="importOpen=false" @saved="imported"/>
<dialog v-if="draft&&caps.manage" ref="editor" class="kb-dialog" aria-labelledby="kb-editor-title" @cancel.prevent="closeEditor">
<form @submit.prevent="save"><div class="kb-dialog-heading"><h2 id="kb-editor-title">{{draft.id?'编辑':'新增'}}知识卡</h2><HelpHint label="知识卡维护说明">保存为草稿。个人命题偏好请在“个人知识库”中维护，避免混入公共医学事实。</HelpHint><button type="button" class="kb-close" aria-label="关闭编辑" :disabled="busy" @click="closeEditor">×</button></div>
<KnowledgeCardEditor :model-value="draft" :disabled="busy" :allow-global="true" :allow-expression="!!draft.origin_submission_id"/>
<p v-if="error" class="kb-error" role="alert">{{error}}</p><div class="kb-dialog-actions"><button type="button" class="ghost-button" :disabled="busy" @click="closeEditor">取消</button><button class="primary-button" :disabled="busy">{{busy?'保存中…':'保存草稿'}}</button></div></form></dialog>
<dialog v-if="selected&&caps.view" ref="viewer" class="kb-dialog" aria-labelledby="kb-view-title" @cancel.prevent="selected=null"><div class="kb-dialog-heading"><h2 id="kb-view-title">{{selected.title}}</h2><HelpHint label="知识卡历史说明">展示最近100个历史版本。</HelpHint><button class="kb-close" aria-label="关闭详情" @click="selected=null">×</button></div><label>查看版本<select :value="selected.revision" @change="selected=versions.find(v=>v.revision===Number($event.target.value))"><option v-for="v in versions" :key="v.revision" :value="v.revision">版本 {{v.revision}} · {{cardStates[v.status]}} · {{formatDateTime(v.updated_at)}}</option></select></label><p v-if="selected.kind==='authoring_guideline'">全局命题规范</p><p v-else>{{cardKinds[selected.kind]}} · {{selected.knowledge_points.map(p=>`${p.version_name} / ${p.topic}`).join('；')}}</p><section v-if="selected.kind!=='authoring_guideline'" v-for="field in dimensionFields" :key="field.key" class="kb-section"><h3>{{field.name}}</h3><p class="kb-text">{{selected.content[field.key]?.join('\n')||'未填写'}}</p></section><section v-if="selected.kind!=='authoring_guideline'" class="kb-section"><h3>事实</h3><article v-for="f in selected.content.facts" :key="f.id" class="kb-fact"><p>{{f.text}}</p><small>前提：{{f.applies_when||'未注明'}} · 来源：{{f.source||selected.source||'未注明'}}</small></article></section><section v-if="selected.content.reference" class="kb-section"><h3>真题参考 · {{selected.content.reference.year}}</h3><p class="kb-text">{{selected.content.reference.stem}}</p><p v-for="o in selected.content.reference.options" :key="o.label">{{o.label}}. {{o.text}}</p><p>参考答案：{{selected.content.reference.answer||'未填写'}}</p><p class="kb-text">{{selected.content.reference.explanation}}</p></section><section class="kb-section"><h3>自定义内容</h3><p class="kb-text">{{selected.content.custom_text||'未填写'}}</p><template v-if="selected.content.expression"><h3>表达要求</h3><p class="kb-text">{{selected.content.expression.instructions||'未填写'}}</p><h3>解析附言</h3><p class="kb-text">{{selected.content.expression.explanation_note||'未填写'}}</p></template><h3>来源与版本</h3><p class="kb-text">{{selected.source||'未填写'}}</p><h3>维护备注</h3><p class="kb-text">{{selected.note||'未填写'}}</p></section></dialog>
<dialog v-if="confirmation&&caps.manage" ref="confirmDialog" class="kb-dialog kb-confirm" aria-labelledby="kb-confirm-title" @cancel.prevent="!busy&&(confirmation=null)"><h2 id="kb-confirm-title">{{confirmation.action==='activate'?'启用':'归档'}}知识卡</h2><p>「{{confirmation.card.title}}」</p><p>{{confirmation.action==='activate'?(confirmation.card.kind==='authoring_guideline'?'请核对通用命题方法及来源。此规范不绑定考点，启用后将用于所有人的新单题和批量任务；已提交任务不变。':'请核对事实、适用条件与来源。启用后，该版本可用于生成任务。'):'归档后不再用于新任务；历史版本和已经提交的任务保留。'}}</p><p v-if="error" class="kb-error" role="alert">{{error}}</p><div class="kb-dialog-actions"><button class="ghost-button" :disabled="busy" @click="confirmation=null">取消</button><button class="primary-button" :disabled="busy" @click="perform">{{busy?'处理中…':'确认'}}</button></div></dialog>
</Teleport>
</template>
<style scoped>
.kb-library{max-width:1240px;padding:24px}.kb-heading,.kb-card-top,.kb-card-bottom,.kb-actions,.kb-dialog-heading,.kb-pager{display:flex;align-items:center;gap:12px;flex-wrap:wrap}.kb-heading,.kb-card-bottom,.kb-dialog-heading>.ui-help-hint{margin-left:auto;flex-shrink:0}.kb-dialog-heading{justify-content:space-between}.kb-heading h2,.kb-dialog h2{font-size:19px;margin:0;color:#263e59}.kb-heading p,.kb-hint,small,.kb-points{color:#748396;font-size:13px;line-height:1.7}.kb-filters{display:flex;gap:10px;flex-wrap:wrap;margin:20px 0}.kb-filters input{flex:1;min-width:160px}.kb-filters select{max-width:290px}.kb-library input,.kb-library select,.kb-dialog input,.kb-dialog select,.kb-dialog textarea{border:1px solid #d6deea;border-radius:6px;background:#fff;padding:9px 11px;color:#354c68;font:inherit;box-sizing:border-box;max-width:100%}.kb-hint{margin:15px 0}.kb-card{padding:20px 0;border-top:1px solid #e8edf3}.kb-title{border:0;background:transparent;padding:0;color:#234f78;font:inherit;font-weight:600;cursor:pointer;text-align:left;overflow-wrap:anywhere}.kb-state{padding:3px 8px;border-radius:4px;font-size:12px;background:#f2f5f8;color:#68788d}.kb-state.active{background:#edf9f1;color:#166534}.kb-state.draft{background:#fff6e6;color:#976116}.kb-card-top>span{font-size:12px}.kb-card-bottom>small{flex:1;min-width:160px;overflow-wrap:anywhere}.kb-actions{gap:8px}.kb-actions button{font-size:12px;padding:5px 10px}.kb-delete,.kb-close{border:0;background:transparent;color:#b34848;cursor:pointer}.kb-close{font-size:26px;color:#748396}.kb-pager{justify-content:flex-end;border-top:1px solid #e8edf3;padding-top:18px;font-size:13px}.kb-empty{padding:40px 0;text-align:center;color:#748396}.kb-error{color:#b91c1c;font-size:14px;white-space:pre-wrap;overflow-wrap:anywhere}.kb-success{color:#166534;font-size:14px}.kb-dialog{position:fixed;border:0;border-radius:10px;padding:26px;width:min(980px,calc(100vw - 32px));max-height:calc(100dvh - 40px);box-sizing:border-box;overflow:auto;background:white;color:#354c68;box-shadow:0 16px 50px #0f172a30}.kb-dialog::backdrop{background:rgba(15,23,42,.38)}.kb-dialog fieldset{border:0;padding:0;margin:0;min-width:0}.kb-dialog label{display:flex;flex-direction:column;gap:7px;font-size:13px;margin:14px 0}.kb-dialog textarea{resize:vertical;line-height:1.7;width:100%}.kb-dialog input,.kb-dialog select{width:100%}.kb-grid{display:grid;grid-template-columns:1fr 1fr;gap:18px}.kb-grid>*{min-width:0}.kb-section{border-top:1px solid #e8edf3;margin-top:20px;padding-top:16px}.kb-section h3{font-size:15px;margin:0;color:#263e59}.kb-dialog-actions{display:flex;justify-content:flex-end;gap:10px;margin-top:24px}.kb-fact{background:#f8fafc;padding:12px 16px;border:1px solid #e8edf3;border-radius:6px;margin-top:12px;overflow-wrap:anywhere}.kb-fact p{line-height:1.8;margin-top:0}.kb-text{white-space:pre-wrap;overflow-wrap:anywhere;line-height:1.8;font-size:14px}.kb-selected{display:flex;flex-wrap:wrap;gap:8px}.kb-selected span{background:#eef5fc;padding:6px 9px;border-radius:5px;font-size:12px}.kb-selected button{border:0;background:none;cursor:pointer}.kb-results{max-height:200px;overflow:auto;display:flex;flex-direction:column;align-items:stretch;margin-top:10px;border:1px solid #e8edf3}.kb-results button{text-align:left;background:#fff;border:0;border-bottom:1px solid #e8edf3;padding:9px 12px;color:#354c68;cursor:pointer}.kb-results small{padding:8px}.kb-confirm{max-width:480px}.kb-confirm p{line-height:1.8;overflow-wrap:anywhere}button:disabled{opacity:.5;cursor:default}@media(max-width:700px){.kb-grid{grid-template-columns:1fr;gap:0}.kb-library{padding:18px}.kb-dialog{padding:20px}.kb-filters select{flex:1;min-width:130px}.kb-heading{align-items:flex-start}.kb-card-bottom{align-items:flex-start}.kb-actions{width:100%}}
</style>
