<script setup>
import {collectionCapabilities} from "../knowledgeCollection.js";
import KnowledgeCardContent from "./KnowledgeCardContent.vue";
import HelpHint from "./HelpHint.vue";
import { computed, ref, watch, onBeforeUnmount } from 'vue';
import { api } from '../api.js';
import { currentUser,getToken } from '../auth.js';
import { refreshSessionIdentity } from '../sessionIdentity.js';
import { knowledgeCapabilities,knowledgeScope,createKnowledgeRequestGate } from '../knowledgeBaseAccess.js';
import { sectionName } from '../presentation.js';
const props=defineProps({question:{type:Object,default:null}});
const caps=computed(()=>({...knowledgeCapabilities(currentUser.value?.permissions),view:knowledgeCapabilities(currentUser.value?.permissions).view||collectionCapabilities(currentUser.value?.permissions).view}));const scope=computed(()=>knowledgeScope(currentUser.value,getToken()));
const record=ref(null),error=ref(''),busy=ref(false),open=ref(false);const gate=createKnowledgeRequestGate();let disposed=false;
const key=()=>`${scope.value}:${props.question?.id}`;
watch([scope,()=>props.question?.id],()=>{gate.invalidate();record.value=null;error.value='';busy.value=false;open.value=false;});
async function load(){if(!caps.value.view||!props.question?.workflow_version||busy.value)return;open.value=!open.value;if(!open.value||record.value)return;const ticket=gate.start(key());busy.value=true;error.value="";try{await refreshSessionIdentity();if(disposed||!gate.accepts(ticket,key())||!caps.value.view)return;const data=await api.questionGenerationContext(props.question.id);if(!disposed&&gate.accepts(ticket,key()))record.value=data;}catch(e){if(!disposed&&gate.accepts(ticket,key()))error.value=e.message;}finally{if(!disposed&&gate.accepts(ticket,key()))busy.value=false;}}
const facts=computed(()=>{if(!record.value)return[];const refs=new Set(record.value.basis.fact_refs);return record.value.bundle.cards.flatMap(c=>(c.content.facts||[]).filter(f=>refs.has(`${c.id}@${c.revision}:${f.id}`)).map(f=>({...f,ref:`${c.id}@${c.revision}:${f.id}`,card:c.title,revision:c.revision,source:f.source||c.source})));});
const casePlan=computed(()=>['knowledge-v2','knowledge-v2.1','knowledge-v3','knowledge-v4'].includes(record.value?.workflow_version)?record.value.basis?.case_plan:null);
const presentationFeedback=computed(()=>['knowledge-v3','knowledge-v4'].includes(record.value?.workflow_version)?record.value.presentation_feedback:null);
const sections={chief_complaint:'主诉',present_illness:'现病史',past_history:'相关既往史',vitals:'生命征',physical_findings:'体格信息',lab_results:'实验室',imaging_results:'影像',other:'其他相关信息'};
const caseFacts=computed(()=>Array.isArray(casePlan.value?.facts)?casePlan.value.facts:[]);
const optionEvidence=computed(()=>{const entries=Array.isArray(casePlan.value?.option_evidence)?casePlan.value.option_evidence:[];return ['A','B','C','D','E'].flatMap(label=>entries.filter(e=>e.label===label));});
const caseText=e=>(e.case_refs||[]).map(id=>id==='person'?casePlan.value.person:caseFacts.value.find(f=>f.id===id)?.text||`未找到病例事实 ${id}`).join('；');
const factText=e=>(e.fact_refs||[]).map(id=>facts.value.find(f=>f.ref===id)?.text||`未找到知识事实 ${id}`).join('；');
onBeforeUnmount(()=>{disposed=true;gate.invalidate();});
</script>
<template><section v-if="question?.workflow_version&&caps.view" class="knowledge-provenance"><button class="ghost-button" type="button" :aria-expanded="open" @click="load">{{open?'收起':'查看'}}生成依据</button><div v-if="open"><p v-if="busy">读取中…</p><p v-if="error" class="provenance-error" role="alert">{{error}}</p><template v-if="record"><h4>考查目标：{{record.target.target}}</h4><p>适用条件：{{record.basis.condition_notes}}</p><p>答案依据：{{record.basis.answer_support}}</p>
<details v-if="record.bundle?.cards?.length"><summary>采用的知识卡（{{record.bundle.cards.length}}）</summary><KnowledgeCardContent v-for="card in record.bundle.cards" :key="card.id" :card="card"/></details>
<template v-if="casePlan">
  <div class="ui-help-heading"><h4>首次生成的病例与选项依据</h4><HelpHint label="首次生成依据说明">以下为生成器自述及首次成题的资料。人工修订后仍保留原记录，不能当作对当前版本的重新检查。</HelpHint></div>
  <article v-if="presentationFeedback" class="presentation-feedback"><strong>本次分组顺序（首次生成）</strong><p>{{presentationFeedback.state==='applied'?'分组顺序已按本次设置呈现':presentationFeedback.state==='not_applicable'?'本题只有一类分组信息，无需比较顺序':'分组顺序待核对'}}</p><p>{{(presentationFeedback.section_order||[]).map(sectionName).join(' → ')}}</p><small v-if="presentationFeedback.unused_sections?.length">本题未使用：{{presentationFeedback.unused_sections.map(sectionName).join('、')}}。排序不增加内容。</small><HelpHint label="病例分组说明">仅按生成器标注的分组排列已有信息；内容、分组含义及其他文字要求仍待核对。</HelpHint></article>
  <p>{{casePlan.person}}</p>
  <ol class="case-facts"><li v-for="fact in caseFacts" :key="fact.id"><strong>{{sections[fact.section]||'病例信息'}}：</strong>{{fact.text}}</li></ol>
  <p>设问：{{casePlan.lead_in}}</p>
  <article v-for="item in optionEvidence" :key="item.label" class="option-evidence"><strong>{{item.label}} 项<span v-if="item.label===casePlan.answer"> · 首次生成答案</span></strong><p>{{item.reason}}</p><p v-if="item.limitations">证据边界：{{item.limitations}}</p><small>引用病例：{{caseText(item)}}</small><small v-if="item.fact_refs?.length">引用知识：{{factText(item)}}</small></article>
  <template v-if="casePlan.preference_notes?.length"><h4>本次偏好说明（生成器自述）</h4><p v-for="(note,index) in casePlan.preference_notes" :key="index">{{note}}</p></template>
</template>
<article v-for="fact in facts" :key="fact.ref"><strong>{{fact.card}} · 版本 {{fact.revision}}</strong><p>{{fact.text}}</p><small>前提：{{fact.applies_when||'未单独注明'}}<br/>来源：{{fact.source||'未填写'}}</small></article><HelpHint label="生成依据适用范围">这里保留首次生成时的资料与生成器说明。人工修改题目后，仍须由专家核对；存在引用不代表医学判断必然正确。</HelpHint></template></div></section></template>
<style scoped>.knowledge-provenance{margin:16px 0;padding-top:16px;border-top:1px solid #e8edf3;color:#475c73;font-size:13px;overflow-wrap:anywhere}.knowledge-provenance h4{font-size:14px;line-height:1.8}.knowledge-provenance p{white-space:pre-wrap;line-height:1.8}.knowledge-provenance article{padding:12px;background:#f8fafc;margin:12px 0;border-radius:6px}.knowledge-provenance small{color:#748396;line-height:1.8;display:block}.provenance-error{color:#b91c1c}</style>
