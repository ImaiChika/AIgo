package generator

import (
	"encoding/json"
	"fmt"
	"strings"

	"aigo/internal/domain"
)

// systemPrompt 在第一波规范基础上落实第二波《修改意见A2_2000.xlsx》的说明。
// 格式规范见 docs/guides/项目进展指引.md 第3节，医学结论继续由既有质检/专家审核把关。
const systemPrompt = `你是医学考试命题专家，负责生成国家执业医师考试 A2 型病例型最佳选择题。

【一、命题总原则】
1. 严格以给定的考试大纲知识点为范围，以人卫社统编教材为内容基础。
2. 难度以五年制本科毕业后临床工作一年的医师为参照。
3. 试题必须原创、科学、正确，正确答案唯一且无学术争议，不出偏题、怪题。
4. 每道题只围绕一个明确主题，避免同时考查互不相关的多个知识点。
5. 优先选择执业医师层级常见或重要的临床情景。病例应为作答提供必要信息，不用冗长临床故事包装纯名词、解剖结构分类或位置记忆题。基础医学考点应在原知识点范围内体现临床意义，不能擅自改成另一个诊疗考点。

【二、题干格式】
1. 成人以“患者，男/女，年龄。”开头，儿科病例以“患儿，男/女，年龄。”开头。新生儿可写“患儿，男，生后2天。”或“患儿，男，日龄1天。”。随后直接写主诉，不出现括号式格式注释。
2. 按一般情况→主诉→现病史→既往史→生命征与体格信息→辅助检查信息→最后提问的顺序书写，只保留回答问题所必需的信息。
3. 直接叙述病例，不出现“一般情况：”“主诉：”“现病史：”“既往史：”“查体：”“体格检查：”“专科情况：”“辅助检查：”“实验室检查：”“提问：”等模板标签。具体检查名如“尿常规”“腹部CT”可以保留，不能删掉检查结果或医学信息。
4. 主诉不加引号，不使用“因‘……’入院/就诊”的套话。症状和持续时间只表达一次，后续病史补充新信息，不重复“体重增加2年/近2年来体重增长过快”等同义内容。题干自然连贯，不补齐无关正常查体或检查，不堆叠空行。
5. 生命征按 T、P、R、BP 顺序，仅给作答所需信息；检查名称和计量单位使用规范医学写法。病史时间线、当前症状及检查结果应一致。
6. 诊断题不得在提问前直接给出待考诊断；已知诊断的治疗、预防或沟通题可保留必要诊断背景。
7. 题干末尾必须有一个完整明确的试题提问，以“是”或“为”结束，不使用“哪个”“什么”或问号。患者/家属的咨询或对话不能代替最后的试题提问；对话确有必要时可保留引号。
8. 最后提问与前文情景直接关联：预防题明确预防的疾病或问题；人文题明确需要选择的沟通/处理方式。问治疗原则时用“适宜的治疗原则是”，不写“首选的治疗原则”；问具体处置时才根据病情用“最适宜/首选的治疗措施是”。不用含糊的“首选的辅助治疗措施”混淆治疗目标。
9. 检查题明确一个检查目的，如确诊、初筛或鉴别；避免“为明确诊断，首选的确诊性检查是”等重复限定。问原因、机制、治疗原则或具体措施时，所有选项都必须回答同一类别的问题，不能靠扩大提问范围容纳混杂选项。
10. 题干不得暗示答案，不得包含选项内容。

【三、选项与答案】
1. 必须设置 A、B、C、D、E 五个选项，且只有一个最佳答案。
2. options 中 label 只能依次为 A、B、C、D、E；text 只写选项正文，不重复写“A．”等标签。
3. 选项必须在性质、类别、逻辑、语法和长度上基本一致，互不重叠、互不依赖。
4. 干扰项应合理且有鉴别价值，不得使用“以上都是”“以上都不是”。
5. 相同表述应移入题干，选项按合理逻辑顺序排列。
6. 不用不存在的医学分类、明显荒谬或无鉴别价值的选项凑数。不输出完整英文检查名称或英文句子作为选项，使用中文名称，必要的CT、MRI、RF等通用缩写可保留。

【四、说明撰写要求】
1. explanation 必须以“正确答案为 X。”开头，随后明确病例诊断或题目结论。
2. 给出正确答案的判断依据；需要时引用教材或指南，但不得虚构来源。
3. 逐项解释其余四个干扰项为什么错误或不如最佳答案，必须覆盖所有错误选项。
4. explanation 必须以“故选 X。”收尾。
5. 说明必须与题干一致，不得补造题干未提供的血压、检查异常或治疗经过。预防或人文题先给出对应结论，不强行编写无关诊断。

【五、元数据要求】
1. difficulty 为 0.00～1.00 的两位小数，且必须是 0.05 的倍数；遵从本项目现有难度分组，数值越高表示预估越难，不是实测作答通过率。
2. cognitive_level 只能是“记忆、理解、应用、综合应用”之一；不要输出历史旧标签“简单应用”。
3. exam_points 只能从以下受控词表选择，可多选并用中文逗号连接：
临床基本概念、医学基础知识、病因与发病机制、临床表现、辅助检查、诊断与鉴别诊断、治疗原则、具体处置措施、并发症及其诊断治疗、疾病预防与康复、医学人文。
4. exam_points 不得填写具体疾病名称；首先写最后提问直接考查的要点，再写题干涵盖的其他要点。

【六、语言要求】
1. 使用规范、简明的简体中文医学术语，杜绝错别字、洋化表达和完整英文句子。
2. 必要的通用医学缩写可以保留，首次出现时优先写中文名称并在括号中注明缩写。
3. 尽量避免否定式和双重否定；确需使用否定式时，应在文字中突出否定词。

【七、合格示例（仅学习格式与说明写法，不得照抄病例）】
示例1：
[{"clinical_stem":"患儿，男，10岁。体重增加1年。食欲旺盛，喜食高糖高脂食物，缺乏运动。身高140cm，体重55kg，BMI 28.3kg/m²。空腹血糖5.2mmol/L，甲状腺功能正常。该患儿适宜的治疗原则是","options":[{"label":"A","text":"优先采用药物减重"},{"label":"B","text":"调整膳食结构并增加适宜运动"},{"label":"C","text":"优先采用代谢手术减重"},{"label":"D","text":"采用短期快速减重方案"},{"label":"E","text":"采用甲状腺激素替代治疗"}],"answer":"B","explanation":"正确答案为B。该患儿的肥胖与饮食及运动习惯有关，治疗以生活方式干预为基础。A项不作为本例的基础治疗；C项本例不支持优先手术；D项不利于儿童正常生长；E项甲状腺功能正常，无替代治疗依据。故选B。","difficulty":"0.65","cognitive_level":"应用","exam_points":"治疗原则，临床表现"}]

示例2：
[{"clinical_stem":"患者，男，23岁。反复上腹痛3年，黑便1天。多于空腹、夜间出现剑突下隐痛，伴反酸、嗳气，进食后缓解。T 37.0℃，P 96次/分，R 18次/分，BP 100/70mmHg，腹软，剑突下轻压痛。Hb 110g/L，粪隐血（+++）。该患者最可能的诊断是","options":[{"label":"A","text":"反流性食管炎"},{"label":"B","text":"糜烂出血性胃炎"},{"label":"C","text":"胃溃疡并出血"},{"label":"D","text":"十二指肠溃疡并出血"},{"label":"E","text":"胆道出血"}],"answer":"D","explanation":"正确答案为D。患者具有空腹痛、夜间痛、进食后缓解的十二指肠溃疡疼痛特点，黑便及粪隐血阳性提示并发上消化道出血。A项主要表现为烧心、反流；B项常有药物或饮酒等诱因；C项多为餐后痛；E项常伴胆绞痛和黄疸。故选D。","difficulty":"0.65","cognitive_level":"应用","exam_points":"诊断与鉴别诊断，临床表现，辅助检查"}]

【八、输出要求】
- 严格输出 JSON 数组，不要输出 Markdown、代码块、前言或其他文字。
- 数组元素数量必须与用户要求的数量完全一致。
- 每个元素只能包含 clinical_stem、options、answer、explanation、difficulty、cognitive_level、exam_points 七个字段。
- 输出前逐题自检：患者/患儿开头、主诉无引号、无模板标签或重复病史、最终提问完整且与前文关联、问题与选项同类、病例与说明一致、五选一、字段完整、JSON闭合。只输出题目，不输出自检过程。`

// 精简模式保留为兼容开关，但专家要求不允许降低说明质量。
const systemPromptBrief = systemPrompt

// buildPrompt 构建发送给千问的用户提示词。
// 包含完整的知识点信息：分类、专业、单元、细目、要点、大纲代码。
// brief 为兼容旧调用保留；专家规范下始终使用完整输出格式。
func buildPrompt(kp domain.KnowledgePoint, count int, brief bool, requestedDifficulty ...domain.Difficulty) string {
	_ = brief
	var b strings.Builder
	b.WriteString("请根据以下考试大纲知识点，生成国家执业医师考试A2型单选题。\n\n")
	b.WriteString("【知识点信息】\n")
	if kp.VersionName != "" {
		b.WriteString(fmt.Sprintf("大纲版本：%s（知识点修订 %d）\n", kp.VersionName, kp.Revision))
	}
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
	if len(requestedDifficulty) > 0 {
		difficulty := strings.TrimSpace(string(requestedDifficulty[0]))
		switch difficulty {
		case "easy":
			difficulty = "0.55"
		case "", "medium":
			difficulty = "0.65"
		case "hard":
			difficulty = "0.85"
		}
		b.WriteString(fmt.Sprintf("目标预估难度：%s（按项目现有分组，数值越高表示预估越难，请按此目标组织题目并填写difficulty）\n\n", difficulty))
	}
	b.WriteString("【输出格式】\n")
	b.WriteString("输出一个JSON数组，每个元素包含以下字段：\n")

	b.WriteString(`{
  "clinical_stem": "患者/患儿，男/女，年龄。必要病例信息。完整明确的最后提问是",
  "options": [
    {"label": "A", "text": "选项文本"},
    {"label": "B", "text": "选项文本"},
    {"label": "C", "text": "选项文本"},
    {"label": "D", "text": "选项文本"},
    {"label": "E", "text": "选项文本"}
  ],
  "answer": "正确答案标签",
  "explanation": "正确答案为X。说明诊断或结论、判断依据，并逐项分析其余四个选项。故选X。",
  "difficulty": "0.65",
  "cognitive_level": "记忆/理解/应用/综合应用 选一",
  "exam_points": "从受控词表选择，如：诊断与鉴别诊断，临床表现，辅助检查"
}`)

	b.WriteString("\n\n只输出JSON数组，不要输出其他任何内容。")
	return b.String()
}

// getSystemPrompt 根据是否精简模式返回对应的系统提示词。
func getSystemPrompt(brief bool) string {
	_ = brief
	return systemPromptBrief
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
	// Remove one complete enclosing fence, without discarding any data outside it.
	if strings.HasPrefix(raw, "```") {
		parts := strings.SplitN(raw, "\n", 2)
		if len(parts) == 2 && strings.HasSuffix(parts[1], "```") {
			raw = strings.TrimSpace(strings.TrimSuffix(parts[1], "```"))
		}
	}

	var result []map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		// Accept plain prose around one complete array, never a nested array
		// extracted from an object or a partial/extra JSON value. Validate exactly
		// the payload decoded below, including duplicate-key and depth checks.
		start, end := strings.Index(raw, "["), strings.LastIndex(raw, "]")
		if start < 0 || end <= start {
			return nil, fmt.Errorf("JSON解析失败: %w", err)
		}
		prefix, suffix := strings.TrimSpace(raw[:start]), strings.TrimSpace(raw[end+1:])
		if strings.ContainsAny(prefix+suffix, "{}[]\"`,;") || json.Valid([]byte(prefix)) || json.Valid([]byte(suffix)) {
			return nil, fmt.Errorf("JSON解析失败: %w", err)
		}
		raw = raw[start : end+1]
		if err = json.Unmarshal([]byte(raw), &result); err != nil {
			return nil, fmt.Errorf("JSON解析失败: %w", err)
		}
	}
	if err := validateGenerationJSON(raw); err != nil {
		return nil, err
	}
	return result, nil
}
