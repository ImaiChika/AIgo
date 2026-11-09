package domain

import (
	"errors"
	"fmt"
	"testing"
)

func TestBasicMedicalEligibilityPreservesClinicalMechanisms(t *testing.T) {
	for _, tc := range []struct {
		name    string
		point   KnowledgePoint
		blocked bool
	}{
		{"basic", KnowledgePoint{Category: "基础医学", Subject: "病理学"}, true},
		{"fallback", KnowledgePoint{Subject: "药理学"}, true},
		{"clinical mechanism", KnowledgePoint{Category: "临床综合", Topic: "肺炎的发病机制"}, false},
		{"clinical pharmacology", KnowledgePoint{Category: "临床综合", Subject: "药理学"}, false},
		{"topic words", KnowledgePoint{Category: "临床综合", Topic: "基础医学临床联系"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsBasicMedicalPoint(tc.point); got != tc.blocked {
				t.Fatal(got)
			}
		})
	}
}

func TestClassifyBasicScopeSeparatesClinicalContradictionAndBasic(t *testing.T) {
	for _, tc := range []struct {
		name  string
		point KnowledgePoint
		want  BasicMedicalScope
	}{
		{"basic category", KnowledgePoint{Category: "基础医学", Subject: "生理"}, ScopeBasicMedical},
		{"basic category with clinical subject stays basic", KnowledgePoint{Category: "基础医学", Subject: "一、呼吸系统"}, ScopeBasicMedical},
		{"short subject fallback", KnowledgePoint{Category: "未分类", Subject: "病生"}, ScopeBasicMedical},
		{"empty category subject fallback", KnowledgePoint{Category: "", Subject: "解剖"}, ScopeBasicMedical},
		{"clinical category with basic subject is conflicted", KnowledgePoint{Category: "临床综合", Subject: "药理"}, ScopeConflicted},
		{"clinical system", KnowledgePoint{Category: "临床综合", Subject: "一、呼吸系统"}, ScopeClinical},
		{"clinical mechanism topic words", KnowledgePoint{Category: "临床综合", Subject: "三、消化系统", Topic: "肝硬化的病理生理机制"}, ScopeClinical},
		{"trimmed labels", KnowledgePoint{Category: " 基础医学 ", Subject: " 免疫 "}, ScopeBasicMedical},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyBasicScope(tc.point); got != tc.want {
				t.Fatalf("want %v got %v", tc.want, got)
			}
		})
	}
}

func TestBasicMedicalScopeErrorIsDetectable(t *testing.T) {
	wrapped := fmt.Errorf("选题范围调整：%w", ErrBasicMedicalScope)
	if !IsBasicMedicalScopeError(wrapped) {
		t.Fatal("wrapped sentinel not detected")
	}
	if IsBasicMedicalScopeError(errors.New("其他错误")) {
		t.Fatal("unrelated error misdetected")
	}
}
