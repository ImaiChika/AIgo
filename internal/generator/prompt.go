package generator

import (
	"encoding/json"
	"fmt"
	"strings"
)

const systemPrompt = `你是医学考试命题助手，负责生成国家执业医师考试A2型单选题。
你必须输出严格JSON，不要输出任何其他文字。
如请求中包含多模态素材，只能根据已结构化的文字描述使用，不得声称自己读取了图片或影像。
干扰项必须有临床鉴别意义，不能明显离题，不能与正确答案重复。`

// A2JSONSchema 描述 LLM 应输出的 JSON 结构。
// 解析为可选字段，首期不强制要求。
const A2JSONSchema = `{
    "clinical_stem": "临床情境题干文本",
    "options": [
      {"label": "A", "text": "选项文本"},
      {"label": "B", "text": "选项文本"},
      {"label": "C", "text": "选项文本"},
      {"label": "D", "text": "选项文本"},
      {"label": "E", "text": "选项文本"}
    ],
    "answer": "A",
    "explanation": "可选，解析文本",
    "difficulty": "medium"
}`

func buildPrompt(subject string, difficulty string, topic string, count int) string {
	var b strings.Builder
	b.WriteString("请根据以下要求生成国家执业医师考试A2型单选题。\n\n")
	b.WriteString(fmt.Sprintf("科目：%s\n", subject))
	b.WriteString(fmt.Sprintf("难度：%s\n", difficulty))
	b.WriteString(fmt.Sprintf("知识点：%s\n", topic))
	b.WriteString(fmt.Sprintf("数量：%d\n\n", count))
	b.WriteString("输出格式要求：\n")
	b.WriteString("输出一个JSON数组，每个元素的字段如下：\n")
	b.WriteString(A2JSONSchema)
	b.WriteString("\n\n只输出JSON数组，不要输出其他任何内容。")
	return b.String()
}
func parseQuestions(raw string) ([]map[string]interface{}, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		var cleaned []string
		inBlock := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				inBlock = !inBlock
				continue
			}
			if inBlock {
				cleaned = append(cleaned, line)
			}
		}
		raw = strings.Join(cleaned, "\n")

	}
	var result []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}
	return result, nil
}
