<script setup>
import { onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { api } from '../api.js';
import HelpHint from './HelpHint.vue';
const loaded=ref(false),loading=ref(true),saving=ref(false),error=ref(''),notice=ref('');
const configured=ref(false),masked=ref('');
const form=ref({independent:false,deployment:'cloud',base_url:'https://dashscope.aliyuncs.com/compatible-mode/v1',api_key:'',clear_api_key:false,model:'qwen3.5-flash',temperature:0.6});
let disposed=false,hydrating=false,timer;
function apply(data){hydrating=true;form.value={...form.value,...data,base_url:data.base_url||'https://dashscope.aliyuncs.com/compatible-mode/v1',api_key:'',clear_api_key:false};configured.value=!!data.api_key_configured;masked.value=data.api_key_masked||'';hydrating=false;}
watch(()=>form.value.deployment,(next,old)=>{if(!hydrating&&next==='local'&&old==='cloud'){form.value.api_key='';form.value.clear_api_key=true;}},{flush:'sync'});
async function load(){loading.value=true;error.value='';try{const data=await api.getLearningProvider();if(!disposed){apply(data);loaded.value=true;}}catch(e){if(!disposed)error.value='读取总结配置失败：'+e.message;}finally{if(!disposed)loading.value=false;}}
async function save(){
 if(saving.value||loading.value||!loaded.value)return;
 error.value='';notice.value='';window.clearTimeout(timer);
 if(!Number.isFinite(form.value.temperature)||form.value.temperature<0||form.value.temperature>2){error.value='温度须在0到2之间';return;}
 if(form.value.independent&&(!form.value.base_url.trim()||!form.value.model.trim())){error.value='请填写总结API地址和模型';return;}
 saving.value=true;
 try{
  const data=await api.saveLearningProvider({...form.value,base_url:form.value.base_url.trim(),model:form.value.model.trim(),api_key:form.value.clear_api_key?'':form.value.api_key.trim()});
  if(disposed)return;apply(data);notice.value='总结配置已保存';timer=window.setTimeout(()=>{notice.value='';},3500);
 }catch(e){if(!disposed)error.value='保存总结配置失败：'+e.message;}finally{if(!disposed)saving.value=false;}
}
onMounted(load);onBeforeUnmount(()=>{disposed=true;window.clearTimeout(timer);form.value.api_key='';});
</script>
<template>
 <section class="learning-config panel" aria-label="AI总结配置">
  <div class="learning-heading"><div><h3>AI总结服务</h3><span class="current-mode">{{form.independent?'独立服务':'沿用出题服务'}} · 温度 {{form.temperature}}</span></div><HelpHint label="总结配置说明">这里只调整老师修改题目后提交的AI总结。出题和首检继续使用各自原配置。未启用独立服务时，总结沿用出题服务的地址、密钥和模型。</HelpHint></div>
  <p v-if="loading" class="loading-text">正在读取总结配置…</p>
  <form v-else @submit.prevent="save">
   <template v-if="loaded">
   <fieldset class="source-choice" :disabled="saving"><legend>模型服务</legend>
    <label :class="{selected:!form.independent}"><input type="radio" name="summary-source" value="inherit" :checked="!form.independent" @change="form.independent=false"/>沿用出题服务</label>
    <label :class="{selected:form.independent}"><input type="radio" name="summary-source" value="independent" :checked="form.independent" @change="form.independent=true"/>独立配置</label>
   </fieldset>
   <div v-if="form.independent" class="learning-fields">
    <label><span>服务类型</span><select v-model="form.deployment" :disabled="saving"><option value="cloud">云端 / 需要API Key</option><option value="local">本地或内网网关</option></select></label>
    <label><span>总结模型</span><input v-model="form.model" aria-label="总结模型" placeholder="qwen3.5-flash" :disabled="saving"/></label>
    <label class="wide"><span>API地址</span><input v-model="form.base_url" aria-label="总结 API 地址" placeholder="https://…/v1" :disabled="saving"/></label>
    <label class="wide"><span>API Key <small v-if="configured">已保存 {{masked}} · 留空保留</small></span><input v-model="form.api_key" type="password" autocomplete="new-password" :disabled="saving||form.clear_api_key" placeholder="填写总结服务密钥"/></label>
    <label v-if="configured" class="clear-key wide"><input v-model="form.clear_api_key" type="checkbox" :disabled="saving"/>清空已保存密钥</label>
   </div>
   <label class="temperature"><span>总结温度 <HelpHint label="总结温度说明">默认0.6。新提交的总结保存此温度；已有任务的温度不变。服务切换在下一次调用生效，已发出的请求不变。模型是否调整实际温度以服务提供方规则为准，思考能力保持。</HelpHint></span><input v-model.number="form.temperature" aria-label="总结温度" type="number" min="0" max="2" step="0.1" :disabled="saving"/></label>
   </template>
   <p v-if="error" class="config-error" role="alert">{{error}}</p>
   <div class="learning-footer"><span v-if="notice" class="saved-note" role="status">{{notice}}</span><button v-if="!loaded" class="ghost-button" type="button" @click="load">重新读取</button><button v-else class="primary-button" type="submit" :disabled="saving">{{saving?'保存中…':'保存总结配置'}}</button></div>
  </form>
 </section>
</template>
<style scoped>
.learning-config{max-width:820px;padding:22px;min-width:0}.learning-heading{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:22px}.learning-heading h3{margin:0 0 7px;font-size:18px;color:#172033}.current-mode{font-size:12px;color:#65778c}.loading-text{color:#65778c;font-size:13px}.source-choice{border:0;padding:0;margin:0 0 22px;display:flex;gap:10px;flex-wrap:wrap}.source-choice legend{margin-bottom:8px;color:#53657b;font-size:12px}.source-choice label{display:flex;align-items:center;gap:8px;border:1px solid #d7e1ec;border-radius:7px;padding:11px 16px;color:#52657c;font-size:13px;background:#fff;cursor:pointer}.source-choice label.selected{color:#096acb;background:#f2f8ff;border-color:#82bafa}.learning-fields{display:grid;grid-template-columns:1fr 1fr;gap:15px;margin-bottom:18px}.learning-fields label,.temperature{display:grid;gap:7px;color:#53657b;font-size:12px;min-width:0}.learning-fields .wide{grid-column:1/-1}.learning-fields input:not([type=checkbox]),.learning-fields select,.temperature input{box-sizing:border-box;width:100%;height:40px;border:1px solid #d7e1ec;border-radius:6px;padding:0 11px;background:#fff;color:#172033;font-size:13px;min-width:0}.learning-fields input:focus-visible,.learning-fields select:focus-visible,.temperature input:focus-visible{outline:2px solid #8bbcf3;outline-offset:2px}.learning-fields small{font-size:11px;color:#78889a;font-weight:normal}.learning-fields .clear-key{display:flex;align-items:center;gap:7px}.temperature{max-width:240px}.temperature span{display:flex;align-items:center;gap:6px}.learning-footer{display:flex;align-items:center;justify-content:flex-end;gap:14px;margin-top:24px;padding-top:18px;border-top:1px solid #edf1f5}.saved-note{font-size:12px;color:#17734f}.config-error{color:#a23b4b;background:#fff6f7;border:1px solid #f0c9cf;border-radius:6px;padding:10px 12px;font-size:13px;overflow-wrap:anywhere}button:disabled{opacity:.6;cursor:wait}@media(max-width:560px){.learning-config{padding:16px}.learning-fields{grid-template-columns:1fr}.learning-fields .wide{grid-column:auto}.source-choice label{flex:1;padding:11px;justify-content:center;white-space:nowrap}.learning-footer{flex-wrap:wrap}.learning-footer button{min-height:40px}}
</style>
