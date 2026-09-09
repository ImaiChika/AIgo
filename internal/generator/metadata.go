package generator

import (
	"strings"

	"aigo/internal/domain"
)

var clinicalProfessionBySystem = map[string]string{
	"一、呼吸系统":          "呼吸",
	"二、心血管系统":         "心血管",
	"三、消化系统":          "消化",
	"四、泌尿系统（含男性生殖系统）": "泌尿",
	"五、女性生殖系统":        "妇产科",
	"六、血液系统":          "血液",
	"七、代谢、内分泌系统":      "内分泌",
	"八、精神、神经系统":       "精神，神经",
	"九、运动系统":          "骨科",
	"十、风湿免疫性疾病":       "风湿免疫",
	"十一、儿科疾病":         "儿科",
	"十二、传染病、性传播疾病":    "传染",
	"十三、其他":           "临床",
}

// QuestionMetadataForKnowledgePoint 根据考试大纲字段确定题目的专业和系统。
// requestedSubject 只有在调用方显式传入了不同于大纲系统的专业时才优先使用。
func QuestionMetadataForKnowledgePoint(kp domain.KnowledgePoint, requestedSubject string) (profession, system string) {
	subject := strings.TrimSpace(kp.Subject)
	category := strings.TrimSpace(kp.Category)
	requestedSubject = strings.TrimSpace(requestedSubject)

	if category == "基础医学" {
		return subject, category
	}
	system = subject
	profession = clinicalProfessionBySystem[subject]
	if profession == "" {
		profession = subject
	}
	if requestedSubject != "" && requestedSubject != subject && requestedSubject != "临床医学" {
		profession = requestedSubject
	}
	return profession, system
}
