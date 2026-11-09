import { upgradeGenerationWorkspace, repairWorkspaceCache } from './workspaceUpgrade.js';
const PREFIX = "aigo_generation_workspace_v1:";

export const generationQuotaFields = [
  { key: "single_max_questions", label: "单次单题生成", unit: "道", min: 1, max: 20, help: "老师每次点击「生成」可提交的题数。" },
  { key: "batch_max_questions", label: "批量任务总量", unit: "道", min: 1, max: 500, help: "按「要点数 × 每点题数」计算，超过上限整单拒绝。" },
  { key: "single_active_per_user", label: "每人未完成单题任务", unit: "个", min: 1, max: 10, help: "等待中与执行中的单题任务合计。" },
  { key: "batch_active_per_user", label: "每人活跃批量任务", unit: "个", min: 1, max: 5, help: "进行中的批量任务合计；失败项重跑也受此限制。" },
  { key: "global_pending_questions", label: "全站队列占用题数", unit: "道", min: 20, max: 5000, help: "单题预留、批量待生成、批量导入预留与首次 AI 检查共用，同题跨阶段交叠时去重。" },
  { key: "generation_concurrency", label: "单题生成并发", unit: "路", min: 1, max: 32, concurrency: true, runtime: "generation", help: "同时处理的单题任务数；一个任务可包含多道题。" },
  { key: "batch_concurrency", label: "批量生成并发", unit: "路", min: 1, max: 32, concurrency: true, runtime: "batch", help: "所有批量任务共享的同时生成题数。" },
  { key:"learning_concurrency",label:"AI总结并发",unit:"路",min:1,max:8,concurrency:true,runtime:"learning_analysis",optional:true,help:"全站同时进行的AI总结数；每位老师仍最多一个在途任务，不改变总结内容、思考或120秒执行预算。" },
  { key: "ai_check_concurrency", label: "AI 首检并发", unit: "路", min: 1, max: 32, concurrency: true, runtime: "ai_check", help: "同时处理的首次质量检查数，不改变检查标准。" },
];

export function createGenerationQuotaEditor() {
  return { draft: {}, saved: {}, baseRevision: 0, conflict: false };
}

function cloneQuota(quota) {
  return { ...quota, model_tools: {
    web_search_enabled: false, web_extractor_enabled: false, ...quota.model_tools,
  } };
}

export function setGenerationModelTool(state, key, enabled) {
  if (!["web_search_enabled", "web_extractor_enabled"].includes(key) || typeof enabled !== "boolean") throw new Error("模型工具设置无效");
  const draft = cloneQuota(state.draft);
  if (key === "web_extractor_enabled" && enabled && !draft.model_tools.web_search_enabled) throw new Error("网页抓取必须先开启联网搜索");
  draft.model_tools[key] = enabled;
  if (!draft.model_tools.web_search_enabled) draft.model_tools.web_extractor_enabled = false;
  return { ...state, draft };
}

export function generationQuotaDirty(state) {
  return generationQuotaFields.filter(field=>!field.optional||Object.hasOwn(state.saved,field.key)).some(field => String(state.draft[field.key] ?? "") !== String(state.saved[field.key] ?? ""))
    || ["web_search_enabled", "web_extractor_enabled"].some(key => (state.draft.model_tools?.[key] ?? false) !== (state.saved.model_tools?.[key] ?? false));
}

export function refreshGenerationQuotaEditor(state, quota) {
  const keepDraft = generationQuotaDirty(state) || state.conflict;
  return {
    draft: keepDraft ? state.draft : cloneQuota(quota),
    saved: cloneQuota(quota),
    baseRevision: keepDraft ? state.baseRevision : Number(quota.revision),
    conflict: keepDraft && (state.conflict || Number(quota.revision) !== state.baseRevision),
  };
}

export function resetGenerationQuotaEditor(state) {
  return { draft: cloneQuota(state.saved), saved: cloneQuota(state.saved), baseRevision: Number(state.saved.revision), conflict: false };
}

export function generationQuotaUpdate(state) {
  if (!Number.isSafeInteger(state.baseRevision) || state.baseRevision < 1) throw new Error("请先载入最新配置");
  const next = { revision: state.baseRevision };
  for (const field of generationQuotaFields) {
    if(field.optional&&!Object.hasOwn(state.saved,field.key))continue;
    const value = Number(state.draft[field.key]);
    if (!Number.isInteger(value) || value < field.min || value > field.max) throw new Error(`${field.label}应为 ${field.min}–${field.max} 的整数`);
    next[field.key] = value;
  }
  if (next.global_pending_questions < Math.max(next.single_max_questions, next.batch_max_questions)) throw new Error("全站队列占用题数不能小于单次单题或批量上限");
  const tools = cloneQuota(state.draft).model_tools;
  for (const value of Object.values(tools)) if (typeof value !== "boolean") throw new Error("模型联网开关必须为布尔值");
  if (tools.web_extractor_enabled && !tools.web_search_enabled) throw new Error("网页抓取必须先开启联网搜索");
  next.model_tools = { ...tools };
  return next;
}

export function generationWorkspaceKey(userId) {
  return `${PREFIX}${String(userId || "anonymous")}`;
}

export function loadGenerationWorkspace(userId, storage = globalThis.localStorage) {
  if (!userId || !storage) return null;
  try {
    const value = JSON.parse(storage.getItem(generationWorkspaceKey(userId)) || "null");
    if (!value || value.version !== 1 || value.userId !== userId) return null;
    const current = upgradeGenerationWorkspace(value);
    repairWorkspaceCache(storage, generationWorkspaceKey(userId), value, current);
    return current;
  } catch {
    return null;
  }
}

export function saveGenerationWorkspace(userId, state, storage = globalThis.localStorage) {
  if (!userId || !storage) return false;
  try {
    storage.setItem(generationWorkspaceKey(userId), JSON.stringify(upgradeGenerationWorkspace({
      ...state,
      version: 1,
      userId,
      savedAt: Date.now(),
    })));
    return true;
  } catch {
    return false;
  }
}

export function clearGenerationWorkspace(userId, storage = globalThis.localStorage) {
  if (!userId || !storage) return;
  try {
    storage.removeItem(generationWorkspaceKey(userId));
  } catch {
    // 隐私模式或禁用本地存储时，清理失败不应阻断退出登录。
  }
}
