import {questionReviewUi,isLegacyQuestionReviewPermission} from "./questionReviewUi.js";
// 兼容缓存中的历史权限目录；退役权限不再出现在授权界面。
export function permissionVisible(code) {
  return (questionReviewUi.enabled()||!isLegacyQuestionReviewPermission(code)) && !["rag:view", "rag:manage", "rag:control"].includes(code);
}
