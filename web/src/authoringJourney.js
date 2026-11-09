import {shallowRef} from 'vue';
export const journeyTask=shallowRef(null);
export const journeyProgress=shallowRef(null);
const key=user=>user?.id?`aigo_journey:${user.id}:${user.role||''}`:'';
export function rememberJourney(user,task){
 const owner=key(user);if(!owner||!['single','batch'].includes(task?.kind)||typeof task.id!=='string'||!task.id||task.id.length>128)return;
 if(journeyTask.value?.owner===owner&&journeyTask.value.kind===task.kind&&journeyTask.value.id===task.id)return;
 journeyTask.value={kind:task.kind,id:task.id,owner};journeyProgress.value=null;
 try{sessionStorage.setItem(owner,JSON.stringify({kind:task.kind,id:task.id}));}catch{}
}
export function restoreJourney(user){const owner=key(user);journeyTask.value=null;journeyProgress.value=null;if(!owner)return;try{const task=JSON.parse(sessionStorage.getItem(owner)||'null');if(task)rememberJourney(user,task);}catch{}}
export function clearJourney(user){journeyTask.value=null;journeyProgress.value=null;try{sessionStorage.removeItem(key(user));}catch{}}
export function journeyParams(task=journeyTask.value){return task?{task_kind:task.kind,task_id:task.id}:{};}
export function routeJourney(query){return typeof query?.task_kind==='string'&&typeof query?.task_id==='string'?{kind:query.task_kind,id:query.task_id}:null;}
export function journeyTarget(path,task=journeyTask.value){return {path,query:{...(path==='/bank'?{scope:'personal'}:{}),...journeyParams(task)}};}
export function verifyJourneyList(data,task){if(task&&(data.task?.kind!==task.kind||data.task?.id!==task.id))throw new Error('当前服务尚不支持本次题目定位，请重启后端再试；可通过左侧查看全部题目。');}
export function journeyNext(progress){const c=progress?.counts;if(!c)return 0;if(c.arrange>0)return 3;if(c.confirmed>0)return 4;return 2;}
