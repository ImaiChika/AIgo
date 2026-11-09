package aicheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"aigo/internal/domain"
)

// ErrCheckProtocol identifies unusable model output, not a medical rejection.
// Existing first-check tasks retry these errors within their original budget.
var ErrCheckProtocol = errors.New("首检响应协议无效")

const checkDecisionPolicy = "first-check-baseline-verdict-v1"

type responseIssue struct {
	Field    string `json:"field"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}
type checkResponse struct {
	Verdict string `json:"verdict"`
	Scores  *struct {
		Scientific *int `json:"scientific"`
		Logic      *int `json:"logic"`
		A2Fit      *int `json:"a2_fit"`
		Answer     *int `json:"answer"`
	} `json:"scores"`
	Issues           *[]responseIssue `json:"issues"`
	MetadataFeedback []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"metadata_feedback"`
	Suggestion string `json:"suggestion"`
}

func protocolError(reason string) error { return fmt.Errorf("%w: %s", ErrCheckProtocol, reason) }
func isMetadataField(field string) bool {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "difficulty", "cognitive_level", "cognitive", "exam_points", "outline_code", "profession", "system", "metadata", "难度", "认知层次", "考核要点", "大纲代码", "专业", "系统", "元数据":
		return true
	}
	return false
}

// Classify the declared field only. A medical issue does not become metadata
// merely because its message happens to contain 'difficulty' or an outline code.
func isMetadataIssue(issue domain.ReviewIssue) bool { return isMetadataField(issue.Field) }

func parseCheckResponse(raw, questionID, modelName string) (*domain.AIReviewResult, error) {
	raw = strings.TrimSpace(raw)
	// Retain the previously supported fenced-JSON transport format.
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		cleaned := []string{}
		inside := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				inside = !inside
				continue
			}
			if inside {
				cleaned = append(cleaned, line)
			}
		}
		raw = strings.Join(cleaned, "\n")
	}
	if !json.Valid([]byte(raw)) {
		return nil, protocolError("需要一个合法JSON对象")
	}
	if err := checkUniqueJSONFields(raw); err != nil {
		return nil, err
	}
	var response checkResponse
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		return nil, protocolError("字段类型不符合输出协议")
	}
	verdict := strings.ToLower(strings.TrimSpace(response.Verdict))
	switch verdict {
	case "pass", "issues_found", "reject":
	default:
		return nil, protocolError("verdict须为pass、issues_found或reject")
	}
	if response.Scores == nil {
		return nil, protocolError("scores不能缺失或为null")
	}
	scores := []*int{response.Scores.Scientific, response.Scores.Logic, response.Scores.A2Fit, response.Scores.Answer}
	for _, score := range scores {
		if score == nil || *score < 0 || *score > 100 {
			return nil, protocolError("四项核心评分必须完整且均为0—100整数")
		}
	}
	if response.Issues == nil {
		return nil, protocolError("issues须为数组，无问题时返回空数组")
	}
	result := &domain.AIReviewResult{ID: fmt.Sprintf("air-%s-%d", questionID, time.Now().UnixNano()), QuestionID: questionID, Verdict: verdict, Scores: domain.ReviewScores{Scientific: *scores[0], Logic: *scores[1], A2Fit: *scores[2], Answer: *scores[3]}, Issues: []domain.ReviewIssue{}, Suggestion: response.Suggestion, Model: modelName, RawResponse: raw, CreatedAt: time.Now()}
	for _, issue := range *response.Issues {
		field := strings.ToLower(strings.TrimSpace(issue.Field))
		severity := strings.ToLower(strings.TrimSpace(issue.Severity))
		message := strings.TrimSpace(issue.Message)
		if field == "" || message == "" {
			return nil, protocolError("问题必须注明字段及非空说明")
		}
		switch severity {
		case "error", "warning", "info":
		default:
			return nil, protocolError("问题级别须为error、warning或info")
		}
		result.Issues = append(result.Issues, domain.ReviewIssue{Field: field, Severity: severity, Message: message})
	}
	if err := normalizeCheckVerdict(result); err != nil {
		return nil, err
	}
	for _, feedback := range response.MetadataFeedback {
		if !isMetadataField(feedback.Field) || strings.TrimSpace(feedback.Message) == "" {
			return nil, protocolError("metadata_feedback只接受明确的命题元数据字段及非空说明")
		}
		result.Issues = append(result.Issues, domain.ReviewIssue{Field: strings.ToLower(strings.TrimSpace(feedback.Field)), Severity: "info", Message: strings.TrimSpace(feedback.Message)})
	}
	return result, nil
}

// JSON's last-key-wins behavior must not silently erase an earlier error or score.
// Go's decoder also matches case-insensitively, hence the same key normalization.
func checkUniqueJSONFields(raw string) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 16 {
			return protocolError("JSON嵌套层级过深")
		}
		token, err := decoder.Token()
		if err != nil {
			return protocolError("JSON结构无效")
		}
		delimiter, container := token.(json.Delim)
		if !container {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return protocolError("JSON字段无效")
				}
				name, ok := key.(string)
				if !ok {
					return protocolError("JSON字段名须为字符串")
				}
				name = foldJSONKey(name)
				if seen[name] {
					return protocolError("JSON对象存在重复字段")
				}
				seen[name] = true
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return protocolError("JSON结构无效")
		}
		_, err = decoder.Token()
		if err != nil {
			return protocolError("JSON结构无效")
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return protocolError("只允许一个检查结果对象")
	}
	return nil
}

// Preserve the pre-workflow model verdict. Numeric thresholds and warning rules
// remain instructions to the checker, not a second server-side quality gate.
// Only declared legacy metadata issues receive the original nonblocking exemption;
// arbitrary medical messages mentioning metadata never change classification.
func normalizeCheckVerdict(result *domain.AIReviewResult) error {
	original := result.Verdict
	hasMetadata, hasCoreBlock := false, false
	for i := range result.Issues {
		issue := &result.Issues[i]
		if isMetadataIssue(*issue) {
			hasMetadata = true
			issue.Severity = "info"
		} else if issue.Severity == "error" || issue.Severity == "warning" {
			hasCoreBlock = true
		}
	}
	if hasMetadata && !hasCoreBlock {
		result.Verdict = "pass"
	}
	if result.Verdict != original {
		slog.Info("ai_check.verdict_resolved", "policy", checkDecisionPolicy, "question_id", result.QuestionID, "model_verdict", original, "effective_verdict", result.Verdict)
	}
	return nil
}

// Match encoding/json's Unicode case folding (e.g. long-s and ASCII s).
func foldJSONKey(value string) string {
	var out strings.Builder
	for _, character := range value {
		smallest := character
		for candidate := unicode.SimpleFold(character); candidate != character; candidate = unicode.SimpleFold(candidate) {
			if candidate < smallest {
				smallest = candidate
			}
		}
		out.WriteRune(smallest)
	}
	return out.String()
}

func verdictName(value string) string {
	switch value {
	case "pass":
		return "通过"
	case "issues_found":
		return "发现问题"
	default:
		return "不通过"
	}
}
