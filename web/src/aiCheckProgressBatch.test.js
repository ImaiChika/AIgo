import test from "node:test";
import assert from "node:assert/strict";
import { splitQuestionIds, combineProgressResponses, checkOutcomeMessage, checkFailureItems, availableGeneratedQuestions, retryingCheckItems, checkItemState, checkProgressSnapshot, mergeGenerationResults, generationPendingMessage } from "./aiCheckProgressBatch.js";

test("large task progress is split within the API limit and recombined", () => {
  const ids = Array.from({ length: 1201 }, (_, i) => `q-${i}`);
  const chunks = splitQuestionIds(ids);
  assert.deepEqual(chunks.map(chunk => chunk.length), [500, 500, 201]);
  const combined = combineProgressResponses([
    { total: 500, counts: { passed: 400, running: 100 }, items: [{ question_id: "a" }] },
    { total: 500, counts: { passed: 500 }, items: [{ question_id: "b" }] },
    { total: 201, counts: { discarded: 1, passed: 200 }, items: [{ question_id: "c" }] },
  ]);
  assert.equal(combined.total, 1201);
  assert.deepEqual(combined.counts, { passed: 1100, running: 100, discarded: 1 });
  assert.equal(combined.items.length, 3);
});

test("outcome counts only passed questions as successful and separates check failures", () => {
  const progress = { total: 6, passed: 3, discarded: 1, exhausted: 2, items: [
    { question_id: "passed", verdict: "pass" },
    { question_id: "quality", discarded: true, verdict: "reject" },
    { question_id: "failure", discarded: true, check_failed: true, task_status: "exhausted" },
    { question_id: "old-failure", task_status: "exhausted" },
  ] };
  assert.equal(checkOutcomeMessage(progress), "本次生成 6 道：成功出题 3 道（首检通过，待整理确认） · 质量淘汰 1 道 · 检查失败 2 道（已丢弃）");
  assert.deepEqual(checkFailureItems(progress).map(item => item.question_id), ["failure", "old-failure"]);
});

test("all checks failed report zero available questions and no retry promise", () => {
  const message = checkOutcomeMessage({total: 1, passed: 0, discarded: 0, exhausted: 1});
  assert.match(message, /成功出题 0 道/);
  assert.match(message, /检查失败 1 道（已丢弃）/);
  assert.doesNotMatch(message, /可重试|全部成功/);
});

test("initial empty generation state renders without a completed result", () => {
  assert.doesNotThrow(() => checkOutcomeMessage(null));
  assert.deepEqual(checkFailureItems(null), []);
});

test("active check polling continues beyond ten minutes and stops at terminal failure", async (t) => {
  const previousStorage = globalThis.localStorage;
  globalThis.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
  t.after(() => { globalThis.localStorage = previousStorage; });
  let finished = false;
  let calls = 0;
  let callbacks = 0;
  t.mock.method(globalThis, "fetch", async () => {
    calls++;
    return { ok: true, status: 200, json: async () => ({ total: 1, counts: finished ? {exhausted: 1} : {running: 1}, items: [] }) };
  });
  const { createSSRApp } = await import("vue");
  const { renderToString } = await import("@vue/server-renderer");
  const { useAICheckProgress } = await import("./aiCheckProgress.js");
  let tracker;
  await renderToString(createSSRApp({ setup() { tracker = useAICheckProgress(); return () => "test"; } }));
  t.after(() => tracker.stop());
  t.mock.timers.enable({ apis: ["setInterval", "Date"] });
  const flush = () => new Promise(resolve => setImmediate(resolve));
  tracker.start(["q"], () => { callbacks++; });
  await flush();
  t.mock.timers.tick(20 * 60 * 1000);
  await flush();
  assert.equal(tracker.progress.value.checking, 1);
  assert.equal(tracker.progress.value.stalled, false);
  assert.equal(callbacks, 0);
  finished = true;
  t.mock.timers.tick(5000);
  await flush();
  assert.equal(tracker.progress.value.exhausted, 1);
  assert.equal(tracker.progress.value.checking, 0);
  assert.equal(callbacks, 1);
  const completedCalls = calls;
  t.mock.timers.tick(10000);
  await flush();
  assert.equal(calls, completedCalls);
});

test("one passed question can be read while other checks are running", () => {
  const questions=[{id:"q1",status:"ai_reviewed"},{id:"q2",status:"ai_draft"},{id:"q3",status:"auto_checked"}];
  const progress={checking:2,items:[{question_id:"q1",verdict:"pass"},{question_id:"q2",task_status:"running"}]};
  assert.deepEqual(availableGeneratedQuestions(questions,progress).map(q=>q.id),["q1"]);
});

test("adding an earlier result does not switch the question being read", () => {
  const merged=mergeGenerationResults(["q1","q2"],[{id:"q1",status:"ai_reviewed"}],[{id:"q2",status:"ai_reviewed"}],"q2");
  assert.equal(merged.index,1);
  assert.equal(merged.questions[merged.index].id,"q2");
});

test("progress does not call a partial visibility or unqueued draft completed", () => {
  const partial=checkProgressSnapshot({total:1,counts:{passed:1},items:[{question_id:"q1",verdict:"pass"}]},3);
  assert.equal(partial.total,3);assert.equal(partial.passed,1);assert.equal(partial.checking,2);
  const unqueued=checkProgressSnapshot({total:1,items:[{question_id:"q",question_status:"ai_draft",task_status:""}]},1);
  assert.equal(unqueued.checking,1);
  const removed=checkProgressSnapshot({total:1,items:[{question_id:"q",missing:true}]},1);
  assert.equal(removed.checking,0);
});

test("automatic retry status is distinct from exhausted discard", () => {
  const items=retryingCheckItems({items:[{question_id:"waiting",task_status:"pending",attempts:1,last_error:"timeout"},{question_id:"retrying",task_status:"running",attempts:2},{question_id:"failed",task_status:"exhausted",attempts:3}]});
  assert.deepEqual(items.map(item=>item.question_id),["waiting","retrying"]);
});

test("check states distinguish first call, backoff, queued retry, active retry and terminal outcomes", () => {
  const now=Date.now();
  assert.equal(checkItemState({task_status:"pending",attempts:0},now).label,"等待首次检查");
  assert.equal(checkItemState({task_status:"running",attempts:1,max_attempts:5},now).detail,"正在进行第 1/5 次检查。");
  const retry={task_status:"pending",attempts:1,max_attempts:5,last_error:"JSON parse error at https://private.example/token",next_attempt_at:new Date(now+15000).toISOString()};
  assert.match(checkItemState(retry,now).detail,/约 15 秒后自动重试（已尝试 1\/5 次）/);
  assert.match(checkItemState(retry,now+15001).detail,/等待重试名额/);
  assert.match(checkItemState({...retry,next_attempt_at:undefined},now).detail,/等待重试名额/);
  assert.doesNotMatch(checkItemState(retry,now).detail,/https|private|token/);
  assert.equal(checkItemState({...retry,task_status:"running",attempts:2},now).label,"自动重试 2/5");
  for (const item of [{task_status:"succeeded",verdict:"pass"},{task_status:"exhausted",check_failed:true},{task_status:"succeeded",discarded:true,verdict:"reject"},{missing:true}]) {
    assert.equal(checkItemState({...retry,...item},now).detail,"");
  }
  assert.equal(checkItemState({task_status:"succeeded",verdict:"issues_found"}).label,"质量淘汰");
  assert.deepEqual(retryingCheckItems({items:[{task_status:"pending",attempts:1},{task_status:"running",attempts:1,last_error:"stale"}]}).map(i=>i.task_status),["pending"]);
});

test("generation retry is explained without exposing provider error details", () => {
  const message=generationPendingMessage({status:"running",attempts:2,max_attempts:3,error:"https://internal.example/api timeout"});
  assert.match(message,/自动重试/);assert.match(message,/第 2\/3 次/);assert.doesNotMatch(message,/https|internal/);
});

test("restoring a saved checking snapshot resumes polling and reaches the real result", async (t) => {
  const previousStorage = globalThis.localStorage;
  globalThis.localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
  t.after(() => { globalThis.localStorage = previousStorage; });
  t.mock.method(globalThis, "fetch", async () => ({ ok: true, status: 200, json: async () => ({ total: 3, counts: { passed: 3 }, items: [] }) }));
  const { createSSRApp } = await import("vue");
  const { renderToString } = await import("@vue/server-renderer");
  const { useAICheckProgress } = await import("./aiCheckProgress.js");
  let tracker;
  await renderToString(createSSRApp({ setup() { tracker = useAICheckProgress(); return () => "test"; } }));
  t.after(() => tracker.stop());
  tracker.progress.value = { total: 3, checking: 3, passed: 0 };
  assert.equal(tracker.isTracking.value, false);
  let callbacks = 0;
  tracker.start(["q1", "q2", "q3"], () => { callbacks++; }, { preserve: true });
  assert.equal(tracker.isTracking.value, true);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(tracker.progress.value.passed, 3);
  assert.equal(tracker.progress.value.checking, 0);
  assert.equal(callbacks, 1);
  assert.equal(tracker.isTracking.value, false);
});
