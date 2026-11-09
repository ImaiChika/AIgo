import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { registerHooks } from 'node:module';
import { parse,compileScript } from '@vue/compiler-sfc';
import { JSDOM } from 'jsdom';
registerHooks({load(url,ctx,next){if(!url.endsWith('.vue'))return next(url,ctx);const{descriptor}=parse(readFileSync(fileURLToPath(url),'utf8'));return{format:'module',source:compileScript(descriptor,{id:url,inlineTemplate:true}).content,shortCircuit:true};}});
const dom=new JSDOM('<!doctype html><html><body></body></html>',{url:'http://localhost/'});
for(const key of ['window','document','Document','Element','HTMLElement','SVGElement','Node','Event','KeyboardEvent','localStorage','sessionStorage'])globalThis[key]=dom.window[key];
window.matchMedia=()=>({matches:false});
const{mount,flushPromises}=await import('@vue/test-utils'),{nextTick,defineComponent,h,ref,KeepAlive}=await import('vue');
const{default:Help}=await import('./components/HelpHint.vue');const settle=async()=>{await flushPromises();await nextTick();};
function page(t,options={}){if(!options.attachTo)document.body.innerHTML='';const w=mount(Help,{props:{label:'知识库使用说明',text:'只采用已匹配的启用卡。'},attachTo:document.body,...options});t.after(()=>w.unmount());return w;}
const tooltip=()=>document.querySelector('[role="tooltip"]');
test('help stays compact until hover and disappears immediately on mouse leave',async t=>{const w=page(t);assert.equal(tooltip(),null);assert.equal(w.get('button').text(),'?');await w.get('button').trigger('mouseenter');await settle();assert.match(tooltip().textContent,/匹配/);assert.equal(w.get('button').attributes('aria-describedby'),tooltip().id);await w.get('button').trigger('mouseleave');await settle();assert.equal(tooltip(),null);});
test('keyboard focus opens help and Escape or blur dismisses it',async t=>{const w=page(t);w.get('button').element.focus();await settle();assert.ok(tooltip());document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape'}));await settle();assert.equal(tooltip(),null);await w.get('button').trigger('focus');await settle();assert.ok(tooltip());await w.get('button').trigger('blur');await settle();assert.equal(tooltip(),null);});
test('touch can toggle help and an outside pointer dismisses it',async t=>{t.mock.method(window,'matchMedia',()=>({matches:true}),{getter:false});const w=page(t);await w.get('button').trigger('click');await settle();assert.ok(tooltip());document.body.dispatchEvent(new Event('pointerdown',{bubbles:true}));await settle();assert.equal(tooltip(),null);});
test('help inside a native dialog is rendered in that dialog and unmount removes it',async t=>{const dialog=document.createElement('dialog');dialog.setAttribute('open','');document.body.append(dialog);const w=page(t,{attachTo:dialog});await w.get('button').trigger('mouseenter');await settle();assert.equal(tooltip().parentElement,dialog);w.unmount();assert.equal(tooltip(),null);});
test('popup clamps to small viewports without covering form semantics',async t=>{const w=page(t);Object.defineProperty(window,'innerWidth',{configurable:true,value:390});Object.defineProperty(window,'innerHeight',{configurable:true,value:844});w.get('button').element.getBoundingClientRect=()=>({left:372,right:390,top:800,bottom:824,width:18,height:24});await w.get('button').trigger('mouseenter');await settle();assert.equal(w.get('button').attributes('type'),'button');assert.ok(parseFloat(tooltip().style.left)>=12);assert.ok(parseFloat(tooltip().style.top)>=12);});
test('only the currently inspected question mark keeps a tooltip open',async t=>{const one=page(t);const two=mount(Help,{props:{text:'第二条说明'},attachTo:document.body});t.after(()=>two.unmount());await one.get('button').trigger('mouseenter');await settle();await two.get('button').trigger('mouseenter');await settle();assert.equal(document.querySelectorAll('[role="tooltip"]').length,1);assert.match(tooltip().textContent,/第二条/);});
test('Escape consumed by open help does not close its surrounding dialog',async t=>{const w=page(t);let passed=0;w.element.addEventListener('keydown',()=>passed++);await w.get('button').trigger('mouseenter');await settle();w.get('button').element.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));await settle();assert.equal(tooltip(),null);assert.equal(passed,0);});

test('switching away from a cached generation view removes its teleported help',async t=>{
 document.body.innerHTML='';const visible=ref(true);
 const Host=defineComponent({setup:()=>()=>h(KeepAlive,null,{default:()=>visible.value?h(Help,{text:'缓存页面说明'}):h('div','其他页面')})});
 const w=mount(Host,{attachTo:document.body});t.after(()=>w.unmount());
 await w.get('button').trigger('mouseenter');await settle();assert.ok(tooltip());
 visible.value=false;await settle();assert.equal(tooltip(),null);
 visible.value=true;await settle();assert.equal(tooltip(),null);
 await w.get('button').trigger('mouseenter');await settle();assert.match(tooltip().textContent,/缓存页面说明/);
});
