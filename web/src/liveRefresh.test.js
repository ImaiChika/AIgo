import test from 'node:test';
import assert from 'node:assert/strict';
import { createLiveRefresh } from './liveRefresh.js';

const settle = async()=>{ for(let i=0;i<6;i++) await Promise.resolve(); };
function fixture() {
 const document=new EventTarget();document.visibilityState='visible';
 const window=new EventTarget();window.navigator={onLine:true};
 const timers=new Map();let id=0;
 const refresh=createLiveRefresh({document,window,setTimer:fn=>{timers.set(++id,fn);return id;},clearTimer:key=>timers.delete(key)});
 return {document,window,timers,refresh,tick:()=>[...timers.values()].forEach(fn=>fn())};
}

test('one timer serves all subscribers and hidden tabs make no requests',async()=>{
 const f=fixture();let a=0,b=0;const offA=f.refresh.subscribe(()=>a++),offB=f.refresh.subscribe(()=>b++);
 assert.equal(f.timers.size,1);f.tick();await settle();assert.equal(a,1);assert.equal(b,1);
 f.document.visibilityState='hidden';f.document.dispatchEvent(new Event('visibilitychange'));assert.equal(f.timers.size,0);
 f.refresh.refresh();await settle();assert.equal(a,1);
 f.document.visibilityState='visible';f.document.dispatchEvent(new Event('visibilitychange'));await settle();assert.equal(a,2);assert.equal(f.timers.size,1);
 offA();offB();assert.equal(f.timers.size,0);f.refresh.destroy();
});

test('overlapping events never duplicate a subscriber request; other subscribers keep refreshing',async()=>{
 const f=fixture();let release,slow=0,fast=0;
 f.refresh.subscribe(()=>{slow++;return new Promise(resolve=>release=resolve)});f.refresh.subscribe(()=>fast++);
 f.refresh.refresh();await settle();f.tick();f.window.dispatchEvent(new Event('focus'));await settle();
 assert.equal(slow,1);assert.equal(fast,2);
 release();await settle();f.refresh.refresh();await settle();assert.equal(slow,2);f.refresh.destroy();release();
});

test('online recovery and local/cross-tab changes refresh visible subscribers, cleanup detaches them',async()=>{
 const f=fixture();let calls=0;const off=f.refresh.subscribe(()=>calls++);
 f.window.navigator.onLine=false;f.tick();await settle();assert.equal(calls,0);
 f.window.navigator.onLine=true;f.window.dispatchEvent(new Event('online'));await settle();assert.equal(calls,1);
 f.window.dispatchEvent(new Event('aigo:refresh-navigation-badges'));await settle();assert.equal(calls,2);
 const storage=new Event('storage');storage.key='aigo:work-change';f.window.dispatchEvent(storage);await settle();assert.equal(calls,3);
 off();f.window.dispatchEvent(new Event('focus'));await settle();assert.equal(calls,3);f.refresh.destroy();
});

test('a tab opened offline resumes periodic refresh when it reconnects',async()=>{
 const f=fixture();f.window.navigator.onLine=false;let calls=0;const off=f.refresh.subscribe(()=>calls++);
 assert.equal(f.timers.size,0);f.window.navigator.onLine=true;f.window.dispatchEvent(new Event('online'));await settle();assert.equal(calls,1);assert.equal(f.timers.size,1);f.tick();await settle();assert.equal(calls,2);off();f.refresh.destroy();
});
