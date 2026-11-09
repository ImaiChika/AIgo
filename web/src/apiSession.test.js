import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';

async function loadAPI() {
  const source = await readFile(new URL('./api.js', import.meta.url), 'utf8');
  const auth = `export const getToken=()=>globalThis.aigoTestAuth.token; export const clearAuth=()=>{globalThis.aigoTestAuth.clears++;globalThis.aigoTestAuth.token='';}; export const setAuth=(token,user)=>{globalThis.aigoTestAuth.token=token;};`;
  const stubURL = 'data:text/javascript;base64,' + Buffer.from(auth).toString('base64');
  const isolated = source.replace('"./auth.js"', JSON.stringify(stubURL));
  return (await import('data:text/javascript;base64,' + Buffer.from(isolated).toString('base64'))).api;
}

test('late unauthorized responses cannot log out a newly signed-in account or identity', async () => {
  const originalFetch = globalThis.fetch, originalWindow = globalThis.window;
  try {
    for (const message of ['登录已过期', '请重新选择身份']) {
      globalThis.aigoTestAuth = { token: 'old-session', clears: 0 };
      globalThis.window = { location: { href: '/my', reload() { throw new Error('stale recovery must not reload'); } } };
      let release, requests = 0;
      globalThis.fetch = () => { requests++; return new Promise(resolve => { release = resolve; }); };
      const api = await loadAPI();
      const pending = api.me();
      globalThis.aigoTestAuth.token = 'new-session';
      release(new Response(JSON.stringify({ error: message }), { status: 401 }));
      await assert.rejects(pending);
      assert.equal(globalThis.aigoTestAuth.token, 'new-session');
      assert.equal(globalThis.aigoTestAuth.clears, 0);
      assert.equal(window.location.href, '/my');
      assert.equal(requests, 1, 'old identity cannot start recovery using the new session');
    }
  } finally { globalThis.fetch=originalFetch; globalThis.window=originalWindow; delete globalThis.aigoTestAuth; }
});

test('successful late responses cannot overwrite a new identity and concurrent GETs are merged',async()=>{
 const originalFetch=globalThis.fetch;
 try {
  globalThis.aigoTestAuth={token:'before',clears:0};let release,calls=0;
  globalThis.fetch=()=>{calls++;return new Promise(resolve=>release=resolve)};
  const api=await loadAPI();const first=api.mySummary(),second=api.mySummary();
  assert.equal(calls,1,'shared summary must use one in-flight request');
  globalThis.aigoTestAuth.token='after';release(new Response(JSON.stringify({generated_count:99}),{status:200}));
  const results=await Promise.allSettled([first,second]);assert.ok(results.every(r=>r.status==='rejected'));
 }finally{globalThis.fetch=originalFetch;delete globalThis.aigoTestAuth;}
});

test('cancelled permission responses retain the active login', async () => {
 const originalFetch=globalThis.fetch,originalWindow=globalThis.window;
 try {
  globalThis.aigoTestAuth={token:'active-session',clears:0};
  globalThis.window={location:{href:'/my'}};
  globalThis.fetch=async()=>new Response(JSON.stringify({error:'请求已取消'}),{status:499});
  const api=await loadAPI();
  await assert.rejects(api.mySummary(),error=>error.status===499);
  assert.equal(globalThis.aigoTestAuth.token,'active-session');
  assert.equal(globalThis.aigoTestAuth.clears,0);
  assert.equal(window.location.href,'/my');
 } finally {globalThis.fetch=originalFetch;globalThis.window=originalWindow;delete globalThis.aigoTestAuth;}
});

test('a mutation prevents new readers from joining an earlier read and old cleanup cannot evict a newer read',async()=>{
 const originalFetch=globalThis.fetch;
 try{
  globalThis.aigoTestAuth={token:'same-session',clears:0};const gets=[];
  globalThis.fetch=(url,options)=>options.method==='POST'?Promise.resolve(new Response('{}',{status:200})):new Promise(resolve=>gets.push(resolve));
  const api=await loadAPI(),old=api.mySummary();await api.submitReviewBatch(['q1'],'flow');const fresh=api.mySummary();
  assert.equal(gets.length,2);gets[0](new Response('{"new_questions_count":0}',{status:200}));await old;
  const joined=api.mySummary();assert.equal(gets.length,2);gets[1](new Response('{"new_questions_count":1}',{status:200}));assert.equal((await fresh).new_questions_count,1);assert.equal((await joined).new_questions_count,1);
 }finally{globalThis.fetch=originalFetch;delete globalThis.aigoTestAuth;}
});
