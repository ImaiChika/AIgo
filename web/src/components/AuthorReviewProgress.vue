<script setup>
import HelpHint from "./HelpHint.vue";
import {computed,onBeforeUnmount,ref,watch} from 'vue';
import {api} from '../api.js';
import {currentUser,getToken,hasPerm} from '../auth.js';
import {libraryExpectation} from '../libraryExpectation.js';
import {reviewCompletionLabel} from '../reviewCompletion.js';
import {useLiveRefresh} from '../useLiveRefresh.js';
import CompactPager from './CompactPager.vue';

const emit=defineEmits(['changed']);
const allowed=computed(()=>hasPerm('question:view')&&hasPerm('review:submit'));
const session=computed(()=>JSON.stringify([getToken(),currentUser.value?.id,currentUser.value?.role,[...(currentUser.value?.permissions||[])].sort()]));
const open=ref(false),rows=ref([]),total=ref(0),page=ref(1),status=ref('reviewing'),loading=ref(false),selected=ref(null),task=ref(null),taskLoading=ref(false),canceling=ref(false),notice=ref('');
const pageSize=20,pageCount=computed(()=>Math.max(1,Math.ceil(total.value/pageSize)));
let disposed=false,listTicket=0,taskTicket=0;
const sameQuestion=(a,b)=>a?.id===b?.id&&a?.version===b?.version&&a?.review_revision===b?.review_revision&&a?.library_revision===b?.library_revision;
const noVotes=computed(()=>Array.isArray(task.value?.round_results)&&task.value.round_results.every(round=>!(round.reviews||[]).length&&!(round.approved_count||round.rejected_count||round.revision_count)));
const cancelable=computed(()=>allowed.value&&selected.value?.owner_id===currentUser.value?.id&&selected.value?.status==='reviewing'&&task.value?.status==='reviewing'&&task.value?.current_round===1&&task.value?.question_version===selected.value?.version&&task.value?.can_cancel_submission===true&&noVotes.value&&!taskLoading.value&&!loading.value&&!canceling.value);
async function selectQuestion(q){
 if(canceling.value)return;
 const ticket=++taskTicket,identity=session.value;selected.value=q;task.value=null;taskLoading.value=true;
 try{const data=await api.getTaskByQuestion(q.id);if(!disposed&&ticket===taskTicket&&identity===session.value&&allowed.value&&sameQuestion(q,selected.value))task.value=data;}
 catch(error){if(!disposed&&ticket===taskTicket&&identity===session.value&&!error.staleSession)notice.value='读取本人审核进度失败：'+error.message;}
 finally{if(ticket===taskTicket&&identity===session.value)taskLoading.value=false;}
}
async function load(silent=false){
 if(!open.value||!allowed.value||canceling.value)return;
 const ticket=++listTicket,identity=session.value;if(!silent)loading.value=true;
 try{
  const data=await api.searchQuestions('',status.value,page.value,pageSize,'',[],'','','working',false,'personal','unfiled');
  if(disposed||ticket!==listTicket||identity!==session.value||!allowed.value)return;
  rows.value=(data.questions||[]).filter(q=>q.owner_id===currentUser.value?.id);total.value=Number(data.total)||0;
  if(page.value>pageCount.value){page.value=pageCount.value;return load(silent)}
  if(selected.value){const fresh=rows.value.find(q=>q.id===selected.value.id);if(!fresh){selected.value=null;task.value=null;++taskTicket;}else{await selectQuestion(fresh)}}
 }catch(error){if(!disposed&&ticket===listTicket&&identity===session.value&&!error.staleSession)notice.value='读取本人审核题目失败：'+error.message;}
 finally{if(ticket===listTicket&&identity===session.value)loading.value=false;}
}
function toggled(event){open.value=event.target.open;if(open.value)load();}
function changeStatus(){page.value=1;selected.value=null;task.value=null;notice.value='';load();}
function goPage(next){if(canceling.value||loading.value)return;page.value=next;selected.value=null;task.value=null;load();}
async function cancel(){
 if(!cancelable.value)return;
 if(!confirm('取消这次尚无投票的可选审核？原来已确认的题目恢复个人确认，其余题回到整理；不会重复首检。'))return;
 const identity=session.value,{question_id,...expected}=libraryExpectation(selected.value),payload={task_id:task.value.id,...expected};canceling.value=true;notice.value='';
 try{
  const data=await api.cancelReview(payload);
  if(disposed||identity!==session.value||!allowed.value)return;
  selected.value=null;task.value=null;
  notice.value=data.question?.library_state==='active'?'已取消审核并恢复原个人确认，可到普通题库查看或导出。':'已取消审核，题目回到整理，可直接确认入库。';
  emit('changed',data);
 }catch(error){if(!disposed&&identity===session.value&&!error.staleSession)notice.value='取消未完成：'+error.message;}
 finally{if(identity===session.value){canceling.value=false;if(!disposed)await load();}}
}
watch(session,()=>{++listTicket;++taskTicket;rows.value=[];total.value=0;page.value=1;selected.value=null;task.value=null;notice.value='';canceling.value=false;taskLoading.value=false;loading.value=false;if(allowed.value&&open.value)load()},{flush:'sync'});
useLiveRefresh(()=>{if(open.value&&!canceling.value)return load(true)});
onBeforeUnmount(()=>{disposed=true;++listTicket;++taskTicket});
</script>

<template>
 <details v-if="allowed" class="panel author-review-progress" @toggle="toggled">
  <summary>查看本人可选审核进度</summary>
  <HelpHint label="可选审核进度说明">这里仅管理主动送审的题目。正常出题、本人确认和导出无需等待人工审核；取消仅适用于首轮尚无任何投票的任务。</HelpHint>
  <div class="author-review-tools"><label>审核状态<select v-model="status" aria-label="本人审核状态" :disabled="canceling" @change="changeStatus"><option value="reviewing">审核中</option><option value="conflict">待最终决断</option></select></label><button class="ghost-button" type="button" :disabled="loading||canceling" @click="load()">刷新进度</button></div>
  <p v-if="notice" role="status">{{notice}}</p>
  <p v-if="loading">读取中…</p>
  <p v-else-if="!rows.length">此状态暂无本人审核题目。</p>
  <ul v-else class="author-review-list"><li v-for="q in rows" :key="q.id"><button type="button" class="author-review-row" :disabled="canceling" @click="selectQuestion(q)">{{q.clinical_stem}} · v{{q.version}}</button></li></ul>
  <CompactPager v-if="total" :page="page" :total-pages="pageCount" :disabled="loading||canceling" @change="goPage" />
  <section v-if="selected" class="author-review-task"><p v-if="taskLoading">读取本题任务…</p><template v-else-if="task"><strong>{{task.flow_name||'本人可选审核'}} · {{reviewCompletionLabel(task)}}</strong><p>当前第 {{task.current_round}} 轮 · 审核版本 v{{task.question_version}}</p><button v-if="cancelable" class="ghost-button" type="button" @click="cancel">取消本次可选审核</button><p v-else-if="!canceling" class="author-review-note">已有投票、已进入后续轮次或状态变化时不能直接取消，请按当前流程处理。</p></template></section>
 </details>
</template>

<style scoped>
.author-review-progress{margin-top:16px;min-width:0}.author-review-progress summary{cursor:pointer;font-weight:600;color:#48637d}.author-review-note{font-size:12px;line-height:1.8;color:#718397}.author-review-tools{display:flex;gap:10px;align-items:center;flex-wrap:wrap;margin:12px 0}.author-review-tools label{font-size:12px;display:flex;gap:8px;align-items:center}.author-review-tools select{font:inherit;padding:7px;border:1px solid #dce6ef;border-radius:5px;background:#fff}.author-review-list{list-style:none;padding:0;display:grid;gap:6px}.author-review-row{display:block;width:100%;text-align:left;white-space:normal;overflow-wrap:anywhere;border:1px solid #dce6ef;border-radius:5px;padding:10px;background:#fff;color:#354b62;font:inherit;cursor:pointer}.author-review-task{padding:12px;background:#f6faff;border:1px solid #dce6ef;border-radius:6px;margin-top:12px;overflow-wrap:anywhere}.author-review-task strong{font-size:13px}.author-review-task p{font-size:12px;line-height:1.8}
</style>
