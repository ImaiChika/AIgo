package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"aigo/internal/auth"
	"aigo/internal/batch"
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/knowledge"
	"aigo/internal/knowledgebase"
	"aigo/internal/storage"
	"aigo/internal/workflowtrace"
)

// withBatchOwnerName 回填任务归属人用户名，供任务卡片展示"由谁生成"。
// 查不到（如账号已注销）时保持为空，前端回退为"其他用户"。
func (s *Server) withBatchOwnerName(job *batch.BatchJob) *batch.BatchJob {
	if job == nil || job.OwnerID == "" {
		return job
	}
	if owner, err := s.authSvc.GetUserByID(job.OwnerID); err == nil && owner != nil {
		job.OwnerName = owner.Username
	}
	return job
}

// handleBatchCapabilities 返回脱敏后的批量执行器状态。
func (s *Server) handleBatchCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.batchSvc.Capabilities())
}

// batchSubmitRequest 批量提交请求体；mode/total_count/request_key 为 C2 新增，
// 省略 mode 时保持旧“每点K题”语义。
type batchSubmitRequest struct {
	Presentation             *domain.PresentationRequirements `json:"presentation"`
	WorkflowMode             string                           `json:"workflow_mode"`
	VersionID                string                           `json:"version_id"`
	KnowledgePointIDs        []string                         `json:"knowledge_point_ids"`
	Limit                    int                              `json:"limit"`
	SkipExisting             bool                             `json:"skip_existing"`
	Count                    int                              `json:"count"`
	TotalCount               int                              `json:"total_count"`
	Mode                     string                           `json:"mode"`
	RequestKey               string                           `json:"request_key"`
	FromCode                 string                           `json:"from_code"`
	ToCode                   string                           `json:"to_code"`
	OutlineCodes             string                           `json:"outline_codes"` // 逗号分隔的知识点代码
	ApplyPersonalExperiences *bool                            `json:"apply_personal_experiences,omitempty"`
	JobName                  string                           `json:"job_name"` // 自定义任务名称
}

// handleBatchSubmit 向当前配置的批量执行器提交任务。
// 请求：{"limit": 100, "skip_existing": true, "count": 1, "from_code": "", "to_code": "", "job_name": "任务名"}
func (s *Server) handleBatchSubmit(w http.ResponseWriter, r *http.Request) {
	var req batchSubmitRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}

	// 批量模式：random_total=所选范围随机共N题；per_point=每点K题（省略时的旧语义）。
	// 两种模式的题数字段互斥，混用直接拒绝，不靠总数猜模式。
	mode := req.Mode
	if mode == "" {
		mode = domain.BatchModePerPoint
	}
	if !domain.ValidateBatchMode(mode) {
		writeError(w, http.StatusBadRequest, "无效的批量模式：仅支持 per_point 或 random_total")
		return
	}
	if mode == domain.BatchModeRandomTotal {
		if req.TotalCount < 1 {
			writeError(w, http.StatusBadRequest, "随机批量需提供正整数总题数 total_count")
			return
		}
		if req.Count > 1 {
			writeError(w, http.StatusBadRequest, "随机批量与每点题数互斥：请仅填写 total_count")
			return
		}
	} else {
		if req.TotalCount != 0 {
			writeError(w, http.StatusBadRequest, "每点模式不支持 total_count：请使用 count，或改用 random_total 模式")
			return
		}
	}
	if req.Count <= 0 {
		req.Count = 1
	}
	if mode == domain.BatchModePerPoint && req.Count > 20 {
		writeError(w, http.StatusBadRequest, "每个知识点最多生成 20 道题")
		return
	}
	if l := len(req.RequestKey); l > 128 {
		writeError(w, http.StatusBadRequest, "request_key 过长（最多 128 字符）")
		return
	}

	// Hash the accepted wire intent before candidate resolution pins defaults.
	requestHash := batchRequestHash(&req, mode)
	release, err := s.acquireBatchSubmission(r.Context(), auth.GetUserID(r.Context()), req.RequestKey)
	if err != nil {
		writePermissionQueryError(w, r, err)
		return
	}
	defer release()

	// 提交幂等：同键同输入重放原任务（含其冻结计划），同键不同输入 409。
	if req.RequestKey != "" {
		existing, err := s.batchRequestRecord(r.Context(), auth.GetUserID(r.Context()), req.RequestKey)
		if err != nil {
			writeError(w, 503, "读取批量提交记录失败")
			return
		}
		if existing != nil {
			if batchRequestMatches(existing, &req, mode, requestHash) {
				writeJSON(w, 200, map[string]any{
					"job_id":   existing.ID,
					"count":    existing.TotalCount,
					"replayed": true,
					"message":  "请求与已提交任务一致，已返回原任务",
				})
				return
			}
			writeError(w, http.StatusConflict, "同一 request_key 已用于不同的批量请求；请刷新后重试或更换请求键")
			return
		}
	}
	if s.aiCheckSvc == nil || !s.aiCheckSvc.AutomaticReady() {
		writeError(w, http.StatusServiceUnavailable, "AI 质量检查服务未就绪，已停止批量出题；请联系管理员恢复后再试")
		return
	}
	if s.batchSvc == nil {
		writeError(w, 503, "批量执行器未就绪")
		return
	}
	capabilities := s.batchSvc.Capabilities()
	if !capabilities.Available {
		writeError(w, http.StatusServiceUnavailable, capabilities.Message)
		return
	}

	candidates, cerr := s.resolveBatchCandidates(r, &req)
	if cerr != nil {
		writeError(w, cerr.status, cerr.message)
		return
	}
	points := candidates.Points
	excludedBasic := candidates.ExcludedBasic
	// 按模式计算计划题数：随机模式是 N（不受“每点最多20”约束，但受批量总额），
	// 每点模式是 候选×每点数。
	planTotal := len(points) * req.Count
	if mode == domain.BatchModeRandomTotal {
		planTotal = req.TotalCount
	}
	if s.quotaStore != nil {
		quota, _, err := s.quotaStore.GetGenerationQuota(r.Context())
		if err != nil {
			writeError(w, 503, "读取生成任务配额失败")
			return
		}
		if planTotal > quota.BatchMaxQuestions {
			writeGenerationQuotaError(w, &storage.GenerationQuotaError{Message: fmt.Sprintf("批量任务最多生成 %d 道题，请减少总题数", quota.BatchMaxQuestions)})
			return
		}
	} else if planTotal > storage.DefaultGenerationQuota().BatchMaxQuestions {
		writeGenerationQuotaError(w, &storage.GenerationQuotaError{Message: "批量任务最多生成 100 道题，请减少总题数"})
		return
	}

	// 生成任务名称
	jobName := req.JobName
	if jobName == "" {
		if mode == domain.BatchModeRandomTotal {
			jobName = fmt.Sprintf("范围随机 %d题（候选 %d）%s", req.TotalCount, len(points), time.Now().Format("01-02 15:04"))
		} else {
			jobName = fmt.Sprintf("批量生成 %d题 %s", len(points), time.Now().Format("01-02 15:04"))
		}
	}
	// 构建显式计划：随机模式在此完成一次性抽样（同点可重复、不保证覆盖），
	// 此后恢复与重跑都按冻结单元执行，不再重抽。
	var plan *domain.BatchPlan
	if mode == domain.BatchModeRandomTotal {
		drawn, err := batch.DrawRandomPlan(points, req.VersionID, req.TotalCount)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		plan = drawn
	} else {
		perPoint, err := batch.PerPointPlan(points, req.VersionID, req.Count)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		plan = perPoint
	}
	plan.Selection = &domain.BatchPlanSelection{VersionID: plan.VersionID, KnowledgePointIDs: append([]string(nil), req.KnowledgePointIDs...), OutlineCodes: req.OutlineCodes, FromCode: req.FromCode, ToCode: req.ToCode, Limit: req.Limit, SkipExisting: req.SkipExisting}
	// 知识工作流只为实际生成的唯一点准备资料；随机模式候选可远大于 N，
	// 不能为未抽中的点冻结知识包。
	workflowPoints := plan.UniquePlanPoints()

	// 提交任务：把当前用户归属写入批量任务，后续状态查询/结果导入必须按任务拥有者隔离。
	batchCtx := storage.WithQuestionChange(r.Context(), storage.QuestionChange{
		Actor:      auth.GetUsername(r.Context()),
		OwnerID:    auth.GetUserID(r.Context()),
		ChangeType: "batch_generate",
		ChangeNote: "单题 API 批量生成任务",
	})
	var jobID string
	var count int
	batchCtx, submission := workflowtrace.Start(batchCtx, s.questionStore, "batch_submission:unaccepted", "submission", 0, "")
	defer submission.Close()
	err = s.withGenerationAccess(batchCtx, domain.PermBatchRun, func(ctx context.Context, snapshot *domain.GenerationPromptSnapshot) error {
		return s.withKnowledgeWorkflow(ctx, req.WorkflowMode, workflowPoints, req.Presentation, func(ctx context.Context, workflow *domain.KnowledgeWorkflowSnapshot) error {
			return s.withPersonalExperiences(r.WithContext(ctx), req.ApplyPersonalExperiences, workflowPoints, func(experiences *domain.PersonalExperienceSnapshot) error {
				if workflow != nil {
					submission.Version(workflow.Version)
				} else {
					submission.Version("direct")
				}
				requests := make([]domain.GenerationRequest, 0, len(workflowPoints))
				for _, p := range workflowPoints {
					requests = append(requests, domain.GenerationRequest{KnowledgePoints: []domain.KnowledgePoint{p}, Count: 1, Difficulty: "0.65"})
				}
				snapshot = generator.FreezeCurrentBaseline(snapshot, requests)
				var e error
				queueStage := workflowtrace.Begin(ctx, "queue_admission", -1, struct{ PointCount, Total int }{len(workflowPoints), plan.RequestedTotal})
				jobID, count, e = s.batchSvc.SubmitPlan(generator.WithPersonalExperiences(knowledgebase.WithSnapshot(generator.WithPromptSnapshot(ctx, snapshot), workflow), experiences), batch.PlanSubmission{
					Plan:        plan,
					JobName:     jobName,
					RequestKey:  req.RequestKey,
					RequestHash: requestHash,
				})
				if e != nil {
					queueStage.End("failed", "queue_admission_failed", nil)
				} else {
					queueStage.End("written", "", jobID)
				}
				return e
			})
		})
	})
	// 并发同键提交被唯一索引兜底拒绝：按键重读，输入一致重放原任务，否则 409。
	if errors.Is(err, storage.ErrBatchDuplicateRequestKey) && req.RequestKey != "" {
		if existing, rerr := s.batchRequestRecord(r.Context(), auth.GetUserID(r.Context()), req.RequestKey); rerr == nil && existing != nil && batchRequestMatches(existing, &req, mode, requestHash) {
			writeJSON(w, 200, map[string]any{
				"job_id":   existing.ID,
				"count":    existing.TotalCount,
				"replayed": true,
				"message":  "请求与已提交任务一致，已返回原任务",
			})
			return
		}
		writeError(w, http.StatusConflict, "同一 request_key 已用于不同的批量请求；请刷新后重试或更换请求键")
		return
	}
	if jobID != "" {
		submission.Operation("batch:" + jobID + ":submission")
	}
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
		status := http.StatusInternalServerError
		if errors.Is(err, batch.ErrUnavailable) {
			status = http.StatusServiceUnavailable
		} else if errors.Is(err, batch.ErrJobNotFound) {
			status = http.StatusNotFound
		}
		if writeAuthoringError(w, err) {
			return
		}
		writeError(w, status, "提交失败: "+err.Error())
		return
	}
	if s.auditSvc != nil {
		detail := fmt.Sprintf("提交批量生成任务「%s」：%d 个知识点 × 每点 %d 题，共 %d 题（任务 %s）", jobName, len(points), req.Count, count, jobID)
		if mode == domain.BatchModeRandomTotal {
			detail = fmt.Sprintf("提交范围随机批量任务「%s」：候选 %d 个知识点，抽题 %d（任务 %s）", jobName, len(points), count, jobID)
		}
		_ = s.auditSvc.Log(r.Context(), "", "generate_batch", auth.GetUsername(r.Context()), detail)
	}

	writeJSON(w, 200, map[string]any{
		"excluded_basic_points": excludedBasic,
		"job_id":                jobID,
		"count":                 count,
		"mode":                  mode,
		"candidate_count":       len(points),
		"message":               capabilities.Message,
	})
}

// batchRequestHash 计算提交请求的规范化指纹（覆盖影响计划内容的全部输入，
// 不含 request_key 本身与抽样结果）：同键重放时据此判断输入是否一致。
func batchRequestHash(req *batchSubmitRequest, mode string) string {
	if req == nil {
		return ""
	}
	canonical := *req
	canonical.KnowledgePointIDs = nil
	seen := map[string]bool{}
	for _, id := range req.KnowledgePointIDs {
		if !seen[id] {
			seen[id] = true
			canonical.KnowledgePointIDs = append(canonical.KnowledgePointIDs, id)
		}
	}
	if canonical.WorkflowMode == "" {
		canonical.WorkflowMode = "direct"
	}

	// Keep all three states: omission may fall back when learning is unavailable,
	// whereas an explicit true requires the learning capability. C5's fingerprint
	// omitted this input entirely; retain that old hash only for proven aliases.
	payload := struct {
		Intent                   string `json:"intent"`
		ApplyPersonalExperiences *bool  `json:"apply_personal_experiences"`
	}{cleanBatchIntentHash(&canonical, mode), canonical.ApplyPersonalExperiences}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return "batch-intent-v3:" + hex.EncodeToString(sum[:])
}

func cleanBatchIntentHash(req *batchSubmitRequest, mode string) string {
	if req == nil {
		return ""
	}
	ids := append([]string{}, req.KnowledgePointIDs...)
	// Random sampling over a full set ignores order; prefix limiting and per-point
	// execution do not. Keep their order in the intent fingerprint.
	if mode == domain.BatchModeRandomTotal && req.Limit <= 0 {
		sort.Strings(ids)
	}
	payload := struct {
		Mode         string                           `json:"mode"`
		VersionID    string                           `json:"version_id"`
		PointIDs     []string                         `json:"point_ids"`
		OutlineCodes string                           `json:"outline_codes"`
		Count        int                              `json:"count"`
		TotalCount   int                              `json:"total_count"`
		Limit        int                              `json:"limit"`
		SkipExisting bool                             `json:"skip_existing"`
		FromCode     string                           `json:"from_code"`
		ToCode       string                           `json:"to_code"`
		WorkflowMode string                           `json:"workflow_mode"`
		Presentation *domain.PresentationRequirements `json:"presentation"`
		JobName      string                           `json:"job_name"`
	}{mode, req.VersionID, ids, req.OutlineCodes, req.Count, req.TotalCount, req.Limit, req.SkipExisting, req.FromCode, req.ToCode, req.WorkflowMode, req.Presentation, req.JobName}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// batchUserError 批量候选解析中的用户可见错误（预览与提交共用同一语义）。
type batchUserError struct {
	status  int
	message string
}

// batchCandidateResult 候选解析结果：提交与预览共用，避免两套判定漂移。
type batchCandidateResult struct {
	Points          []domain.KnowledgePoint
	ExcludedBasic   int
	SkippedExisting int
}

// resolveBatchCandidates 按提交请求解析候选知识点：显式选择（少量逐点、
// 大批量走快照批解）或整版本自动选择；统一执行跳过已有题、代码范围与
// 临床范围过滤及 limit 截断。无效/已删除/越权点明确报错，绝不静默忽略。
func (s *Server) resolveBatchCandidates(r *http.Request, req *batchSubmitRequest) (*batchCandidateResult, *batchUserError) {
	// Pin this batch to one syllabus and fail on missing/deleted selections.
	points := []domain.KnowledgePoint{}
	if len(req.KnowledgePointIDs) > 0 || req.OutlineCodes != "" {
		ids := req.KnowledgePointIDs
		codes := false
		if len(ids) == 0 {
			ids = splitAndTrim(req.OutlineCodes)
			codes = true
		}
		if codes || len(ids) <= 20 {
			seen := map[string]bool{}
			for _, value := range ids {
				id, code := value, ""
				if codes {
					id = ""
					code = value
				}
				p, err := s.kpSvc.ResolveForGeneration(r.Context(), req.VersionID, id, code)
				if err != nil {
					return nil, kpBatchError(err)
				}
				req.VersionID = p.VersionID
				if !seen[p.ID] {
					seen[p.ID] = true
					points = append(points, *p)
				}
			}
		} else {
			// 大批量显式选择（跨页全选）走快照批量解析：先用第一个点钉住版本并
			// 校验启用状态，其余按 ID 集合一次解析；缺失/越权点明确报错。
			first, err := s.kpSvc.ResolveForGeneration(r.Context(), req.VersionID, ids[0], "")
			if err != nil {
				return nil, kpBatchError(err)
			}
			req.VersionID = first.VersionID
			filtered, err := s.kpSvc.SearchFiltered(r.Context(), knowledge.KPSearchOptions{VersionID: first.VersionID, IDs: ids})
			if err != nil {
				return nil, kpBatchError(err)
			}
			byID := make(map[string]domain.KnowledgePoint, len(filtered))
			for _, p := range filtered {
				byID[p.ID] = p
			}
			var missing []string
			seen := map[string]bool{}
			points = append(points, *first)
			seen[first.ID] = true
			for _, id := range ids[1:] {
				p, ok := byID[id]
				if !ok {
					if len(missing) < 3 {
						missing = append(missing, id)
					}
					continue
				}
				if !seen[p.ID] {
					seen[p.ID] = true
					points = append(points, p)
				}
			}
			if len(missing) > 0 {
				return nil, &batchUserError{http.StatusNotFound, "所选知识点不存在或已从有效大纲剔除：" + strings.Join(missing, "、")}
			}
		}
	} else {
		v, err := s.kpSvc.ResolveVersion(r.Context(), req.VersionID)
		if err != nil {
			return nil, kpBatchError(err)
		}
		if v.Status != "published" {
			return nil, &batchUserError{http.StatusBadRequest, "请先启用大纲版本"}
		}
		all, err := s.kpSvc.SearchFiltered(r.Context(), knowledge.KPSearchOptions{VersionID: v.ID})
		if err != nil {
			return nil, kpBatchError(err)
		}
		points = all
	}

	// 跳过已有题目
	skipped := 0
	if req.SkipExisting {
		questions, err := s.questionStore.ListQuestions(r.Context())
		if err != nil {
			return nil, &batchUserError{http.StatusInternalServerError, "读取已有题目失败"}
		}
		ownerID := auth.GetUserID(r.Context())
		personalQuestions := make([]domain.A2Question, 0, len(questions))
		for _, question := range questions {
			if question.OwnerID == ownerID {
				personalQuestions = append(personalQuestions, question)
			}
		}
		existing := domain.ExistingKnowledgeKeys(personalQuestions)
		var filtered []domain.KnowledgePoint
		for _, p := range points {
			if !existing[domain.KnowledgePointKey(p)] {
				filtered = append(filtered, p)
			} else {
				skipped++
			}
		}
		points = filtered
	}

	// 按范围筛选
	if req.FromCode != "" || req.ToCode != "" {
		var filtered []domain.KnowledgePoint
		for _, kp := range points {
			if req.FromCode != "" && kp.OutlineCode < req.FromCode {
				continue
			}
			if req.ToCode != "" && kp.OutlineCode > req.ToCode {
				continue
			}
			filtered = append(filtered, kp)
		}
		points = filtered
	}

	excludedBasic := 0
	eligible := make([]domain.KnowledgePoint, 0, len(points))
	for _, point := range points {
		if !domain.IsEligibleClinicalPoint(point) {
			if len(req.KnowledgePointIDs) > 0 || req.OutlineCodes != "" {
				return nil, &batchUserError{http.StatusBadRequest, "所选包含基础医学知识点（已从有效大纲剔除），请取消后提交"}
			}
			excludedBasic++
			continue
		}
		eligible = append(eligible, point)
	}
	points = eligible
	// 限制数量
	if req.Limit > 0 && req.Limit < len(points) {
		points = points[:req.Limit]
	}

	if len(points) == 0 {
		return nil, &batchUserError{http.StatusBadRequest, "没有符合条件的知识点"}
	}
	return &batchCandidateResult{Points: points, ExcludedBasic: excludedBasic, SkippedExisting: skipped}, nil
}

// kpBatchError 把知识点解析错误映射为预览/提交共用的用户错误。
func kpBatchError(err error) *batchUserError {
	status := http.StatusBadRequest
	if errors.Is(err, storage.ErrKnowledgeNotFound) {
		status = http.StatusNotFound
	}
	if errors.Is(err, storage.ErrKnowledgeConflict) {
		status = http.StatusConflict
	}
	return &batchUserError{status, err.Error()}
}

// handleBatchPreview 预览批量候选：与提交共用同一候选解析；知识模式额外做
// 有界的知识卡覆盖检查，明确未检查范围；提交校验实际抽中的冻结单元。
func (s *Server) handleBatchPreview(w http.ResponseWriter, r *http.Request) {
	if !s.hasPermission(r, domain.PermBatchRun) {
		writeError(w, http.StatusForbidden, "无批量出题权限")
		return
	}
	var req batchSubmitRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, 400, "请求格式错误: "+err.Error())
		return
	}
	mode := req.Mode
	if mode == "" {
		mode = domain.BatchModePerPoint
	}
	if !domain.ValidateBatchMode(mode) {
		writeError(w, http.StatusBadRequest, "无效的批量模式：仅支持 per_point 或 random_total")
		return
	}
	candidates, cerr := s.resolveBatchCandidates(r, &req)
	if cerr != nil {
		writeError(w, cerr.status, cerr.message)
		return
	}
	result := map[string]any{
		"mode":                 mode,
		"version_id":           req.VersionID,
		"candidate_count":      len(candidates.Points),
		"excluded_basic":       candidates.ExcludedBasic,
		"skipped_existing":     candidates.SkippedExisting,
		"missing_card_count":   0,
		"missing_card_samples": []map[string]string{},
		"coverage_checked":     0,
	}
	if req.WorkflowMode == "knowledge" {
		result["coverage_complete"] = false
		result["coverage_unchecked"] = len(candidates.Points)
		if s.knowledgeBaseSvc == nil {
			result["coverage_error"] = "知识库服务暂不可用"
			writeJSON(w, 200, result)
			return
		}
		missing, checked, err := s.knowledgeBaseSvc.CardCoverage(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), candidates.Points)
		if err != nil {
			// 覆盖检查失败保留候选统计；不得把未检查显示为全部可用。
			result["coverage_error"] = err.Error()
		} else {
			result["missing_card_count"] = len(missing)
			result["coverage_checked"] = checked
			result["coverage_complete"] = checked == len(candidates.Points)
			result["coverage_unchecked"] = len(candidates.Points) - checked
			samples := make([]map[string]string, 0, min(len(missing), 20))
			for _, p := range missing {
				if len(samples) >= 20 {
					break
				}
				samples = append(samples, map[string]string{"topic": p.Topic, "outline_code": p.OutlineCode})
			}
			result["missing_card_samples"] = samples
		}
	}
	writeJSON(w, 200, result)
}

// handleBatchStatus 查询批量任务状态。
func (s *Server) handleBatchStatus(w http.ResponseWriter, r *http.Request) {
	if !s.hasPermission(r, domain.PermBatchRun) && !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		writeError(w, http.StatusForbidden, "无权查看批量任务")
		return
	}
	jobID := r.PathValue("jobId")
	if jobID == "" {
		writeError(w, 400, "缺少 job_id")
		return
	}

	job, err := s.batchSvc.GetJobStatus(r.Context(), jobID)
	if err == nil && job == nil {
		err = batch.ErrJobNotFound
	}
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, batch.ErrUnavailable) {
			status = http.StatusServiceUnavailable
		} else if errors.Is(err, batch.ErrJobNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, "查询失败: "+err.Error())
		return
	}
	if !s.canAccessBatchJob(r, job) {
		writeError(w, http.StatusForbidden, "无权查看其他用户的批量任务")
		return
	}

	writeJSON(w, 200, s.withBatchOwnerName(job))
}

// handleBatchHistory 为历史页提供归属过滤和服务端分页。
func (s *Server) handleBatchHistory(w http.ResponseWriter, r *http.Request) {
	if !s.hasPermission(r, domain.PermBatchRun) && !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		writeError(w, http.StatusForbidden, "无权查看批量任务历史")
		return
	}
	global := r.URL.Query().Get("scope") == "global"
	if global && !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		writeError(w, http.StatusForbidden, "无权查看其他用户的批量任务")
		return
	}
	ownerID := auth.GetUserID(r.Context())
	if global {
		ownerID = ""
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 || page > 1000000 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize < 1 || pageSize > 50 {
		pageSize = 12
	}
	pager, ok := s.batchSvc.(batch.HistoryPager)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "批量任务历史暂不可用")
		return
	}
	jobs, total, err := pager.ListJobsPage(r.Context(), ownerID, r.URL.Query().Get("name"), r.URL.Query().Get("status"), pageSize, (page-1)*pageSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "查询批量任务历史失败: "+err.Error())
		return
	}
	for i := range jobs {
		s.withBatchOwnerName(&jobs[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs, "total": total, "page": page, "page_size": pageSize})
}

// handleBatchList 查询批量任务列表（支持按名称搜索）。
// 查询参数：name=任务名称关键词，status=状态筛选，limit=数量限制
func (s *Server) handleBatchList(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	status := r.URL.Query().Get("status")
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	jobs, err := s.batchSvc.ListJobs(r.Context(), name, status, limit)
	if err != nil {
		writeError(w, 500, "查询失败: "+err.Error())
		return
	}
	if !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		viewerID := auth.GetUserID(r.Context())
		filtered := jobs[:0]
		for _, job := range jobs {
			if job.OwnerID == viewerID {
				filtered = append(filtered, job)
			}
		}
		jobs = filtered
	}
	for i := range jobs {
		s.withBatchOwnerName(&jobs[i])
	}

	writeJSON(w, 200, map[string]any{
		"jobs":  jobs,
		"total": len(jobs),
	})
}

// handleBatchDownload 下载批量任务结果并导入题库。
func (s *Server) handleBatchDownload(w http.ResponseWriter, r *http.Request) {
	if !s.hasPermission(r, domain.PermBatchRun) && !s.hasPermission(r, domain.PermQuestionViewGlobal) {
		writeError(w, http.StatusForbidden, "无权查看批量任务结果")
		return
	}
	jobID := r.PathValue("jobId")
	if jobID == "" {
		writeError(w, 400, "缺少 job_id")
		return
	}
	job, statusErr := s.batchSvc.GetJobStatus(r.Context(), jobID)
	if statusErr == nil && job == nil {
		statusErr = batch.ErrJobNotFound
	}
	if statusErr != nil {
		status := http.StatusInternalServerError
		if errors.Is(statusErr, batch.ErrUnavailable) {
			status = http.StatusServiceUnavailable
		} else if errors.Is(statusErr, batch.ErrJobNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, "查询批量任务失败: "+statusErr.Error())
		return
	}
	// 执行器按统一 job ID 导入结果；具体 provider 文件 ID 留在适配器内部。
	// 访问规则：任务归属人全权导入/重放；全局查看权限的管理员仅可回放
	// 已导入任务（ImportResults 对已导入任务只重放存量结果、不再入库），
	// 解决角色拆分后存量任务的原主失去批量权限时无人能看结果的问题。
	if job == nil || job.OwnerID == "" || job.OwnerID != auth.GetUserID(r.Context()) {
		if !(job != nil && s.hasPermission(r, domain.PermQuestionViewGlobal) && job.ImportedAt != "") {
			writeError(w, http.StatusForbidden, "无权导入其他用户的批量任务")
			return
		}
	}
	if job.ImportedAt == "" && !s.hasPermission(r, domain.PermBatchRun) {
		writeError(w, http.StatusForbidden, "当前身份无权导入批量任务结果")
		return
	}
	owner, ownerErr := s.authSvc.GetUserByID(job.OwnerID)
	if job.ImportedAt == "" && (ownerErr != nil || owner == nil || !owner.Enabled) {
		writeError(w, http.StatusConflict, "批量任务原所有者不存在或已停用，不能导入")
		return
	}
	ownerName := "历史账号"
	if owner != nil {
		ownerName = owner.Username
	}
	// 导入归属固定使用任务提交时的所有者，不能因管理员查看而改变。
	importCtx := storage.WithQuestionChange(r.Context(), storage.QuestionChange{
		Actor:      ownerName,
		OwnerID:    job.OwnerID,
		ChangeType: "batch_generate",
	})
	result, err := s.batchSvc.ImportResults(importCtx, jobID, nil)
	if err != nil {
		if writeGenerationQuotaError(w, err) {
			return
		}
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, batch.ErrUnavailable):
			status = http.StatusServiceUnavailable
		case errors.Is(err, batch.ErrNotReady), errors.Is(err, storage.ErrBatchJobAlreadyActive):
			status = http.StatusConflict
		case errors.Is(err, storage.ErrBatchJobAlreadyActive):
			status = http.StatusConflict
		}
		writeError(w, status, "导入失败: "+err.Error())
		return
	}

	// 导入的草稿自动提交 AI 质量检查（后台异步执行）
	if s.aiCheckSvc != nil && job.ImportedAt == "" && len(result.QuestionIDs) > 0 {
		s.aiCheckSvc.CheckAsync(result.QuestionIDs...)
	}
	// 已导入任务的 ImportResults 只重放历史结果，切换任务查看不属于导入操作。
	if s.auditSvc != nil && job.ImportedAt == "" {
		_ = s.auditSvc.Log(r.Context(), "", "batch_import", auth.GetUsername(r.Context()),
			fmt.Sprintf("导入批量任务结果「%s」：%d 道题（任务 %s，归属 %s）", job.JobName, len(result.QuestionIDs), jobID, ownerName))
	}

	writeJSON(w, 200, result)
}

// handleBatchRetryFailed 重跑任务中的失败生成单元。
// 仅任务所有者可在结果导入前调用；任务转回 in_progress 后按既有轮询与
// 自动导入流程继续。受全局并发上限约束，与正常执行共享同一队列。
func (s *Server) handleBatchRetryFailed(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("jobId")
	if jobID == "" {
		writeError(w, 400, "缺少 job_id")
		return
	}
	job, statusErr := s.batchSvc.GetJobStatus(r.Context(), jobID)
	if statusErr == nil && job == nil {
		statusErr = batch.ErrJobNotFound
	}
	if statusErr != nil {
		status := http.StatusInternalServerError
		if errors.Is(statusErr, batch.ErrUnavailable) {
			status = http.StatusServiceUnavailable
		} else if errors.Is(statusErr, batch.ErrJobNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, "查询批量任务失败: "+statusErr.Error())
		return
	}
	if job == nil || job.OwnerID == "" || job.OwnerID != auth.GetUserID(r.Context()) {
		writeError(w, http.StatusForbidden, "无权重跑其他用户的批量任务")
		return
	}
	retried, err := s.batchSvc.RetryFailed(r.Context(), jobID)
	if err != nil {
		if writeGenerationQuotaError(w, err) {
			return
		}
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, batch.ErrUnavailable):
			status = http.StatusServiceUnavailable
		case errors.Is(err, batch.ErrNotReady), errors.Is(err, storage.ErrBatchJobAlreadyActive):
			status = http.StatusConflict
		}
		writeError(w, status, "重跑失败: "+err.Error())
		return
	}
	if s.auditSvc != nil {
		_ = s.auditSvc.Log(r.Context(), "", "batch_retry", auth.GetUsername(r.Context()),
			fmt.Sprintf("重跑批量任务「%s」的失败生成单元（任务 %s，失败 %d 题）", job.JobName, jobID, job.Failed))
	}
	writeJSON(w, 200, s.withBatchOwnerName(retried))
}

func (s *Server) canAccessBatchJob(r *http.Request, job *batch.BatchJob) bool {
	if job == nil {
		return false
	}
	if s.hasPermission(r, domain.PermQuestionViewGlobal) {
		return true
	}
	return job.OwnerID != "" && job.OwnerID == auth.GetUserID(r.Context())
}

// splitAndTrim 按逗号分割并去除空白。
func splitAndTrim(s string) []string {
	var result []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
