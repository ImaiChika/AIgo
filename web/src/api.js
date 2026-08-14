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
  if (!res.ok) throw new Error(data.error || `请求失败: ${res.status}`);
  return data;
}

export const api = {
  // 认证
  login: (username, password) =>
    request("/auth/login", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    }),
  me: () => request("/auth/me"),
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

  // 用户管理
  listUsers: () => request("/users"),
  createUser: (params) =>
    request("/users", {
      method: "POST",
      body: JSON.stringify(params),
    }),

  // 统计
  stats: () => request("/stats"),

  // 操作日志
  auditLogs: (limit = 100) => request(`/audit-logs?limit=${limit}`),
  auditLogsByQuestion: (id) => request(`/audit-logs/question/${id}`),
  auditLogsByActor: (actor) => request(`/audit-logs/actor/${actor}`),

  // 题目
  listQuestions: (page = 1, pageSize = 100) => {
    const params = new URLSearchParams({ page: String(page), page_size: String(pageSize) });
    return request(`/questions?${params}`);
  },
  getQuestion: (id) => request(`/questions/${id}`),
  updateQuestion: (id, data) =>
    request(`/questions/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  deleteQuestion: (id) => request(`/questions/${id}`, { method: "DELETE" }),
  publishQuestion: (id) =>
    request(`/questions/${id}/publish`, { method: "POST" }),
  searchQuestions: (q, status = "", page = 1, pageSize = 100) => {
    const params = new URLSearchParams();
    if (q) params.set("q", q);
    if (status) params.set("status", status);
    params.set("page", String(page));
    params.set("page_size", String(pageSize));
    return request(`/questions/search?${params}`);
  },
  generate: (params) =>
    request("/questions/generate", {
      method: "POST",
      body: JSON.stringify(params),
    }),

  // 知识点
  listKP: (params = {}) => {
    const qs = new URLSearchParams(params).toString();
    return request(`/knowledge-points?${qs}`);
  },
  searchKP: (q) => request(`/knowledge-points/search?q=${encodeURIComponent(q)}`),
  createKP: (data) =>
    request("/knowledge-points", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  deleteKP: (id) => request(`/knowledge-points/${id}`, { method: "DELETE" }),
  importKP: async (file) => {
    const form = new FormData();
    form.append("file", file);
    return request("/knowledge-points/import", {
      method: "POST",
      body: form,
    });
  },

  // 图片
  imagePrompt: (questionId) =>
    request("/images/prompt", {
      method: "POST",
      body: JSON.stringify({ question_id: questionId }),
    }),
  imageGenerate: (questionId, count = 4) =>
    request("/images/generate", {
      method: "POST",
      body: JSON.stringify({ question_id: questionId, count }),
    }),
  listImages: (questionId) => request(`/images/${questionId}`),
  imageReview: (params) =>
    request("/images/review", {
      method: "POST",
      body: JSON.stringify(params),
    }),

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
  downloadExport: (filename) =>
    request(`/export/download/${filename}`),

  // 批量推理
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
  aiCheck: (questionIds) =>
    request("/ai-check", {
      method: "POST",
      body: JSON.stringify({ question_ids: questionIds }),
    }),
  aiCheckResult: (questionId) =>
    request(`/ai-check/result/${questionId}`),
  aiCheckResults: (limit = 50) =>
    request(`/ai-check/results?limit=${limit}`),

  // 专家
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
  submitReview: (questionId, flowId) =>
    request("/review/submit", {
      method: "POST",
      body: JSON.stringify({ question_id: questionId, flow_id: flowId }),
    }),
  reviewAction: (params) =>
    request("/review/action", {
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
  updateFlow: (id, data) =>
    request(`/review/flows/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  deleteFlow: (id) => request(`/review/flows/${id}`, { method: "DELETE" }),
};
