<script setup>
import HelpHint from "./HelpHint.vue";
import { computed, ref, watch, onMounted, onBeforeUnmount } from 'vue';
import { api } from '../api.js';
import { currentUser, getToken } from '../auth.js';
import { refreshSessionIdentity } from '../sessionIdentity.js';
import { knowledgeCapabilities, knowledgeScope, cardKinds } from '../knowledgeBaseAccess.js';
const emit=defineEmits(['close','saved']);
const caps=computed(()=>knowledgeCapabilities(currentUser.value?.permissions));
const scope=computed(()=>knowledgeScope(currentUser.value,getToken()));
const dialog=ref(null),versions=ref([]),version=ref(''),documentText=ref(''),preview=ref(null),busy=ref(''),error=ref('');
let disposed=false,epoch=0;
const current=(key,step)=>!disposed&&key===scope.value&&step===epoch&&caps.value.manage;
watch([version,documentText],()=>{epoch++;preview.value=null;error.value='';});
watch(scope,()=>{epoch++;preview.value=null;documentText.value='';versions.value=[];version.value='';busy.value='';error.value='';emit('close');});
async function loadFile(event){
 const file=event.target.files?.[0];if(!file||busy.value)return;
 error.value='';preview.value=null;
 if(file.size>2*1024*1024){error.value='文件超过2 MiB，请拆分后导入';event.target.value='';return;}
 const key=scope.value,step=epoch;busy.value='file';
 try{const text=await file.text();if(current(key,step))documentText.value=text.replace(/^\uFEFF/,'');}
 catch{if(current(key,step))error.value='文件读取失败，请选择UTF-8 JSON文件';}
 finally{if(!disposed&&key===scope.value)busy.value='';event.target.value='';}
}
async function inspect(){
 if(busy.value||!caps.value.manage)return;
 const key=scope.value,step=epoch;busy.value='preview';error.value='';preview.value=null;
 try{await refreshSessionIdentity();if(!current(key,step))return;const value=await api.previewKnowledgeImport({version_id:version.value,document:documentText.value});if(current(key,step))preview.value=value;}
 catch(e){if(current(key,step))error.value=e.message;}
 finally{if(current(key,step))busy.value='';}
}
async function confirm(){
 if(busy.value||!preview.value?.valid||!preview.value?.preview_token||!caps.value.manage)return;
 const key=scope.value,step=epoch;busy.value='confirm';error.value='';
 const data={version_id:version.value,document:documentText.value,preview_token:preview.value.preview_token};
 try{await refreshSessionIdentity();if(!current(key,step))return;const saved=await api.confirmKnowledgeImport(data);if(current(key,step))emit('saved',saved);}
 catch(e){if(current(key,step)){error.value=e.message;if([400,409].includes(e.status))preview.value=null;}}
 finally{if(current(key,step))busy.value='';}
}
function downloadTemplate(){
 const template={format:'aigo-knowledge-import-v1',cards:[{kind:'medical',title:'示例考点知识（请修改）',knowledge_point_codes:['示例.001'],content:{exam_targets:['本次考核目标'],features:['本次相关特征'],preconditions:['适用条件'],facts:[{id:'f1',text:'待人工核对的事实',applies_when:'适用场景',source:'实际资料出处'}],pitfalls:['本次注意事项'],custom_text:''},source:'请填写实际资料名称、版本或链接',note:'示例模板；请核对后再导入'}]};
 const url=URL.createObjectURL(new Blob([JSON.stringify(template,null,2)],{type:'application/json'}));const link=document.createElement('a');link.href=url;link.download='知识卡导入模板.json';link.click();URL.revokeObjectURL(url);
}
onMounted(async()=>{
 dialog.value?.showModal();const key=scope.value;
 try{const data=await api.kpVersions();if(!disposed&&scope.value===key&&caps.value.manage)versions.value=data.versions||[];}
 catch(e){if(!disposed&&scope.value===key)error.value=e.message;}
});
onBeforeUnmount(()=>{disposed=true;epoch++;});
</script>
<template>
<dialog v-if="caps.manage" ref="dialog" class="knowledge-import" aria-labelledby="knowledge-import-title" @cancel.prevent="!busy&&emit('close')">
 <div class="import-heading"><div><h2 id="knowledge-import-title">批量导入知识卡</h2><p>选择大纲 → 预览核对 → 保存草稿</p></div><button class="import-close" aria-label="关闭导入" :disabled="!!busy" @click="emit('close')">×</button></div>
 <HelpHint label="知识卡导入说明">支持UTF-8 JSON，每批1—50张、文件最多2 MiB。导入后为新草稿，人工核对后可逐张启用。个人命题偏好在“个人知识库”中维护。</HelpHint>
 <fieldset :disabled="!!busy"><label>关联大纲<select v-model="version" aria-label="导入大纲版本"><option value="">请选择大纲</option><option v-for="v in versions" :key="v.id" :value="v.id">{{v.name}}</option></select></label>
 <div class="import-file"><label>选择JSON文件<input type="file" accept=".json,application/json" @change="loadFile"/></label><button type="button" class="ghost-button" @click="downloadTemplate">下载模板</button></div>
 <label>JSON内容<textarea v-model="documentText" aria-label="导入JSON内容" rows="8" spellcheck="false" placeholder="选择文件或粘贴JSON内容"/></label></fieldset>
 <p v-if="error" class="import-error" role="alert">{{error}}</p>
 <section v-if="preview" class="import-preview" aria-label="知识卡导入预览"><h3>预览 {{preview.rows.length}} 张知识卡</h3><p v-if="!preview.valid" class="import-error">请修正标出的条目，再重新预览。</p><HelpHint v-else label="导入预览说明">核对资料与知识点关联后，可一次保存全部草稿。预览凭证30分钟内有效。</HelpHint>
  <article v-for="row in preview.rows" :key="row.number"><strong>{{row.number}}. {{row.card?.title||'待修正条目'}}</strong><small v-if="row.card">{{cardKinds[row.card.kind]}} · {{row.card.knowledge_points.map(p=>`${p.version_name} / ${p.outline_code} / ${p.topic}`).join('；')}}</small><p v-if="row.error" class="import-error">{{row.error}}</p><p v-if="row.warning" class="import-warning">{{row.warning}}</p>
   <details v-if="row.card"><summary>查看内容与来源</summary><p v-for="key in ['exam_targets','features','preconditions','pitfalls']" :key="key">{{({exam_targets:'考核目标',features:'特征',preconditions:'适用条件',pitfalls:'注意事项'})[key]}}：{{row.card.content[key]?.join('；')||'未填写'}}</p><p v-for="fact in row.card.content.facts" :key="fact.id">事实 {{fact.id}}：{{fact.text}}<br/>前提：{{fact.applies_when||'未注明'}}；来源：{{fact.source||row.card.source||'未注明'}}</p><template v-if="row.card.content.reference"><p>参考题：{{row.card.content.reference.stem}}</p><p v-for="o in row.card.content.reference.options" :key="o.label">{{o.label}}. {{o.text}}</p><p>参考答案：{{row.card.content.reference.answer||'未填写'}}</p><p>{{row.card.content.reference.explanation}}</p></template><p>{{row.card.content.custom_text}}</p><p>来源：{{row.card.source||'未填写'}}</p><p>备注：{{row.card.note||'未填写'}}</p></details>
  </article>
 </section>
 <div class="import-actions"><button class="ghost-button" :disabled="!!busy" @click="emit('close')">取消</button><button class="ghost-button" :disabled="!!busy||!version||!documentText.trim()" @click="inspect">{{busy==='preview'?'预览中…':'预览核对'}}</button><button class="primary-button" :disabled="!!busy||!preview?.valid||!preview?.preview_token" @click="confirm">{{busy==='confirm'?'保存中…':'保存全部为草稿'}}</button></div>
</dialog>
</template>
<style scoped>
.knowledge-import{width:min(940px,calc(100vw - 32px));max-height:calc(100dvh - 40px);box-sizing:border-box;overflow:auto;border:0;border-radius:10px;padding:26px;background:#fff;color:#354c68;box-shadow:0 16px 50px #0f172a30;overflow-wrap:anywhere}.knowledge-import::backdrop{background:#0f172a60}.import-heading,.import-file,.import-actions{display:flex;align-items:center;gap:12px;flex-wrap:wrap}.import-heading{justify-content:space-between}.import-heading h2{font-size:19px;margin:0;color:#263e59}.import-heading p,.import-hint,small{font-size:13px;line-height:1.8;color:#748396}.import-close{border:0;background:none;font-size:26px;color:#748396}.knowledge-import fieldset{min-width:0;padding:0;border:0}.knowledge-import label{display:flex;flex-direction:column;gap:8px;margin:14px 0;font-size:13px}.knowledge-import select,.knowledge-import textarea{box-sizing:border-box;max-width:100%;width:100%;border:1px solid #d6deea;border-radius:6px;padding:10px;color:inherit;background:#fff;font:inherit}.knowledge-import textarea{resize:vertical;line-height:1.7}.import-file{justify-content:space-between}.import-file input{max-width:100%}.import-file label{min-width:0}.import-preview{border-top:1px solid #e8edf3;margin-top:20px;padding-top:16px}.import-preview h3{font-size:15px}.import-preview article{padding:14px;background:#f8fafc;border:1px solid #e8edf3;border-radius:6px;margin:12px 0;min-width:0}.import-preview small{display:block}.import-preview p{white-space:pre-wrap;line-height:1.8;font-size:13px}.import-preview summary{cursor:pointer;font-size:13px;margin-top:12px}.import-error{color:#b91c1c;white-space:pre-wrap;line-height:1.8}.import-warning{color:#976116}.import-actions{justify-content:flex-end;border-top:1px solid #e8edf3;margin-top:20px;padding-top:16px}button:disabled{opacity:.5}@media(max-width:600px){.knowledge-import{padding:18px}.import-actions button{flex:1;min-width:110px}}
</style>
