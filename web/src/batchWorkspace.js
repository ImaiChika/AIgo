import { upgradeBatchWorkspace, repairWorkspaceCache } from './workspaceUpgrade.js';
const PREFIX = "aigo_batch_workspace_v1:";

// Sparse snapshots contain queued/in-flight slots. Only finished units are rows;
// keep the original index so out-of-order replies cannot renumber knowledge points.
export function batchUnitRows(items = []) {
  return items.map((item, index) => ({
    idx: index + 1,
    code: item.outline_code || "—",
    topic: item.topic || "",
    ok: item.status === "ok",
    terminal: item.status === "ok" || item.status === "failed",
    error: item.status === "ok" ? "" : (item.error || "生成失败"),
  })).filter(row => row.terminal);
}

export function batchWorkspaceKey(userId) {
  return `${PREFIX}${String(userId || "anonymous")}`;
}

export function loadBatchWorkspace(userId, storage = globalThis.localStorage) {
  if (!userId || !storage) return null;
  try {
    const value = JSON.parse(storage.getItem(batchWorkspaceKey(userId)) || "null");
    if (value?.version !== 1 || value.userId !== userId || typeof value.jobId !== 'string') return null;
    const current = upgradeBatchWorkspace(value);
    repairWorkspaceCache(storage, batchWorkspaceKey(userId), value, current);
    return current;
  } catch { return null; }
}

export function saveBatchWorkspace(userId, jobId, storage = globalThis.localStorage, settings = {}) {
  if (!userId || !storage) return false;
  try {
    storage.setItem(batchWorkspaceKey(userId), JSON.stringify(upgradeBatchWorkspace({ ...loadBatchWorkspace(userId, storage), ...settings, version: 1, userId, jobId: jobId || "", savedAt: Date.now() })));
    return true;
  } catch { return false; }
}

export function clearBatchWorkspace(userId, storage = globalThis.localStorage) {
  if (!userId || !storage) return;
  try { storage.removeItem(batchWorkspaceKey(userId)); } catch { /* 隐私模式下不影响页面 */ }
}

// Preserve submission positions while more out-of-order results are added.
export function batchResultRows(result = {}, progress = {}) {
  const visible = new Map((progress?.items || []).map(item => [item.question_id, item]));
  const ok = (result?.items || []).map((item, index) => ({...item, unitNo: index + 1})).filter(item => item.status === "ok");
  const byId = new Map((result?.question_ids || []).map((id, index) => [id, ok[index]]));
  return (result?.question_ids || []).map((id, index) => {
    const item = visible.get(id) || {};
    const fallback = byId.get(id) || {};
    return { no: fallback.unitNo || index + 1, id, check: item, stem: item.stem_summary || "", outlineCode: item.outline_code || fallback.outline_code || "", topic: fallback.topic || item.question?.knowledge_points?.[0]?.topic || "", status: item.question_status || "", checking: !item.task_status || item.task_status === "pending" || item.task_status === "running", exhausted: !!item.check_failed || item.task_status === "exhausted", discarded: !!item.discarded, missing: !!item.missing, reason: item.suggestion || "" };
  });
}

export function batchPipelinePhase(job, progress) {
  if (!job?.pipeline) return "";
  const remaining = Math.max(0, Number(job.total_count || 0) - Number(job.completed || 0) - Number(job.failed || 0));
  if (remaining > 0) return Number(job.completed || 0) > 0 ? "生成与首检进行中" : "正在生成";
  const submitted = Number(job.pipeline_result?.saved || 0);
  if (submitted > 0 && (!progress || progress.stalled || progress.total < submitted || progress.checking > 0)) return "首检进行中";
  if (Number(job.completed || 0) === 0 && Number(job.failed || 0) > 0) return "生成失败";
  if (Number(progress?.exhausted || 0) > 0 && !Number(progress?.passed || 0) && !Number(progress?.discarded || 0)) return "检查失败";
  if (Number(job.failed || 0) > 0 || Number(progress?.exhausted || 0) > 0) return "部分完成";
  return "处理完成";
}

export function batchGenerationFailures(result) {
  return (result?.items || []).filter(item => item.status === "failed");
}
