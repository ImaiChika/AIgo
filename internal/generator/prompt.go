package generator

import (
	"encoding/json"
	"fmt"
	"strings"

	"aigo/internal/domain"
)

// systemPrompt 千问系统提示词，包含 A2 型题命题规范（完整版）。
// 内容要求来自《试题命制要求》三、任务要求 和 （一）内容要求。
const systemPrompt = `你是医学考试命题助手，负责生成国家执业医师考试A2型单选题。

【一、命题总原则】
1. 以《考试大纲》为依据，以人卫社统编教材为内容基础
2. 执业医师命题以本科生毕业后培训一年的水平为标准
3. 所命试题应是新编原创试题，采用新的素材或临床情景，避免照搬书本现成实例

【二、A2型题格式】
题干按病历书写顺序：一般情况→主诉→现病史→既往史→查体→辅助检查→提问
- 一般情况：男/女，年龄
- 主诉：主要症状或体征+时间
- 现病史：发病诱因、症状特点、伴随症状、诊治经过
- 既往史：根据病例需要编写
- 查体：按生命征→一般情况→头颈→肺→心→腹→脊柱→四肢→神经系统顺序
- 辅助检查：常用检查执业医师以英文表示
- 提问：该患者最可能的诊断是 / 最有价值的检查是 / 治疗原则是

【三、内容要求】
1. 试题内容科学、正确
2. 正确答案唯一、且无学术上的争议
3. 内容取样有较好的代表性，避免出偏题、怪题
4. 试题必须有明确的主题，题干和备选答案必须围绕同一知识点，避免在一道题中考查多个知识点
5. 避免存在性别、种族、地域、文化的不公平或歧视
6. 必须使用规范的医学术语，名词术语、药物名称、化验数值和计量单位须准确、规范

【四、题干要求】
1. 题干叙述简明扼要，包含回答问题所必需的全部要素
2. 题干提出的问题具体明确，使应试者一看到题干就能明确要考察的知识点和具体内容
3. 题干中不能包括备选答案或对回答问题有所暗示
4. 题干尽量以叙述式书写

【五、备选答案要求】
1. 备选答案之间不能有相互重叠、相互依赖的内容
2. 备选答案应在性质上、类别上相同，在逻辑、语法和内容长短上也应基本一致
3. 备选答案中避免无意义或无用的干扰答案
4. 不使用"以上都是"和"以上都不是"作为备选答案
5. 备选答案与题干符合逻辑性
6. 备选答案按逻辑顺序排列
7. 备选答案中的相同表述，统一合理地放到题干中

【六、文字要求】
1. 试题所用文字简明、扼要，避免生僻、艰涩、洋化用语
2. 避免暗示或模糊性用语，杜绝错别字
3. 尽量避免使用否定式，必须使用否定句时用黑体加粗强调否定词，杜绝使用双重否定

【七、输出要求】
- 严格输出JSON数组，不要输出任何其他文字
- 选项五选一
- 每道题必须包含完整解析，说明正确答案依据和干扰项错误原因
- 难度以0-1之间两位小数表示（0.05的倍数），如0.65
- 认知层次：记忆/理解/简单应用/综合应用 选一
- 考核要点标注具体，如：诊断与鉴别诊断，临床表现，辅助检查
- 大纲代码标到最后一级`

// systemPromptBrief 精简版系统提示词，解析限制200字以内但保留必要医学内容。
const systemPromptBrief = `你是医学考试命题助手，负责生成国家执业医师考试A2型单选题。

【A2型题格式】
题干按病历顺序：一般情况→主诉→现病史→既往史→查体→辅助检查→提问

【要求】
1. 试题内容科学、正确，答案唯一
2. 题干简明扼要，不暗示答案
3. 五选一，不使用"以上都是/都不是"
4. 使用规范医学术语

【输出要求】
- 严格输出JSON数组
- 解析限制在200字以内，需包含：正确答案的诊断依据（关键症状、体征、检查指标）、干扰项的鉴别要点
- 难度0-1两位小数（0.05倍数）
- 认知层次：记忆/理解/简单应用/综合应用
- 考核要点：诊断/表现/检查/治疗等`

// buildPrompt 构建发送给千问的用户提示词。
// 包含完整的知识点信息：分类、专业、单元、细目、要点、大纲代码。
// brief 为 true 时使用精简输出格式（解析限制100字）。
func buildPrompt(kp domain.KnowledgePoint, count int, brief bool) string {
	var b strings.Builder
	b.WriteString("请根据以下考试大纲知识点，生成国家执业医师考试A2型单选题。\n\n")
	b.WriteString("【知识点信息】\n")
	b.WriteString(fmt.Sprintf("大纲代码：%s\n", kp.OutlineCode))
	b.WriteString(fmt.Sprintf("分类：%s\n", kp.Category))
	b.WriteString(fmt.Sprintf("专业/系统：%s\n", kp.Subject))
	if kp.Unit != "" {
		b.WriteString(fmt.Sprintf("单元：%s\n", kp.Unit))
	}
	if kp.SubItem != "" {
		b.WriteString(fmt.Sprintf("细目：%s\n", kp.SubItem))
	}
	b.WriteString(fmt.Sprintf("要点：%s\n", kp.Topic))
	b.WriteString(fmt.Sprintf("数量：%d道\n\n", count))
	b.WriteString("【输出格式】\n")
	b.WriteString("输出一个JSON数组，每个元素包含以下字段：\n")

	if brief {
		// 精简版：解析限制200字，保留必要医学内容
		b.WriteString(`{
  "clinical_stem": "临床情境题干",
  "options": [{"label":"A","text":""},{"label":"B","text":""},{"label":"C","text":""},{"label":"D","text":""},{"label":"E","text":""}],
  "answer": "正确答案标签",
  "explanation": "200字以内解析，包含诊断依据和鉴别要点",
  "difficulty": "0.65",
  "cognitive_level": "记忆/理解/简单应用/综合应用",
  "exam_points": "考核要点"
}`)
	} else {
		// 完整版
		b.WriteString(`{
  "clinical_stem": "临床情境题干（按病历顺序书写）",
  "options": [
    {"label": "A", "text": "选项文本"},
    {"label": "B", "text": "选项文本"},
    {"label": "C", "text": "选项文本"},
    {"label": "D", "text": "选项文本"},
    {"label": "E", "text": "选项文本"}
  ],
  "answer": "正确答案标签",
  "explanation": "完整解析：说明正确答案依据和干扰项错误原因",
  "difficulty": "0.65",
  "cognitive_level": "记忆/理解/简单应用/综合应用 选一",
  "exam_points": "考核要点，如：诊断与鉴别诊断，临床表现"
}`)
	}

	b.WriteString("\n\n只输出JSON数组，不要输出其他任何内容。")
	return b.String()
}

// getSystemPrompt 根据是否精简模式返回对应的系统提示词。
func getSystemPrompt(brief bool) string {
	if brief {
		return systemPromptBrief
	}
	return systemPrompt
}

// BuildPrompt 构建用户提示词（导出版本，供 batch 包复用）。
func BuildPrompt(kp domain.KnowledgePoint, count int) string {
	return buildPrompt(kp, count, false)
}

// GetSystemPrompt 返回完整版系统提示词（导出版本，供 batch 包复用）。
func GetSystemPrompt() string {
	return getSystemPrompt(false)
}

// ParseQuestionsFromRaw 从 LLM 返回的文本中解析 JSON 数组（导出版本，供 batch 包使用）。
func ParseQuestionsFromRaw(raw string) ([]map[string]interface{}, error) {
	return parseQuestions(raw)
}

// parseQuestions 从 LLM 返回的文本中解析 JSON 数组。
// 兼容 LLM 可能用 ```json ... ``` 包裹的情况。
func parseQuestions(raw string) ([]map[string]interface{}, error) {
	raw = strings.TrimSpace(raw)
	// 去掉可能的 markdown 代码块标记
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
		return nil, fmt.Errorf("JSON解析失败: %w", err)
	}
	return result, nil
}
