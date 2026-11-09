package batch

import (
	"aigo/internal/storage"
	"context"
	"encoding/json"
	"time"
)

func (s *LocalExecutor) recoveryInterval() time.Duration {
	if s.RecoveryInterval > 0 {
		return s.RecoveryInterval
	}
	return 5 * time.Second
}
func (s *LocalExecutor) recoveryLoop() {
	timer := time.NewTicker(s.recoveryInterval())
	defer timer.Stop()
	for {
		s.restoreCheckpoints()
		s.resumePending()
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
		}
	}
}
func (s *LocalExecutor) noteRecovery(id string) {
	if s.ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	r := s.recovery[id]
	r.State = "waiting"
	r.Message = "系统暂时异常，任务已保留并将自动恢复；已通过题目可继续使用。"
	r.Attempts++
	r.RetryAt = time.Now().Add(s.recoveryInterval()).UnixMilli()
	s.recovery[id] = r
	s.mu.Unlock()
	// Best effort: memory still exposes recovery during a write outage. Once the
	// database accepts writes the marker also survives a process restart.
	ctx, cancel := context.WithTimeout(s.ctx, 2*time.Second)
	defer cancel()
	record, err := s.batchJobStore.GetBatchJob(ctx, id)
	if err != nil || record == nil {
		return
	}
	var out localBatchOutput
	if json.Unmarshal([]byte(record.OutputJSON), &out) != nil {
		return
	}
	out.Recovery = &r
	raw, err := json.Marshal(out)
	if err != nil {
		return
	}
	record.OutputJSON = string(raw)
	_ = s.batchJobStore.UpdateBatchJob(ctx, *record)
}
func (s *LocalExecutor) clearRecovery(id string) { s.mu.Lock(); delete(s.recovery, id); s.mu.Unlock() }
func (s *LocalExecutor) failCorruptQueue(ctx context.Context, record storage.BatchJobRecord, message string) {
	r := RecoveryProgress{State: "failed", Message: message + "；已通过题目保留，可重新生成新题。"}
	var out localBatchOutput
	_ = json.Unmarshal([]byte(record.OutputJSON), &out)
	out.Recovery = &r
	raw, _ := json.Marshal(out)
	record.OutputJSON = string(raw)
	record.Status = "failed"
	record.FinishedAt = time.Now().Format(time.RFC3339Nano)
	if s.batchJobStore.UpdateBatchJob(ctx, record) != nil {
		s.noteRecovery(record.ID)
	} else {
		s.clearRecovery(record.ID)
	}
}

// Preserve successful model replies while a checkpoint is temporarily unavailable.
// Recovery retries the same commit, not the paid model call. A process crash still
// falls back to the last durable attempt/checkpoint.
type batchCheckpoint struct {
	status string
	output localBatchOutput
}

func (s *LocalExecutor) rememberCheckpoint(id, status string, out localBatchOutput) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checkpoints == nil {
		s.checkpoints = map[string]batchCheckpoint{}
	}
	s.checkpoints[id] = batchCheckpoint{status, out}
}

// Checkpoint persistence and retry admission serialize only for the same job.
// A slow database response for one batch cannot stop unrelated batches.
type batchCheckpointOperation struct {
	token chan struct{}
	users int
}

func (s *LocalExecutor) lockCheckpointOperation(ctx context.Context, id string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.checkpointOps == nil {
		s.checkpointOps = map[string]*batchCheckpointOperation{}
	}
	op := s.checkpointOps[id]
	if op == nil {
		op = &batchCheckpointOperation{token: make(chan struct{}, 1)}
		s.checkpointOps[id] = op
	}
	op.users++
	s.mu.Unlock()
	leave := func() {
		s.mu.Lock()
		op.users--
		if op.users == 0 {
			delete(s.checkpointOps, id)
		}
		s.mu.Unlock()
	}
	select {
	case op.token <- struct{}{}:
		return func() { <-op.token; leave() }, nil
	case <-ctx.Done():
		leave()
		return nil, ctx.Err()
	}
}
func (s *LocalExecutor) restoreCheckpoints() {
	s.mu.Lock()
	var pending []string
	for id := range s.checkpoints {
		if !s.active[id] && time.Now().UnixMilli() >= s.recovery[id].RetryAt {
			pending = append(pending, id)
		}
	}
	s.mu.Unlock()
	for _, id := range pending {
		if s.ctx.Err() != nil {
			return
		}
		s.restoreCheckpoint(id)
	}
}
func (s *LocalExecutor) restoreCheckpoint(id string) {
	release, err := s.lockCheckpointOperation(s.ctx, id)
	if err != nil {
		return
	}
	defer release()
	// A retry may have replaced or removed this checkpoint while we waited.
	s.mu.Lock()
	p, exists := s.checkpoints[id]
	eligible := exists && !s.active[id] && time.Now().UnixMilli() >= s.recovery[id].RetryAt
	s.mu.Unlock()
	if !eligible {
		return
	}
	record, err := s.batchJobStore.GetBatchJob(s.ctx, id)
	if err == nil && record != nil && isTerminalStatus(record.Status) {
		s.mu.Lock()
		delete(s.checkpoints, id)
		delete(s.recovery, id)
		s.mu.Unlock()
		return
	}
	if err == nil && record != nil {
		completed, failed := outputCounts(p.output)
		err = s.updateJob(s.ctx, id, p.status, completed, failed, p.output)
	}
	if err != nil || record == nil {
		s.noteRecovery(id)
		return
	}
	s.mu.Lock()
	delete(s.checkpoints, id)
	delete(s.recovery, id)
	s.mu.Unlock()
}
