// Package exporter 提供题目导出功能。
// 支持导出为 xlsx 和 docx 格式。
// 格式要求来自《试题命制要求》（二）格式要求。
package exporter

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"aigo/internal/domain"
)

// ExportToDocx 将题目列表导出为 Word 文档（.docx 格式）。
// 使用 pandoc 将 HTML 转换为真正的 docx 格式。
func ExportToDocx(questions []domain.A2Question, path string) error {
	// 先生成 HTML
	htmlPath := strings.TrimSuffix(path, ".docx") + ".html"
	if err := generateHTML(questions, htmlPath); err != nil {
		return fmt.Errorf("生成 HTML 失败: %w", err)
	}
	defer os.Remove(htmlPath) // 转换后删除临时 HTML

	// 用 pandoc 转换为 docx
	cmd := exec.Command("pandoc", htmlPath, "-o", path, "--from=html", "--to=docx")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pandoc 转换失败: %s %w", string(output), err)
	}

	return nil
}

// generateHTML 生成 HTML 文件。
func generateHTML(questions []domain.A2Question, path string) error {
	var b strings.Builder

	b.WriteString(`<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<style>
body {
  font-family: 宋体, SimSun, serif;
  font-size: 10.5pt;
  line-height: 1.8;
}
h1 {
  font-size: 16pt;
  text-align: center;
  margin-bottom: 20px;
}
.question {
  margin-bottom: 24px;
  page-break-inside: avoid;
}
.stem {
  margin-bottom: 10px;
}
.option {
  margin-left: 24px;
  margin-bottom: 4px;
}
.answer {
  font-weight: bold;
  margin-top: 8px;
}
.explanation {
  margin-top: 8px;
  color: #333;
}
.meta {
  font-size: 9pt;
  color: #666;
  margin-top: 8px;
  border-top: 1px solid #eee;
  padding-top: 6px;
}
</style>
</head>
<body>
`)
	b.WriteString("<h1>国家执业医师考试 A2 型试题</h1>\n")
	b.WriteString(fmt.Sprintf("<p>共 %d 道题</p>\n", len(questions)))

	for i, q := range questions {
		b.WriteString("<div class='question'>\n")
		b.WriteString(fmt.Sprintf("<p><strong>%d．</strong></p>\n", i+1))
		b.WriteString(fmt.Sprintf("<div class='stem'>%s</div>\n", escapeHTML(q.ClinicalStem)))

		for _, opt := range q.Options {
			b.WriteString(fmt.Sprintf("<div class='option'>%s．%s</div>\n", opt.Label, escapeHTML(opt.Text)))
		}

		b.WriteString(fmt.Sprintf("<div class='answer'>答案：%s</div>\n", q.Answer))

		if q.Explanation != "" {
			b.WriteString(fmt.Sprintf("<div class='explanation'>解析：%s</div>\n", escapeHTML(q.Explanation)))
		}

		b.WriteString("<div class='meta'>")
		if q.OutlineCode != "" {
			b.WriteString(fmt.Sprintf("大纲代码：%s | ", q.OutlineCode))
		}
		if q.Difficulty != "" {
			b.WriteString(fmt.Sprintf("预估难度：%s | ", q.Difficulty))
		}
		if q.CognitiveLevel != "" {
			b.WriteString(fmt.Sprintf("认知层次：%s | ", q.CognitiveLevel))
		}
		if q.ExamPoints != "" {
			b.WriteString(fmt.Sprintf("考核要点：%s | ", q.ExamPoints))
		}
		if q.Profession != "" {
			b.WriteString(fmt.Sprintf("专业：%s | ", q.Profession))
		}
		if q.System != "" {
			b.WriteString(fmt.Sprintf("系统：%s", q.System))
		}
		b.WriteString("</div>\n")
		b.WriteString("</div>\n")
	}

	b.WriteString("</body></html>")

	return os.WriteFile(path, []byte(b.String()), 0644)
}

// escapeHTML 转义 HTML 特殊字符。
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
