// useAICheckProgress 轮询题目粒度的 AI 检查进度，供生成页/批量页的分段进度展示。
// 进度数据来自 POST /ai-check/progress（登录即可，逐题校验题库范围）。
import { ref, onBeforeUnmount } from "vue";
import { api } from "./api.js";
import { splitQuestionIds, combineProgressResponses } from "./aiCheckProgressBatch.js";

export function useAICheckProgress({ intervalMs = 5000, timeoutMs = 10 * 60 * 1000 } = {}) {
  const progress = ref(null);
  let timer = null;
  let startedAt = 0;
  let questionIds = [];
  let finishedCallback = null;
  let requestInFlight = false;
  let generation = 0;

  function stop() {
    generation++;
    if (timer) {
      clearInterval(timer);
      timer = null;
    }
  }

  async function tick() {
	if (!questionIds.length || requestInFlight) return;
	// 整体超时兜底：前端停止轮询，检查仍在后台按同一任务自动重试；耗尽后阻断流程。
    if (Date.now() - startedAt > timeoutMs) {
      const snapshot = progress.value || {};
      stop();
      progress.value = { ...snapshot, checking: 0, stalled: true };
      return;
    }
    try {
	  requestInFlight = true;
	  const current = generation;
	  const responses = [];
	  const chunks = splitQuestionIds(questionIds);
	  for (let index = 0; index < chunks.length; index += 4) {
	    responses.push(...await Promise.all(chunks.slice(index, index + 4).map(chunk => api.aiCheckProgress(chunk))));
	    if (current !== generation) return;
	  }
	  const data = combineProgressResponses(responses);
      const counts = data.counts || {};
      const checking = (counts.pending || 0) + (counts.running || 0);
      progress.value = {
        total: data.total ?? questionIds.length,
        pending: counts.pending || 0,
        running: counts.running || 0,
        passed: counts.passed || 0,
        issues: counts.issues || 0,
        exhausted: counts.exhausted || 0,
        noTask: counts.no_task || 0,
        discarded: counts.discarded || 0,
        checking,
        stalled: false,
        // 逐题明细（含淘汰题的原因），供生成页展示"本次生题具体情况"
        items: data.items || [],
      };
      if (checking === 0) {
        stop();
        if (finishedCallback) finishedCallback(progress.value);
      }
    } catch (e) {
      // 单次轮询失败静默，等待下一轮
    } finally { requestInFlight = false; }
  }

  function start(ids, onFinished, { preserve = false } = {}) {
    stop();
    questionIds = (ids || []).filter(Boolean);
    finishedCallback = onFinished || null;
    if (!questionIds.length) return;
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
    tick();
    timer = setInterval(tick, intervalMs);
  }

  onBeforeUnmount(stop);
  return { progress, start, stop };
}
