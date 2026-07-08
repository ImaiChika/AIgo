const BASE = "http://127.0.0.1:8080/api";

async function request(path, options = {}) {
  const res = await fetch(`${BASE}${path}`, {
    headers: { "Content-Type": "application/json" },
    ...options,
  });
  const data = await res.json();
  if (!res.ok) throw new Error(data.error || `请求失败: ${res.status}`);
  return data;
}

export const api = {
  // 统计
  stats: () => request("/stats"),

  // 题目
  listQuestions: () => request("/questions"),
  getQuestion: (id) => request(`/questions/${id}`),
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
  importKP: async (file) => {
    const form = new FormData();
    form.append("file", file);
    const res = await fetch(`${BASE}/knowledge-points/import`, {
      method: "POST",
      body: form,
    });
    const data = await res.json();
    if (!res.ok) throw new Error(data.error || "导入失败");
    return data;
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
  reviewRecords: (taskId) => request(`/review/records/${taskId}`),
};
