// useAICheckProgress 轮询题目粒度的 AI 检查进度，供生成页/批量页的分段进度展示。
// 进度数据来自 POST /ai-check/progress（登录即可，逐题校验题库范围）。
import { ref, onBeforeUnmount } from "vue";
import { api } from "./api.js";

export function useAICheckProgress({ intervalMs = 5000, timeoutMs = 10 * 60 * 1000 } = {}) {
  const progress = ref(null);
  let timer = null;
  let startedAt = 0;
  let questionIds = [];
  let finishedCallback = null;

  function stop() {
    if (timer) {
      clearInterval(timer);
      timer = null;
    }
  }

  async function tick() {
    if (!questionIds.length) return;
    // 整体超时兜底：前端停止轮询，检查仍在后台进行（任务表持久化，可稍后查看或补查）
    if (Date.now() - startedAt > timeoutMs) {
      const snapshot = progress.value || {};
      stop();
      progress.value = { ...snapshot, checking: 0, stalled: true };
      return;
    }
    try {
      const data = await api.aiCheckProgress(questionIds);
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
    }
  }

  function start(ids, onFinished) {
    stop();
    questionIds = (ids || []).filter(Boolean);
    finishedCallback = onFinished || null;
    if (!questionIds.length) return;
    startedAt = Date.now();
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
    tick();
    timer = setInterval(tick, intervalMs);
  }

  onBeforeUnmount(stop);
  return { progress, start, stop };
}
