// 使用页面实际读取的快照；禁止在写入前刷新修订号绕过冲突保护。
export function libraryExpectation(question) {
  return {
    question_id: question.id,
    expected_version: question.version,
    expected_library_revision: question.library_revision || 0,
    expected_review_revision: question.review_revision || 0,
  };
}
