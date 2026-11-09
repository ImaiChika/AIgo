<script setup>
import {formatDateTime} from "../dateTime.js";
import HelpHint from "./HelpHint.vue";
import {computed,ref,watch,onBeforeUnmount} from 'vue';
import {api} from '../api.js';
import {currentUser,getToken} from '../auth.js';
import {refreshSessionIdentity} from '../sessionIdentity.js';
import {knowledgeScope,createKnowledgeRequestGate} from '../knowledgeBaseAccess.js';
const props=defineProps({kind:{type:String,default:'single'},taskId:{type:String,default:''},ownerId:{type:String,default:''}});
const open=ref(false),busy=ref(false),error=ref(''),page=ref(null),cursor=ref(''),previous=ref([]);
const scope=computed(()=>knowledgeScope(currentUser.value,getToken()));
const allowed=computed(()=>(!props.ownerId||props.ownerId===currentUser.value?.id)&&(currentUser.value?.permissions||[]).includes(props.kind==='batch'?'batch:run':'question:generate'));
const gate=createKnowledgeRequestGate();let disposed=false;
const key=()=>`${scope.value}:${props.kind}:${props.taskId}:${cursor.value}`;
watch([scope,()=>props.kind,()=>props.taskId],()=>{gate.invalidate();page.value=null;open.value=false;busy.value=false;error.value='';cursor.value='';previous.value=[];});
async function load(){
 if(!allowed.value||!props.taskId||busy.value)return;
 const ticket=gate.start(key());busy.value=true;error.value='';
 try{await refreshSessionIdentity();if(disposed||!gate.accepts(ticket,key())||!allowed.value)return;const data=await api.workflowDiagnostics(props.kind,props.taskId,cursor.value);if(!disposed&&gate.accepts(ticket,key()))page.value=data;}
 catch(e){if(!disposed&&gate.accepts(ticket,key())){page.value=null;error.value=e.message;}}
 finally{if(!disposed&&gate.accepts(ticket,key()))busy.value=false;}
}
function toggle(){open.value=!open.value;if(!open.value){gate.invalidate();busy.value=false;page.value=null;return;}page.value=null;load();}
function next(){if(busy.value||!page.value?.next_cursor)return;previous.value.push(cursor.value);cursor.value=page.value.next_cursor;page.value=null;load();}
function back(){if(busy.value||!previous.value.length)return;cursor.value=previous.value.pop();page.value=null;load();}
const labels={prepared:'准备完成',written:'已受理写入',returned:'已返回',parsed:'解析通过',validated:'结构核对通过',rendered:'呈现完成',committed:'已提交首次检查',checkpointed:'状态已保存',accepted:'已受理',failed:'本步未完成',generation_failed:'本次生成未完成',submission_failed:'未受理',handoff_failed:'提交未完成',handoff_not_attempted:'尚未交接',not_applicable:'本次无需执行',incomplete:'记录未完成',started:'尚无结束记录'};
const label=value=>labels[value]||'记录待核对';
onBeforeUnmount(()=>{disposed=true;gate.invalidate();});
</script>
<template>
<section v-if="taskId&&allowed" class="workflow-diagnostics">
 <div class="ui-help-heading"><button class="ghost-button" type="button" :aria-expanded="open" @click="toggle">{{open?'收起':'查看'}}执行记录</button><HelpHint label="执行记录说明">按每次实际尝试保存。记录可能尚未写入或缺失；提交首次检查不代表题目已通过。原进度和检查结果仍为准。</HelpHint></div>
 <div v-if="open" class="diagnostic-content">

  <button class="ghost-button" :disabled="busy" @click="load">{{busy?'读取中…':'刷新记录'}}</button>
  <p v-if="error" class="diagnostic-error" role="alert">{{error}}</p>
  <p v-else-if="page&&!page.entries?.length">暂无已保存的阶段记录。执行可能仍在进行，旧任务或记录缺失也会出现此情况；这不表示出题失败。</p>
  <article v-for="entry in page?.entries||[]" :key="entry.id" class="diagnostic-entry">
   <h4>{{entry.phase==='submission'?'资料准备与任务受理':entry.unit?`第 ${entry.unit} 单元 · 第 ${entry.attempt} 次尝试`:`生成 · 第 ${entry.attempt} 次尝试`}} <span>{{label(entry.outcome)}}</span></h4>
   <small>{{formatDateTime(entry.at)}} · 本段记录耗时 {{entry.duration_ms}} ms</small>
   <p v-if="!entry.complete" class="diagnostic-hint">这份记录不完整，未记录的步骤仍待核对。</p>
   <ol><li v-for="(step,i) in entry.steps" :key="i"><span>{{step.name}}<small v-if="step.item>=0"> · 本次第 {{step.item+1}} 题</small></span><strong>{{label(step.status)}}</strong><small>{{step.duration_ms}} ms</small><p v-if="step.issue" class="diagnostic-hint">{{step.issue}}</p></li></ol>
   <p v-if="entry.call?.observed" class="diagnostic-hint">本次观察到搜索 {{entry.call.search_calls}} 次、网页抓取 {{entry.call.extractor_calls}} 次<span v-if="entry.call.extractor_failure_notices">，其中有 {{entry.call.extractor_failure_notices}} 次抓取失败提示</span>。工具由模型按需选择。</p>
   <HelpHint v-else-if="entry.phase==='generation'" label="工具记录说明">本份记录没有完整的工具使用证据，不据此判定生成成败。</HelpHint>
  </article>
  <nav v-if="page||previous.length" class="diagnostic-pager"><button class="ghost-button" :disabled="busy||!previous.length" @click="back">上一页</button><span>每页最多 10 份记录</span><button class="ghost-button" :disabled="busy||!page?.next_cursor" @click="next">下一页</button></nav>
 </div>
</section>
</template>
<style scoped>
.workflow-diagnostics{margin:14px 0;font-size:13px;color:#475c73;min-width:0}.diagnostic-content{padding:14px;border:1px solid #e5ebf2;border-radius:7px;margin-top:10px}.diagnostic-hint{color:#748396;line-height:1.7;overflow-wrap:anywhere}.diagnostic-error{color:#b91c1c;overflow-wrap:anywhere}.diagnostic-entry{border-top:1px solid #e5ebf2;padding-top:12px;margin-top:14px;min-width:0}.diagnostic-entry h4{font-size:14px;line-height:1.7;margin:0 0 5px}.diagnostic-entry h4 span{font-weight:400;margin-left:8px}.diagnostic-entry small{color:#748396}.diagnostic-entry ol{padding-left:22px}.diagnostic-entry li{display:grid;grid-template-columns:minmax(0,1fr) auto auto;gap:6px 12px;margin:10px 0;line-height:1.7}.diagnostic-entry li p{grid-column:1/-1;margin:0}.diagnostic-pager{display:flex;justify-content:flex-end;align-items:center;gap:10px;flex-wrap:wrap;margin-top:15px}@media(max-width:600px){.diagnostic-entry li{grid-template-columns:minmax(0,1fr) auto}.diagnostic-entry li>small{grid-column:1/-1}.diagnostic-content{padding:10px}.diagnostic-pager{justify-content:flex-start}}
</style>
