package generator

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"aigo/internal/domain"
)

func TestPersonalExperienceGenerationUsesFrozenMatchingRulesWithoutPrivateMetadata(t *testing.T) {
	request := domain.GenerationRequest{KnowledgePoints: []domain.KnowledgePoint{{ID: "point-one", Topic: "消化性溃疡", OutlineCode: "001", Subject: "消化系统", Category: "临床综合"}}, Count: 1, Difficulty: "0.65"}
	_, item := workflowFixture()
	delete(item, "knowledge_basis")
	raw, _ := json.Marshal([]any{item})
	client := &MockClient{Response: string(raw)}
	service := NewService(client)
	if _, err := service.Generate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	baseline := append(client.LastMessages[:0:0], client.LastMessages...)
	options := client.LastOptions
	request.PersonalExperiences = &domain.PersonalExperienceSnapshot{Version: domain.ExperienceProtocolVersion, OwnerID: "private-user", Entries: []domain.PersonalExperienceUse{
		{ID: "secret-experience-id", Revision: 7, Title: "表达经验", MatchedPointIDs: []string{"point-one"}, Rules: []domain.LearningRule{{ID: "private-rule-id", Text: "本次经验要求只叙述决定本题结论所需的线索", Before: "private-old-example", After: "private-new-example"}}},
		{ID: "other-point-id", Revision: 8, Title: "不匹配的经验", MatchedPointIDs: []string{"point-two"}, Rules: []domain.LearningRule{{Text: "禁止出现在这个考点的其他点经验"}}},
	}}
	questions, err := service.Generate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(client.LastMessages[1].Content, "本次经验要求只叙述") {
		t.Fatal("saved personal experience did not affect generation input")
	}
	for _, private := range []string{"private-user", "secret-experience-id", "private-rule-id", "private-old-example", "private-new-example", "禁止出现在这个考点"} {
		if strings.Contains(client.LastMessages[1].Content, private) {
			t.Fatalf("private/unmatched metadata entered model: %s", private)
		}
	}
	if !reflect.DeepEqual(options, client.LastOptions) || client.LastMessages[0] != baseline[0] {
		t.Fatal("experience changed model options or system baseline")
	}
	if len(questions) != 1 || questions[0].PersonalExperiences == nil || len(questions[0].PersonalExperiences.Entries) != 1 || questions[0].PersonalExperiences.Entries[0].Revision != 7 {
		t.Fatal("question lost frozen adoption evidence")
	}
	public, _ := json.Marshal(questions[0])
	if strings.Contains(string(public), "secret-experience-id") {
		t.Fatal("adoption leaked through public question JSON")
	}
	request.PersonalExperiences = &domain.PersonalExperienceSnapshot{Version: domain.ExperienceProtocolVersion, OwnerID: "private-user"}
	if _, err := service.Generate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(client.LastMessages, baseline) {
		t.Fatal("empty experience changed original direct mode")
	}
}

func TestUnsupportedPersonalExperienceSnapshotStopsBeforeModel(t *testing.T) {
	request := domain.GenerationRequest{KnowledgePoints: []domain.KnowledgePoint{{ID: "p", Topic: "考点", Category: "临床综合", Subject: "内科"}}, Count: 1, PersonalExperiences: &domain.PersonalExperienceSnapshot{Version: "unknown", OwnerID: "owner", Entries: []domain.PersonalExperienceUse{{ID: "x", Rules: []domain.LearningRule{{Text: "经验"}}, MatchedPointIDs: []string{"p"}}}}}
	client := &MockClient{Response: "[]"}
	_, err := NewService(client).Generate(context.Background(), request)
	if err == nil || len(client.LastMessages) > 0 {
		t.Fatal("unknown frozen protocol reached model", err)
	}
}

func TestOversizedPersonalExperienceSnapshotStopsBeforeModel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		entries []domain.PersonalExperienceUse
	}{
		{"too_many", make([]domain.PersonalExperienceUse, 6)},
		{"oversized", []domain.PersonalExperienceUse{{MatchedPointIDs: []string{"p"}, Rules: []domain.LearningRule{{Text: strings.Repeat("字", 3001)}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := domain.GenerationRequest{KnowledgePoints: []domain.KnowledgePoint{{ID: "p", Topic: "考点", Category: "临床综合", Subject: "内科"}}, Count: 1, PersonalExperiences: &domain.PersonalExperienceSnapshot{Version: domain.ExperienceProtocolVersion, OwnerID: "owner", Entries: tc.entries}}
			client := &MockClient{Response: "[]"}
			_, err := NewService(client).Generate(t.Context(), request)
			if err == nil || len(client.LastMessages) > 0 {
				t.Fatal("corrupt frozen snapshot reached model", err)
			}
		})
	}
}
