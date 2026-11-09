<script setup>
import {computed,ref,watch,onBeforeUnmount,useId} from 'vue';
import {api} from '../api.js';
import {currentUser,getToken} from '../auth.js';
import {knowledgeScope} from '../knowledgeBaseAccess.js';
import {pathKey,selectedCountsByPath} from '../knowledgePickerTree.js';
import {subscribeKnowledgeVersions} from '../knowledgeVersions.js';
import KnowledgePickerTreeNode from './KnowledgePickerTreeNode.vue';
import HelpHint from './HelpHint.vue';

const props=defineProps({modelValue:{type:Array,default:()=>[]},disabled:Boolean});
const emit=defineEmits(['update:modelValue']);
const panelId=useId(),limit=20,pageSize=20;
const opened=ref(false),versions=ref([]),versionId=ref(''),versionsBusy=ref(false),versionsError=ref('');
const tree=ref([]),expanded=ref(new Set()),path=ref([]),treeBusy=ref(false),treeError=ref('');
const keyword=ref(''),appliedKeyword=ref(''),points=ref([]),total=ref(0),page=ref(1),busy=ref(false),error=ref(''),mobilePane=ref('directory');
let disposed=false,versionTicket=0,treeTicket=0,searchTicket=0,debounce;
const identity=computed(()=>knowledgeScope(currentUser.value,getToken()));
const selectedIDs=computed(()=>new Set(props.modelValue.map(p=>p.id)));
const selectedCounts=computed(()=>selectedCountsByPath(props.modelValue.filter(p=>p.version_id===versionId.value)));
const pages=computed(()=>Math.max(1,Math.ceil(total.value/pageSize)));
const directory=computed(()=>path.value.join(' / ')||'全部考点');
function versionName(p){return p.version_name||versions.value.find(v=>v.id===p.version_id)?.name||p.version_year||'原大纲版本';}
function pointPath(p){return [p.category,p.subject,p.unit,p.sub_item].filter(Boolean).join(' / ');}
function invalidate(){versionTicket++;treeTicket++;searchTicket++;clearTimeout(debounce);versionsBusy.value=false;treeBusy.value=false;busy.value=false;}
function close(){opened.value=false;invalidate();}
watch(identity,()=>{close();versions.value=[];versionId.value='';points.value=[];tree.value=[];error.value='';versionsError.value='';treeError.value='';});
watch(()=>props.disabled,value=>{if(value)close();});
const active=(ticket,current,scope)=>!disposed&&opened.value&&!props.disabled&&ticket===current&&scope===identity.value;

async function loadVersions(background=false){
  if(!opened.value||props.disabled||versionsBusy.value)return;
  const ticket=++versionTicket,scope=identity.value;if(!background){versionsBusy.value=true;versionsError.value='';}
  try{
    const data=await api.kpVersions();if(!active(ticket,versionTicket,scope))return;
    versions.value=data.versions||[];
    const ids=new Set(versions.value.map(v=>v.id));
    const firstBound=props.modelValue.find(p=>ids.has(p.version_id))?.version_id;
    const next=ids.has(versionId.value)?versionId.value:firstBound||(ids.has(data.default_version_id)?data.default_version_id:versions.value[0]?.id)||'';
    const changed=next!==versionId.value;versionId.value=next;versionsError.value='';
    if(changed){path.value=[];expanded.value=new Set();keyword.value='';page.value=1;}
    if(!background||changed){loadDirectory();search(1);}
  }catch(e){if(active(ticket,versionTicket,scope))versionsError.value='大纲版本读取失败：'+e.message;}
  finally{if(active(ticket,versionTicket,scope))versionsBusy.value=false;}
}
async function open(){if(props.disabled)return;opened.value=true;mobilePane.value='directory';await loadVersions();}
function toggleOpen(){if(opened.value)close();else open();}
async function loadDirectory(){
  const ticket=++treeTicket,scope=identity.value,v=versionId.value;
  tree.value=[];treeError.value='';treeBusy.value=false;if(!v||!opened.value||props.disabled)return;
  treeBusy.value=true;
  try{const data=await api.kpTree(v);if(active(ticket,treeTicket,scope)&&v===versionId.value)tree.value=data.tree||[];}
  catch(e){if(active(ticket,treeTicket,scope))treeError.value='目录读取失败：'+e.message;}
  finally{if(active(ticket,treeTicket,scope))treeBusy.value=false;}
}
async function search(nextPage=1){
  clearTimeout(debounce);
  const ticket=++searchTicket,scope=identity.value,v=versionId.value;
  points.value=[];error.value='';busy.value=false;if(!v||!opened.value||props.disabled){total.value=0;return;}
  busy.value=true;appliedKeyword.value=keyword.value.trim();
  const params={version_id:v,q:appliedKeyword.value,page:nextPage,page_size:pageSize,...(path.value.length?{path:JSON.stringify(path.value)}:{})};
  try{
    const data=await api.searchKPFiltered(params);
    if(!active(ticket,searchTicket,scope)||v!==versionId.value)return;
    points.value=data.points||[];total.value=data.total??points.value.length;page.value=nextPage;
  }catch(e){if(active(ticket,searchTicket,scope))error.value='考点读取失败：'+e.message;}
  finally{if(active(ticket,searchTicket,scope))busy.value=false;}
}
function queryChanged(){searchTicket++;points.value=[];busy.value=true;clearTimeout(debounce);debounce=setTimeout(()=>search(1),300);}
function changeVersion(){
  invalidate();path.value=[];expanded.value=new Set();keyword.value='';page.value=1;
  // Browsing another version never removes existing card mappings.
  loadDirectory();search(1);
}
function toggleDirectory(id){const value=new Set(expanded.value);if(value.has(id))value.delete(id);else value.add(id);expanded.value=value;}
function openDirectory(node){path.value=[...node.path];mobilePane.value='points';search(1);}
function reset(){keyword.value='';path.value=[];search(1);}
function togglePoint(p){
  if(props.disabled)return;
  if(selectedIDs.value.has(p.id))emit('update:modelValue',props.modelValue.filter(x=>x.id!==p.id));
  else if(props.modelValue.length<limit)emit('update:modelValue',[...props.modelValue,{...p}]);
}
function remove(id){if(!props.disabled)emit('update:modelValue',props.modelValue.filter(p=>p.id!==id));}
const unsubscribe=subscribeKnowledgeVersions(()=>{if(opened.value)loadVersions(true);});
onBeforeUnmount(()=>{disposed=true;invalidate();unsubscribe();});
</script>

<template>
  <section class="point-binding" aria-label="知识点绑定">
    <div class="binding-heading">
      <div class="ui-help-heading"><h3>关联知识点</h3><HelpHint label="知识卡绑定说明">按大纲目录逐级浏览，再勾选具体考点；不知道准确名称时可先选专业或章节，再搜索相关词。最多关联20个考点，可跨版本；切换浏览版本不会清空已绑定项。</HelpHint></div>
      <button type="button" class="binding-trigger" :disabled="disabled" :aria-expanded="opened" :aria-controls="panelId" @click="toggleOpen">{{opened?'收起选择':modelValue.length?'调整知识点':'选择知识点'}}<span>{{modelValue.length}} / {{limit}}</span></button>
    </div>
    <div v-if="modelValue.length" class="binding-chips" aria-label="已绑定知识点">
      <span v-for="p in modelValue" :key="p.id" class="binding-chip" :title="`${versionName(p)} · ${pointPath(p)} · ${p.topic} · ${p.outline_code||''}`"><span><strong>{{p.topic}}</strong><small>{{versionName(p)}}<template v-if="p.subject"> · {{p.subject}}</template></small></span><button type="button" :disabled="disabled" :aria-label="`移除绑定 ${p.topic}（${versionName(p)}）`" @click="remove(p.id)">×</button></span>
    </div>
    <p v-else-if="!opened" class="binding-empty">尚未关联，点击选择后可按目录查找。</p>
    <div v-if="opened" :id="panelId" class="binding-workspace">
      <div class="binding-toolbar">
        <select v-model="versionId" aria-label="绑定大纲版本" :disabled="disabled||versionsBusy||!versions.length" @change="changeVersion"><option v-if="!versions.length" value="">{{versionsBusy?'读取大纲中…':'暂无大纲版本'}}</option><option v-for="v in versions" :key="v.id" :value="v.id">{{v.name}}{{v.status==='draft'?'（草稿）':''}}</option></select>
        <div class="binding-search"><input v-model="keyword" aria-label="搜索绑定知识点" placeholder="搜索疾病、章节或相关词" :disabled="disabled||!versionId" @input="queryChanged" @keydown.enter.prevent="search(1);mobilePane='points'"/><button type="button" :disabled="disabled||!versionId" @click="search(1);mobilePane='points'">搜索</button></div>
      </div>
      <p v-if="versionsError" class="binding-error" role="alert">{{versionsError}} <button type="button" @click="loadVersions()">重试</button></p>
      <div class="binding-mobile-tabs" aria-label="切换目录或考点"><button type="button" :class="{active:mobilePane==='directory'}" :aria-pressed="mobilePane==='directory'" @click="mobilePane='directory'">大纲目录</button><button type="button" :class="{active:mobilePane==='points'}" :aria-pressed="mobilePane==='points'" @click="mobilePane='points'">考点列表</button></div>
      <div class="binding-columns" :class="`show-${mobilePane}`">
        <div class="binding-directory" aria-label="绑定大纲目录">
          <div class="binding-pane-heading"><strong>大纲目录</strong><button type="button" :disabled="!versionId" @click="path=[];search(1);mobilePane='points'">全部考点</button></div>
          <div class="binding-scroll">
            <p v-if="treeBusy" class="binding-empty">读取目录中…</p>
            <p v-else-if="treeError" class="binding-error" role="alert">{{treeError}} <button type="button" @click="loadDirectory">重试</button></p>
            <ul v-else-if="tree.length" class="binding-tree"><KnowledgePickerTreeNode v-for="node in tree" :key="node.id" :node="node" :expanded="expanded" :selected-counts="selectedCounts" :active-key="pathKey(path)" :show-checkbox="false" @toggle="toggleDirectory" @open="openDirectory"/></ul>
            <p v-else class="binding-empty">当前版本暂无目录</p>
          </div>
        </div>
        <div class="binding-results" aria-label="可绑定知识点">
          <div class="binding-pane-heading"><strong :title="directory">{{directory}}</strong><button v-if="path.length||keyword" type="button" @click="reset">重置筛选</button></div>
          <div class="binding-scroll" tabindex="0" aria-label="知识点列表">
            <p v-if="busy" class="binding-empty" role="status">读取考点中…</p>
            <p v-else-if="error" class="binding-error" role="alert">{{error}} <button type="button" @click="search(page)">重试</button></p>
            <p v-else-if="!points.length" class="binding-empty">{{appliedKeyword?'没有找到相关考点，可换一个相关词或重置筛选。':'当前目录暂无考点。'}}</p>
            <label v-for="p in busy||error?[]:points" :key="p.id" class="binding-point" :class="{chosen:selectedIDs.has(p.id)}"><input type="checkbox" :aria-label="`绑定 ${p.topic}（${p.outline_code||p.id}）`" :checked="selectedIDs.has(p.id)" :disabled="disabled||!selectedIDs.has(p.id)&&modelValue.length>=limit" @change="togglePoint(p)"/><span><strong>{{p.topic}}</strong><small>{{pointPath(p)}}<template v-if="p.outline_code"> · {{p.outline_code}}</template></small></span></label>
          </div>
          <div class="binding-pagination"><span>{{total}} 个考点 · {{page}} / {{pages}} 页</span><button type="button" aria-label="绑定考点上一页" :disabled="busy||page<=1" @click="search(page-1)">‹</button><button type="button" aria-label="绑定考点下一页" :disabled="busy||page>=pages" @click="search(page+1)">›</button></div>
        </div>
      </div>
      <div class="binding-footer"><span role="status">{{modelValue.length>=limit?'已达20个上限，可先移除再选择。':`已绑定 ${modelValue.length} 个考点`}}</span><button type="button" @click="close">完成选择</button></div>
    </div>
  </section>
</template>

<style scoped>
.point-binding{min-width:0;margin-top:16px;border-top:1px solid #e4ebf3;padding-top:12px;color:#425a73}.binding-heading{display:flex;align-items:center;justify-content:space-between;gap:10px;flex-wrap:wrap}.binding-heading h3{font-size:14px;margin:0}.point-binding button,.point-binding select,.point-binding input{font:inherit;box-sizing:border-box}.point-binding button{cursor:pointer}.binding-trigger{display:flex;align-items:center;gap:12px;padding:8px 10px;border:1px solid #c7dcf3;border-radius:6px;background:#f4f9ff;color:#1768ab;font-size:12px}.binding-trigger span{font-size:11px;color:#6b829b;font-variant-numeric:tabular-nums}.binding-chips{display:flex;flex-wrap:wrap;gap:6px;max-height:92px;overflow:auto;margin-top:10px;padding:1px}.binding-chip{display:flex;align-items:center;gap:8px;min-width:0;max-width:100%;padding:5px 8px;background:#f1f7ff;border:1px solid #dbe7f6;border-radius:5px}.binding-chip>span{min-width:0}.binding-chip strong,.binding-chip small{display:block;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:240px}.binding-chip strong{font-size:12px;font-weight:500}.binding-chip small{font-size:11px;color:#75869b;line-height:1.6}.binding-chip button{border:0;background:transparent;color:#6483a5;flex:0 0 24px;width:24px;height:28px;padding:0}.binding-empty{font-size:12px;line-height:1.7;color:#7a8c9f;padding:8px;margin:4px 0}.binding-workspace{height:min(430px,58dvh);min-height:280px;display:flex;flex-direction:column;overflow:hidden;border:1px solid #dbe5ef;border-radius:7px;background:white;margin-top:10px}.binding-toolbar{display:flex;gap:8px;flex:0 0 auto;padding:10px;border-bottom:1px solid #e5edf5;background:#f8fbff}.binding-toolbar select{flex:0 1 180px;width:180px;min-width:0;max-width:100%;padding:8px;border:1px solid #d6deea;border-radius:5px;font-size:12px;color:inherit;background:white}.binding-search{display:flex;flex:1;min-width:0;gap:6px}.binding-search input{width:100%;min-width:0;padding:8px;border:1px solid #d6deea;border-radius:5px;color:inherit;background:white;font-size:12px}.binding-search button,.binding-pagination button,.binding-footer button{flex:0 0 auto;border:1px solid #cbddef;border-radius:5px;background:#fff;color:#276ba4;padding:6px 10px;font-size:12px}.binding-columns{display:grid;grid-template-columns:minmax(150px,36%) minmax(0,1fr);min-height:0;flex:1;overflow:hidden}.binding-directory,.binding-results{display:flex;flex-direction:column;min-width:0;min-height:0}.binding-directory{border-right:1px solid #e5edf5}.binding-pane-heading{display:flex;align-items:center;justify-content:space-between;gap:8px;flex:0 0 auto;min-height:38px;border-bottom:1px solid #edf1f6;padding:6px 10px;font-size:12px;box-sizing:border-box}.binding-pane-heading strong{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;min-width:0}.binding-pane-heading button,.binding-error button{border:0;background:transparent;color:#1674c6;font-size:12px;white-space:nowrap;padding:3px}.binding-scroll{min-height:0;overflow:auto;overscroll-behavior:contain;flex:1;padding:4px 6px;scrollbar-gutter:stable}.binding-tree{padding:0;margin:0;list-style:none}.point-binding .binding-point{display:flex;flex-direction:row;align-items:flex-start;gap:9px;padding:9px 8px;margin:3px 0;border:1px solid transparent;border-radius:5px;cursor:pointer;font-size:13px;line-height:1.5}.binding-point:hover{background:#f6f9fd}.binding-point.chosen{border-color:#b9d7fa;background:#edf5ff}.point-binding .binding-point input{width:16px;height:16px;flex:0 0 16px;margin:2px 0 0;padding:0;accent-color:#1684ed}.binding-point>span{min-width:0;overflow-wrap:anywhere}.binding-point strong{font-size:13px;font-weight:500}.binding-point small{display:block;font-size:11px;line-height:1.6;color:#7a8c9f;margin-top:3px}.binding-pagination{display:flex;align-items:center;justify-content:flex-end;gap:6px;flex:0 0 auto;padding:6px 8px;border-top:1px solid #edf1f6;font-size:11px}.binding-pagination>span{margin-right:auto;color:#7b8ca0}.binding-pagination button{padding:2px 10px;font-size:18px;line-height:1.4}.binding-footer{display:flex;align-items:center;justify-content:space-between;gap:8px;flex:0 0 auto;padding:8px 10px;border-top:1px solid #dce7f1;background:#f8fbff;font-size:12px}.binding-footer>span{color:#71859d}.binding-footer button{background:#187fe0;border-color:#187fe0;color:white}.binding-error{font-size:12px;color:#b42318;line-height:1.7;margin:4px 0;padding:6px 10px;overflow-wrap:anywhere}.binding-mobile-tabs{display:none}.point-binding button:disabled{opacity:.5;cursor:default}.point-binding button:focus-visible,.point-binding input:focus-visible,.point-binding select:focus-visible,.binding-scroll:focus-visible{outline:2px solid #1684ed;outline-offset:2px}
@media(max-width:600px){.binding-toolbar{flex-direction:column;gap:6px;padding:8px}.binding-toolbar select{width:100%;flex:auto}.binding-mobile-tabs{display:flex;padding:4px 8px;gap:6px;border-bottom:1px solid #e5edf5}.binding-mobile-tabs button{flex:1;border:0;border-radius:4px;background:white;padding:6px;color:#71859d;font-size:12px}.binding-mobile-tabs button.active{background:#eaf3ff;color:#1768ab;font-weight:600}.binding-columns{grid-template-columns:minmax(0,1fr)}.binding-directory{border-right:0}.show-directory .binding-results,.show-points .binding-directory{display:none}.binding-chip strong,.binding-chip small{max-width:190px}.binding-pane-heading{min-height:32px}.binding-footer{padding:6px 8px}.binding-point{min-height:44px}.binding-pagination{padding:3px 8px}}
</style>
