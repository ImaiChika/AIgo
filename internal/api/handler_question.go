package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"aigo/internal/auth"
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/pipeline"
	"aigo/internal/storage"
	"aigo/internal/workflowtrace"
)

// handleGenerate 提交单题命题持久化任务。
// 请求：{"subject": "消化", "difficulty": "0.65", "topic": "消化性溃疡", "outline_code": "110.4.3.1.1", "count": 1, "bank_id": "bank-neike"}
// 返回：{"run": {...status=pending...}, "questions": [], "count": 0}
// 服务端把请求快照与 pending 运行落库后立即返回；由后台 worker 抢占执行
// （生成 → 落库 → AI 检查 → 题库归纳 → 审计），进程重启后未完成运行可被接管。
// 前端凭 run_id 轮询 GET /api/generation-runs/{id} 获取进度与已生成题目。
func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	if s.aiCheckSvc == nil || !s.aiCheckSvc.AutomaticReady() {
		writeError(w, http.StatusServiceUnavailable, "AI 质量检查服务未就绪，已停止出题；请联系管理员恢复后再试")
		return
	}
	var req struct {
		Presentation             *domain.PresentationRequirements `json:"presentation"`
		WorkflowMode             string                           `json:"workflow_mode"`
		ApplyPersonalExperiences *bool                            `json:"apply_personal_experiences,omitempty"`
		RunID                    string                           `json:"run_id"` // 客户端生成的幂等运行 ID，用于刷新恢复
		KnowledgePointID         string                           `json:"knowledge_point_id"`
		VersionID                string                           `json:"version_id"`
		Subject                  string                           `json:"subject"`      // 专业科目
		Category                 string                           `json:"category"`     // 分类：基础医学/临床综合
		Difficulty               string                           `json:"difficulty"`   // 难度系数：0.55/0.65/0.75/0.85
		Topic                    string                           `json:"topic"`        // 知识点主题
		OutlineCode              string                           `json:"outline_code"` // 大纲代码
		Count                    int                              `json:"count"`        // 生成数量
		BankID                   string                           `json:"bank_id"`      // 目标题库（分库）
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	// 设置默认值
	if req.Count <= 0 {
		req.Count = 1
	}
	// 先按当前配置拒绝超大请求，数据库写入时再原子检查全部配额。
	if s.quotaStore != nil {
		quota, _, err := s.quotaStore.GetGenerationQuota(r.Context())
		if err != nil {
			writeError(w, 503, "读取生成任务配额失败")
			return
		}
		if req.Count > quota.SingleMaxQuestions {
			writeGenerationQuotaError(w, &storage.GenerationQuotaError{Message: fmt.Sprintf("单次最多生成 %d 道题，请减少题数", quota.SingleMaxQuestions)})
			return
		}
	} else if req.Count > storage.DefaultGenerationQuota().SingleMaxQuestions {
		writeGenerationQuotaError(w, &storage.GenerationQuotaError{Message: "单次最多生成 20 道题，请减少题数"})
		return
	}
	if req.Subject == "" {
		req.Subject = "临床医学"
	}
	if req.Difficulty == "" {
		req.Difficulty = "0.65"
	}
	if req.Topic == "" {
		req.Topic = "常见症状鉴别诊断"
	}
	if req.RunID == "" {
		req.RunID = fmt.Sprintf("gen-%d", time.Now().UnixNano())
	}
	if !validGenerationRunID(req.RunID) {
		writeError(w, 400, "命题运行 ID 格式错误")
		return
	}
	userID := auth.GetUserID(r.Context())
	// 目标题库不再由出题人选择：题目始终归属生成者个人题库（owner_id），
	// 分类子题库归属由系统自动处理——范围受限账号归入其可见题库（保证受限
	// 管理员可见），其余按专业自动归纳；管理员可在送审时明确分类子题库。
	// 请求中的 bank_id 字段仅为兼容旧客户端保留，服务端忽略。
	scope, fullScope, _, err := s.authSvc.GetBankScope(r.Context(), userID, domain.PermQuestionGenerate)
	if err != nil {
		writeError(w, 500, "查询出题范围失败")
		return
	}

	// 从数据库获取完整知识点信息（包含 Category、Unit、SubItem 等）
	kp := domain.KnowledgePoint{
		ID:          req.OutlineCode,
		Category:    req.Category,
		Subject:     req.Subject,
		Topic:       req.Topic,
		OutlineCode: req.OutlineCode,
	}
	if req.KnowledgePointID != "" || req.OutlineCode != "" || req.VersionID != "" {
		dbKP, err := s.kpSvc.ResolveForGeneration(r.Context(), req.VersionID, req.KnowledgePointID, req.OutlineCode)
		if err != nil {
			kpError(w, err)
			return
		}
		kp = *dbKP
	}
	genReq := domain.GenerationRequest{
		Subject:         req.Subject,
		Difficulty:      domain.Difficulty(req.Difficulty),
		KnowledgePoints: []domain.KnowledgePoint{kp},
		Count:           req.Count,
	}

	startedAt := time.Now()
	if s.generationRunStore == nil {
		writeError(w, 503, "命题运行记录服务不可用")
		return
	}
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = "ai"
	}
	// 请求快照随运行落库：worker 依据快照执行，进程重启后仍可接管未完成运行。
	// ScopedBankIDs：范围受限账号的可见题库集合（空=全范围，按专业自动归纳）。
	spec := pipeline.GenerationSpec{
		Request: genReq, Actor: actor, OwnerID: userID,
	}
	if !fullScope {
		spec.ScopedBankIDs = append([]string(nil), scope...)
	}
	// An existing owned run keeps its original references, even if cards were
	// later edited or the user's KB permission was revoked.
	existingRun, readErr := s.generationRunStore.GetGenerationRun(r.Context(), req.RunID)
	if readErr != nil {
		writeError(w, 503, "读取命题运行记录失败")
		return
	}
	if existingRun != nil {
		if existingRun.OwnerID != userID {
			writeError(w, 404, "命题运行记录不存在")
			return
		}
		s.respondGenerationRun(w, r.Context(), existingRun)
		return
	}
	if req.KnowledgePointID == "" && req.OutlineCode == "" && req.VersionID == "" {
		writeError(w, 400, "请选择有效大纲知识点后出题，缺少知识点的请求不会进入生成队列")
		return
	}
	if !domain.IsEligibleClinicalPoint(kp) {
		writeError(w, 400, "基础医学知识点已从有效大纲剔除，请选择临床知识点")
		return
	}

	var created bool
	submissionCtx, submission := workflowtrace.Start(r.Context(), s.questionStore, "generate:"+req.RunID, "submission", 0, "")
	defer submission.Close()
	err = s.withGenerationAccess(submissionCtx, domain.PermQuestionGenerate, func(ctx context.Context, snapshot *domain.GenerationPromptSnapshot) error {
		return s.withKnowledgeWorkflow(ctx, req.WorkflowMode, []domain.KnowledgePoint{kp}, req.Presentation, func(ctx context.Context, workflow *domain.KnowledgeWorkflowSnapshot) error {
			return s.withPersonalExperiences(r.WithContext(ctx), req.ApplyPersonalExperiences, []domain.KnowledgePoint{kp}, func(experiences *domain.PersonalExperienceSnapshot) error {
				spec.Request.PersonalExperiences = experiences
				spec.Request.Workflow = workflow
				if workflow != nil {
					submission.Version(workflow.Version)
				} else {
					submission.Version("direct")
				}
				snapshot = generator.FreezeCurrentBaseline(snapshot, []domain.GenerationRequest{genReq})
				spec.Request.PromptSnapshot = snapshot
				specJSON, e := json.Marshal(spec)
				if e != nil {
					return e
				}
				queueStage := workflowtrace.Begin(ctx, "queue_admission", -1, struct {
					RunID string
					Count int
				}{req.RunID, req.Count})
				created, e = s.generationRunStore.CreateGenerationRun(ctx, domain.GenerationRun{
					ID: req.RunID, OwnerID: userID, Status: domain.GenerationRunPending, RequestedCount: req.Count, StartedAt: startedAt, MaxAttempts: 3, RequestJSON: specJSON,
				})
				if e != nil {
					queueStage.End("failed", "queue_admission_failed", nil)
				} else {
					queueStage.End("written", "", created)
				}
				return e
			})
		})
	})
	if err == nil {
		submission.Complete("accepted", nil)
	} else {
		submission.Complete("submission_failed", nil)
	}
	if err != nil {
		if writeLearningOperationError(w, err) {
			return
		}
		if writeKnowledgeBaseError(w, err) {
			return
		}
		if writeGenerationQuotaError(w, err) {
			return
		}
		if writeAuthoringError(w, err) {
			return
		}
		writeError(w, 500, "创建命题运行记录失败")
		return
	}
	if !created {
		// 幂等：同一 run_id 重复提交时返回已有运行状态，不重复执行
		existing, err := s.generationRunStore.GetGenerationRun(r.Context(), req.RunID)
		if err != nil {
			writeError(w, 500, "读取命题运行记录失败")
			return
		}
		if existing == nil || existing.OwnerID != userID {
			writeError(w, 404, "命题运行记录不存在")
			return
		}
		s.respondGenerationRun(w, r.Context(), existing)
		return
	}
	if s.auditSvc != nil {
		_ = s.auditSvc.Log(r.Context(), "", "generate", actor,
			fmt.Sprintf("提交单题生成：知识点「%s」（大纲 %s），数量 %d，运行 %s", kp.Topic, kp.OutlineCode, req.Count, req.RunID))
	}
	// 提交成功：立即返回 pending 运行（无题目），前端进入轮询恢复流程
	run := &domain.GenerationRun{
		ID: req.RunID, OwnerID: userID, Status: domain.GenerationRunPending,
		RequestedCount: req.Count, StartedAt: startedAt, MaxAttempts: 3, Attempts: 0,
		UpdatedAt: startedAt,
	}
	s.respondGenerationRun(w, r.Context(), run)
}

var generationRunIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

func validGenerationRunID(id string) bool { return generationRunIDPattern.MatchString(id) }

// handleGetGenerationRun 返回当前用户的一次命题执行状态及仍存活的题目。
// 检查淘汰的题目已物理删除，其 ID 仍保留在 run.question_ids，并由检查进度接口返回淘汰原因。
func (s *Server) handleGetGenerationRun(w http.ResponseWriter, r *http.Request) {
	if s.generationRunStore == nil {
		writeError(w, 503, "命题运行记录服务不可用")
		return
	}
	id := r.PathValue("id")
	if !validGenerationRunID(id) {
		writeError(w, 400, "命题运行 ID 格式错误")
		return
	}
	run, err := s.generationRunStore.GetGenerationRun(r.Context(), id)
	if err != nil {
		writeError(w, 500, "读取命题运行记录失败")
		return
	}
	if run == nil || run.OwnerID != auth.GetUserID(r.Context()) {
		writeError(w, 404, "命题运行记录不存在")
		return
	}
	// Reading a task never mutates a possibly renewed lease. Background workers
	// close exhausted/legacy leases even while the owner is away.
	s.respondGenerationRun(w, r.Context(), run)
}

func (s *Server) respondGenerationRun(w http.ResponseWriter, ctx context.Context, run *domain.GenerationRun) {
	// Only public syllabus metadata is exposed; personal rules, curated knowledge snapshots and
	// internal execution inputs remain private in the persisted request snapshot.
	var spec pipeline.GenerationSpec
	_ = json.Unmarshal(run.RequestJSON, &spec)
	questions := make([]domain.A2Question, 0, len(run.QuestionIDs))
	hiddenDeletedIDs := []string{}
	for _, id := range run.QuestionIDs {
		question, err := s.questionStore.GetQuestion(ctx, id)
		if err != nil {
			writeJSON(w, 500, "读取命题结果失败")
			return
		}
		// 暂存态（AI 检查未通过）不下发：检查通过后前端回捞本接口才能拿到题目；
		// 检查淘汰的题目已物理删除，天然不在结果中。
		if question != nil && question.LibraryState == domain.LibraryDeleted {
			hiddenDeletedIDs = append(hiddenDeletedIDs, question.ID)
			continue
		}
		if question != nil && !domain.IsStagingStatus(question.Status) {
			questions = append(questions, *question)
		}
	}
	writeJSON(w, 200, map[string]any{
		"run":                  run,
		"hidden_deleted_ids":   hiddenDeletedIDs,
		"hidden_deleted_count": len(hiddenDeletedIDs),
		"provider_waiting":     !s.pipe.GenerationReady(ctx),
		"request_summary":      map[string]any{"knowledge_points": spec.Request.KnowledgePoints},
		"questions":            questions,
		"count":                len(run.QuestionIDs),
	})
}

// handleListQuestions 列出题目（数据库端分页）。
// 按用户权限的题库范围过滤（bank_ids 空=全部）。
// 题库分层过滤：tier=formal/working/eliminated（需对应查看权限）；
// 未指定时仅返回当前用户可见分层的并集。
// 查询参数：page=页码（默认1），page_size=每页数量（默认100，最大500），bank_id=题库筛选。
func (s *Server) handleListQuestions(w http.ResponseWriter, r *http.Request) {
	filter := storage.QuestionFilter{}
	if bankID := r.URL.Query().Get("bank_id"); bankID != "" {
		if bankID == "__unclassified__" {
			filter.Unclassified = true
		} else {
			filter.BankID = bankID
		}
	}
	filter, ok := s.applyTierScope(w, r, filter)
	if !ok {
		return
	}
	if !s.applyPersonalLibraryScope(w, r, &filter, r.URL.Query().Get("scope") == "global") {
		return
	}
	s.respondPagedQuestions(w, r, filter)
}

// handleGetQuestion 根据 ID 获取单道题目详情。
// 校验题库范围：受限范围用户仅可访问其可见题库内的题目。
func (s *Server) handleGetQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id") // 从 URL 路径提取题目 ID
	q, share, status, err := s.loadQuestionForRead(r, id)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	projected := questionLibraryProjection(*q, share == nil && q.OwnerID == auth.GetUserID(r.Context()))
	writeJSON(w, 200, projected)
}

func questionLibraryProjection(q domain.A2Question, private bool) domain.A2Question {
	if !private {
		q.ConfirmedBy = ""
		q.DeletedBy = ""
	}
	return q
}

// handleUpdateQuestion 修改题目内容（题干、选项、答案、解析）。
// 只更新请求中提供的字段，版本号自动递增。校验题库范围（question:edit）。
// 编辑窗口：AI 检查通过且尚未送审的新题，或审核流程退回的题目；题目唯一来源是 AI 生成。
func (s *Server) handleUpdateQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, status, err := s.loadScopedQuestion(r, id, domain.PermQuestionEdit); err != nil {
		writeError(w, status, err.Error())
		return
	}
	var req struct {
		ExpectedVersion         int              `json:"expected_version"`
		ExpectedReviewRevision  *int64           `json:"expected_review_revision"`
		ExpectedLibraryRevision *int64           `json:"expected_library_revision"`
		ClinicalStem            *string          `json:"clinical_stem"`
		Options                 *[]domain.Option `json:"options"`
		Answer                  *string          `json:"answer"`
		Explanation             *string          `json:"explanation"`
		ChangeReason            string           `json:"change_reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	if req.ExpectedVersion < 1 || req.ExpectedReviewRevision == nil || req.ExpectedLibraryRevision == nil || *req.ExpectedReviewRevision < 0 || *req.ExpectedLibraryRevision < 0 {
		writeError(w, 400, "请携带当前题目版本、审核修订和题库修订后保存")
		return
	}
	var saved *domain.A2Question
	err := s.withLibraryQuestionMutation(r.Context(), id, func(ctx context.Context) error {
		txRequest := r.WithContext(ctx)
		q, status, err := s.loadScopedQuestion(txRequest, id, domain.PermQuestionEdit)
		if err != nil {
			return &questionMutationError{status, err}
		}
		if err = s.ensureQuestionEditable(txRequest, q); err != nil {
			if errors.Is(err, ErrNotQuestionCreator) {
				return err
			}
			return &questionMutationError{409, err}
		}
		if req.ExpectedVersion != q.Version || *req.ExpectedReviewRevision != q.ReviewRevision || *req.ExpectedLibraryRevision != q.LibraryRevision {
			return storage.ErrQuestionLibraryConflict
		}
		original := *q
		if req.ClinicalStem != nil {
			q.ClinicalStem = *req.ClinicalStem
		}
		if req.Options != nil {
			q.Options = *req.Options
		}
		if req.Answer != nil {
			q.Answer = *req.Answer
		}
		if req.Explanation != nil {
			q.Explanation = *req.Explanation
		}
		if err := q.Validate(); err != nil {
			return &questionMutationError{400, err}
		}
		if domain.QuestionContentEqual(original, *q) {
			saved = q
			return nil
		}
		// Teachers own the wording of their revisions. Basic structure was
		// checked above; generation-only prose rules must not block an edit.
		q.Version++
		q.UpdatedAt = time.Now()
		if q.Status == domain.StatusPublished {
			q.Status = domain.StatusAIReviewed
		}
		actor := auth.GetUsername(ctx)
		if actor == "" {
			actor = auth.GetUserID(ctx)
		}
		changeType := "pre_review_edit"
		if original.LibraryState == domain.LibraryActive {
			changeType = "author_edit"
		} else if s.reviewSvc != nil {
			if task, err := s.reviewSvc.GetTaskByQuestionID(ctx, id); err != nil {
				return err
			} else if task != nil && task.Status == domain.StatusRevisionRequired {
				changeType = "revision_edit"
			}
		}
		changeCtx := storage.WithQuestionChange(ctx, storage.QuestionChange{Actor: actor, ChangeType: changeType, ChangeNote: strings.TrimSpace(req.ChangeReason)})
		if err := s.questionStore.SaveQuestion(changeCtx, *q); err != nil {
			return err
		}
		if s.auditSvc != nil {
			if err := s.auditSvc.LogUpdate(changeCtx, id, actor, "作者保存修订，当前版本需重新确认入库"); err != nil {
				return err
			}
		}
		saved, err = s.questionStore.GetQuestion(changeCtx, id)
		return err
	})
	if err != nil {
		s.writeQuestionMutationError(w, err)
		return
	}
	writeJSON(w, 200, saved)
}

type questionMutationError struct {
	status int
	err    error
}

func (e *questionMutationError) Error() string { return e.err.Error() }
func (e *questionMutationError) Unwrap() error { return e.err }
func (s *Server) withLibraryQuestionMutation(ctx context.Context, id string, fn func(context.Context) error) error {
	store, ok := s.questionStore.(storage.QuestionLibraryTransactionStore)
	if !ok {
		return &questionMutationError{503, errors.New("题目事务服务不可用")}
	}
	return store.WithLibraryTransaction(ctx, []string{id}, fn)
}
func (s *Server) writeQuestionMutationError(w http.ResponseWriter, err error) {
	var typed *questionMutationError
	switch {
	case errors.As(err, &typed):
		writeError(w, typed.status, typed.Error())
	case errors.Is(err, ErrNotQuestionCreator):
		writeError(w, 403, err.Error())
	case errors.Is(err, storage.ErrQuestionLibraryConflict), errors.Is(err, domain.ErrReviewConflict), errors.Is(err, domain.ErrQuestionVersionConflict):
		writeError(w, 409, err.Error())
	default:
		writeError(w, 500, "题目保存失败，请稍后重试")
	}
}

// ErrNotQuestionCreator 退回修改窗口的编辑人不是题目出题人本人。
// 属于权限类拒绝，API 层应映射为 403。
var ErrNotQuestionCreator = errors.New("仅出题人本人可以修改退回的题目")

// ensureQuestionEditable 校验题目是否处于可编辑窗口：
// 1) AI 检查通过、尚未创建审核任务的新题；2) 审核退回修改的题目。
// 两种窗口都只允许出题人本人编辑，保存只生成新版本，不触发 AI 复检。
func (s *Server) ensureQuestionEditable(r *http.Request, q *domain.A2Question) error {
	if q.LibraryState == domain.LibraryDeleted {
		return errors.New("题目在删除题库，请先恢复后整理")
	}
	if q.Status != domain.StatusAIReviewed && !(q.Status == domain.StatusPublished && q.PersonallyExportable()) {
		return errors.New("题目尚未通过首检，或处于审核/驳回/归档锁定状态，不能修改")
	}
	callerID, callerName := auth.GetUserID(r.Context()), auth.GetUsername(r.Context())
	isCreator := q.OwnerID != "" && callerID != "" && q.OwnerID == callerID
	if callerID == "" || q.OwnerID == "" {
		isCreator = q.CreatedBy != "" && q.CreatedBy == callerName || q.CreatedBy == "" && q.OwnerID != "" && q.OwnerID == callerName
	}
	if !isCreator {
		return ErrNotQuestionCreator
	}
	if q.Status == domain.StatusPublished && q.ConfirmationSource == domain.ConfirmSourceLegacy {
		evidence, ok := s.questionStore.(storage.InitialCheckEvidenceStore)
		if !ok {
			return errors.New("历史题缺少可核验的首检依据，目前仅可读取和导出")
		}
		passed, err := evidence.HasInitialCheckPass(r.Context(), q.ID)
		if err != nil {
			return errors.New("暂时无法核验历史首检依据，请稍后重试")
		}
		if !passed {
			return errors.New("历史题缺少初始首检通过依据，目前仅可读取和导出，不能修订")
		}
	}

	if s.shareStore != nil {
		share, err := s.shareStore.GetQuestionShareByQuestionID(r.Context(), q.ID)
		if err != nil {
			return errors.New("无法核对分享资格，暂不能修改")
		}
		if share != nil && share.Status == domain.QuestionSharePending {
			return errors.New("分享审批中暂不能修改，请等待审批结束")
		}
		if share != nil && share.Status == domain.QuestionShareApproved {
			store, ok := s.shareStore.(storage.QuestionShareSnapshotStore)
			if !ok {
				return errors.New("无法核验共享固定版本，暂不能修改")
			}
			snapshot, _, err := store.GetQuestionShareSnapshot(r.Context(), q.ID)
			if err != nil || snapshot == nil {
				return errors.New("共享固定版本不可用，暂不能修改")
			}
		}
	}
	var task *domain.ReviewTask
	var err error
	if store, ok := s.questionStore.(interface {
		GetTaskByQuestionID(context.Context, string) (*domain.ReviewTask, error)
	}); ok {
		task, err = store.GetTaskByQuestionID(r.Context(), q.ID)
	} else if s.reviewSvc != nil {
		task, err = s.reviewSvc.GetTaskByQuestionID(r.Context(), q.ID)
	} else {
		return errors.New("无法核对持久审核任务，暂不能修改")
	}
	if err != nil {
		return errors.New("查询审核任务失败，暂不能修改")
	}
	if store, ok := s.questionStore.(interface {
		HasActiveReviewTask(context.Context, string) (bool, error)
	}); ok {
		active, err := store.HasActiveReviewTask(r.Context(), q.ID)
		if err != nil {
			return errors.New("查询审核任务失败，暂不能修改")
		}
		if active && (task == nil || task.Status != domain.StatusRevisionRequired) {
			return errors.New("题目仍有在途审核任务，不能修改")
		}
	}
	if task == nil || task.Status == domain.StatusRevisionRequired || task.Status == domain.StatusPublished || task.Status == domain.StatusArchived {
		return nil
	}

	return errors.New("题目已经进入审核流程，不能在整理页编辑")
}

func (s *Server) handleListQuestionVersions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	current, share, status, err := s.loadQuestionForRead(r, id)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	if share != nil {
		versions := []domain.QuestionVersion{{QuestionID: id, Version: current.Version, Snapshot: questionLibraryProjection(*current, false), ChangeType: "shared", ChangeNote: "申请分享时固定的内容版本", CreatedAt: share.CreatedAt}}
		writeJSON(w, 200, map[string]any{"versions": versions, "total": 1})
		return
	}
	versions, err := s.questionStore.ListQuestionVersions(r.Context(), id)
	if err != nil {
		writeError(w, 500, "读取版本历史失败: "+err.Error())
		return
	}
	if current.OwnerID != auth.GetUserID(r.Context()) {
		for i := range versions {
			versions[i].Snapshot = questionLibraryProjection(versions[i].Snapshot, false)
		}
	}
	writeJSON(w, 200, map[string]any{"versions": versions, "total": len(versions)})
}

func (s *Server) handleRestoreQuestionVersion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, status, err := s.loadScopedQuestion(r, id, domain.PermQuestionEdit); err != nil {
		writeError(w, status, err.Error())
		return
	}
	var req struct {
		ExpectedVersion         int    `json:"expected_version"`
		ExpectedReviewRevision  *int64 `json:"expected_review_revision"`
		ExpectedLibraryRevision *int64 `json:"expected_library_revision"`
		Version                 int    `json:"version"`
		Reason                  string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	if req.ExpectedVersion < 1 || req.ExpectedReviewRevision == nil || req.ExpectedLibraryRevision == nil || *req.ExpectedReviewRevision < 0 || *req.ExpectedLibraryRevision < 0 || req.Version < 1 {
		writeError(w, 400, "请携带当前三项修订及有效的历史版本")
		return
	}
	var saved *domain.A2Question
	err := s.withLibraryQuestionMutation(r.Context(), id, func(ctx context.Context) error {
		txRequest := r.WithContext(ctx)
		current, status, err := s.loadScopedQuestion(txRequest, id, domain.PermQuestionEdit)
		if err != nil {
			return &questionMutationError{status, err}
		}
		if err = s.ensureQuestionEditable(txRequest, current); err != nil {
			if errors.Is(err, ErrNotQuestionCreator) {
				return err
			}
			return &questionMutationError{409, err}
		}
		if current.Version != req.ExpectedVersion || current.ReviewRevision != *req.ExpectedReviewRevision || current.LibraryRevision != *req.ExpectedLibraryRevision {
			return storage.ErrQuestionLibraryConflict
		}
		history, err := s.questionStore.GetQuestionVersion(ctx, id, req.Version)
		if err != nil {
			return err
		}
		if history == nil {
			return &questionMutationError{404, errors.New("历史版本不存在")}
		}
		if domain.QuestionContentEqual(*current, history.Snapshot) {
			return errors.New("当前内容与该版本相同，无需重复恢复")
		}
		restored := domain.RestoreQuestionContent(*current, history.Snapshot)
		restored.Version = current.Version + 1
		restored.Status = domain.StatusAIReviewed
		restored.UpdatedAt = time.Now()
		if err := restored.Validate(); err != nil {
			return &questionMutationError{400, err}
		}
		actor := auth.GetUsername(ctx)
		if actor == "" {
			actor = auth.GetUserID(ctx)
		}
		changeCtx := storage.WithQuestionChange(ctx, storage.QuestionChange{Actor: actor, ChangeType: "restore", ChangeNote: strings.TrimSpace(req.Reason)})
		if err := s.questionStore.SaveQuestion(changeCtx, restored); err != nil {
			return err
		}
		if s.auditSvc != nil {
			if err := s.auditSvc.Log(changeCtx, id, "restore", actor, fmt.Sprintf("恢复历史v%d并生成v%d，需重新确认", req.Version, restored.Version)); err != nil {
				return err
			}
		}
		saved, err = s.questionStore.GetQuestion(changeCtx, id)
		return err
	})
	if err != nil {
		s.writeQuestionMutationError(w, err)
		return
	}
	writeJSON(w, 200, saved)
}

// handleUnpublishQuestion 管理员把已通过（published）的题目撤回到 ai 检查通过状态
// （ai_reviewed），供发现已通过题目需要修订时使用；撤回后可重新送审。
// 仅用户管理权限（user:manage）可用；写审计留痕。
func (s *Server) handleUnpublishQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q, status, err := s.loadScopedQuestion(r, id, domain.PermQuestionEdit)
	if err != nil {
		writeError(w, status, err.Error())
		return
	}
	if q.LibraryState == domain.LibraryDeleted {
		writeError(w, 409, "题目已移入删除题库，请由本人先恢复；不能通过旧撤回改写删除状态")
		return
	}
	if q.Status != domain.StatusPublished {
		writeError(w, http.StatusConflict, "仅已通过（published）的题目可以撤回")
		return
	}
	share, shareErr := s.shareForMutation(r.Context(), id)
	if shareErr != nil {
		writeError(w, 503, "无法核验分享状态，暂不能撤回")
		return
	}
	if share != nil {
		writeError(w, http.StatusConflict, "题目已经提交全局题库分享，不能撤回；请先完成或结束分享申请")
		return
	}
	// 撤回会把题目送回出题人「待我修改」窗口，而退修题仅出题人本人可改；
	// 属主账号已停用或无编辑权限时撤回会让题目永久无人可改，必须先恢复账号。
	if owner, ownerErr := s.authSvc.GetUserByID(q.OwnerID); ownerErr != nil || owner == nil {
		writeError(w, http.StatusConflict, "题目归属账号不存在，撤回后题目将无人可修改；请改用删除归档")
		return
	} else if !owner.Enabled {
		writeError(w, http.StatusConflict, fmt.Sprintf("题目归属账号 %s 已停用，撤回后题目将无人可修改；请先恢复该账号或改用删除归档", owner.Username))
		return
	} else if ownerPerms, permErr := s.authSvc.PermissionsForAssignments(r.Context(), owner.Roles, owner.Permissions); permErr != nil {
		writeError(w, 500, "校验归属账号权限失败: "+permErr.Error())
		return
	} else if !slices.Contains(ownerPerms, domain.PermQuestionEdit) {
		writeError(w, http.StatusConflict, fmt.Sprintf("题目归属账号 %s 已无编辑权限，撤回后题目将无人可修改；请先恢复其权限或改用删除归档", owner.Username))
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}

	note := "撤回已通过题目至 AI 检查通过状态"
	if reason := strings.TrimSpace(req.Reason); reason != "" {
		note += "：" + reason
	}
	actor := auth.GetUsername(r.Context())
	reviewerID := auth.GetUserID(r.Context())
	reviewerName := actor
	if user, userErr := s.authSvc.GetUserByID(reviewerID); userErr == nil && user != nil && strings.TrimSpace(user.DisplayName) != "" {
		reviewerName = user.DisplayName
	}
	ok := s.withAuditedTx(w, r, "撤回题目",
		func(txCtx context.Context) error {
			return s.reviewSvc.ReturnPublishedForRevision(txCtx, q.ID, reviewerID, reviewerName, note)
		},
		func(txCtx context.Context) error {
			return s.auditSvc.Log(txCtx, q.ID, "unpublish", actor, note)
		})
	if !ok {
		return
	}
	updated, _ := s.questionStore.GetQuestion(r.Context(), id)
	writeJSON(w, 200, updated)
}

// handleDeleteQuestion 删除题目（一律归档：移入淘汰题库，版本快照与审核记录
// 完整保留）。两类调用者：
//   - 有权限者（question:delete / 正式题库 question:delete_formal）：可删除其
//     题库范围内的题目，含全局题库清理（如管理员撤下他人题目）。
//
// 普通作者使用带三轴守卫的 library-delete，可在个人删除题库恢复。
// 此旧端点仅保留有权限的兼容管理归档，不是个人删除的旁路。
//
// 通用红线（有权限者与所有者同样受限）：淘汰终态不可再删；流程中的题不可删
// （审核中/待决断/需修改）；暂存态不可见亦不可删；分享审批中或已入全局正式库
// 的题不可删（分享被否决不阻塞删除）。
func (s *Server) guardArchiveReview(ctx context.Context, id string) error {
	var active bool
	var err error
	if tasks, ok := s.questionStore.(interface {
		HasActiveReviewTask(context.Context, string) (bool, error)
	}); ok {
		active, err = tasks.HasActiveReviewTask(ctx, id)
	} else if s.reviewSvc != nil {
		var task *domain.ReviewTask
		task, err = s.reviewSvc.GetTaskByQuestionID(ctx, id)
		if task != nil {
			switch task.Status {
			case domain.StatusPublished, domain.StatusApproved, domain.StatusRejected, domain.StatusArchived:
			default:
				active = true
			}
		}
	} else {
		err = errors.New("持久审核状态不可用")
	}
	if err != nil {
		return &questionMutationError{503, errors.New("无法核验在途任务，暂不能归档")}
	}
	if active {
		return &questionMutationError{409, errors.New("题目仍有在途审核或退修任务，不能归档；请继续处理当前任务")}
	}
	return nil
}

func (s *Server) handleDeleteQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	q, err := s.questionStore.GetQuestion(r.Context(), id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if q == nil {
		writeError(w, 404, "题目不存在")
		return
	}

	// 通用红线：先于权限判定，语义对两类调用者一致
	if q.LibraryState == domain.LibraryDeleted || q.Tier() == domain.TierEliminated {
		writeError(w, http.StatusConflict, "已淘汰的题目为终态留档（驳回锁定或已归档），不可删除")
		return
	}
	if domain.IsStagingStatus(q.Status) {
		writeError(w, http.StatusForbidden, "暂存态题目（AI 检查未完成）不可见，也不可删除")
		return
	}
	if q.Status == domain.StatusReviewing || q.Status == domain.StatusConflict || q.Status == domain.StatusRevisionRequired {
		writeError(w, http.StatusConflict, "流程中的题目不能删除（审核中/待决断/需修改）：请先完成当前环节")
		return
	}

	perm := domain.PermQuestionDelete
	if q.Tier() == domain.TierFormal {
		perm = domain.PermQuestionDeleteFormal
	}
	privileged := s.hasPermission(r, perm)
	userID := auth.GetUserID(r.Context())
	isOwner := q.OwnerID != "" && q.OwnerID == userID

	if !privileged && !isOwner {
		writeError(w, http.StatusForbidden, "无权删除该题目：只能删除本人题目，或由有权限者操作")
		return
	}
	if !privileged {
		writeError(w, http.StatusConflict, "请在个人题库使用可恢复删除入口，并提交当前版本及修订号")
		return
	}
	// A returned question is ai_reviewed while its persisted task remains
	// revision_required. The same in-flight guard applies to administrators.
	if err := s.guardArchiveReview(r.Context(), id); err != nil {
		s.writeQuestionMutationError(w, err)
		return
	}

	// 分享守卫：审批中/已入全局正式库不可删；被否决的分享不阻塞
	share, shareErr := s.shareForMutation(r.Context(), id)
	if shareErr != nil {
		writeError(w, 503, "无法核验分享状态，暂不能归档")
		return
	}
	if share != nil &&
		(share.Status == domain.QuestionSharePending || share.Status == domain.QuestionShareApproved) {
		writeError(w, http.StatusConflict, "分享审批中或已进入全局正式库的题目不能删除；请先完成或结束分享申请")
		return
	}

	// 有权限者按题库范围与分层权限校验（全局题库清理走同一入口）
	if privileged {
		if _, status, err := s.loadScopedQuestion(r, id, perm); err != nil {
			writeError(w, status, err.Error())
			return
		}
	}

	actor := auth.GetUsername(r.Context())
	note := "题目移入淘汰题库归档（保留版本快照与审核记录）"
	archived := *q
	archived.Status = domain.StatusArchived
	archived.UpdatedAt = time.Now()
	changeType := "archive"
	if isOwner {
		changeType = "owner_archive"
	}

	ok := s.withAuditedTx(w, r, "归档题目",
		func(txCtx context.Context) error {
			return s.withLibraryQuestionMutation(txCtx, id, func(locked context.Context) error {
				fresh, err := s.questionStore.GetQuestion(locked, id)
				if err != nil {
					return err
				}
				if fresh == nil || fresh.Version != q.Version || fresh.ReviewRevision != q.ReviewRevision || fresh.LibraryRevision != q.LibraryRevision {
					return storage.ErrQuestionLibraryConflict
				}
				if err := s.guardArchiveReview(locked, id); err != nil {
					return err
				}
				share, err := s.shareForMutation(locked, id)
				if err != nil {
					return &questionMutationError{503, errors.New("无法核验分享状态，暂不能归档")}
				}
				if share != nil && (share.Status == domain.QuestionSharePending || share.Status == domain.QuestionShareApproved) {
					return storage.ErrQuestionLibraryConflict
				}
				archiveCtx := storage.WithQuestionChange(locked, storage.QuestionChange{Actor: actor, ChangeType: changeType, ChangeNote: note})
				return s.questionStore.SaveQuestion(archiveCtx, archived)
			})
		},
		func(txCtx context.Context) error {
			return s.auditSvc.Log(txCtx, id, "archive", actor, note)
		})
	if !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"status": "ok", "id": id, "archived": true})
}

// handlePublishQuestion 将审核通过的题目发布到正式题库。
// 校验题库范围（review:final）。
func (s *Server) handlePublishQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	actor := auth.GetUsername(r.Context())
	if _, status, err := s.loadScopedQuestion(r, id, domain.PermReviewFinal); err != nil {
		writeError(w, status, err.Error())
		return
	}
	ok := s.withAuditedTx(w, r, "发布题目",
		func(txCtx context.Context) error {
			if err := s.reviewSvc.PublishQuestion(txCtx, id); err != nil {
				writeError(w, 400, err.Error())
				return errResponded
			}
			return nil
		},
		func(txCtx context.Context) error {
			return s.auditSvc.LogPublish(txCtx, id, actor)
		})
	if !ok {
		return
	}

	q, _ := s.questionStore.GetQuestion(r.Context(), id)
	writeJSON(w, 200, q)
}

// handleSearchQuestions 搜索题目（数据库端过滤 + 分页）。
// 准确筛选：status（状态）、tier（题库分层，需对应查看权限；未指定时收敛到可见分层并集）、
//
//	profession（专业，逗号多选）、difficulty（难度）、outline_code（大纲代码前缀）、
//	bank_id（分类子题库，__unclassified__=待归类）
//
// 模糊搜索：q（题干、选项、解析、专业、系统、知识点、大纲代码、ID 等；多词 AND）
func (s *Server) handleSearchQuestions(w http.ResponseWriter, r *http.Request) {
	difficulty := r.URL.Query().Get("difficulty")
	filter := storage.QuestionFilter{
		Status:           r.URL.Query().Get("status"),
		Keyword:          r.URL.Query().Get("q"),
		OutlineCode:      r.URL.Query().Get("outline_code"),
		ClassifiableOnly: r.URL.Query().Get("classifiable") == "true",
	}
	if difficulty == "easy" || difficulty == "medium" || difficulty == "hard" {
		filter.DifficultyBand = difficulty
	} else {
		filter.Difficulty = difficulty
	}
	// 专业筛选：支持多选（逗号分隔）
	if p := r.URL.Query().Get("profession"); p != "" {
		for _, part := range strings.Split(p, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				filter.Professions = append(filter.Professions, part)
			}
		}
	}
	if bankID := r.URL.Query().Get("bank_id"); bankID != "" {
		if bankID == "__unclassified__" {
			filter.Unclassified = true
		} else {
			filter.BankID = bankID
		}
	}
	filter, ok := s.applyTierScope(w, r, filter)
	if !ok {
		return
	}
	s.respondPagedQuestions(w, r, filter)
}

// handleMyNewQuestions 返回当前账号刚生成且 AI 检查通过、尚未送审的题目。
// 该接口始终按 owner_id 过滤，不受 question:view_all 等管理员范围权限影响，
// 用于“新题修改与提交审核”工作区避免误提交他人题目。
func (s *Server) handleMyNewQuestions(w http.ResponseWriter, r *http.Request) {
	filter := storage.QuestionFilter{
		OwnerID:            auth.GetUserID(r.Context()),
		Status:             string(domain.StatusAIReviewed),
		Tiers:              []string{string(domain.TierWorking)},
		NoActiveReviewTask: true,
	}
	// 题目整理页 = 待确认（unfiled）+ 已确认待修订（active 且内容版本已超过
	// 确认版本的回落项也属 unfiled）；已确认入库的题移入普通题库页签。
	filter.LibraryStates = []string{string(domain.LibraryUnfiled)}
	s.respondPagedQuestions(w, r, filter)
}

// applyTierScope 解析 tier 查询参数并按用户可见分层收敛过滤条件。
//   - 显式指定分层：无对应查看权限返回 403，参数非法返回 400；
//   - 未指定：收敛为当前用户可见分层的并集（正式/过程/淘汰各自独立授权）；
//   - 用户无任何分层查看权限：直接输出空页结果。
//
// 返回 (filter, false) 表示响应已写出，调用方应立即返回。
func (s *Server) applyTierScope(w http.ResponseWriter, r *http.Request, filter storage.QuestionFilter) (storage.QuestionFilter, bool) {
	// 暂存态（AI 检查未通过）对用户不可见：显式按暂存状态筛选一律返回空页，
	// 包括本人题目与管理员——待检内容只经检查进度接口暴露状态，不暴露题目。
	if domain.IsStagingStatus(domain.QuestionStatus(filter.Status)) {
		page, pageSize := questionPageParams(r)
		writeJSON(w, 200, map[string]any{
			"questions": []domain.A2Question{},
			"total":     0,
			"page":      page,
			"page_size": pageSize,
			"has_more":  false,
		})
		return filter, false
	}
	filter, ok := s.applyQuestionScope(w, r, filter)
	if !ok {
		return filter, false
	}
	globalScope := r.URL.Query().Get("scope") == "global"
	if globalScope || r.URL.Query().Get("tier") == string(domain.TierFormal) {
		filter.ExcludeDeleted = true
	}
	if requested := r.URL.Query().Get("tier"); requested != "" {
		tier, ok := domain.ParseTier(requested)
		if !ok {
			writeError(w, 400, "无效的题库分层: "+requested)
			return filter, false
		}
		if globalScope {
			_, tierPerm := s.globalTierPermissions(globalShareStatusForTier(tier))
			if !s.hasPermission(r, tierPerm) {
				writeError(w, 403, "无权访问全局"+tier.Name())
				return filter, false
			}
			filter.Tiers = nil
			filter.GlobalStatuses = []string{string(globalShareStatusForTier(tier))}
			filter.BankScope, filter.ScopeRestricted = s.questionBankScope(r, tierPerm)
			return filter, true
		}
		if r.URL.Query().Get("scope") == "personal" {
			if !s.hasPermission(r, domain.PermQuestionView) && !s.canViewTier(r, tier) {
				writeError(w, 403, "无权访问"+tier.Name())
				return filter, false
			}
			// 新版个人题库：严格按 owner_id 过滤（applyQuestionScope 已写入）。
			// 2026-09-19 产品口径：任何人的"我的题库"只能看到自己的题目，
			// view_all/题库范围不再放大个人视野；他人题目只经全局题库
			// （分享推导分层）或审核任务等专门入口出现。
		} else if !s.canViewTier(r, tier) {
			// 未显式传 scope 的旧客户端继续使用原有分层权限语义。
			writeError(w, http.StatusForbidden, "无权访问"+tier.Name())
			return filter, false
		} else {
			s.applyQuestionVisibility(r, &filter, domain.TierViewPerm(tier))
		}
		filter.Tiers = []string{string(tier)}
		return filter, true
	}
	if globalScope {
		statuses := []string{}
		for _, tier := range []domain.QuestionTier{domain.TierFormal, domain.TierWorking, domain.TierEliminated} {
			_, tierPerm := s.globalTierPermissions(globalShareStatusForTier(tier))
			if s.hasPermission(r, tierPerm) {
				statuses = append(statuses, string(globalShareStatusForTier(tier)))
			}
		}
		if len(statuses) == 0 {
			page, pageSize := questionPageParams(r)
			writeJSON(w, http.StatusOK, map[string]any{
				"questions": []domain.A2Question{}, "total": 0,
				"page": page, "page_size": pageSize, "has_more": false,
			})
			return filter, false
		}
		filter.GlobalStatuses = statuses
		filter.Tiers = nil
		for _, tier := range []domain.QuestionTier{domain.TierFormal, domain.TierWorking, domain.TierEliminated} {
			_, tierPerm := s.globalTierPermissions(globalShareStatusForTier(tier))
			if s.hasPermission(r, tierPerm) {
				filter.BankScope, filter.ScopeRestricted = s.questionBankScope(r, tierPerm)
				break
			}
		}
		return filter, true
	}
	if r.URL.Query().Get("scope") == "personal" {
		// 个人视野严格仅本人（applyQuestionScope 已写入 OwnerID），
		// 不被 view_all/题库范围放大——2026-09-19 产品口径。
		if s.hasPermission(r, domain.PermQuestionView) {
			filter.Tiers = []string{string(domain.TierFormal), string(domain.TierWorking), string(domain.TierEliminated)}
		} else {
			for _, tier := range s.visibleTiers(r) {
				filter.Tiers = append(filter.Tiers, string(tier))
			}
		}
		return filter, true
	}
	visible := s.visibleTiers(r)
	if len(visible) == 0 {
		page, pageSize := questionPageParams(r)
		writeJSON(w, 200, map[string]any{
			"questions": []domain.A2Question{},
			"total":     0,
			"page":      page,
			"page_size": pageSize,
			"has_more":  false,
		})
		return filter, false
	}
	tiers := make([]string, 0, len(visible))
	for _, t := range visible {
		tiers = append(tiers, string(t))
	}
	// 个人轴可见范围：view_all 全部 / bank_ids 范围 / 默认仅本人。
	s.applyQuestionVisibility(r, &filter, domain.PermQuestionView)
	filter.Tiers = tiers
	return filter, true
}

// handleListProfessions 返回题库中所有出现的专业值（供筛选下拉，数据库端去重）。
func (s *Server) handleListProfessions(w http.ResponseWriter, r *http.Request) {
	questions, err := s.questionStore.ListQuestions(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	set := map[string]bool{}
	canManageBanks, _ := s.authSvc.HasPermission(r.Context(), auth.GetUserID(r.Context()), domain.PermBankManage)
	globalCatalog := s.hasPermission(r, domain.PermQuestionViewGlobal)
	viewerID := auth.GetUserID(r.Context())
	for i := range questions {
		if !globalCatalog && questions[i].OwnerID != viewerID {
			continue
		}
		visibleForManagement := canManageBanks && questions[i].Tier() == domain.TierWorking
		if questions[i].Profession != "" && (visibleForManagement || s.canViewQuestion(r, &questions[i])) {
			set[questions[i].Profession] = true
		}
	}
	professions := make([]string, 0, len(set))
	for profession := range set {
		professions = append(professions, profession)
	}
	sort.Strings(professions)
	writeJSON(w, 200, map[string]any{"professions": professions, "total": len(professions)})
}

// handleAddQuestionBank 把题目加入指定题库（多对多，不影响其他库归属）。
func (s *Server) handleAddQuestionBank(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req struct {
		BankID string `json:"bank_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if err := s.bankSvc.AddQuestionToBank(r.Context(), id, req.BankID); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	q, _ := s.questionStore.GetQuestion(r.Context(), id)
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = auth.GetUserID(r.Context())
	}
	if q != nil {
		s.auditSvc.LogUpdate(r.Context(), id, actor, fmt.Sprintf("题目加入题库: %s", req.BankID))
	}
	writeJSON(w, 200, q)
}

// handleRemoveQuestionBank 把题目从指定题库移出（不影响其他库归属）。
func (s *Server) handleRemoveQuestionBank(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	bankID := r.PathValue("bankId")
	if err := s.bankSvc.RemoveQuestionFromBank(r.Context(), id, bankID); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	q, _ := s.questionStore.GetQuestion(r.Context(), id)
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = auth.GetUserID(r.Context())
	}
	if q != nil {
		s.auditSvc.LogUpdate(r.Context(), id, actor, fmt.Sprintf("题目移出题库: %s", bankID))
	}
	writeJSON(w, 200, q)
}

// handleMoveQuestionsBankBatch 批量把题目加入题库（多对多，含所有状态题目，高效 SQL）。
// 请求：{"ids": [...], "bank_id": "xxx"}
func (s *Server) handleMoveQuestionsBankBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs    []string `json:"ids"`
		BankID string   `json:"bank_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if len(req.IDs) == 0 {
		writeError(w, 400, "请至少选择一道题目")
		return
	}
	moved, err := s.bankSvc.AddQuestionsToBankBatch(r.Context(), req.IDs, req.BankID)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	actor := auth.GetUsername(r.Context())
	if actor == "" {
		actor = auth.GetUserID(r.Context())
	}
	if moved > 0 {
		s.auditSvc.LogUpdate(r.Context(), "", actor, fmt.Sprintf("批量加入 %d 道题到题库 %s", moved, req.BankID))
	}
	writeJSON(w, 200, map[string]any{"moved": moved, "failed": []string{}})
}

func bankIDLabel(bankID string) string {
	if bankID == "" {
		return "待归类"
	}
	return bankID
}

// questionBankScope 返回当前用户对指定权限的题库可见范围。
// 全范围用户返回 (nil, false)；权限缺失或查询失败时返回空受限范围（失败关闭）。
// 受限范围用户返回 (scope, true)。
func (s *Server) questionBankScope(r *http.Request, perm string) ([]string, bool) {
	userID := auth.GetUserID(r.Context())
	if userID == "" {
		return nil, false
	}
	scope, fullScope, hasPerm, err := s.authSvc.GetBankScope(r.Context(), userID, perm)
	if err != nil || !hasPerm {
		return []string{}, true
	}
	if fullScope {
		return nil, false
	}
	return scope, true
}

// respondPagedQuestions 在存储端过滤分页后输出统一 JSON 结构。
// 支持 page（默认1）和 page_size（默认100，最大500）参数。
func (s *Server) respondPagedQuestions(w http.ResponseWriter, r *http.Request, filter storage.QuestionFilter) {
	var ok bool
	filter, ok = s.applyAuthoringTaskFilter(w, r, filter)
	if !ok {
		return
	}
	page, pageSize := questionPageParams(r)
	questions, total, err := s.questionStore.SearchQuestions(r.Context(), filter, page, pageSize)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if questions == nil {
		questions = []domain.A2Question{}
	}
	for i := range questions {
		questions[i] = questionLibraryProjection(questions[i], r.URL.Query().Get("scope") != "global" && questions[i].OwnerID == auth.GetUserID(r.Context()))
	}
	writeJSON(w, 200, map[string]any{
		"questions": questions,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
		"has_more":  page*pageSize < total,
		"task":      map[string]string{"kind": r.URL.Query().Get("task_kind"), "id": r.URL.Query().Get("task_id")},
	})
}

// questionPageParams 解析并钳制分页参数（page<1 视为 1，page_size 越界视为 100）。
func questionPageParams(r *http.Request) (page, pageSize int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	pageSize, _ = strconv.Atoi(r.URL.Query().Get("page_size"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 500 {
		pageSize = 100
	}
	return page, pageSize
}

// A missing or unavailable share lookup is never evidence that a protected
// publication has no sharing relationship.
func (s *Server) shareForMutation(ctx context.Context, id string) (*domain.QuestionShareRequest, error) {
	if s.shareStore != nil {
		return s.shareStore.GetQuestionShareByQuestionID(ctx, id)
	}
	if store, ok := s.questionStore.(storage.QuestionShareStore); ok {
		return store.GetQuestionShareByQuestionID(ctx, id)
	}
	return nil, errors.New("分享存储不可用")
}
