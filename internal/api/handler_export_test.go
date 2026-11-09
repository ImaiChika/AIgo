package api

import (
	"strings"
	"testing"

	"aigo/internal/domain"
)

func TestIsExportableQuestion(t *testing.T) {
	tests := []struct {
		status domain.QuestionStatus
		want   bool
	}{
		{status: domain.StatusAIDraft, want: false},
		{status: domain.StatusAutoChecked, want: false},
		{status: domain.StatusAIReviewed, want: false},
		{status: domain.StatusReviewing, want: false},
		{status: domain.StatusConflict, want: false},
		{status: domain.StatusRevisionRequired, want: false},
		{status: domain.StatusRejected, want: false},
		{status: domain.StatusApproved, want: false},
		{status: domain.StatusPublished, want: true},
		{status: domain.StatusArchived, want: false},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			got := isExportableQuestion(domain.A2Question{Status: tt.status})
			if got != tt.want {
				t.Fatalf("status %s: got %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestMatchesQuestionExportFilter(t *testing.T) {
	question := domain.A2Question{
		ID:           "q-cardio-001",
		ClinicalStem: "患者活动后胸痛三个月",
		Answer:       "B",
		Profession:   "心血管系统",
		BankIDs:      []string{"bank-a", "bank-common"},
		Status:       domain.StatusPublished,
	}
	tests := []struct {
		name string
		req  questionExportRequest
		want bool
	}{
		{name: "empty", req: questionExportRequest{}, want: true},
		{name: "keyword stem", req: questionExportRequest{Keyword: "胸痛"}, want: true},
		{name: "keyword case insensitive id", req: questionExportRequest{Keyword: "CARDIO"}, want: true},
		{name: "wrong keyword", req: questionExportRequest{Keyword: "腹痛"}, want: false},
		{name: "status", req: questionExportRequest{Status: "published"}, want: true},
		{name: "wrong status", req: questionExportRequest{Status: "approved"}, want: false},
		{name: "bank", req: questionExportRequest{BankID: "bank-common"}, want: true},
		{name: "wrong bank", req: questionExportRequest{BankID: "bank-b"}, want: false},
		{name: "profession", req: questionExportRequest{Professions: []string{"呼吸系统", "心血管系统"}}, want: true},
		{name: "wrong profession", req: questionExportRequest{Professions: []string{"呼吸系统"}}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesQuestionExportFilter(question, tt.req); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewExportFilenameIsUniqueAndKeepsExtension(t *testing.T) {
	first := newExportFilename("xlsx")
	second := newExportFilename("xlsx")
	if first == second {
		t.Fatalf("连续导出文件名重复: %s", first)
	}
	if !strings.HasPrefix(first, "题目_") || !strings.HasSuffix(first, ".xlsx") {
		t.Fatalf("文件名格式错误: %s", first)
	}
}
