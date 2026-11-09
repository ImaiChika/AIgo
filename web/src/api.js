import { getToken, clearAuth, setAuth } from "./auth.js";

const BASE = "/api";

// 防止恢复流程里的请求再次 401 时递归触发。
let recoveringIdentity = false;

// 当前工作身份被管理员撤销时，自动切换到账号剩余身份并刷新页面，
// 而不是整体登出；账号已无任何身份时才回登录页并提示。
async function recoverIdentity(expectedToken) {
  if (recoveringIdentity || getToken() !== expectedToken) return;
  recoveringIdentity = true;
  try {
    const meRes = await fetch(`${BASE}/auth/me`, {
      headers: { Authorization: `Bearer ${expectedToken}` },
    });
    if (!meRes.ok) throw new Error("读取身份失败");
    const user = await meRes.json();
    if (getToken() !== expectedToken) return;
    const target = (user.role || "").trim() || (user.roles || [])[0] || "";
    if (!target) {
      clearAuth();
      window.location.href = "/login?revoked=1";
      return;
    }
    const swRes = await fetch(`${BASE}/auth/switch-role`, {
      method: "POST",
      headers: { Authorization: `Bearer ${expectedToken}`, "Content-Type": "application/json" },
      body: JSON.stringify({ role: target }),
    });
    if (!swRes.ok) throw new Error("切换身份失败");
    const data = await swRes.json();
    if (getToken() !== expectedToken) return;
    setAuth(data.token, data.user);
    sessionStorage.setItem("aigo_identity_recovered", data.user?.role || target);
    window.location.reload();
  } catch (e) {
    if (getToken() !== expectedToken) return;
    clearAuth();
    window.location.href = "/login?revoked=1";
  } finally {
    recoveringIdentity = false;
  }
}

const inflightReads = new Map();
function request(path, options = {}) {
  if (options.method && options.method !== "GET") {
    inflightReads.clear();
    return performRequest(path, options).finally(() => inflightReads.clear());
  }
  const key = JSON.stringify([getToken(), path]);
  if (inflightReads.has(key)) return inflightReads.get(key);
  const pending = performRequest(path, options).finally(() => { if (inflightReads.get(key) === pending) inflightReads.delete(key); });
  inflightReads.set(key, pending);
  return pending;
}

async function performRequest(path, options = {}) {
  const token = getToken();
  const headers = { ...options.headers };

  if (token) {
    headers["Authorization"] = `Bearer ${token}`;
  }
  if (!(options.body instanceof FormData)) {
    headers["Content-Type"] = "application/json";
  }

  const res = await fetch(`${BASE}${path}`, { ...options, headers });

  if (res.status === 401 && !path.startsWith("/auth/login")) {
    let message = "";
    try {
      const body = await res.json();
      message = body.error || "";
    } catch (e) {
      message = "";
    }
    // 身份切换、退出或新账号登录后的旧响应不能清除/恢复当前会话。
    if (!token || getToken() !== token) {
      const error = new Error(message || "登录已过期");
      error.status = 401;
      throw error;
    }
    if (message.includes("重新选择身份")) {
      await recoverIdentity(token);
      throw new Error(message);
    }
    clearAuth();
    window.location.href = "/login";
    throw new Error("登录已过期");
  }

  const data = await res.json();
  if (getToken() !== token) {
    const error = new Error("工作身份已变化，请刷新后重试");
    error.staleSession = true;
    throw error;
  }
  if (!res.ok) { const error = new Error(data.error || `请求失败: ${res.status}`); error.status = res.status; throw error; }
  return data;
}

export const api = {
  learningComparison: id => request(`/questions/${encodeURIComponent(id)}/learning-comparison`),
  createLearningAnalysis: (id, input) => request(`/questions/${encodeURIComponent(id)}/learning-analyses`, { method: 'POST', body: JSON.stringify(input) }),
  questionLearningAnalyses: id => request(`/questions/${encodeURIComponent(id)}/learning-analyses`),
  learningAnalysis: id => request(`/learning-analyses/${encodeURIComponent(id)}`),
  importLearningAnalysis: (id, input) => request(`/learning-analyses/${encodeURIComponent(id)}/import`, { method: 'POST', body: JSON.stringify(input) }),
  personalExperiences: (filter = {}) => request(`/personal-experiences?${new URLSearchParams(Object.entries(filter).filter(([, value]) => value !== '' && value != null))}`),
  createPersonalExperience: input => request("/personal-experiences", { method: "POST", body: JSON.stringify(input) }),
  personalExperienceHistory: id => request(`/personal-experiences/${encodeURIComponent(id)}/history`),
  savePersonalExperience: (id, input) => request(`/personal-experiences/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(input) }),
  questionPersonalExperiences: id => request(`/questions/${encodeURIComponent(id)}/personal-experiences`),
 prepareKnowledgeWorkflow: (points,presentation=null) => request('/knowledge-base/prepare', {method:'POST',body:JSON.stringify({knowledge_points:points.map(p=>({id:p.id,version_id:p.version_id})),...(presentation?{presentation}: {})})}),
 workflowDiagnostics: (kind,id,cursor="") => request(`${kind==="batch"?`/batch/status/${encodeURIComponent(id)}`:`/generation-runs/${encodeURIComponent(id)}`}/diagnostics?limit=10&cursor=${encodeURIComponent(cursor)}`),
 questionGenerationContext: (id) => request(`/questions/${encodeURIComponent(id)}/generation-context`),
 privateKnowledge: (filters={})=>request(`/private-knowledge?${new URLSearchParams(filters)}`),
 savePrivateKnowledge: input=>request('/private-knowledge',{method:'POST',body:JSON.stringify(input)}),
 privateKnowledgeCard: id=>request(`/private-knowledge/${encodeURIComponent(id)}`),
 privateKnowledgeHistory: id=>request(`/private-knowledge/${encodeURIComponent(id)}/history`),
 knowledgeChoices: ()=>request('/knowledge-selections'),
 chooseKnowledgeCard: input=>request('/knowledge-selections',{method:'PUT',body:JSON.stringify(input)}),
 knowledgeSubmissions: (filters={})=>request(`/knowledge-submissions?${new URLSearchParams(filters)}`),
 submitKnowledgeCards: input=>request('/knowledge-submissions',{method:'POST',body:JSON.stringify(input)}),
 decideKnowledgeCards: input=>request('/knowledge-submissions/decisions',{method:'POST',body:JSON.stringify(input)}),
 learningKnowledgeCards: (id,input)=>request(`/learning-analyses/${encodeURIComponent(id)}/knowledge-cards`,{method:'POST',body:JSON.stringify(input)}),
 knowledgeCards: (filters = {}) => request(`/knowledge-base?${new URLSearchParams(filters)}`),
 knowledgeCard: (id) => request(`/knowledge-base/${encodeURIComponent(id)}`),
 knowledgeCardHistory: (id) => request(`/knowledge-base/${encodeURIComponent(id)}/history`),
 saveKnowledgeCard: (card) => request(card.id ? `/knowledge-base/${encodeURIComponent(card.id)}` : '/knowledge-base', { method: card.id ? 'PUT' : 'POST', body: JSON.stringify(card) }),
 previewKnowledgeImport: (data) => request('/knowledge-base/import/preview', {method:'POST',body:JSON.stringify(data)}),
 confirmKnowledgeImport: (data) => request('/knowledge-base/import/confirm', {method:'POST',body:JSON.stringify(data)}),
 activateKnowledgeCard: (card) => request(`/knowledge-base/${encodeURIComponent(card.id)}/activate`, { method: 'POST', body: JSON.stringify({revision: card.revision}) }),
 archiveKnowledgeCard: (card) => request(`/knowledge-base/${encodeURIComponent(card.id)}`, { method: 'DELETE', body: JSON.stringify({revision: card.revision}) }),
  // 认证
  login: (username, password) =>
    request("/auth/login", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    }),
  register: (username, password, display_name) =>
    request("/auth/register", {
      method: "POST",
      body: JSON.stringify({ username, password, display_name }),
    }),
  me: () => request("/auth/me"),
  mySummary: (view = "") => request("/my/summary" + (view === "authoring" ? "?view=authoring" : "")),
  switchRole: (role) => request("/auth/switch-role", { method: "POST", body: JSON.stringify({ role }) }),
  changePassword: (old_password, new_password) =>
    request("/auth/change-password", {
      method: "POST",
      body: JSON.stringify({ old_password, new_password }),
    }),
  updateProfile: (display_name) =>
    request("/auth/profile", {
      method: "PUT",
      body: JSON.stringify({ display_name }),
    }),

  // 权限元数据
  listPermissions: () => request("/permissions"),

  // 角色模板
  listRoles: () => request("/roles"),
  createRole: (data) =>
    request("/roles", { method: "POST", body: JSON.stringify(data) }),
  updateRole: (id, data) =>
    request(`/roles/${id}`, { method: "PUT", body: JSON.stringify(data) }),
  deleteRole: (id) => request(`/roles/${id}`, { method: "DELETE" }),

  // 系统级 AI 服务配置（仅超级管理员，API Key 由后端脱敏返回）
  getModelSampling: () => request("/system/ai-sampling"),
  saveModelSampling: (data) => request("/system/ai-sampling", { method: "PUT", body: JSON.stringify(data) }),
  getLearningProvider: () => request("/system/ai-learning"),
  saveLearningProvider: (data) => request("/system/ai-learning", { method: "PUT", body: JSON.stringify(data) }),
  listAIProviders: () => request("/system/ai-providers"),
  generationQuota: () => request("/system/generation-quota"),
  saveGenerationQuota: (quota) => request("/system/generation-quota", { method: "PUT", body: JSON.stringify(quota) }),
  createAIProvider: (data) => request("/system/ai-providers", { method: "POST", body: JSON.stringify(data) }),
  updateAIProvider: (id, data) => request(`/system/ai-providers/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(data) }),
  activateAIProvider: (id) => request(`/system/ai-providers/${encodeURIComponent(id)}/activate`, { method: "POST" }),
  deleteAIProvider: (id) => request(`/system/ai-providers/${encodeURIComponent(id)}`, { method: "DELETE" }),

  // 题库
  listBanks: (includeStats = true) => request(`/banks?include_stats=${includeStats ? "true" : "false"}`),
  createBank: (data) =>
    request("/banks", { method: "POST", body: JSON.stringify(data) }),
  updateBank: (id, data) =>
    request(`/banks/${id}`, { method: "PUT", body: JSON.stringify(data) }),
  deleteBank: (id) => request(`/banks/${id}`, { method: "DELETE" }),
  listBankQuestions: (id, q = "", page = 1, pageSize = 100) => {
    const params = new URLSearchParams({ page: String(page), page_size: String(pageSize) });
    if (q) params.set("q", q);
    return request(`/banks/${id}/questions?${params}`);
  },
  collectBank: (id) => request(`/banks/${id}/collect`, { method: "POST" }),

  // 元数据
  listProfessions: () => request("/meta/professions"),

  // 用户管理
  listUsers: () => request("/users"),
  createUser: (params) =>
    request("/users", {
      method: "POST",
      body: JSON.stringify(params),
    }),
  updateUser: (id, params) =>
    request(`/users/${id}`, {
      method: "PUT",
      body: JSON.stringify(params),
    }),
  deleteUser: (id) => request(`/users/${id}`, { method: "DELETE" }),
  resetUserPassword: (id, params) =>
    request(`/users/${id}/reset-password`, {
      method: "POST",
      body: JSON.stringify(params || {}),
    }),

  // 统计
  stats: (scope = "") => request(`/stats${scope ? `?scope=${encodeURIComponent(scope)}` : ""}`),

  // 操作日志
  // filter: { action, actor, question } 均可选，任意组合
  auditLogs: (page = 1, limit = 200, filter = {}) => {
    const params = new URLSearchParams({ page: String(page), limit: String(limit) });
    if (filter.action) params.set("action", filter.action);
    if (filter.actor) params.set("actor", filter.actor);
    if (filter.question) params.set("question", filter.question);
    return request(`/audit-logs?${params.toString()}`);
  },
  auditLogActions: () => request(`/audit-logs/actions`),
  auditLogsByQuestion: (id) => request(`/audit-logs/question/${id}`),
  auditLogsByActor: (actor) => request(`/audit-logs/actor/${actor}`),

  // 题目
  // tier: 题库分层（formal=正式题库 / working=待审核题库 / eliminated=淘汰题库）
  listQuestions: (page = 1, pageSize = 100, bankId = "", tier = "", scope = "", library = "", task = {}) => {
    const params = new URLSearchParams({ page: String(page), page_size: String(pageSize) });
    if (bankId) params.set("bank_id", bankId);
    if (tier) params.set("tier", tier);
    if (scope) params.set("scope", scope);
    if (library) params.set("library", library);
    Object.entries(task).forEach(([k,v])=>params.set(k,v));
    return request(`/questions?${params}`);
  },
  getQuestion: (id, scope = "") => request(`/questions/${id}${scope ? `?scope=${encodeURIComponent(scope)}` : ""}`),
  listQuestionVersions: (id, scope = "") => request(`/questions/${id}/versions${scope ? `?scope=${encodeURIComponent(scope)}` : ""}`),
  restoreQuestionVersion: (id, version, reason = "", expectation = {}) =>
    request(`/questions/${id}/restore`, {
      method: "POST",
      body: JSON.stringify({ version, reason, ...expectation }),
    }),
  // 管理员撤回已通过题目至 AI 检查通过状态
  unpublishQuestion: (id, reason = "") =>
    request(`/questions/${id}/unpublish`, {
      method: "POST",
      body: JSON.stringify({ reason }),
    }),
  updateQuestion: (id, data) =>
    request(`/questions/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  deleteQuestion: (id) => request(`/questions/${id}`, { method: "DELETE" }),
  publishQuestion: (id) =>
    request(`/questions/${id}/publish`, { method: "POST" }),
  searchQuestions: (q, status = "", page = 1, pageSize = 100, bankId = "", professions = [], difficulty = "", outlineCode = "", tier = "", classifiable = false, scope = "", library = "", task = {}) => {
    const params = new URLSearchParams();
    if (q) params.set("q", q);
    if (status) params.set("status", status);
    if (bankId) params.set("bank_id", bankId);
    if (professions.length) params.set("profession", professions.join(","));
    if (difficulty) params.set("difficulty", difficulty);
    if (outlineCode) params.set("outline_code", outlineCode);
    if (tier) params.set("tier", tier);
    if (classifiable) params.set("classifiable", "true");
    if (scope) params.set("scope", scope);
    if (library) params.set("library", library);
    params.set("page", String(page));
    params.set("page_size", String(pageSize));
    Object.entries(task).forEach(([k,v])=>params.set(k,v));
    return request(`/questions/search?${params}`);
  },
  authoringJourney: (params) => request(`/authoring-journey?${new URLSearchParams(params)}`),
  myNewQuestions: (page = 1, pageSize = 20,task={}) => request(`/questions/my-new?${new URLSearchParams({page,page_size:pageSize,...task})}`),
  addQuestionBank: (id, bankId) =>
    request(`/questions/${id}/bank`, {
      method: "POST",
      body: JSON.stringify({ bank_id: bankId }),
    }),
  removeQuestionBank: (id, bankId) =>
    request(`/questions/${id}/bank/${bankId}`, { method: "DELETE" }),
  moveQuestionsBankBatch: (ids, bankId) =>
    request(`/questions/bank-move-batch`, {
      method: "POST",
      body: JSON.stringify({ ids, bank_id: bankId }),
    }),
  generate: (params) =>
    request("/questions/generate", {
      method: "POST",
      body: JSON.stringify(params),
    }),
  getGenerationRun: (id) => request(`/generation-runs/${encodeURIComponent(id)}`),

  // 个人题目分享至全局题库（每题只能申请一次）
  createQuestionShare: (questionId, expectation = {}) =>
    request(`/questions/${questionId}/share`, { method: "POST", body: JSON.stringify(expectation) }),
  previewQuestionShares: (filters) => request('/question-shares/preview', { method: 'POST', body: JSON.stringify(filters) }),
  createQuestionShares: (items) => request('/question-shares/batch', { method: 'POST', body: JSON.stringify({ items }) }),
  listQuestionShares: (scope = "mine") => request(`/question-shares?scope=${encodeURIComponent(scope)}`),
  reviewQuestionShare: (id, status, note = "", expectedShareRevision) =>
    request(`/question-shares/${id}/review`, {
      method: "POST",
      body: JSON.stringify({ status, note, ...(expectedShareRevision === undefined ? {} : { expected_share_revision: expectedShareRevision }) }),
    }),

  // 知识点
  listKP: (params = {}) => {
    const qs = new URLSearchParams(params).toString();
    return request(`/knowledge-points?${qs}`);
  },
  searchKP: (q) => request(`/knowledge-points/search?q=${encodeURIComponent(q)}`),
  searchKPFiltered: (params = {}) => {
    const qs = new URLSearchParams();
    Object.entries(params).forEach(([k, v]) => {
      if (v !== "" && v !== undefined && v !== null) qs.set(k, String(v));
    });
    return request(`/knowledge-points/search?${qs}`);
  },
  kpMeta: (versionId = "") => request(`/knowledge-points/meta?version_id=${encodeURIComponent(versionId)}`),
  kpTree: (versionId) => request(`/knowledge-points/tree?version_id=${encodeURIComponent(versionId)}`),
  kpVersions: () => request("/knowledge-versions"),
  deleteKPVersion: (id) => request(`/knowledge-versions/${encodeURIComponent(id)}`, { method: "DELETE" }),
  createKPVersion: (data) => request("/knowledge-versions", { method: "POST", body: JSON.stringify(data) }),
  publishKPVersion: (id) => request(`/knowledge-versions/${encodeURIComponent(id)}/publish`, { method: "POST" }),
  updateKP: (id, data) => request(`/knowledge-points/${encodeURIComponent(id)}`, { method: "PUT", body: JSON.stringify(data) }),
  createKP: (data) =>
    request("/knowledge-points", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  deleteKP: (id) => request(`/knowledge-points/${encodeURIComponent(id)}`, { method: "DELETE" }),
  importKP: async (files, versionId, mode = "merge", { preview = false, previewToken = "" } = {}) => {
    const form = new FormData();
    for (const file of (Array.isArray(files) ? files : [files])) form.append("files", file);
    form.append("version_id", versionId);
    form.append("mode", mode);
    if (preview) form.append("preview", "1");
    if (previewToken) form.append("preview_token", previewToken);
    return request("/knowledge-points/import", {
      method: "POST",
      body: form,
    });
  },
  exportKnowledgePoints: async (versionId) => {
    const token = getToken();
    const res = await fetch(`${BASE}/knowledge-points/export`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: JSON.stringify({ version_id: versionId }),
    });
    if (!res.ok) {
      let msg = `导出失败: ${res.status}`;
      try {
        const data = await res.json();
        if (data.error) msg = data.error;
      } catch (e) {
        /* ignore */
      }
      throw new Error(msg);
    }
    return { blob: await res.blob(), filename: res.headers.get("Content-Disposition")?.match(/filename="?([^";]+)"?/)?.[1] || "知识点导出.xlsx" };
  },

  // 导出
  exportXlsx: (params) =>
    request("/export/xlsx", {
      method: "POST",
      body: JSON.stringify(params),
    }),
  exportDocx: (params) =>
    request("/export/docx", {
      method: "POST",
      body: JSON.stringify(params),
    }),
  // 下载导出文件（带认证）
  downloadExport: async (filename) => {
    const token = getToken();
    const res = await fetch(`/api/export/download/${filename}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (getToken() !== token) { const error = new Error("工作身份已变化，请重新导出"); error.staleSession = true; throw error; }
    if (!res.ok) {
      let msg = `下载失败: ${res.status}`;
      try {
        const data = await res.json();
        if (data.error) msg = data.error;
      } catch (e) {
        /* ignore */
      }
      throw new Error(msg);
    }
    const blob = await res.blob();
    if (getToken() !== token) { const error = new Error("工作身份已变化，请重新导出"); error.staleSession = true; throw error; }
    return blob;
  },

  // 批量推理
  batchCapabilities: () => request("/batch/capabilities"),
  batchSubmit: (params) =>
    request("/batch/submit", {
      method: "POST",
      body: JSON.stringify(params),
    }),
  confirmQuestions: (items) =>
    request("/questions/confirm", {
      method: "POST",
      body: JSON.stringify({ items }),
    }),
  libraryDelete: (items) =>
    request("/questions/library-delete", {
      method: "POST",
      body: JSON.stringify({ items }),
    }),
  libraryRestore: (items) =>
    request("/questions/library-restore", {
      method: "POST",
      body: JSON.stringify({ items }),
    }),
  batchPreview: (params) =>
    request("/batch/preview", {
      method: "POST",
      body: JSON.stringify(params),
    }),
  batchList: (params = {}) => {
    const qs = new URLSearchParams(params).toString();
    return request(`/batch/list?${qs}`);
  },
  batchHistory: (params = {}) => {
    const qs = new URLSearchParams(params).toString();
    return request(`/batch/history?${qs}`);
  },
  batchStatus: (jobId) =>
    request(`/batch/status/${jobId}`),
  batchDownload: (jobId) =>
    request(`/batch/download/${jobId}`, {
      method: "POST",
    }),
  batchRetryFailed: (jobId) =>
    request(`/batch/retry-failed/${jobId}`, {
      method: "POST",
    }),

  // AI 检查
  // 题目粒度检查进度（登录即可，逐题校验题库范围）
  aiCheckProgress: (questionIds) =>
    request("/ai-check/progress", {
      method: "POST",
      body: JSON.stringify({ question_ids: questionIds }),
    }),
  // 检查概况统计（质量检查权限）
  aiCheckSummary: () => request("/ai-check/summary"),
  aiCheckResult: (questionId) =>
    request(`/ai-check/result/${questionId}`),
  aiCheckResults: (limit = 50) =>
    request(`/ai-check/results?limit=${limit}`),

  // 专家（历史兼容接口，界面已由角色管理/审题人候选替代）
  listExperts: () => request("/experts"),
  createExpert: (data) =>
    request("/experts", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  updateExpert: (id, data) =>
    request(`/experts/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  deleteExpert: (id) => request(`/experts/${id}`, { method: "DELETE" }),

  // 审核
  submitReview: (questionId, flowId, bankId = "", expectation = {}) =>
    request("/review/submit", {
      method: "POST",
      body: JSON.stringify({ question_id: questionId, flow_id: flowId, bank_id: bankId, ...expectation }),
    }),
	  submitReviewBatch: (questionIds, flowId, expectations = {}) =>
	    request("/review/submit-batch", {
	      method: "POST",
      body: JSON.stringify({ question_ids: questionIds, flow_id: flowId, expectations }),
	    }),
	  resubmitRevisions: (questionIds, flowId = "", expectations = {}) =>
	    request("/review/resubmit-revisions", {
	      method: "POST",
      body: JSON.stringify({ question_ids: questionIds, flow_id: flowId, expectations }),
	    }),
  reviewResults: (params = {}) => {
    const qs = new URLSearchParams();
    Object.entries(params).forEach(([k, v]) => {
      if (v !== "" && v !== undefined && v !== null) qs.set(k, String(v));
    });
    return request(`/review/results?${qs}`);
  },
  listReviewers: () => request("/review/reviewers"),
  reviewHistory: id => request(`/questions/${encodeURIComponent(id)}/review-history`),
  restoreCandidates: (params = {}) => request(`/review/restore-candidates?${new URLSearchParams(params)}`),
  restoreOptions: () => request("/review/restore-options"),
  restoreCandidate: id => request(`/review/restore-candidates/${encodeURIComponent(id)}`),
  previewReviewRestore: params => request("/review/restore-preview", { method: "POST", body: JSON.stringify(params) }),
  restoreReview: params => request("/review/restore", { method: "POST", body: JSON.stringify(params) }),
  availableReviewFlows: () => request("/review/available-flows"),
  myTasks: (page = 1, pageSize = 20) => request(`/review/my-tasks?page=${page}&page_size=${pageSize}`),
  myDecisions: (page = 1, pageSize = 20) => request(`/review/my-decisions?page=${page}&page_size=${pageSize}`),
  // 待我修改（退回修改的题目；提交修改=保存回库，送审由管理员负责）
  myRevisions: () => request("/review/my-revisions"),
  reviewAction: (params) =>
    request("/review/action", {
      method: "POST",
      body: JSON.stringify(params),
    }),
  cancelReview: params => request("/review/cancel", { method: "POST", body: JSON.stringify(params) }),
  finalizeReview: (params) =>
    request("/review/finalize", {
      method: "POST",
      body: JSON.stringify(params),
    }),
  getReviewTask: (id) => request(`/review/task/${id}`),
  getTaskByQuestion: (questionId) => request(`/review/task-by-question/${questionId}`),
  reviewRecords: (taskId) => request(`/review/records/${taskId}`),
  listFlows: () => request("/review/flows"),
  createFlow: (data) =>
    request("/review/flows", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  deleteFlow: (id) => request(`/review/flows/${id}`, { method: "DELETE" }),
};
