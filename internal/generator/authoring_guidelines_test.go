package generator

import (
	"aigo/internal/domain"
	"encoding/json"
	"strings"
	"testing"
)

func TestGlobalMethodsFreezeWithoutChangingEmptyBaseline(t *testing.T) {
	req := domain.GenerationRequest{KnowledgePoints: []domain.KnowledgePoint{{ID: "p", Category: "临床综合", Topic: "诊断"}}, Count: 1}
	baseline := FreezeCurrentBaseline(nil, []domain.GenerationRequest{req})
	if baseline.SystemPrompt != CurrentSystemPrompt() {
		t.Fatal("empty global library changed baseline")
	}
	item := domain.AuthoringGuideline{ID: "method-1", Revision: 2, Title: "方法", Text: "选项互斥唯一标记", Source: "培训"}
	snap := FreezeCurrentBaseline(&domain.GenerationPromptSnapshot{Guidelines: []domain.AuthoringGuideline{item}}, []domain.GenerationRequest{req})
	if snap.CapturedAt.IsZero() || !strings.Contains(snap.SystemPrompt, item.Text) {
		t.Fatal("method not frozen")
	}
	raw, _ := json.Marshal(snap)
	var restored domain.GenerationPromptSnapshot
	json.Unmarshal(raw, &restored)
	item.Text = "后来修改"
	req.PromptSnapshot = &restored
	system, _, e := generationMessages(req, false)
	if e != nil || !strings.Contains(system, "选项互斥唯一标记") || strings.Contains(system, "后来修改") {
		t.Fatal("restored task lost original method", e)
	}
	req.PromptSnapshot.Guidelines[0].Revision = 0
	if _, _, e = generationMessages(req, false); e == nil {
		t.Fatal("malformed frozen method accepted")
	}
}
