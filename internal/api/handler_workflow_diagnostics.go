package api

import (
	"aigo/internal/auth"
	"aigo/internal/knowledgebase"
	"aigo/internal/pipeline"
	"aigo/internal/storage"
	"aigo/internal/workflowtrace"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type diagnosticStep struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Item       int    `json:"item"`
	Issue      string `json:"issue,omitempty"`
}
type diagnosticEntry struct {
	ID         string                     `json:"id"`
	Phase      string                     `json:"phase"`
	Attempt    int                        `json:"attempt"`
	Unit       *int                       `json:"unit,omitempty"`
	Outcome    string                     `json:"outcome"`
	At         time.Time                  `json:"at"`
	DurationMS int64                      `json:"duration_ms"`
	Complete   bool                       `json:"complete"`
	Steps      []diagnosticStep           `json:"steps"`
	Call       *workflowtrace.CallSummary `json:"call,omitempty"`
}

var diagnosticNames = map[string]string{"knowledge_prepare": "准备知识资料", "queue_admission": "任务受理", "generation_prepare": "准备本次生成", "model_call": "模型生成", "json_parse": "解析完整结果", "knowledge_validation": "核对资料引用结构", "render": "组织题目呈现", "a2_validation": "核对原题目格式", "handoff": "提交题目及首次检查", "unit_checkpoint": "保存本次尝试状态"}

func diagnosticStatus(value string) string {
	switch value {
	case "prepared", "written", "returned", "parsed", "validated", "rendered", "committed", "checkpointed", "accepted", "failed", "generation_failed", "submission_failed", "handoff_failed", "handoff_not_attempted", "not_applicable", "incomplete", "started":
		return value
	}
	return "unknown"
}
func (s *Server) handleRunDiagnostics(w http.ResponseWriter, r *http.Request) {
	if !validGenerationRunID(r.PathValue("id")) {
		writeError(w, 400, "命题运行 ID 格式错误")
		return
	}
	if s.generationRunStore == nil {
		writeError(w, 503, "运行记录服务不可用")
		return
	}
	run, e := s.generationRunStore.GetGenerationRun(r.Context(), r.PathValue("id"))
	if e != nil {
		writeError(w, 503, "读取运行记录失败")
		return
	}
	if run == nil || run.OwnerID != auth.GetUserID(r.Context()) {
		writeError(w, 404, "命题运行记录不存在")
		return
	}
	var spec pipeline.GenerationSpec
	if json.Unmarshal(run.RequestJSON, &spec) != nil {
		writeError(w, 503, "原任务快照不可读取")
		return
	}
	s.respondWorkflowDiagnostics(w, r, "generate:"+run.ID, false, spec.Request.Workflow != nil, 0)
}
func (s *Server) handleBatchDiagnostics(w http.ResponseWriter, r *http.Request) {
	store, ok := s.questionStore.(storage.BatchJobStore)
	if !ok {
		writeError(w, 503, "批量记录服务不可用")
		return
	}
	job, e := store.GetBatchJob(r.Context(), r.PathValue("jobId"))
	if e != nil {
		writeError(w, 503, "读取批量记录失败")
		return
	}
	// A global question-view grant does not grant diagnostic access to another author.
	if job == nil || job.OwnerID != auth.GetUserID(r.Context()) {
		writeError(w, 404, "批量任务不存在")
		return
	}
	knowledge := strings.TrimSpace(job.WorkflowSnapshotJSON) != "" && strings.TrimSpace(job.WorkflowSnapshotJSON) != "null"
	s.respondWorkflowDiagnostics(w, r, "batch:"+job.ID+":", true, knowledge, job.TotalCount)
}
func (s *Server) respondWorkflowDiagnostics(w http.ResponseWriter, r *http.Request, operation string, batch, knowledge bool, units int) {
	reader, ok := s.questionStore.(workflowtrace.Reader)
	if !ok {
		writeError(w, 503, "此运行环境暂无详细执行记录")
		return
	}
	limit := 10
	if value := r.URL.Query().Get("limit"); value != "" {
		n, e := strconv.Atoi(value)
		if e != nil || n < 1 || n > 20 {
			writeError(w, 400, "每页诊断记录须为1—20条")
			return
		}
		limit = n
	}
	after, e := workflowtrace.DecodeCursor(r.URL.Query().Get("cursor"))
	if e != nil {
		writeError(w, 400, "诊断分页参数无效")
		return
	}
	var page workflowtrace.Page
	load := func(ctx context.Context) error {
		var err error
		page, err = reader.ReadWorkflowExecutions(ctx, workflowtrace.Query{Operation: operation, Batch: batch, Limit: limit, After: after, UnitCount: units})
		return err
	}
	if knowledge {
		if !s.knowledgeBaseReady(w) {
			return
		}
		e = s.knowledgeBaseSvc.Access(r.Context(), auth.GetUserID(r.Context()), auth.GetRole(r.Context()), func(ctx context.Context, c knowledgebase.Capabilities) error {
			if !c.View {
				return knowledgebase.ErrDenied
			}
			return load(ctx)
		})
	} else {
		e = load(r.Context())
	}
	if e != nil {
		if errors.Is(e, knowledgebase.ErrDenied) {
			writeError(w, 403, "当前身份无权查看知识工作流记录")
		} else {
			writeError(w, 503, "执行记录暂不可读，原任务仍按原流程运行")
		}
		return
	}
	entries := make([]diagnosticEntry, 0, len(page.Rows))
	for _, row := range page.Rows {
		record := row.Execution
		phase := "generation"
		if record.Phase == "submission" {
			phase = "submission"
		}
		entry := diagnosticEntry{ID: record.ID, Phase: phase, Attempt: record.Attempt, Outcome: diagnosticStatus(record.Outcome), At: record.StartedAt, DurationMS: record.DurationMS, Complete: !record.StagesTruncated && record.Outcome != "incomplete" && len(record.Stages) > 0, Steps: []diagnosticStep{}, Call: row.Call}
		if batch && phase == "generation" {
			if n, err := strconv.Atoi(strings.TrimPrefix(record.Operation, operation)); err == nil {
				unit := n + 1
				entry.Unit = &unit
			}
		}
		for _, step := range record.Stages {
			label, ok := diagnosticNames[step.Name]
			if !ok {
				label = "其他执行步骤"
			}
			status := diagnosticStatus(step.Status)
			issue := ""
			if status == "failed" {
				issue = "本步骤未完成；任务是否重试及最终结果以原进度为准"
			}
			if status == "started" {
				entry.Complete = false
			}
			entry.Steps = append(entry.Steps, diagnosticStep{Name: label, Status: status, DurationMS: step.DurationMS, Item: step.Item, Issue: issue})
		}
		entries = append(entries, entry)
	}
	availability := "available"
	if len(entries) == 0 {
		availability = "not_recorded"
	}
	writeJSON(w, 200, map[string]any{"entries": entries, "next_cursor": page.Next, "availability": availability, "notice": "记录可能尚未写入、缺失或来自旧任务。提交成功不等于首次检查通过；题目可用性以原检查及审核状态为准。"})
}
