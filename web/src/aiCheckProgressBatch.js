export function splitQuestionIds(ids, size = 500) {
  const chunks = [];
  for (let index = 0; index < ids.length; index += size) chunks.push(ids.slice(index, index + size));
  return chunks;
}

export function combineProgressResponses(responses) {
  const counts = {};
  const items = [];
  let total = 0;
  for (const response of responses) {
    total += Number(response.total || 0);
    for (const [key, value] of Object.entries(response.counts || {})) counts[key] = (counts[key] || 0) + Number(value || 0);
    items.push(...(response.items || []));
  }
  return responses.some(r => r.provider_waiting) ? { total, counts, items, provider_waiting: true } : { total, counts, items };
}

// 成功只统计已通过首次检查的题；生成完成不等于进入题库。
export function checkOutcomeMessage(progress = {}) {
  progress = progress || {};
  return `本次生成 ${progress.total || 0} 道：成功出题 ${progress.passed || 0} 道（首检通过，待整理确认） · 质量淘汰 ${progress.discarded || 0} 道 · 检查失败 ${progress.exhausted || 0} 道（已丢弃）`;
}

export function checkFailureItems(progress = {}) {
  progress = progress || {};
  return (progress.items || []).filter(item => item.check_failed || item.task_status === "exhausted");
}

export function availableGeneratedQuestions(questions = [], progress) {
  const items = new Map((progress?.items || []).map(item => [item.question_id, item]));
  return questions.filter(question => {
    const item = items.get(question.id);
    if (item?.discarded || item?.check_failed || item?.missing) return false;
    if (["ai_draft", "auto_checked"].includes(question.status)) return false;
    return item?.verdict === "pass" || ["ai_reviewed", "reviewing", "published", "rejected", "archived", "conflict"].includes(question.status);
  });
}

export function retryingCheckItems(progress) {
  return (progress?.items || []).filter(item =>
    (item.task_status === "pending" && item.attempts > 0) ||
    (item.task_status === "running" && item.attempts > 1));
}

// Attempts count calls already started, including the first call. A retry deadline
// only means the task may run then; it can still wait for a free check slot.
export function checkItemState(item = {}, now = Date.now()) {
  if (item.check_failed || item.task_status === "exhausted") return { label: "检查失败，已丢弃", cls: "failed", detail: "" };
  if (item.discarded || ["issues_found", "reject"].includes(item.verdict)) return { label: "质量淘汰", cls: "failed", detail: "" };
  if (item.missing) return { label: "已删除", cls: "muted", detail: "" };
  if (item.verdict === "pass" || item.task_status === "succeeded") return { label: "已通过", cls: "passed", detail: "" };
  const attempts = Number(item.attempts) || 0;
  const max = Number(item.max_attempts) || 3;
  const error = item.last_error || "";
  const reason = /超时|timeout|deadline exceeded/i.test(error) ? "上次检查超时"
    : /json|解析|格式/i.test(error) ? "上次检查结果格式异常" : "上次检查未完成";
  if (item.task_status === "running") {
    return attempts > 1
      ? { label: `自动重试 ${attempts}/${max}`, cls: "retrying", detail: `${reason}；正在进行第 ${attempts}/${max} 次检查，完成后自动更新。` }
      : { label: "首次检查中", cls: "checking", detail: `正在进行第 ${attempts || 1}/${max} 次检查。` };
  }
  if (item.task_status === "pending" && attempts > 0) {
    const deadline = item.next_attempt_at ? new Date(item.next_attempt_at).getTime() : 0;
    const seconds = Math.max(0, Math.ceil((deadline - now) / 1000));
    const waiting = seconds > 0 ? `约 ${seconds} 秒后自动重试` : "等待重试名额，系统将自动继续";
    return { label: "等待自动重试", cls: "retrying", detail: `${reason}；${waiting}（已尝试 ${attempts}/${max} 次）。` };
  }
  return { label: "等待首次检查", cls: "checking", detail: "获得检查名额后自动开始，通过后展示题目内容。" };
}

export function checkProgressSnapshot(data, requestedTotal) {
  const counts = data.counts || {};
  const items = data.items || [];
  const unqueued = items.filter(item => !item.task_status && !item.discarded && !item.missing && ["ai_draft", "auto_checked"].includes(item.question_status)).length;
  const unresolved = Math.max(0, requestedTotal - Number(data.total ?? requestedTotal)) + Math.max(unqueued, Number(counts.no_task || 0));
  return { providerWaiting: !!data.provider_waiting, total: requestedTotal, pending: counts.pending || 0, running: counts.running || 0, passed: counts.passed || 0, issues: counts.issues || 0, exhausted: counts.exhausted || 0, noTask: counts.no_task || 0, discarded: counts.discarded || 0, unresolved, checking: (counts.pending || 0) + (counts.running || 0) + unresolved, stalled: false, items };
}

export function mergeGenerationResults(ids, received, cached, selectedId) {
  const previous = new Map(cached.map(question => [question.id, question]));
  const incoming = new Map(received.map(question => [question.id, question]));
  const questions = ids.map(id => incoming.get(id) || previous.get(id)).filter(Boolean);
  const selected = questions.findIndex(question => question.id === selectedId);
  return { questions, index: Math.max(0, selected) };
}

export function generationPendingMessage(run, restoring = false) {
  if (String(run?.error || "").includes("保存生成结果及首检任务失败")) {
    return `生成结果暂时无法保存，系统正在自动恢复（第 ${Math.max(1, Number(run.attempts || 0))}/${run.max_attempts || 3} 次尝试）；请勿重复提交。`;
  }
  if (Number(run?.attempts || 0) > 1 || run?.error) {
    const attempt = Math.max(1, Number(run.attempts || 0));
    return `上次生成调用未完成，系统正在自动重试（第 ${attempt}/${run.max_attempts || 3} 次尝试）；可继续使用其他页面，任务会在后台运行`;
  }
  return restoring ? "已恢复命题任务，系统仍在生成试题" : "任务已提交，正在生成试题...";
}

export function generationFailureMessage(message) {
  if (String(message || "").includes("保存生成结果及首检任务失败")) return "系统暂时无法保存本次生成结果，此次任务已结束；可稍后重新生成新题。";
  return message || "生成失败，请重新提交";
}
