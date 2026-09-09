package domain

import "testing"

// 题库分层映射：状态 → 分层必须与产品语义一致
// （published=正式题库；流程中状态=过程题库；rejected/archived=淘汰题库）。
func TestTierForStatus(t *testing.T) {
	tests := []struct {
		status QuestionStatus
		want   QuestionTier
	}{
		{StatusAIDraft, TierWorking},
		{StatusAutoChecked, TierWorking},
		{StatusAIReviewed, TierWorking},
		{StatusReviewing, TierWorking},
		{StatusConflict, TierWorking},
		{StatusRevisionRequired, TierWorking},
		{StatusPublished, TierFormal},
		{StatusRejected, TierEliminated},
		{StatusArchived, TierEliminated},
		// 旧版终态 approved 收口为 published，应归正式题库
		{StatusApproved, TierFormal},
	}

	for _, tt := range tests {
		if got := TierForStatus(tt.status); got != tt.want {
			t.Errorf("TierForStatus(%s) = %s, want %s", tt.status, got, tt.want)
		}
	}
}

// TierStatuses 必须与 TierForStatus 互为反函数：层内每个状态都映射回本层。
func TestTierStatusesRoundTrip(t *testing.T) {
	for _, tier := range []QuestionTier{TierFormal, TierWorking, TierEliminated} {
		for _, status := range TierStatuses(tier) {
			if got := TierForStatus(status); got != tier {
				t.Errorf("TierStatuses(%s) 含状态 %s，但其映射分层为 %s", tier, status, got)
			}
		}
	}
}

func TestParseTier(t *testing.T) {
	for _, valid := range []string{"formal", "working", "eliminated"} {
		if _, ok := ParseTier(valid); !ok {
			t.Errorf("ParseTier(%q) 应合法", valid)
		}
	}
	for _, invalid := range []string{"", "Formal", "formal ", "all", "trash"} {
		if _, ok := ParseTier(invalid); ok {
			t.Errorf("ParseTier(%q) 应非法", invalid)
		}
	}
}

func TestQuestionTierName(t *testing.T) {
	if TierFormal.Name() != "正式题库" {
		t.Errorf("正式题库名称错误: %s", TierFormal.Name())
	}
	if TierWorking.Name() != "待审核题库" {
		t.Errorf("待审核题库名称错误: %s", TierWorking.Name())
	}
	if TierEliminated.Name() != "淘汰题库" {
		t.Errorf("淘汰题库名称错误: %s", TierEliminated.Name())
	}
}

func TestQuestionTierMethod(t *testing.T) {
	q := A2Question{Status: StatusPublished}
	if q.Tier() != TierFormal {
		t.Errorf("published 题目应属于正式题库, got %s", q.Tier())
	}
	q.Status = StatusRejected
	if q.Tier() != TierEliminated {
		t.Errorf("rejected 题目应属于淘汰题库, got %s", q.Tier())
	}
}
