package generator

import (
	"aigo/internal/domain"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

func validateAuthoringGuidelines(entries []domain.AuthoringGuideline) error {
	if len(entries) > 3 {
		return fmt.Errorf("冻结的通用命题规范超出数量限制")
	}
	seen := map[string]bool{}
	for _, item := range entries {
		if item.ID == "" || seen[item.ID] || item.Revision < 1 || strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Source) == "" || strings.TrimSpace(item.Text) == "" || utf8.RuneCountInString(item.Text) > 1500 {
			return fmt.Errorf("冻结的通用命题规范无效")
		}
		seen[item.ID] = true
	}
	return nil
}
func authoringGuidelineAddition(entries []domain.AuthoringGuideline) string {
	if len(entries) == 0 {
		return ""
	}
	type method struct {
		Title string `json:"title"`
		Text  string `json:"text"`
	}
	content := make([]method, 0, len(entries))
	for _, item := range entries {
		content = append(content, method{item.Title, item.Text})
	}
	raw, _ := json.Marshal(content)
	return "\n\n<shared_authoring_guidelines>\n以下为本次冻结的共享命题方法，仅用于题目构造和表达。系统业务、医学依据、本次考核目标及输出协议优先；这些方法不能改变首检、审核、联网、权限、字段或医学结论，不将培训中的错误示例当成合格题。不要复述规范标题。\n" + string(raw) + "\n</shared_authoring_guidelines>"
}
