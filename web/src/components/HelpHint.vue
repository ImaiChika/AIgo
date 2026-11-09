<script>let activeHelpClose=null;</script>
<script setup>
import { ref, nextTick, onMounted, onBeforeUnmount, onDeactivated, useId } from 'vue';
const props = defineProps({ text: { type:String, default:'' }, label: { type:String, default:'查看说明' } });
const anchor=ref(null), bubble=ref(null), open=ref(false), target=ref(null), position=ref({});
const tooltipId=`help-${useId()}`;
let disposed=false;
function place(){
  if(!open.value||!anchor.value||!bubble.value)return;
  const rect=anchor.value.getBoundingClientRect(), box=bubble.value.getBoundingClientRect();
  const width=window.innerWidth,height=window.innerHeight,gap=8,pad=12;
  if(rect.bottom<0||rect.top>height){hide();return;}
  const left=Math.max(pad,Math.min(rect.left+rect.width/2-box.width/2,width-box.width-pad));
  const top=rect.bottom+gap+box.height<=height-pad?rect.bottom+gap:Math.max(pad,rect.top-gap-box.height);
  position.value={left:`${left}px`,top:`${top}px`};
}
function show(){
  if(disposed)return;
  if(activeHelpClose&&activeHelpClose!==hide)activeHelpClose();
  activeHelpClose=hide;
  target.value=anchor.value?.closest('dialog[open]')||document.body;
  open.value=true;
  nextTick(()=>{if(!disposed)place();});
}
function hide(){open.value=false;if(activeHelpClose===hide)activeHelpClose=null;}
function touchLayout(){return window.matchMedia?.('(hover: none)').matches===true;}
function hover(){if(!touchLayout())show();}
function focus(){if(anchor.value?.matches(':focus-visible'))show();}
function click(event){if(touchLayout()||event.detail===0){if(open.value)hide();else show();}}
function outside(event){if(open.value&&!anchor.value?.contains(event.target))hide();}
function escape(event){if(open.value&&event.key==='Escape'){hide();event.preventDefault();event.stopPropagation();}}
onMounted(()=>{window.addEventListener('resize',place);window.addEventListener('scroll',place,true);document.addEventListener('pointerdown',outside);document.addEventListener('keydown',escape,true);});
onDeactivated(hide);
onBeforeUnmount(()=>{disposed=true;hide();window.removeEventListener('resize',place);window.removeEventListener('scroll',place,true);document.removeEventListener('pointerdown',outside);document.removeEventListener('keydown',escape,true);});
</script>
<template>
<span class="ui-help-hint"><button ref="anchor" class="ui-help-button" type="button" :aria-label="label" :aria-describedby="open?tooltipId:undefined" :aria-expanded="open" @mouseenter="hover" @mouseleave="hide" @focus="focus" @blur="hide" @click.stop.prevent="click">?</button><Teleport v-if="open&&target" :to="target"><div :id="tooltipId" ref="bubble" role="tooltip" class="ui-help-bubble" :style="position"><slot>{{text}}</slot></div></Teleport></span>
</template>
<style scoped>
.ui-help-hint{display:inline-flex;vertical-align:middle;align-items:center;margin:0 3px;line-height:1}.ui-help-hint>.ui-help-button{position:relative;display:inline-grid;place-items:center;width:20px;height:20px;min-width:20px;max-width:20px;min-height:20px;max-height:20px;flex:0 0 20px;margin:0;padding:0;border:0;border-radius:50%;background:transparent;color:#7b91a8;font-family:inherit;font-weight:600;font-size:11px;line-height:1;cursor:help;box-shadow:none}.ui-help-button::before{content:'';position:absolute;inset:3px;border:1px solid currentColor;border-radius:50%;pointer-events:none}.ui-help-button:hover{color:#246cae;background:#eef5fc}.ui-help-button:focus-visible{outline:2px solid #277cc1;outline-offset:2px}.ui-help-bubble{position:fixed;z-index:10000;box-sizing:border-box;width:max-content;max-width:min(340px,calc(100vw - 24px));max-height:calc(100dvh - 24px);overflow:auto;padding:11px 14px;border:1px solid #dce6f0;border-radius:7px;background:#f8fbff;color:#405872;box-shadow:0 6px 22px #163c5c22;font-size:12px;font-weight:400;line-height:1.8;white-space:pre-wrap;overflow-wrap:anywhere;text-align:left;pointer-events:none}
</style>
