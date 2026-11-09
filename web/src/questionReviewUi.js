// Temporary presentation isolation only. This must never rewrite server grants,
// task states or database records. Restore with VITE_QUESTION_REVIEW_UI=true and
// restart/rebuild the frontend; see /审核界面隔离与恢复.md in the repository root.
export const questionReviewUi = {
  enabled: () => import.meta.env?.VITE_QUESTION_REVIEW_UI === 'true',
};
export const legacyQuestionReviewPaths = Object.freeze([
  '/review','/review-decisions','/review-results','/review-restore',
  '/review-flows','/my-revisions','/share-requests',
]);
export function isLegacyQuestionReviewPath(path='') {
  const clean=path.split(/[?#]/)[0].replace(/\/$/,'').toLowerCase();
  return legacyQuestionReviewPaths.some(p=>clean===p||clean.startsWith(p+'/'));
}
export function questionReviewPathVisible(path) {
  return questionReviewUi.enabled()||!isLegacyQuestionReviewPath(path);
}
export function isLegacyQuestionReviewPermission(code='') {
  return code.startsWith('review:')||['flow:manage','question:share','question:share_review','question:rollback','expert:manage'].includes(code);
}
const activeRoleFunctions=['question:generate','batch:run','question:edit','question:confirm','knowledge:manage','kb:manage','kb:review','user:manage','role:manage','generation_quota:manage','learning:analyze','learning:save'];
export function isLegacyReviewOnlyRole(role={}) {
  if(role.id==='expert')return true;
  const p=role.permissions||[];
  return p.some(isLegacyQuestionReviewPermission)&&!p.some(code=>activeRoleFunctions.includes(code));
}
export function reviewRoleVisible(role) {return questionReviewUi.enabled()||!isLegacyReviewOnlyRole(role);}
export function preserveIsolatedRoleIDs(previous,next,roles) {
  if(questionReviewUi.enabled())return next;
  const hidden=previous.filter(id=>!reviewRoleVisible(roles.find(r=>r.id===id)||{id}));
  return [...new Set([...next,...hidden])];
}
export function reviewAccountVisible(user,roles,selfID) {
  if(questionReviewUi.enabled()||user.id===selfID)return true;
  const ids=user.roles?.length?user.roles:user.role?[user.role]:[];
  const p=user.permissions||[];
  if(ids.some(id=>['super_admin','admin'].includes(id))||p.some(code=>activeRoleFunctions.includes(code)))return true;
  const legacy=ids.length?ids.every(id=>!reviewRoleVisible(roles.find(r=>r.id===id)||{id})):p.some(isLegacyQuestionReviewPermission);
  return !legacy;
}
export function presentedWorkRoleName(id,name,permissions=[]) {
  return reviewRoleVisible({id,permissions})?name:'已保留身份';
}

// Preserve historic state facts without presenting an active human-review task.
export function isolatedQuestionState(status) {
  if(questionReviewUi.enabled())return null;
  return ({reviewing:'历史流程中',conflict:'历史流程待处理',revision_required:'历史流程待修改',rejected:'历史驳回'})[status]||null;
}
