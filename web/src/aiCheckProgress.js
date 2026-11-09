// useAICheckProgress 轮询题目粒度的 AI 检查进度，供生成页/批量页的分段进度展示。
// 进度数据来自 POST /ai-check/progress（登录即可，逐题校验题库范围）。
import { ref, onBeforeUnmount } from "vue";
import { api } from "./api.js";
import { splitQuestionIds, combineProgressResponses, checkProgressSnapshot } from "./aiCheckProgressBatch.js";

export function useAICheckProgress({ intervalMs = 5000, timeoutMs = 0 } = {}) {
  const progress = ref(null);
  const isTracking = ref(false);
  let timer = null;
  let startedAt = 0;
  let questionIds = [];
  let finishedCallback = null;
  let requestInFlight = false;
  let generation = 0;

  function stop() {
    isTracking.value = false;
    generation++;
    if (timer) {
      clearInterval(timer);
      timer = null;
    }
  }

  async function tick() {
	if (!questionIds.length || requestInFlight) return;
	// 整批可能有排队和多次长检查，默认持续跟踪到终态；轮询预算不是调用超时。
    if (timeoutMs > 0 && Date.now() - startedAt > timeoutMs) {
      const snapshot = progress.value || {};
      stop();
      progress.value = { ...snapshot, stalled: true };
      return;
    }
	const current = generation;
    try {
	  requestInFlight = true;
	  const responses = [];
	  const chunks = splitQuestionIds(questionIds);
	  for (let index = 0; index < chunks.length; index += 4) {
	    responses.push(...await Promise.all(chunks.slice(index, index + 4).map(chunk => api.aiCheckProgress(chunk))));
	    if (current !== generation) return;
	  }
	  const data = combineProgressResponses(responses);
      progress.value = checkProgressSnapshot(data, questionIds.length);
      if (progress.value.checking === 0) {
        stop();
        if (finishedCallback) finishedCallback(progress.value);
      }
    } catch (e) {
      if (current !== generation) return;
      progress.value = { ...(progress.value || {}), connectionError: "暂时无法获取检查进度，正在重连；已显示的结果会保留。" };
    } finally { requestInFlight = false; }
  }

  function start(ids, onFinished, { preserve = false } = {}) {
    stop();
    questionIds = (ids || []).filter(Boolean);
    finishedCallback = onFinished || null;
    if (!questionIds.length) return;
    isTracking.value = true;
    startedAt = Date.now();
    if (!preserve || !progress.value) {
      progress.value = {
        total: questionIds.length,
        pending: questionIds.length,
        running: 0,
        passed: 0,
        issues: 0,
        exhausted: 0,
        noTask: 0,
        checking: questionIds.length,
        stalled: false,
      };
    }
    timer = setInterval(tick, intervalMs);
    tick();
  }

  onBeforeUnmount(stop);
  return { progress, isTracking, start, stop };
}
