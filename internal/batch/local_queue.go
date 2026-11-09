package batch

import (
	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/knowledgebase"
	"aigo/internal/llm"
	"aigo/internal/storage"
	"aigo/internal/workflowtrace"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Each job has at most one outstanding FIFO permit request. A failed attempt
// returns its permit before its durable retry delay, allowing other jobs to run.
func (s *LocalExecutor) runQueue(jobID string, units []domain.KnowledgePoint, pending []int, output localBatchOutput, snapshot *domain.GenerationPromptSnapshot, workflow *domain.KnowledgeWorkflowSnapshot) {
	ctx, cancel := context.WithCancel(generator.WithPromptSnapshot(s.ctx, snapshot))
	defer cancel()
	record, err := s.batchJobStore.GetBatchJob(ctx, jobID)
	if err != nil {
		s.noteRecovery(jobID)
		return
	}
	if record == nil || record.Status != "in_progress" {
		return
	}
	units, err = BatchUnitsFromRecord(*record)
	if err != nil {
		s.failCorruptQueue(ctx, *record, "任务单元记录无效，无法继续生成")
		return
	}
	plan, err := BatchPlanFromRecord(*record)
	if err != nil {
		s.failCorruptQueue(ctx, *record, "批量计划记录损坏，无法继续生成")
		return
	}
	snapshot, err = decodePromptSnapshot(record.PromptSnapshotJSON)
	if err != nil {
		s.failCorruptQueue(ctx, *record, "任务规则快照损坏，无法继续生成")
		return
	}
	workflow, err = decodeWorkflowSnapshot(record.WorkflowSnapshotJSON)
	if err != nil {
		s.failCorruptQueue(ctx, *record, "任务知识快照损坏，无法继续生成")
		return
	}
	ctx = generator.WithPromptSnapshot(ctx, snapshot)
	experiences, err := decodeExperienceSnapshot(record.ExperienceSnapshotJSON)
	if err != nil {
		s.failCorruptQueue(ctx, *record, "任务个人经验快照损坏，无法继续生成")
		return
	}
	if experiences != nil && experiences.OwnerID != record.OwnerID {
		s.failCorruptQueue(ctx, *record, "任务个人经验归属不一致")
		return
	}
	ctx = generator.WithPersonalExperiences(ctx, experiences)
	ctx = storage.WithQuestionChange(ctx, storage.QuestionChange{OwnerID: record.OwnerID})
	// Always use the latest committed snapshot, including an ambiguous prior commit.
	if record.OutputJSON != "" {
		if err = json.Unmarshal([]byte(record.OutputJSON), &output); err != nil {
			s.failCorruptQueue(ctx, *record, "任务结果记录损坏，无法继续生成")
			return
		}
	}
	output, err = normalizeOutput(units, output)
	if err != nil {
		s.failCorruptQueue(ctx, *record, "任务结果记录不完整，无法继续生成")
		return
	}
	todo := map[int]bool{}
	for _, pos := range pending {
		if pos < 0 || pos >= len(units) {
			s.failCorruptQueue(ctx, *record, "任务单元记录无效，无法继续生成")
			return
		}
		item := output.Items[pos]
		if item.Status == "ok" {
			continue
		}
		if item.Status == "failed" {
			item = ImportItem{OutlineCode: units[pos].OutlineCode, Topic: units[pos].Topic}
		}
		if item.MaxAttempts < 1 {
			item.MaxAttempts = s.maxAttempts()
		}
		if item.Attempts >= item.MaxAttempts {
			item.Status = "failed"
			item.Error = "生成执行中断，尝试次数已耗尽；可补生成新题"
			item.LastError = item.Error
			output.Items[pos] = item
			continue
		}
		item.Status = "pending"
		output.Items[pos] = item
		todo[pos] = true
	}
	output.Recovery = nil
	if err = s.persistOutput(ctx, jobID, "in_progress", output); err != nil {
		return
	}
	s.clearRecovery(jobID)
	type permit struct {
		pos     int
		release func()
	}
	type result struct {
		pos  int
		item ImportItem
	}
	requests := make(chan int)
	permits := make(chan permit, 1)
	results := make(chan result, 32)
	producerDone := make(chan struct{})
	var workers sync.WaitGroup
	go func() {
		defer close(producerDone)
		for {
			var pos int
			select {
			case <-ctx.Done():
				return
			case pos = <-requests:
			}
			if e := s.generator.WaitReady(ctx); e != nil {
				return
			}
			release, e := s.limiter.Acquire(ctx)
			if e != nil {
				return
			}
			select {
			case permits <- permit{pos, release}:
			case <-ctx.Done():
				release()
				return
			}
		}
	}()
	defer func() {
		cancel()
		<-producerDone
		select {
		case p := <-permits:
			p.release()
		default:
		}
		workers.Wait()
		// Results accepted into the buffer just as cancellation wins still need a
		// diagnostic close. Business recovery remains the original durable checkpoint.
		for {
			select {
			case r := <-results:
				r.item.execution.Complete("handoff_not_attempted", nil)
				r.item.execution.Close()
			default:
				return
			}
		}
	}()
	active := 0
	waiting := false
	for len(todo) > 0 || active > 0 || waiting {
		var send chan int
		next := -1
		var earliest int64
		if !waiting {
			now := time.Now().UnixMilli()
			for _, pos := range pending {
				if !todo[pos] {
					continue
				}
				at := output.Items[pos].NextAttemptAt
				if at <= now {
					next = pos
					break
				}
				if earliest == 0 || at < earliest {
					earliest = at
				}
			}
			if next >= 0 {
				send = requests
			}
		}
		var wake <-chan time.Time
		var timer *time.Timer
		if send == nil && !waiting && earliest > 0 {
			delay := time.Until(time.UnixMilli(earliest))
			if delay < time.Millisecond {
				delay = time.Millisecond
			}
			timer = time.NewTimer(delay)
			wake = timer.C
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-wake:
		case send <- next:
			delete(todo, next)
			waiting = true
		case p := <-permits:
			waiting = false
			item := output.Items[p.pos]
			previous := item
			item.Status = "running"
			item.Attempts++
			item.StartedAt = time.Now().UnixMilli()
			item.NextAttemptAt = 0
			output.Items[p.pos] = item
			if err = s.persistOutput(ctx, jobID, "in_progress", output); err != nil {
				output.Items[p.pos] = previous
				s.rememberCheckpoint(jobID, "in_progress", output)
				p.release()
				if timer != nil {
					timer.Stop()
				}
				return
			}
			active++
			workers.Add(1)
			go func(p permit, attempt int) {
				defer workers.Done()
				operation := fmt.Sprintf("batch:%s:%d", jobID, p.pos)
				offset := p.pos // Accepted v1/legacy jobs retain their historical offset.
				if plan != nil && plan.Units[p.pos].TargetOffset != nil {
					offset = *plan.Units[p.pos].TargetOffset
				}
				unitCtx := generator.WithGenerationAttempt(llm.WithCallOperation(knowledgebase.WithPlanOffset(knowledgebase.WithSnapshot(ctx, workflow), offset), operation), attempt)
				version := ""
				if workflow != nil {
					version = workflow.Version
				}
				unitCtx, execution := workflowtrace.Start(unitCtx, s.batchJobStore, operation, "generation", attempt, version)
				item := s.generateUnit(unitCtx, units[p.pos])
				item.execution = execution
				p.release()
				select {
				case results <- result{p.pos, item}:
				case <-ctx.Done():
					execution.Complete("handoff_not_attempted", nil)
					execution.Close()
				}
			}(p, item.Attempts)
		case r := <-results:
			active--
			previous := output.Items[r.pos]
			item := r.item
			execution := item.execution
			item.execution = nil
			item.Attempts = previous.Attempts
			item.MaxAttempts = previous.MaxAttempts
			item.StartedAt = previous.StartedAt
			if item.Status != "ok" {
				item.LastError = item.Error
				if !item.nonRetryable && item.Attempts < item.MaxAttempts {
					item.Status = "pending"
					item.Error = ""
					item.NextAttemptAt = time.Now().Add(s.retryDelay(item.Attempts)).UnixMilli()
					todo[r.pos] = true
				}
			}
			if item.Status == "ok" && item.question.GenerationContext != nil {
				if output.PendingContexts == nil {
					output.PendingContexts = map[string]*domain.QuestionGenerationContext{}
				}
				output.PendingContexts[item.QuestionID] = item.question.GenerationContext
			}
			if item.Status == "ok" && item.question.PersonalExperiences != nil {
				if output.PendingExperiences == nil {
					output.PendingExperiences = map[string]*domain.PersonalExperienceSnapshot{}
				}
				output.PendingExperiences[item.QuestionID] = item.question.PersonalExperiences
			}
			if output.Pipeline && item.Status == "ok" {
				item.CheckQueued = true
			}
			output.Items[r.pos] = item
			output.Questions = orderedQuestions(output)
			stageName := "unit_checkpoint"
			if item.Status == "ok" {
				stageName = "handoff"
			}
			handoff := execution.Begin(stageName, r.pos, struct {
				QuestionID string
				Status     string
			}{item.QuestionID, item.Status})
			if err = s.persistOutput(ctx, jobID, "in_progress", output); err != nil {
				handoff.End("failed", "checkpoint_write_failed", nil)
				execution.Complete("handoff_failed", nil)
				execution.Close()
				if timer != nil {
					timer.Stop()
				}
				return
			}

			if item.Status == "ok" {
				status := "checkpointed"
				if output.Pipeline {
					status = "committed"
				}
				handoff.End(status, "", item.QuestionID)
				execution.Complete(status, []string{item.QuestionID})
			} else {
				handoff.End("checkpointed", "", nil)
				execution.Complete("generation_failed", nil)
			}
			execution.Close()
			if output.Pipeline {
				output.PendingContexts = nil
				output.PendingExperiences = nil
			}
		}
		if timer != nil {
			timer.Stop()
		}
	}
	if ctx.Err() == nil {
		_ = s.persistOutput(ctx, jobID, "completed", output)
	}
}

func (s *LocalExecutor) retryDelay(attempts int) time.Duration {
	if len(s.RetryDelays) == 0 {
		return time.Second
	}
	i := attempts - 1
	if i >= len(s.RetryDelays) {
		i = len(s.RetryDelays) - 1
	}
	return s.RetryDelays[i]
}

// One call only; runQueue persists attempts and schedules retry without a permit.
func (s *LocalExecutor) generateUnit(ctx context.Context, point domain.KnowledgePoint) ImportItem {
	started := time.Now()
	item := ImportItem{OutlineCode: point.OutlineCode, Topic: point.Topic, Status: "failed"}
	questions, err := s.generator.Generate(ctx, domain.GenerationRequest{Subject: point.Subject, Difficulty: "0.65", KnowledgePoints: []domain.KnowledgePoint{point}, Count: 1, PromptSnapshot: generator.PromptSnapshotFromContext(ctx), PersonalExperiences: generator.PersonalExperiencesFromContext(ctx), Workflow: knowledgebase.SnapshotFromContext(ctx), WorkflowOffset: knowledgebase.PlanOffsetFromContext(ctx)})
	if err == nil && len(questions) > 0 {
		item.Status = "ok"
		item.Count = 1
		item.question = questions[0]
		item.QuestionID = questions[0].ID
	} else if err == nil {
		item.Error = "模型未返回可用题目，请重新生成"
	} else {
		item.Error = err.Error()
		// 取消与基础医学选题范围调整都是终态：不进入退避重试，也不换考点补生。
		item.nonRetryable = errors.Is(err, context.Canceled) || domain.IsBasicMedicalScopeError(err)
	}
	slog.Info("批量生成单元尝试结束", "operation", llm.CallOperation(ctx), "status", item.Status, "duration_ms", time.Since(started).Milliseconds())
	return item
}
