package generator

import (
	"aigo/internal/domain"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

func BaselineSystemPrompt() string { return getSystemPrompt(false) }
func generationPromptKey(req domain.GenerationRequest) string {
	raw, _ := json.Marshal(struct {
		Point      domain.KnowledgePoint
		Count      int
		Difficulty domain.Difficulty
	}{req.KnowledgePoints[0], req.Count, req.Difficulty})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// FreezePromptBaseline 保存这次请求的底座，批量重启/重跑不套用之后的底座版本。
func FreezePromptBaseline(snapshot *domain.GenerationPromptSnapshot, requests []domain.GenerationRequest) {
	if snapshot == nil {
		return
	}
	snapshot.SystemPrompt = BaselineSystemPrompt()
	snapshot.UserPrompts = map[string]string{}
	for _, req := range requests {
		if req.Count <= 0 {
			req.Count = 1
		}
		if len(req.KnowledgePoints) > 0 {
			snapshot.UserPrompts[generationPromptKey(req)] = buildPrompt(req.KnowledgePoints[0], req.Count, false, req.Difficulty)
		}
	}
}
func FrozenPromptAppendix(snapshot *domain.GenerationPromptSnapshot) string {
	if snapshot == nil {
		return ""
	}
	return snapshot.Appendix
}
func generationMessages(req domain.GenerationRequest, brief bool) (system, user string, err error) {
	system = getSystemPrompt(brief)
	user = buildPrompt(req.KnowledgePoints[0], req.Count, brief, req.Difficulty)
	snapshot := req.PromptSnapshot
	if snapshot != nil {
		if err := validateAuthoringGuidelines(snapshot.Guidelines); err != nil {
			return "", "", err
		}
	}
	if snapshot != nil && snapshot.PolicyVersion != "" && !domain.SupportsA2GenerationPolicy(snapshot.PolicyVersion) {
		return "", "", fmt.Errorf("不支持的命题策略版本")
	}
	if snapshot == nil || (snapshot.Appendix == "" && snapshot.PolicyVersion == "") {
		return system, user, nil
	}
	frozen, ok := snapshot.UserPrompts[generationPromptKey(req)]
	if snapshot.SystemPrompt == "" || !ok {
		return "", "", fmt.Errorf("生成任务底座快照缺失，无法继续生成")
	}
	return snapshot.SystemPrompt, frozen + FrozenPromptAppendix(snapshot), nil
}

type promptSnapshotContextKey struct{}

func WithPromptSnapshot(ctx context.Context, s *domain.GenerationPromptSnapshot) context.Context {
	return context.WithValue(ctx, promptSnapshotContextKey{}, s)
}
func PromptSnapshotFromContext(ctx context.Context) *domain.GenerationPromptSnapshot {
	s, _ := ctx.Value(promptSnapshotContextKey{}).(*domain.GenerationPromptSnapshot)
	return s
}
