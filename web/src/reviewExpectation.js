// 使用页面实际读到的快照，禁止提交前临时获取新修订号来绕过旧写入保护。
export function reviewExpectation(question, task) {
 return { expected_version: question.version, expected_review_revision: question.review_revision || 0, ...(task?.id ? { previous_task_id: task.id } : {}) };
}
export function reviewExpectations(items) {
 return Object.fromEntries(items.map(item => { const question = item.question || item; return [question.id, reviewExpectation(question, item.task)]; }));
}
