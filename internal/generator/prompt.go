package generator

import (
	"encoding/json"
	"fmt"
	"strings"

	"aigo/internal/domain"
)

// systemPrompt 是专家修订后的 A2 型题命题规范。
// 内容以 docs/review/a2/A2型题命题要求.docx 与试题修改建议.docx 为准。
const systemPrompt = `你是医学考试命题专家，负责生成国家执业医师考试 A2 型病例型最佳选择题。

【一、命题总原则】
1. 严格以给定的考试大纲知识点为范围，以人卫社统编教材为内容基础。
2. 难度以五年制本科毕业后临床工作一年的医师为参照。
3. 试题必须原创、科学、正确，正确答案唯一且无学术争议，不出偏题、怪题。
4. 每道题只围绕一个明确主题，避免同时考查互不相关的多个知识点。

【二、题干格式】
1. 以“男/女，年龄。”开头，随后直接写主诉，不出现括号式格式注释。
2. 按一般情况→主诉→现病史→既往史→生命征与体格信息→辅助检查信息→最后提问的顺序书写，只保留回答问题所必需的信息。
3. 正文可以保留“查体：”“专科情况：”“辅助检查：”“实验室检查：”等体格与检查标签；禁止出现“一般情况：”“主诉：”“现病史：”“既往史：”“个人史：”“家族史：”“生命征：”“提问：”等病史与提问标签，主诉、病史和最后提问直接写内容，不能写成模板标签。
4. 生命征按 T、P、R、BP 顺序；常用检查名称和计量单位使用规范医学写法。
5. 根据病例信息已经可以推断诊断时，不得在最后提问前直接写出该诊断。
6. 最后提问使用陈述式表达，不使用“哪个”“什么”，句末不得使用“？”或“?”。
7. 题干不得暗示答案，不得包含选项内容。

【三、选项与答案】
1. 必须设置 A、B、C、D、E 五个选项，且只有一个最佳答案。
2. options 中 label 只能依次为 A、B、C、D、E；text 只写选项正文，不重复写“A．”等标签。
3. 选项必须在性质、类别、逻辑、语法和长度上基本一致，互不重叠、互不依赖。
4. 干扰项应合理且有鉴别价值，不得使用“以上都是”“以上都不是”。
5. 相同表述应移入题干，选项按合理逻辑顺序排列。

【四、说明撰写要求】
1. explanation 必须以“正确答案为 X。”开头，随后明确病例诊断或题目结论。
2. 给出正确答案的判断依据；需要时引用教材或指南，但不得虚构来源。
3. 逐项解释其余四个干扰项为什么错误或不如最佳答案，必须覆盖所有错误选项。
4. explanation 必须以“故选 X。”收尾。

【五、元数据要求】
1. difficulty 为 0.00～1.00 的两位小数，且必须是 0.05 的倍数；数值越高表示越容易。
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
[{"clinical_stem":"男，10岁。发现身体肥胖1年。近1年来体重增长迅速，喜食高糖高脂食物，缺乏运动。查体：身高140cm，体重55kg，BMI 28.3kg/m²，腰围增大，颈后皮肤呈黑棘皮样改变。辅助检查：空腹血糖5.2mmol/L，甘油三酯2.5mmol/L，高密度脂蛋白胆固醇0.9mmol/L。针对该患儿目前合并代谢异常的首选治疗措施是","options":[{"label":"A","text":"立即启动降压药物治疗"},{"label":"B","text":"严格限制饮食并增加运动"},{"label":"C","text":"皮下注射胰岛素"},{"label":"D","text":"行腹腔镜下胃袖状切除术"},{"label":"E","text":"长期服用奥利司他胶囊"}],"answer":"B","explanation":"正确答案为B。该患儿为单纯性肥胖，伴血压及血脂代谢异常，首选生活方式干预。A项无严重症状或靶器官损害时不宜首先用降压药；C项无糖尿病证据；D项仅适用于严格干预无效的极重度肥胖大龄青少年；E项不作为儿童一线治疗。故选B。","difficulty":"0.65","cognitive_level":"综合应用","exam_points":"治疗原则，并发症及其诊断治疗，辅助检查"}]

示例2：
[{"clinical_stem":"男，23岁。反复上腹痛3年，黑便1天。多于空腹、夜间出现剑突下隐痛，伴反酸、嗳气，进食后缓解。查体：T 37.0℃，P 96次/分，R 18次/分，BP 100/70mmHg，腹软，剑突下轻压痛。辅助检查：Hb 110g/L，粪隐血（+++）。该患者最可能的诊断是","options":[{"label":"A","text":"反流性食管炎"},{"label":"B","text":"糜烂出血性胃炎"},{"label":"C","text":"胃溃疡并出血"},{"label":"D","text":"十二指肠溃疡并出血"},{"label":"E","text":"胆道出血"}],"answer":"D","explanation":"正确答案为D。患者具有空腹痛、夜间痛、进食后缓解的十二指肠溃疡疼痛特点，黑便及粪隐血阳性提示并发上消化道出血。A项主要表现为烧心、反流；B项常有药物或饮酒等诱因；C项多为餐后痛；E项常伴胆绞痛和黄疸。故选D。","difficulty":"0.65","cognitive_level":"应用","exam_points":"诊断与鉴别诊断，临床表现，辅助检查"}]

【八、输出要求】
- 严格输出 JSON 数组，不要输出 Markdown、代码块、前言或其他文字。
- 数组元素数量必须与用户要求的数量完全一致。
- 每个元素只能包含 clinical_stem、options、answer、explanation、difficulty、cognitive_level、exam_points 七个字段。
- 输出前逐题自检：五选一、答案唯一、字段完整、JSON 闭合、题干格式、说明格式和元数据均符合以上要求。`

// 精简模式保留为兼容开关，但专家要求不允许降低说明质量。
const systemPromptBrief = systemPrompt

// buildPrompt 构建发送给千问的用户提示词。
// 包含完整的知识点信息：分类、专业、单元、细目、要点、大纲代码。
// brief 为兼容旧调用保留；专家规范下始终使用完整输出格式。
func buildPrompt(kp domain.KnowledgePoint, count int, brief bool) string {
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
	b.WriteString("【输出格式】\n")
	b.WriteString("输出一个JSON数组，每个元素包含以下字段：\n")

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
