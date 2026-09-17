import { getToken, clearAuth } from "./auth.js";

const BASE = "/api";

async function request(path, options = {}) {
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
    clearAuth();
    window.location.href = "/login";
    throw new Error("登录已过期");
  }

  const data = await res.json();
  if (!res.ok) { const error = new Error(data.error || `请求失败: ${res.status}`); error.status = res.status; throw error; }
  return data;
}

export const api = {
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
  mySummary: () => request("/my/summary"),
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
  listAIProviders: () => request("/system/ai-providers"),
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

  // 统计
  stats: (scope = "") => request(`/stats${scope ? `?scope=${encodeURIComponent(scope)}` : ""}`),

  // 操作日志
  auditLogs: (limit = 100) => request(`/audit-logs?limit=${limit}`),
  auditLogsByQuestion: (id) => request(`/audit-logs/question/${id}`),
  auditLogsByActor: (actor) => request(`/audit-logs/actor/${actor}`),

  // 题目
  // tier: 题库分层（formal=正式题库 / working=待审核题库 / eliminated=淘汰题库）
  listQuestions: (page = 1, pageSize = 100, bankId = "", tier = "", scope = "") => {
    const params = new URLSearchParams({ page: String(page), page_size: String(pageSize) });
    if (bankId) params.set("bank_id", bankId);
    if (tier) params.set("tier", tier);
    if (scope) params.set("scope", scope);
    return request(`/questions?${params}`);
  },
  getQuestion: (id) => request(`/questions/${id}`),
  listQuestionVersions: (id) => request(`/questions/${id}/versions`),
  restoreQuestionVersion: (id, version, reason = "") =>
    request(`/questions/${id}/restore`, {
      method: "POST",
      body: JSON.stringify({ version, reason }),
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
  searchQuestions: (q, status = "", page = 1, pageSize = 100, bankId = "", professions = [], difficulty = "", outlineCode = "", tier = "", classifiable = false, scope = "") => {
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
    params.set("page", String(page));
    params.set("page_size", String(pageSize));
    return request(`/questions/search?${params}`);
  },
  myNewQuestions: (page = 1, pageSize = 20) => request(`/questions/my-new?page=${page}&page_size=${pageSize}`),
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
  createQuestionShare: (questionId) =>
    request(`/questions/${questionId}/share`, { method: "POST" }),
  previewQuestionShares: (filters) => request('/question-shares/preview', { method: 'POST', body: JSON.stringify(filters) }),
  createQuestionShares: (questionIds) => request('/question-shares/batch', { method: 'POST', body: JSON.stringify({ question_ids: questionIds }) }),
  listQuestionShares: (scope = "mine") => request(`/question-shares?scope=${encodeURIComponent(scope)}`),
  reviewQuestionShare: (id, status, note = "") =>
    request(`/question-shares/${id}/review`, {
      method: "POST",
      body: JSON.stringify({ status, note }),
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
  importKP: async (files, versionId, mode = "merge") => {
    const form = new FormData();
    for (const file of (Array.isArray(files) ? files : [files])) form.append("files", file);
    form.append("version_id", versionId);
    form.append("mode", mode);
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
    return res.blob();
  },

  // 批量推理
  batchCapabilities: () => request("/batch/capabilities"),
  batchSubmit: (params) =>
    request("/batch/submit", {
      method: "POST",
      body: JSON.stringify(params),
    }),
  batchList: (params = {}) => {
    const qs = new URLSearchParams(params).toString();
    return request(`/batch/list?${qs}`);
  },
  batchStatus: (jobId) =>
    request(`/batch/status/${jobId}`),
  batchDownload: (jobId) =>
    request(`/batch/download/${jobId}`, {
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
  submitReview: (questionId, flowId, bankId = "") =>
    request("/review/submit", {
      method: "POST",
      body: JSON.stringify({ question_id: questionId, flow_id: flowId, bank_id: bankId }),
    }),
	  submitReviewBatch: (questionIds, flowId) =>
	    request("/review/submit-batch", {
	      method: "POST",
	      body: JSON.stringify({ question_ids: questionIds, flow_id: flowId }),
	    }),
	  resubmitRevisions: (questionIds) =>
	    request("/review/resubmit-revisions", {
	      method: "POST",
	      body: JSON.stringify({ question_ids: questionIds }),
	    }),
  reviewResults: (params = {}) => {
    const qs = new URLSearchParams();
    Object.entries(params).forEach(([k, v]) => {
      if (v !== "" && v !== undefined && v !== null) qs.set(k, String(v));
    });
    return request(`/review/results?${qs}`);
  },
  listReviewers: () => request("/review/reviewers"),
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
