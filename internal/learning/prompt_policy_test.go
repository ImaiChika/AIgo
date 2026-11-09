package learning

import (
	"aigo/internal/domain"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestReflectionPromptPolicyPreservesLegacyAndFullInput(t *testing.T) {
	_, job := budgetJob()
	legacy := analysisMessages(job)
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(legacy[0].Content))); got != "07a290e0d867045b815dc006d978495bbb2f962b04f98e2153ae83481cf5fc3c" {
		t.Fatal("legacy accepted prompt changed", got)
	}
	job.PromptPolicy = domain.LearningScopedReflectionPrompt
	scoped := analysisMessages(job)
	if scoped[1].Content != legacy[1].Content {
		t.Fatal("scoped prompt lost original/current/differences/note")
	}
	marker := "只输出一个JSON对象"
	if strings.SplitN(scoped[0].Content, marker, 2)[1] != strings.SplitN(legacy[0].Content, marker, 2)[1] {
		t.Fatal("candidate output contract changed")
	}
	for _, want := range []string{"必要领域判断", "医学事实或答案变化须标medical_change=true", "不制定审核人数/轮数", "不把AI新生成格式反向强加", "未描述的检查/病史不等于未实施"} {
		if !strings.Contains(scoped[0].Content, want) {
			t.Fatal("reflection responsibility missing", want)
		}
	}
}
