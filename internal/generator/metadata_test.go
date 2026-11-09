package generator

import (
	"testing"

	"aigo/internal/domain"
)

func TestQuestionMetadataForKnowledgePoint(t *testing.T) {
	tests := []struct {
		name       string
		kp         domain.KnowledgePoint
		requested  string
		profession string
		system     string
	}{
		{name: "clinical digestive", kp: domain.KnowledgePoint{Category: "临床综合", Subject: "三、消化系统"}, requested: "三、消化系统", profession: "消化", system: "消化系统"},
		{name: "clinical orthopedics", kp: domain.KnowledgePoint{Category: "临床综合", Subject: "九、运动系统"}, profession: "骨科", system: "运动系统"},
		{name: "explicit profession", kp: domain.KnowledgePoint{Category: "临床综合", Subject: "三、消化系统"}, requested: "消化内科", profession: "消化内科", system: "消化系统"},
		{name: "basic medicine", kp: domain.KnowledgePoint{Category: "基础医学", Subject: "病理"}, requested: "病理", profession: "病理", system: "基础医学"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profession, system := QuestionMetadataForKnowledgePoint(test.kp, test.requested)
			if profession != test.profession || system != test.system {
				t.Fatalf("got (%q, %q), want (%q, %q)", profession, system, test.profession, test.system)
			}
		})
	}
}
