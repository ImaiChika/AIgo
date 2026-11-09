<script setup>
import {computed,ref,watch,onBeforeUnmount} from 'vue';
import {useRoute,useRouter,isNavigationFailure} from 'vue-router';
import {api} from '../api.js';
import {currentUser,getToken,hasPerm} from '../auth.js';
import {sessionScope} from '../sessionAccess.js';
import {journeyTask,journeyProgress,rememberJourney,restoreJourney,clearJourney,journeyParams,routeJourney,journeyNext} from '../authoringJourney.js';
import {useLiveRefresh} from '../useLiveRefresh.js';
const route=useRoute(),router=useRouter();
const scope=computed(()=>sessionScope(currentUser.value,getToken()));
const eligible=computed(()=>hasPerm('question:generate')||hasPerm('batch:run')||hasPerm('question:view')&&hasPerm('question:confirm'));
const main=computed(()=>eligible.value&&['/my','/generate','/generate/batch','/new-questions','/bank'].includes(route.path)&&route.query.scope!=='global');
const open=ref(false),error=ref(''),busy=ref(false);let epoch=0,disposed=false,blocked=false;
const c=computed(()=>journeyProgress.value?.counts||{}),next=computed(()=>journeyNext(journeyProgress.value));
const active=computed(()=>route.path==='/new-questions'?3:route.path==='/bank'?4:route.path.startsWith('/generate')?(journeyTask.value?2:1):1);
const steps=computed(()=>[
 {n:1,title:'选择考点',hint:'选择临床考点与题数',done:!!journeyProgress.value},
 {n:2,title:'AI 出题与检查',hint:!journeyTask.value?'需有出题任务':journeyProgress.value?`${c.value.checking||0} 题检查中${journeyProgress.value.generating?' · 仍在生成':''}`:'生成后自动检查',done:!!journeyProgress.value&&!c.value.checking&&!journeyProgress.value.generating},
 {n:3,title:'题目整理与确认',hint:journeyProgress.value?`${c.value.arrange||0} 题待确认`:'查看、修改并确认',done:!!journeyProgress.value&&!c.value.arrange&&!!c.value.confirmed},
 {n:4,title:'个人题库与导出',hint:journeyProgress.value?`${c.value.confirmed||0} 题已确认`:'选择 Excel / Word',done:false},
]);
const genPath=computed(()=>hasPerm('question:generate')?'/generate':hasPerm('batch:run')?'/generate/batch':'/new-questions');
const progressLink=computed(()=>journeyTask.value?.kind==='batch'?{path:'/generate/batch',query:{job_id:journeyTask.value.id}}:{path:'/generate',query:journeyTask.value?{run_id:journeyTask.value.id}:{}});
const primary=computed(()=>{
 if(!journeyTask.value)return {label:'开始命题',to:{path:genPath.value,hash:'#authoring-points'},disabled:!hasPerm('question:generate')&&!hasPerm('batch:run')};
 if(error.value)return {label:'重新读取进度',retry:true};
 if(!hasPerm('question:view'))return {label:'查看本次生成进度',to:progressLink.value};
 if(!journeyProgress.value)return {label:'正在核对本次进度…',disabled:true};
 if(c.value.arrange>0)return {label:`去题目整理`,to:{path:'/new-questions'},disabled:!hasPerm('question:confirm')||!hasPerm('question:view')};
 if(c.value.confirmed>0)return {label:hasPerm('question:download')?`去个人题库导出`:`去个人题库`,to:{path:'/bank',query:{scope:'personal'}},disabled:!hasPerm('question:view')};
 if(journeyProgress.value.task.needs_import)return {label:'查看批量结果并导入首检',to:progressLink.value,disabled:!hasPerm('batch:run')};
 if(c.value.checking||journeyProgress.value.generating)return {label:'查看本次生成进度',to:progressLink.value,disabled:journeyTask.value.kind==='single'?!hasPerm('question:generate'):!hasPerm('batch:run')};
 return {label:'继续出题',to:{path:genPath.value,hash:'#authoring-points'},disabled:!hasPerm('question:generate')&&!hasPerm('batch:run')};
});
async function refresh(force=false){
 if(disposed||busy.value||!main.value||!journeyTask.value||!hasPerm('question:view')||blocked&&!force)return;
 const ticket=++epoch,s=scope.value,t=journeyTask.value;busy.value=true;
 try{const data=await api.authoringJourney(journeyParams(t));if(disposed||ticket!==epoch||s!==scope.value||t!==journeyTask.value)return;if(data?.task?.id!==t.id||data.task.kind!==t.kind||!data.counts)throw new Error('任务进度不匹配');journeyProgress.value=data;error.value='';blocked=false;}
 catch(e){if(!disposed&&ticket===epoch&&s===scope.value){journeyProgress.value=null;error.value='暂时无法核对本次进度，可通过左侧继续操作。';blocked=true;}}
 finally{if(!disposed&&ticket===epoch)busy.value=false;}
}
watch(scope,()=>{epoch++;busy.value=false;error.value='';blocked=false;restoreJourney(currentUser.value);const t=routeJourney(route.query);if(t)rememberJourney(currentUser.value,t);refresh();},{immediate:true,flush:'sync'});
watch(()=>[route.path,route.query.task_kind,route.query.task_id],()=>{const t=routeJourney(route.query);if(t)rememberJourney(currentUser.value,t);blocked=false;refresh();});
watch(journeyTask,()=>{epoch++;busy.value=false;error.value='';blocked=false;refresh();});
useLiveRefresh(()=>refresh());
async function go(to){if(to)await router.push(to);}
async function act(){if(primary.value.retry)await refresh(true);else if(!primary.value.disabled)await go(primary.value.to);}
async function leaveTask(){if(!route.query.task_kind&&!route.query.task_id){clearJourney(currentUser.value);return;}const q={...route.query};delete q.task_kind;delete q.task_id;const failure=await router.push({path:route.path,query:q});if(!isNavigationFailure(failure))clearJourney(currentUser.value);}
function stepTarget(n){if(n===1)return {path:genPath.value,hash:'#authoring-points'};if(n===2)return progressLink.value;return {path:n===3?'/new-questions':'/bank',query:n===4?{scope:'personal'}:{}};}
function stepAllowed(n){
 if(n===1)return hasPerm('question:generate')||hasPerm('batch:run');
 if(n===2)return !!journeyTask.value&&(journeyTask.value.kind==='batch'?hasPerm('batch:run'):hasPerm('question:generate'));
 return n===3?hasPerm('question:view')&&hasPerm('question:confirm'):hasPerm('question:view');
}
onBeforeUnmount(()=>{disposed=true;epoch++;});
</script>
<template>
 <section v-if="main" class="authoring-guide" aria-label="命题操作指引">
   <div class="guide-caption"><span class="guide-eyebrow">命题流程</span><span class="guide-context">{{journeyProgress?.task.name|| (journeyTask?'本次命题任务':'按四步完成命题与导出')}}</span><button v-if="journeyTask" class="guide-text" type="button" @click="leaveTask">取消本次定位</button><button class="guide-mobile-toggle" type="button" :aria-expanded="open" @click="open=!open">{{open?'收起步骤':'展开四步'}}</button></div>
   <ol class="guide-steps" :class="{expanded:open}"><li v-for="step in steps" :key="step.n" :class="{done:step.done,here:active===step.n,recommended:next===step.n}"><button type="button" :disabled="!stepAllowed(step.n)" :aria-current="active===step.n?'step':undefined" @click="go(stepTarget(step.n))"><span class="guide-number">{{step.done?'✓':step.n}}</span><span><strong>{{step.title}}</strong><small>{{step.hint}}</small></span><span v-if="next===step.n" class="guide-next-tag">下一步</span></button></li></ol>
   <div class="guide-bottom"><div class="guide-status"><span v-if="error">{{error}}</span><template v-else-if="journeyProgress"><span>{{journeyProgress.task.total}} 题 · 待整理 {{c.arrange||0}} · 已确认 {{c.confirmed||0}}</span><span v-if="c.rejected||c.check_failed||journeyProgress.task.generation_failed" class="guide-notice">未通过/失败 {{(c.rejected||0)+(c.check_failed||0)+(journeyProgress.task.generation_failed||0)}}</span><span v-if="c.deleted">删除留档 {{c.deleted}}</span></template><span v-else>生成通过后去题目整理，确认后进入个人题库。</span></div><button class="guide-primary" type="button" :disabled="primary.disabled||busy&&primary.retry" @click="act">{{primary.label}} <span aria-hidden="true">→</span></button><button v-if="route.path==='/bank'&&c.confirmed>0&&(hasPerm('question:generate')||hasPerm('batch:run'))" class="guide-text" type="button" @click="go({path:genPath,hash:'#authoring-points'})">继续出题</button></div>

 </section>
</template>
<style scoped>
.authoring-guide{margin:0 0 20px;border:1px solid #dbe8f5;border-radius:10px;background:linear-gradient(112deg,#f4f9ff,#fff 72%);color:#354f6a;min-width:0;overflow:hidden}.guide-caption{display:flex;align-items:center;gap:12px;padding:12px 18px 2px;font-size:12px;min-height:26px}.guide-eyebrow{color:#2878bc;font-size:11px;font-weight:700;letter-spacing:.08em;white-space:nowrap}.guide-context{overflow:hidden;white-space:nowrap;text-overflow:ellipsis;flex:1;color:#71849a}.guide-steps{list-style:none;padding:10px 14px 12px;margin:0;display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:8px}.guide-steps li{min-width:0}.guide-steps button{position:relative;width:100%;display:flex;align-items:center;gap:10px;padding:9px 8px;border:1px solid transparent;border-radius:7px;background:transparent;text-align:left;color:#6d8197;cursor:pointer;min-height:58px}.guide-steps .here button{border-color:#c6dffa;background:#edf6ff}.guide-number{display:grid;place-items:center;width:30px;height:30px;border-radius:50%;background:#e6eef7;color:#67839f;flex:0 0 30px;font-size:13px;font-weight:600}.here .guide-number,.recommended .guide-number{background:#197fe2;color:white}.done .guide-number{background:#e3f3ed;color:#278364}.guide-steps strong{display:block;font-size:13px;font-weight:600;white-space:nowrap}.guide-steps small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:11px;color:#8090a4;line-height:1.8}.guide-steps button>span:nth-child(2){min-width:0}.guide-next-tag{position:absolute;right:5px;top:-6px;background:#e4f0ff;color:#1874c6;font-size:10px;padding:1px 5px;border-radius:3px}.guide-bottom{display:flex;align-items:center;gap:12px;min-height:48px;padding:6px 18px 10px;border-top:1px solid #e5edf5;box-sizing:border-box}.guide-status{min-width:0;flex:1;display:flex;gap:10px;align-items:center;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;font-size:12px;color:#6e8195}.guide-notice{color:#a56b26}.guide-primary{border:0;border-radius:6px;background:#197fe2;color:white;padding:9px 14px;font:inherit;font-size:12px;font-weight:600;cursor:pointer;white-space:nowrap}.guide-primary:disabled{opacity:.45;cursor:default}.guide-steps button:disabled{cursor:default;color:#7b8a9a}.guide-steps button:disabled small{color:#5c7187}.guide-text,.guide-mobile-toggle{border:0;background:transparent;color:#527b9f;font:inherit;font-size:12px;cursor:pointer;white-space:nowrap;padding:6px}.guide-mobile-toggle{display:none}.guide-scope-note{font-size:11px;color:#8090a0;margin:0;padding:0 18px 10px}button:focus-visible,a:focus-visible{outline:2px solid #1684ed;outline-offset:2px}
@media(max-width:800px){.guide-steps{gap:2px;padding-inline:6px}.guide-steps button{gap:6px;padding:6px 4px}.guide-steps strong{font-size:12px}.guide-status{white-space:normal;flex-wrap:wrap;gap:3px 8px}.guide-bottom{gap:8px;padding-inline:12px}.guide-caption{padding-inline:12px}}
@media(max-width:600px){.authoring-guide{margin-bottom:14px}.guide-mobile-toggle{display:block}.guide-caption{padding:8px 10px 2px;gap:6px}.guide-caption>.guide-text{display:none}.guide-steps:not(.expanded) li:not(.here){display:none}.guide-steps:not(.expanded){grid-template-columns:1fr;padding:3px 8px}.guide-steps.expanded{grid-template-columns:1fr 1fr;gap:4px;padding:8px}.guide-steps button{min-height:43px}.guide-steps small{font-size:10px}.guide-bottom{flex-wrap:wrap;padding:7px 10px;gap:5px 8px}.guide-status{flex-basis:100%;font-size:11px;min-height:17px}.guide-primary{padding:8px 10px}.guide-bottom>.guide-primary{flex:1;text-align:center}.guide-scope-note{padding:0 10px 8px}}
</style>
