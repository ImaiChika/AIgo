// 题目答案验证脚本
// 随机抽取指定比例的题目，调用当前配置的千问云端或本地端点验证答案。
// 用法: go run ./cmd/verify [选项]
//
// 选项:
//
//	--percent N    抽取百分比（默认 10，即 10%）
//	--limit N      最多验证多少道（默认不限）
//	--output path  输出报告路径（默认 output/verify_report.json）
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"aigo/internal/config"
	"aigo/internal/domain"
	"aigo/internal/llm"
	"aigo/internal/storage/postgres"
)

// VerifyResult 单道题目的验证结果。
type VerifyResult struct {
	QuestionID    string `json:"question_id"`
	OutlineCode   string `json:"outline_code"`
	Stem          string `json:"stem"`           // 题干前100字
	CurrentAnswer string `json:"current_answer"` // 当前答案
	LLMAnswer     string `json:"llm_answer"`     // LLM 判断的答案
	IsCorrect     bool   `json:"is_correct"`     // 是否一致
	LLMReasoning  string `json:"llm_reasoning"`  // LLM 的判断理由
	VerifiedAt    string `json:"verified_at"`
}

// VerifyReport 验证报告（只记录错误题目）。
type VerifyReport struct {
	TotalVerified  int            `json:"total_verified"`
	CorrectCount   int            `json:"correct_count"`
	WrongCount     int            `json:"wrong_count"`
	UnknownCount   int            `json:"unknown_count"`   // 无法判断
	AccuracyRate   float64        `json:"accuracy_rate"`   // 正确率
	WrongQuestions []VerifyResult `json:"wrong_questions"` // 答案可能错误的题目
	GeneratedAt    string         `json:"generated_at"`
}

func main() {
	percent := flag.Int("percent", 10, "抽取百分比（1-100）")
	limit := flag.Int("limit", 0, "最多验证多少道（0=不限）")
	outputPath := flag.String("output", "output/verify_report.json", "输出报告路径")
	flag.Parse()

	if err := run(*percent, *limit, *outputPath); err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		os.Exit(1)
	}
}

func run(percent int, limit int, outputPath string) error {
	ctx := context.Background()

	// 加载配置
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("配置加载失败: %w", err)
	}
	if err := cfg.ValidateInference(); err != nil {
		return err
	}
	if cfg.Qwen.Deployment == llm.DeploymentCloud && cfg.Qwen.APIKey == "" {
		return fmt.Errorf("缺少 DASHSCOPE_API_KEY，请在 .env 文件中配置")
	}
	if cfg.Qwen.BaseURL == "" || cfg.Qwen.Model == "" {
		return fmt.Errorf("LLM 配置不完整：请设置 QWEN_BASE_URL 和 QWEN_MODEL")
	}

	// 连接数据库
	pgStore, err := postgres.New(cfg.DB.DSN)
	if err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}
	defer pgStore.Close()

	// 创建 LLM 客户端
	client := llm.NewQwenClient(cfg.Qwen)

	// 获取所有题目
	questions, err := pgStore.ListQuestions(ctx)
	if err != nil {
		return fmt.Errorf("获取题目失败: %w", err)
	}

	if len(questions) == 0 {
		return fmt.Errorf("题库为空")
	}

	// 计算抽样数量
	sampleCount := len(questions) * percent / 100
	if sampleCount < 1 {
		sampleCount = 1
	}
	if limit > 0 && sampleCount > limit {
		sampleCount = limit
	}

	// 随机抽样
	rand.Seed(time.Now().UnixNano())
	indices := rand.Perm(len(questions))
	if len(indices) > sampleCount {
		indices = indices[:sampleCount]
	}

	fmt.Printf("题库总量: %d 道\n", len(questions))
	fmt.Printf("抽样比例: %d%%\n", percent)
	fmt.Printf("验证数量: %d 道\n", sampleCount)
	fmt.Printf("LLM: %s / %s @ %s\n", cfg.Qwen.Deployment, cfg.Qwen.Model, cfg.Qwen.BaseURL)
	fmt.Println()
	fmt.Println("开始验证...")

	// 逐题验证
	report := VerifyReport{
		WrongQuestions: make([]VerifyResult, 0),
		GeneratedAt:    time.Now().Format("2006-01-02 15:04:05"),
	}

	for i, idx := range indices {
		q := questions[idx]

		// 进度显示
		if (i+1)%10 == 0 || i == 0 {
			fmt.Printf("[%d/%d] 验证中...\n", i+1, sampleCount)
		}

		// 调用 LLM 验证
		result, err := verifyQuestion(ctx, client, q)
		if err != nil {
			fmt.Printf("  ⚠ 验证失败 (%s): %v\n", q.ID, err)
			report.UnknownCount++
			continue
		}

		report.TotalVerified++
		if result.IsCorrect {
			report.CorrectCount++
			fmt.Printf("  ✓ [%s] 答案正确\n", q.OutlineCode)
		} else {
			report.WrongCount++
			report.WrongQuestions = append(report.WrongQuestions, result)
			fmt.Printf("  ✗ [%s] 答案可能错误: %s → %s\n", q.OutlineCode, result.CurrentAnswer, result.LLMAnswer)
		}

		// 避免 API 限流
		if (i+1)%50 == 0 {
			time.Sleep(2 * time.Second)
		}
	}

	// 计算正确率
	if report.TotalVerified > 0 {
		report.AccuracyRate = float64(report.CorrectCount) / float64(report.TotalVerified) * 100
	}

	// 输出报告（只有存在错误题目时才保存）
	if report.WrongCount > 0 {
		if err := saveReport(report, outputPath); err != nil {
			return fmt.Errorf("保存报告失败: %w", err)
		}
	}

	// 打印摘要
	fmt.Println()
	fmt.Println("========== 验证结果 ==========")
	fmt.Printf("验证数量: %d 道\n", report.TotalVerified)
	fmt.Printf("答案正确: %d 道\n", report.CorrectCount)
	fmt.Printf("可能错误: %d 道\n", report.WrongCount)
	fmt.Printf("无法判断: %d 道\n", report.UnknownCount)
	fmt.Printf("正确率: %.1f%%\n", report.AccuracyRate)

	if report.WrongCount > 0 {
		fmt.Printf("\n报告已保存: %s（仅包含错误题目）\n", outputPath)
	} else {
		fmt.Println("\n全部正确，无需生成报告。")
	}

	return nil
}

// verifyQuestion 验证单道题目的答案是否正确。
func verifyQuestion(ctx context.Context, client llm.Client, q domain.A2Question) (VerifyResult, error) {
	result := VerifyResult{
		QuestionID:    q.ID,
		OutlineCode:   q.OutlineCode,
		CurrentAnswer: string(q.Answer),
		VerifiedAt:    time.Now().Format("2006-01-02 15:04:05"),
	}

	// 截取题干前200字
	stem := q.ClinicalStem
	if len([]rune(stem)) > 200 {
		stem = string([]rune(stem)[:200]) + "..."
	}
	result.Stem = stem

	// 构建选项文本
	var optionsText strings.Builder
	for _, opt := range q.Options {
		optionsText.WriteString(fmt.Sprintf("%s. %s\n", opt.Label, opt.Text))
	}

	// 构建验证 prompt
	prompt := fmt.Sprintf(`你是一位医学考试审题专家。请判断以下题目的答案是否正确。

【题目】
%s

【选项】
%s

【当前答案】%s

请分析：
1. 根据题目和选项，正确答案应该是哪个？
2. 当前答案是否正确？
3. 简要说明理由（50字以内）。

请严格按以下JSON格式输出，不要输出其他内容：
{"correct_answer": "A/B/C/D/E", "is_correct": true/false, "reason": "判断理由"}`,
		q.ClinicalStem, optionsText.String(), q.Answer)

	// 调用 LLM
	raw, err := client.Complete(ctx, []llm.Message{
		{Role: llm.RoleUser, Content: prompt},
	}, llm.GenerateOptions{Temperature: 0.1})
	if err != nil {
		return result, fmt.Errorf("LLM 调用失败: %w", err)
	}

	// 解析响应
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

	var resp struct {
		CorrectAnswer string `json:"correct_answer"`
		IsCorrect     bool   `json:"is_correct"`
		Reason        string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		return result, fmt.Errorf("解析响应失败: %w", err)
	}

	result.LLMAnswer = resp.CorrectAnswer
	result.IsCorrect = resp.IsCorrect
	result.LLMReasoning = resp.Reason

	return result, nil
}

// saveReport 保存验证报告。
func saveReport(report VerifyReport, path string) error {
	// 确保目录存在
	dir := strings.TrimSuffix(path, "/"+strings.Split(path, "/")[len(strings.Split(path, "/"))-1])
	if dir != "" {
		os.MkdirAll(dir, 0755)
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
