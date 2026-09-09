package exporter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aigo/internal/domain"
)

func TestGenerateHTMLUsesExpertLabels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "questions.html")
	question := domain.A2Question{
		ClinicalStem:   "男，45岁。腹痛1天。最可能的诊断是",
		Options:        []domain.Option{{Label: "A", Text: "胃溃疡"}},
		Answer:         "A",
		Explanation:    "正确答案为 A。故选 A。",
		OutlineCode:    "110.4.3.1.1",
		Difficulty:     "0.65",
		CognitiveLevel: "简单应用",
		ExamPoints:     "诊断与鉴别诊断",
		Profession:     "消化",
		System:         "消化系统",
	}
	if err := generateHTML([]domain.A2Question{question}, path); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	html := string(content)
	for _, want := range []string{
		"<strong>1．</strong>男，45岁",
		"A．胃溃疡",
		"说明：正确答案为 A",
		"大纲代码：110.4.3.1.1<br>",
		"命题人：朝阳医院AI",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
}
