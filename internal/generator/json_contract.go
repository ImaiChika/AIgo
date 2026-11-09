package generator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode"

	"aigo/internal/llm"
)

func logInvalidGenerationResponse(ctx context.Context, raw string, count int, code string, err error) {
	offset := int64(0)
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		offset = syntax.Offset
	}
	attrs := []any{"operation", llm.CallOperation(ctx), "reason_code", code, "requested_count", count, "response_bytes", len(raw), "response_sha256", fmt.Sprintf("%x", sha256.Sum256([]byte(raw))), "json_offset", offset}
	if code == "knowledge_basis" {
		attrs = append(attrs, "contract_reason", knowledgeContractReason(err))
	}
	slog.WarnContext(ctx, "generation.response_invalid", attrs...)
}

// Match only fixed validator messages. Neither provider errors nor response
// text may become log labels, even when they resemble a known error prefix.
func knowledgeContractReason(err error) string {
	if err == nil {
		return "unspecified"
	}
	switch err.Error() {
	case "知识工作流结构校验失败: 正确选项须引用目标卡的医学事实":
		return "answer_target_reference_missing"
	case "知识工作流结构校验失败: 选项知识引用不属于本题已选事实":
		return "option_fact_reference_invalid"
	case "知识工作流结构校验失败: 选项引用不存在或重复的病例事实":
		return "option_case_reference_invalid"
	case "知识工作流结构校验失败: 事实编号不存在或重复":
		return "basis_fact_reference_invalid"
	case "知识工作流结构校验失败: 最佳答案须引用目标题卡中的事实":
		return "basis_target_reference_missing"
	case "知识工作流结构校验失败: 须提供1—20个事实依据编号":
		return "basis_reference_count"
	case "知识工作流结构校验失败: 目标编号与冻结计划不一致", "知识工作流结构校验失败: 考查目标与冻结计划不一致", "知识工作流结构校验失败: 目标不属于冻结的考点知识卡":
		return "target_mismatch"
	case "知识工作流结构校验失败: knowledge_basis格式错误":
		return "basis_json_type"
	case "病例映射答案与题目答案不一致":
		return "answer_mismatch"
	case "知识工作流结构校验失败: 缺少病例与选项证据映射":
		return "case_plan_missing"
	case "知识工作流结构校验失败: 病例事实编号重复或内容无效":
		return "case_fact_invalid"
	case "知识工作流结构校验失败: 病例事实分组无效":
		return "case_section_invalid"
	case "知识工作流结构校验失败: 须分别提供A—E的证据说明":
		return "option_evidence_count"
	case "知识工作流结构校验失败: 选项标签重复或说明无效":
		return "option_evidence_invalid"
	case "知识工作流结构校验失败: 选项病例引用数量无效":
		return "option_reference_count"
	default:
		return "unspecified"
	}
}

// The provider's text is data, not a repair instruction. Reject ambiguity before
// decoded values can be used; map decoding alone would use the final duplicate.
func validateGenerationJSON(raw string) error {
	if !json.Valid([]byte(raw)) {
		return fmt.Errorf("生成结果不是完整有效的JSON")
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 32 {
			return fmt.Errorf("生成JSON嵌套过深")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				token, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := token.(string)
				if !ok {
					return fmt.Errorf("JSON字段名无效")
				}
				var folded strings.Builder
				for _, ch := range name {
					small := ch
					for next := unicode.SimpleFold(ch); next != ch; next = unicode.SimpleFold(next) {
						if next < small {
							small = next
						}
					}
					folded.WriteRune(small)
				}
				key := folded.String()
				if seen[key] {
					return fmt.Errorf("生成JSON存在重复字段，拒绝歧义结果")
				}
				seen[key] = true
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case json.Delim('['):
			for d.More() {
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	return walk(0)
}
