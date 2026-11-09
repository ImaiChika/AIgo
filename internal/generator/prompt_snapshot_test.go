package generator

import (
	"aigo/internal/domain"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPromptSnapshotPreserveBaselineAndFreeze(t *testing.T) {
	req := domain.GenerationRequest{KnowledgePoints: []domain.KnowledgePoint{{ID: "test", Topic: "消化性溃疡", OutlineCode: "001", Subject: "消化系统", Category: "临床综合"}}, Count: 1, Difficulty: "0.65"}
	_, item := workflowFixture()
	delete(item, "knowledge_basis")
	raw, _ := json.Marshal([]any{item})
	client := &MockClient{Response: string(raw)}
	svc := NewService(client)
	if _, err := svc.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	baseline := append(client.LastMessages[:0:0], client.LastMessages...)
	options := client.LastOptions
	for _, name := range []string{"no_snapshot", "empty_snapshot"} {
		t.Run(name, func(t *testing.T) {
			copy := req
			if name == "empty_snapshot" {
				copy.PromptSnapshot = &domain.GenerationPromptSnapshot{}
			}
			if _, e := svc.Generate(context.Background(), copy); e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(baseline, client.LastMessages) || !reflect.DeepEqual(options, client.LastOptions) {
				t.Fatal("无规则改变底座或调用参数")
			}
		})
	}
	snapshot := &domain.GenerationPromptSnapshot{OwnerID: "private-owner", Appendix: "\n冻结的历史追加正文：解析逐段表达"}
	FreezePromptBaseline(snapshot, []domain.GenerationRequest{req})
	req.PromptSnapshot = snapshot
	if _, err := svc.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if client.LastMessages[0] != baseline[0] || !strings.HasPrefix(client.LastMessages[1].Content, baseline[1].Content) || !strings.Contains(client.LastMessages[1].Content, "解析逐段表达") || !reflect.DeepEqual(options, client.LastOptions) {
		t.Fatal("规则未在原底座上追加")
	}
	for _, private := range []string{"private-owner", "private-id", "revision"} {
		if strings.Contains(client.LastMessages[1].Content, private) {
			t.Fatal("模型收到账号/存储元数据")
		}
	}
	snapshot.SystemPrompt = "提交时系统底座"
	snapshot.UserPrompts[generationPromptKey(req)] = "提交时用户底座"
	if _, err := svc.Generate(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if client.LastMessages[0].Content != "提交时系统底座" || !strings.HasPrefix(client.LastMessages[1].Content, "提交时用户底座") {
		t.Fatal("未沿用任务底座快照")
	}
	req.Count = 2
	if _, err := svc.Generate(context.Background(), req); err == nil {
		t.Fatal("缺失请求快照不应静默换新底座")
	}
}
