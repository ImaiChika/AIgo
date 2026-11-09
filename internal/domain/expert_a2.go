package domain

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A2FormatSecondWave 只绑定新生成题；历史空值继续按第一波协议审核。
const A2FormatSecondWave = "a2-2026-09-r2"

// A2FormatContextual binds new questions to evidence-based checking. Historical
// questions keep their immutable format and original checking behavior.
const A2FormatContextual = "a2-2026-10-context-v1"

const A2FormatFocused = "a2-2026-10-context-v2"

func IsContextualA2Format(version string) bool {
	return version == A2FormatContextual || version == A2FormatFocused
}

var secondWaveStemStartPattern = regexp.MustCompile(`^(患者|患儿)，\s*(男|女)，\s*(?:(生后|日龄)\s*)?(\d+)\s*(岁|个月|月|天|小时)`)
var generatedPatientStartPattern = regexp.MustCompile(`^(男|女)，\s*(?:(生后|日龄)\s*)?(\d+)\s*(岁|个月|月|天|小时)`)
var quotedChiefComplaintPattern = regexp.MustCompile(`^(?:因)?[“‘"']([^“”‘’"']+)[”’"'](?:就诊|入院)?$`)
var englishPhrasePattern = regexp.MustCompile(`[A-Za-z]*[a-z][A-Za-z]*[ \t-]+[A-Za-z]*[a-z][A-Za-z]*`)
var longEnglishWordPattern = regexp.MustCompile(`[A-Za-z]{4,}`)

var secondWaveStemMarkers = []string{"一般情况", "主诉", "现病史", "既往史", "个人史", "家族史", "生命征", "生命体征", "查体", "体格检查", "专科情况", "辅助检查", "实验室检查", "提问"}

var a2StemStartPattern = regexp.MustCompile(`^(男|女)，\s*\d+\s*(岁|个月|月|天)`) // 专家要求：性别、年龄开头
var a2DifficultyPattern = regexp.MustCompile(`^(0\.\d{2}|1\.00)$`)

var allowedA2CognitiveLevels = map[string]struct{}{
	"记忆": {}, "理解": {}, "应用": {}, "综合应用": {},
	// “简单应用”是历史数据中的旧标签，新生成题会在 NormalizeGeneratedA2 中归一为“应用”。
	"简单应用": {},
}

var allowedA2ExamPoints = map[string]struct{}{
	"临床基本概念":    {},
	"医学基础知识":    {},
	"病因与发病机制":   {},
	"临床表现":      {},
	"辅助检查":      {},
	"诊断与鉴别诊断":   {},
	"治疗原则":      {},
	"具体处置措施":    {},
	"并发症及其诊断治疗": {},
	"疾病预防与康复":   {},
	"医学人文":      {},
}

// forbiddenStemMarkers 是需要从题干中清除的病史与提问引导词。
// “查体：/专科情况：/辅助检查：/实验室检查：”等体格与检查标签允许保留。
var forbiddenStemMarkers = []string{"一般情况：", "主诉：", "现病史：", "既往史：", "个人史：", "家族史：", "生命征：", "生命体征：", "提问："}

// NormalizeGeneratedA2 修正不会改变医学语义的格式问题。
// 该方法仅用于 AI 新生成题，不改变历史题兼容校验。
func (q *A2Question) NormalizeGeneratedA2() {
	if q == nil {
		return
	}
	stem := strings.TrimSpace(q.ClinicalStem)
	for _, marker := range forbiddenStemMarkers {
		stem = strings.ReplaceAll(stem, marker, "")
	}
	if q.FormatVersion == A2FormatSecondWave || IsContextualA2Format(q.FormatVersion) {
		stem = normalizeSecondWaveStem(stem)
	}
	stem = strings.TrimRight(stem, "？? \t\r\n")
	q.ClinicalStem = stem
	q.Answer = strings.ToUpper(strings.Trim(strings.TrimSpace(q.Answer), ".．、：:"))
	for i := range q.Options {
		q.Options[i].Label = strings.ToUpper(strings.Trim(strings.TrimSpace(q.Options[i].Label), ".．、：:"))
		q.Options[i].Text = strings.TrimSpace(q.Options[i].Text)
	}
	q.Explanation = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(q.Explanation), "说明："))
	q.CognitiveLevel = strings.TrimSpace(q.CognitiveLevel)
	if q.CognitiveLevel == "简单应用" {
		q.CognitiveLevel = "应用"
	}
	q.ExamPoints = normalizeA2ExamPoints(q.ExamPoints)
}

// ValidateA2ContentForReview 校验题目核心内容与 A2 格式。
//
// 难度、认知层次、考核要点、大纲代码、专业和系统属于命题元数据，
// 不在本方法中校验。元数据有误时应反馈给出题提示词或人工审核，
// 不能因此否决医学内容正确且格式可用的题目。
func (q *A2Question) ValidateA2ContentForReview() error {
	if err := q.Validate(); err != nil {
		return err
	}
	// 新生成与批量生成使用五选项 A2 格式；历史四选项题仍由 ValidateForReview 兼容。
	if len(q.Options) != 5 {
		return invalidQuestionError("AI生成的A2题必须有A-E五个选项")
	}
	for i, option := range q.Options {
		want := string(rune('A' + i))
		if option.Label != want {
			return invalidQuestionError("选项标签必须依次为A、B、C、D、E")
		}
		if strings.Contains(option.Text, "以上都是") || strings.Contains(option.Text, "以上都不是") {
			return invalidQuestionError("选项不得使用“以上都是”或“以上都不是”")
		}
	}
	if q.FormatVersion == A2FormatSecondWave || IsContextualA2Format(q.FormatVersion) {
		if err := q.validateSecondWaveFormat(); err != nil {
			return err
		}
	} else if q.FormatVersion != "" {
		return invalidQuestionError("不支持的命题格式版本")
	} else if !a2StemStartPattern.MatchString(q.ClinicalStem) {
		return invalidQuestionError("题干必须以“男/女，年龄”开头")
	}
	for _, marker := range forbiddenStemMarkers {
		if strings.Contains(q.ClinicalStem, marker) {
			return invalidQuestionError("题干不得出现格式引导词“" + marker + "”")
		}
	}
	if strings.HasSuffix(q.ClinicalStem, "？") || strings.HasSuffix(q.ClinicalStem, "?") {
		return invalidQuestionError("最后提问句末不得使用问号")
	}
	tail := q.ClinicalStem
	if index := strings.LastIndex(tail, "。"); index >= 0 && index+len("。") < len(tail) {
		tail = tail[index+len("。"):]
	}
	if q.FormatVersion != A2FormatSecondWave && !IsContextualA2Format(q.FormatVersion) && (strings.Contains(tail, "哪个") || strings.Contains(tail, "什么")) {
		return invalidQuestionError("最后提问不得使用“哪个”或“什么”")
	}
	if strings.TrimSpace(q.Explanation) != "" {
		compactExplanation := strings.ReplaceAll(strings.ReplaceAll(q.Explanation, " ", ""), "\n", "")
		if !strings.HasPrefix(compactExplanation, "正确答案为"+q.Answer) {
			return invalidQuestionError("说明必须以正确答案及诊断或结论开头")
		}
		ending := strings.TrimRight(compactExplanation, "。；;！!")
		if !strings.HasSuffix(ending, "故选"+q.Answer) {
			return invalidQuestionError("说明必须以“故选X”收尾")
		}
		for _, option := range q.Options {
			if option.Label == q.Answer {
				continue
			}
			if !mentionsOptionExplanation(compactExplanation, option.Label) {
				return invalidQuestionError("说明必须逐项分析干扰项" + option.Label)
			}
		}
	}
	return nil
}

// ValidateGeneratedA2 校验 AI 新生成题是否符合专家 A2 规范。
// 生成协议仍要求元数据完整且来自受控值；AI 质量检查和审核门禁不使用这些
// 元数据作为内容淘汰条件，详见 ValidateA2ContentForReview。
func (q *A2Question) ValidateGeneratedA2() error {
	if err := q.ValidateA2ContentForReview(); err != nil {
		return err
	}
	if !a2DifficultyPattern.MatchString(string(q.Difficulty)) {
		return invalidQuestionError("预估难度必须是0.00到1.00之间的两位小数")
	}
	difficulty, _ := strconv.ParseFloat(string(q.Difficulty), 64)
	if math.Abs(difficulty*20-math.Round(difficulty*20)) > 1e-9 {
		return invalidQuestionError("预估难度必须是0.05的倍数")
	}
	if _, ok := allowedA2CognitiveLevels[q.CognitiveLevel]; !ok {
		return invalidQuestionError("认知层次不在受控范围内")
	}
	if q.ExamPoints == "" {
		return invalidQuestionError("考核要点不能为空")
	}
	seen := map[string]struct{}{}
	for _, point := range strings.Split(q.ExamPoints, "，") {
		if _, ok := allowedA2ExamPoints[point]; !ok {
			return invalidQuestionError("考核要点不在受控词表内：" + point)
		}
		if _, duplicated := seen[point]; duplicated {
			return invalidQuestionError("考核要点不得重复：" + point)
		}
		seen[point] = struct{}{}
	}
	if strings.TrimSpace(q.OutlineCode) == "" {
		return invalidQuestionError("大纲代码不能为空")
	}
	if strings.TrimSpace(q.Profession) == "" {
		return invalidQuestionError("专业不能为空")
	}
	if strings.TrimSpace(q.System) == "" {
		return invalidQuestionError("系统不能为空")
	}
	return nil
}

// ValidateForReview 保留 AI 生成/首次检查使用的专家格式兼容校验。
// 老师修订及其确认/可选审核流程仅使用 Validate 校验基本数据结构，
// 不将生成提示词中的措辞规范重新施加于老师已保存的正文。
func (q *A2Question) ValidateForReview() error {
	if err := q.Validate(); err != nil {
		return err
	}
	if q.FormatVersion == "" && strings.TrimSpace(q.OutlineCode) == "" &&
		strings.TrimSpace(q.CognitiveLevel) == "" &&
		strings.TrimSpace(q.ExamPoints) == "" &&
		strings.TrimSpace(q.Profession) == "" &&
		strings.TrimSpace(q.System) == "" {
		return nil
	}
	return q.ValidateA2ContentForReview()
}

func normalizeA2ExamPoints(value string) string {
	value = strings.ReplaceAll(value, ",", "，")
	parts := strings.Split(value, "，")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return strings.Join(result, "，")
}

func mentionsOptionExplanation(explanation, label string) bool {
	return strings.Contains(explanation, label+"项") || strings.Contains(explanation, label+"选项")
}

// normalizeSecondWaveStem 仅整理开头、标签与主诉引号，不增删病例事实或补写问题。
func normalizeSecondWaveStem(stem string) string {
	for _, marker := range secondWaveStemMarkers {
		stem = strings.ReplaceAll(stem, marker+"：", "")
		stem = strings.ReplaceAll(stem, marker+":", "")
	}
	if start := generatedPatientStartPattern.FindStringSubmatch(stem); start != nil {
		prefix := "患者，"
		age, _ := strconv.Atoi(start[3])
		if start[4] != "岁" || age < 18 {
			prefix = "患儿，"
		}
		stem = prefix + stem
	}
	start := secondWaveStemStartPattern.FindStringIndex(stem)
	if start != nil {
		ageEnd := strings.Index(stem[start[1]:], "。")
		if ageEnd >= 0 {
			chiefStart := start[1] + ageEnd + len("。")
			if end := strings.Index(stem[chiefStart:], "。"); end >= 0 {
				chiefEnd := chiefStart + end
				chief := strings.TrimSpace(stem[chiefStart:chiefEnd])
				if quoted := quotedChiefComplaintPattern.FindStringSubmatch(chief); quoted != nil {
					stem = stem[:chiefStart] + quoted[1] + stem[chiefEnd:]
				}
			}
		}
	}
	return stem
}

func (q *A2Question) validateSecondWaveFormat() error {
	start := secondWaveStemStartPattern.FindStringIndex(q.ClinicalStem)
	if start == nil {
		return invalidQuestionError("题干必须以“患者/患儿，男/女，年龄”开头")
	}
	for _, marker := range secondWaveStemMarkers {
		if strings.Contains(q.ClinicalStem, marker+"：") || strings.Contains(q.ClinicalStem, marker+":") {
			return invalidQuestionError("新题须直接叙述病例，不使用“" + marker + "”模板标签")
		}
	}
	if ageEnd := strings.Index(q.ClinicalStem[start[1]:], "。"); ageEnd >= 0 {
		chief := q.ClinicalStem[start[1]+ageEnd+len("。"):]
		if end := strings.Index(chief, "。"); end >= 0 {
			chief = chief[:end]
		}
		if strings.ContainsAny(chief, "“”‘’\"'") {
			return invalidQuestionError("主诉不应使用引号")
		}
	}
	tail := strings.TrimRight(q.ClinicalStem, "。！!？? \t\r\n")
	if last := strings.LastIndexAny(tail, "。！？!?\n"); last >= 0 {
		_, size := utf8.DecodeRuneInString(tail[last:])
		tail = strings.TrimSpace(tail[last+size:])
	}
	if !(strings.HasSuffix(tail, "是") || strings.HasSuffix(tail, "为")) {
		return invalidQuestionError("题干末尾必须有完整、明确的提问，并以“是”或“为”结束")
	}
	if strings.Contains(tail, "哪个") || strings.Contains(tail, "什么") {
		return invalidQuestionError("最后提问不得使用“哪个”或“什么”")
	}
	if IsContextualA2Format(q.FormatVersion) {
		// Clinical adequacy, duplicated qualifiers and language conventions need
		// context. They cannot be inferred from the presence of a few words (or
		// an English eponym such as Killip III) and turned into forced rejection.
		return nil
	}
	if strings.Contains(tail, "首选") && strings.Contains(tail, "治疗原则") {
		return invalidQuestionError("治疗原则不使用“首选”，请区分原则与具体治疗措施")
	}
	if strings.Contains(tail, "为明确诊断") && strings.Contains(tail, "确诊性") {
		return invalidQuestionError("检查目的表述重复，请明确一个检查目标")
	}
	if englishPhrasePattern.MatchString(q.ClinicalStem) {
		return invalidQuestionError("题干检查名称应使用中文，必要医学缩写可保留")
	}
	for _, opt := range q.Options {
		if englishPhrasePattern.MatchString(opt.Text) ||
			(strings.IndexFunc(opt.Text, func(r rune) bool { return unicode.Is(unicode.Han, r) || unicode.IsDigit(r) }) < 0 &&
				longEnglishWordPattern.MatchString(opt.Text) && strings.IndexFunc(opt.Text, unicode.IsLower) >= 0) {
			return invalidQuestionError("选项" + opt.Label + "应使用中文医学名称，必要医学缩写可保留")
		}
	}
	return nil
}
