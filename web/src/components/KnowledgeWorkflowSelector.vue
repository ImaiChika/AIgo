<script setup>
import { computed, watch } from 'vue';
import { currentUser } from '../auth.js';
import { collectionCapabilities } from '../knowledgeCollection.js';
import HelpHint from './HelpHint.vue';
const props=defineProps({modelValue:{type:String,default:'direct'},knowledgePoints:{type:Array,default:()=>[]},presentation:{type:Object,default:null}});
const emit=defineEmits(['update:modelValue','update:presentation']);
const allowed=computed(()=>collectionCapabilities(currentUser.value?.permissions).workflow);
watch(()=>props.modelValue,mode=>{if(mode!=='knowledge')emit('update:presentation',null);});
watch(()=>[currentUser.value?.id,currentUser.value?.role],()=>emit('update:presentation',null));
</script>
<template>
 <section v-if="allowed||modelValue==='knowledge'" class="workflow-choice">
  <div class="workflow-choice-row"><label class="workflow-check"><input type="checkbox" aria-label="使用知识库工作流" :checked="modelValue==='knowledge'" :disabled="!allowed&&modelValue!=='knowledge'" @change="emit('update:modelValue',$event.target.checked?'knowledge':'direct')"/>使用知识库工作流</label><HelpHint label="知识库工作流说明">自动匹配本次考点的基础资料及本人已勾选的私人、共享知识卡；无匹配资料时照常出题，无需预览。提交时冻结可用资料版本，仍执行原AI首检。</HelpHint></div>
  <p v-if="modelValue==='knowledge'&&!allowed" class="workflow-error" role="alert">当前身份已无知识库使用权限，请联系管理员，或取消勾选。</p>
 </section>
</template>
<style scoped>
.workflow-choice{border:1px solid #dce6ef;background:#f8fbfe;border-radius:7px;padding:12px 14px;margin:12px 0;color:#425a73;font-size:13px;min-width:0}.workflow-choice-row{display:flex;align-items:center;justify-content:space-between;gap:8px}.workflow-choice .workflow-check{display:inline-flex;align-items:center;gap:8px;font-weight:600;margin:0;min-width:0}.workflow-check input{width:16px;height:16px;flex:0 0 16px;margin:0;accent-color:#1385f8}.workflow-error{color:#b91c1c;margin:8px 0 0;line-height:1.7}
</style>
