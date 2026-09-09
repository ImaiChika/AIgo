package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"aigo/internal/domain"
	"aigo/internal/evaluator"
	"aigo/internal/exporter"
	"aigo/internal/generator"
	"aigo/internal/importer"
	"aigo/internal/knowledge"
	"aigo/internal/review"
	"aigo/internal/storage"
)

// DraftChecker 草稿自动检查接口，由 aicheck.Service 实现（CheckAsync 入队后台检查）。
// 通过 SetDraftChecker 注入，使 pipeline 与检查实现解耦：CLI 路径不注入即不触发。
type DraftChecker interface {
	CheckAsync(questionIDs ...string)
}

type Pipeline struct {
	generator *generator.Service
	evaluator *evaluator.Service
	review    *review.Service
	kpSvc     *knowledge.Service
	store     storage.QuestionStore
	checker   DraftChecker
}

func New(generator *generator.Service, evaluator *evaluator.Service, review *review.Service, kpSvc *knowledge.Service, store storage.QuestionStore) *Pipeline {
	return &Pipeline{
		generator: generator,
		evaluator: evaluator,
		review:    review,
		kpSvc:     kpSvc,
		store:     store,
	}
}

func (p *Pipeline) Doctor(ctx context.Context) error {
	fmt.Println("framework: ok")
	fmt.Println("language: go")
	fmt.Println("llm: qwen openai-compatible text-only client (cloud/local selected by deployment config)")
	fmt.Println("question format: text-only A2 single-choice")
	return nil
}

// SetDraftChecker 注入草稿自动检查器（serve 模式装配，见 cmd/aigo/main.go）。
func (p *Pipeline) SetDraftChecker(c DraftChecker) { p.checker = c }

// Generate 直接调用生成服务，供 API 使用。
func (p *Pipeline) Generate(ctx context.Context, req domain.GenerationRequest) ([]domain.A2Question, error) {
	questions, err := p.generator.Generate(ctx, req)
	if err != nil {
		return nil, err
	}
	for i := range questions {
		if err := questions[i].Validate(); err != nil {
			questions[i].SourceRefs = append(questions[i].SourceRefs, domain.SourceRef{
				Title: "校验未通过",
				Note:  err.Error(),
			})
		}
		if err := p.store.SaveQuestion(ctx, questions[i]); err != nil {
			return nil, err
		}
	}
	// 草稿落库后自动提交 AI 质量检查（后台异步执行，不阻塞生成响应）
	if p.checker != nil && len(questions) > 0 {
		ids := make([]string, 0, len(questions))
		for _, q := range questions {
			ids = append(ids, q.ID)
		}
		p.checker.CheckAsync(ids...)
	}
	return questions, nil
}

func (p *Pipeline) GenerateSample(ctx context.Context) error {
	req := domain.GenerationRequest{
		Subject:    "临床医学",
		Difficulty: domain.DifficultyMedium,
		KnowledgePoints: []domain.KnowledgePoint{{
			ID:          "demo-kp-001",
			Category:    "临床综合",
			Subject:     "临床医学",
			Topic:       "常见症状鉴别诊断",
			OutlineCode: "demo-kp-001",
		}},
		Count: 1,
	}

	questions, err := p.generator.Generate(ctx, req)
	if err != nil {
		return fmt.Errorf("生成失败: %w", err)
	}

	for i := range questions {
		if err := questions[i].Validate(); err != nil {
			questions[i].SourceRefs = append(questions[i].SourceRefs, domain.SourceRef{
				Title: "校验未通过",
				Note:  err.Error(),
			})
		}
		if err := p.store.SaveQuestion(ctx, questions[i]); err != nil {
			return fmt.Errorf("存储失败: %w", err)
		}
	}

	return printJSON(questions)
}

func (p *Pipeline) EvaluateSample(ctx context.Context) error {
	questions, err := p.store.ListQuestions(ctx)
	if err != nil {
		return err
	}
	if len(questions) == 0 {
		return fmt.Errorf("题库为空，请先运行 generate 或 import 导入题目")
	}

	q := questions[len(questions)-1]
	report := p.evaluator.Evaluate(q)

	// 评估后更新状态为 auto_checked
	q.Status = domain.StatusAutoChecked
	q.UpdatedAt = time.Now()
	if err := p.store.SaveQuestion(ctx, q); err != nil {
		return fmt.Errorf("保存评估结果失败: %w", err)
	}

	return printJSON(report)
}

// ImportXlsx 从 xlsx 文件导入题目到题库。
func (p *Pipeline) ImportXlsx(ctx context.Context, path string) error {
	rows, err := importer.ReadXlsx(path, true)
	if err != nil {
		return fmt.Errorf("读取xlsx失败: %w", err)
	}
	fmt.Printf("读取到 %d 行原始数据\n", len(rows))

	questions, errs := importer.ConvertToQuestions(rows)
	if len(errs) > 0 {
		fmt.Printf("转换警告 (%d 条):\n", len(errs))
		for _, e := range errs {
			fmt.Printf("  - %v\n", e)
		}
	}
	fmt.Printf("有效题目: %d 道\n", len(questions))

	importCtx := storage.WithQuestionChange(ctx, storage.QuestionChange{Actor: "import", ChangeType: "excel_import", ChangeNote: "Excel批量导入题目"})
	count, err := p.store.SaveQuestions(importCtx, questions)
	if err != nil {
		return fmt.Errorf("批量存储失败: %w", err)
	}
	fmt.Printf("成功导入 %d 道题目到题库\n", count)

	total, _ := p.store.Count(ctx)
	fmt.Printf("题库当前总量: %d 道\n", total)
	return nil
}

// ListQuestions 列出题库中的所有题目摘要。
func (p *Pipeline) ListQuestions(ctx context.Context) error {
	questions, err := p.store.ListQuestions(ctx)
	if err != nil {
		return err
	}
	if len(questions) == 0 {
		fmt.Println("题库为空")
		return nil
	}
	fmt.Printf("题库共 %d 道题目:\n", len(questions))
	for i, q := range questions {
		stem := q.ClinicalStem
		if len([]rune(stem)) > 50 {
			stem = string([]rune(stem)[:50]) + "..."
		}
		fmt.Printf("  [%d] ID=%s | 状态:%s | 答案:%s | 选项:%d个\n", i+1, q.ID, q.Status, q.Answer, len(q.Options))
	}
	return nil
}

// EvaluateByID 根据 ID 评估指定题目。
func (p *Pipeline) EvaluateByID(ctx context.Context, id string) error {
	q, err := p.store.GetQuestion(ctx, id)
	if err != nil {
		return err
	}
	if q == nil {
		return fmt.Errorf("未找到 ID=%s 的题目", id)
	}
	report := p.evaluator.Evaluate(*q)

	q.Status = domain.StatusAutoChecked
	q.UpdatedAt = time.Now()
	if err := p.store.SaveQuestion(ctx, *q); err != nil {
		return fmt.Errorf("保存评估结果失败: %w", err)
	}

	return printJSON(report)
}

// ===== 审核相关 =====

// CreateExpert 创建专家。
func (p *Pipeline) CreateExpert(ctx context.Context, expert domain.Expert) error {
	return p.review.CreateExpert(ctx, expert)
}

// ListExperts 列出所有专家。
func (p *Pipeline) ListExperts(ctx context.Context) error {
	experts, err := p.review.ListExperts(ctx)
	if err != nil {
		return err
	}
	if len(experts) == 0 {
		fmt.Println("专家库为空")
		return nil
	}
	fmt.Printf("专家库共 %d 位专家:\n", len(experts))
	for _, e := range experts {
		status := "启用"
		if !e.Enabled {
			status = "停用"
		}
		fmt.Printf("  ID=%s | %s | %s %s | %s\n", e.ID, e.Name, e.Department, e.Title, status)
	}
	return nil
}

// CreateFlow 创建审核流程。
func (p *Pipeline) CreateFlow(ctx context.Context, flow domain.ReviewFlowConfig) error {
	return p.review.CreateFlow(ctx, flow)
}

// ListFlows 列出审核流程。
func (p *Pipeline) ListFlows(ctx context.Context) error {
	flows, err := p.review.ListFlows(ctx)
	if err != nil {
		return err
	}
	if len(flows) == 0 {
		fmt.Println("暂无审核流程配置")
		return nil
	}
	for _, f := range flows {
		fmt.Printf("流程: %s (ID=%s)\n", f.Name, f.ID)
		if f.Description != "" {
			fmt.Printf("  说明: %s\n", f.Description)
		}
		for _, r := range f.Rounds {
			required := r.RequiredCount
			if required == 0 {
				required = len(r.ExpertIDs)
			}
			fmt.Printf("  第%d轮: %s | 审核人:%v | 需通过:%d/%d | 可修改:%v\n",
				r.RoundNumber, r.Name, r.ExpertIDs, required, len(r.ExpertIDs), r.CanModify)
		}
	}
	return nil
}

// LoadFlows 从 JSON 文件加载审核流程配置。
func (p *Pipeline) LoadFlows(ctx context.Context, path string) error {
	count, err := p.review.LoadFlowsFromFile(ctx, path)
	if err != nil {
		return err
	}
	fmt.Printf("从 %s 加载了 %d 个审核流程\n", path, count)
	return nil
}

// SubmitQuestion 提交题目到审核流程。
func (p *Pipeline) SubmitQuestion(ctx context.Context, questionID string, flowID string) error {
	task, err := p.review.SubmitQuestion(ctx, questionID, flowID)
	if err != nil {
		return err
	}
	fmt.Printf("题目 %s 已提交到审核流程 %s\n", questionID, flowID)
	return printJSON(task)
}

// Review 执行审核。
func (p *Pipeline) Review(ctx context.Context, req review.ReviewRequest) error {
	return p.review.Review(ctx, req)
}

// GetReviewTask 查看审核任务。
func (p *Pipeline) GetReviewTask(ctx context.Context, taskID string) error {
	task, err := p.review.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("审核任务 %s 不存在", taskID)
	}
	return printJSON(task)
}

// GetReviewTaskByQuestion 根据题目 ID 查看审核任务。
func (p *Pipeline) GetReviewTaskByQuestion(ctx context.Context, questionID string) error {
	task, err := p.review.GetTaskByQuestionID(ctx, questionID)
	if err != nil {
		return err
	}
	if task == nil {
		return fmt.Errorf("题目 %s 没有审核任务", questionID)
	}
	return printJSON(task)
}

// ListReviewRecords 查看审核记录。
func (p *Pipeline) ListReviewRecords(ctx context.Context, taskID string) error {
	records, err := p.review.ListRecords(ctx, taskID)
	if err != nil {
		return err
	}
	if len(records) == 0 {
		fmt.Println("暂无审核记录")
		return nil
	}
	fmt.Printf("审核记录共 %d 条:\n", len(records))
	for _, r := range records {
		fmt.Printf("  第%d轮 | 审核人:%s | 结论:%s | 意见:%s\n", r.RoundNumber, r.ExpertID, r.Conclusion, r.Opinion)
	}
	return nil
}

// PublishQuestion 发布审核通过的题目。
func (p *Pipeline) PublishQuestion(ctx context.Context, questionID string) error {
	return p.review.PublishQuestion(ctx, questionID)
}

// ===== 知识点相关 =====

// ImportKnowledgePoints 从 xlsx 导入知识点。
func (p *Pipeline) ImportKnowledgePoints(ctx context.Context, path string) error {
	inserted, updated, duplicated, err := p.kpSvc.ImportFromXlsx(ctx, path)
	if err != nil {
		return err
	}
	total, _ := p.kpSvc.Count(ctx)
	fmt.Printf("导入完成：新增 %d，更新 %d，跳过重复 %d；当前总量 %d\n", inserted, updated, duplicated, total)
	return nil
}

// SearchKnowledgePoints 搜索知识点。
func (p *Pipeline) SearchKnowledgePoints(ctx context.Context, keyword string) error {
	points, err := p.kpSvc.Search(ctx, keyword)
	if err != nil {
		return err
	}
	if len(points) == 0 {
		fmt.Printf("未找到匹配 %q 的知识点\n", keyword)
		return nil
	}
	fmt.Printf("搜索 %q 结果 %d 条:\n", keyword, len(points))
	for _, pt := range points {
		kw := ""
		if len(pt.Keywords) > 0 {
			kw = pt.Keywords[0]
		}
		fmt.Printf("  %s | %s | %s | %s\n", pt.ID, pt.Subject, pt.Topic, kw)
	}
	return nil
}

// ListKnowledgePoints 列出知识点（可选按系统筛选）。
func (p *Pipeline) ListKnowledgePoints(ctx context.Context, system string) error {
	var points []domain.KnowledgePoint
	var err error
	if system != "" {
		points, err = p.kpSvc.ListBySubject(ctx, system)
	} else {
		points, err = p.kpSvc.ListAll(ctx)
	}
	if err != nil {
		return err
	}
	if len(points) == 0 {
		fmt.Println("知识点库为空")
		return nil
	}
	fmt.Printf("知识点共 %d 条:\n", len(points))
	for i, pt := range points {
		if i >= 20 {
			fmt.Printf("  ... 还有 %d 条\n", len(points)-20)
			break
		}
		kw := ""
		if len(pt.Keywords) > 0 {
			kw = pt.Keywords[0]
		}
		fmt.Printf("  %s | %s | %s | %s\n", pt.ID, pt.Subject, pt.Topic, kw)
	}
	return nil
}

// KnowledgePointStats 知识点统计。
func (p *Pipeline) KnowledgePointStats(ctx context.Context) error {
	categories, err := p.kpSvc.ListCategories(ctx)
	if err != nil {
		return err
	}
	total, _ := p.kpSvc.Count(ctx)
	fmt.Printf("知识点总量: %d\n", total)
	fmt.Printf("\n各分类分布:\n")
	for cat, count := range categories {
		fmt.Printf("  %s: %d\n", cat, count)
	}
	return nil
}

// ===== 批量生成 =====

// GenerateAll 遍历所有知识点，逐个调用千问生成题目。
// 生成过程中会打印进度信息。返回生成的题目总数。
func (p *Pipeline) GenerateAll(ctx context.Context, countPerPoint int) (int, error) {
	// 获取所有知识点
	points, err := p.kpSvc.ListAll(ctx)
	if err != nil {
		return 0, fmt.Errorf("获取知识点列表失败: %w", err)
	}
	if len(points) == 0 {
		return 0, fmt.Errorf("知识点库为空，请先导入考试大纲")
	}

	fmt.Printf("共 %d 个知识点，每个生成 %d 道题\n", len(points), countPerPoint)

	total := 0
	failed := 0
	for i, kp := range points {
		// 打印进度
		if (i+1)%50 == 0 || i == 0 {
			fmt.Printf("[%d/%d] 正在为 %s 生成题目...\n", i+1, len(points), kp.Topic)
		}

		req := domain.GenerationRequest{
			Subject:         kp.Subject,
			Difficulty:      domain.Difficulty("0.65"),
			KnowledgePoints: []domain.KnowledgePoint{kp},
			Count:           countPerPoint,
		}

		questions, err := p.generator.Generate(ctx, req)
		if err != nil {
			failed++
			if failed <= 10 {
				fmt.Printf("  ⚠ 生成失败 (%s): %v\n", kp.OutlineCode, err)
			}
			continue
		}

		// 保存到数据库
		for _, q := range questions {
			if err := q.Validate(); err != nil {
				q.SourceRefs = append(q.SourceRefs, domain.SourceRef{
					Title: "校验未通过",
					Note:  err.Error(),
				})
			}
			if err := p.store.SaveQuestion(ctx, q); err != nil {
				fmt.Printf("  ⚠ 保存失败: %v\n", err)
				continue
			}
			total++
		}

		// 避免 API 限流，每 10 个请求暂停 1 秒
		if (i+1)%10 == 0 {
			time.Sleep(1 * time.Second)
		}
	}

	fmt.Printf("\n生成完成！成功 %d 道，失败 %d 个知识点\n", total, failed)
	return total, nil
}

// ExportXlsx 将题库中所有题目导出为 xlsx 文件。
func (p *Pipeline) ExportXlsx(ctx context.Context, path string) error {
	questions, err := p.store.ListQuestions(ctx)
	if err != nil {
		return fmt.Errorf("获取题目列表失败: %w", err)
	}
	if len(questions) == 0 {
		return fmt.Errorf("题库为空")
	}

	if err := importer.ExportToXlsx(questions, path); err != nil {
		return fmt.Errorf("导出 xlsx 失败: %w", err)
	}
	fmt.Printf("已导出 %d 道题目到 %s\n", len(questions), path)
	return nil
}

// ExportDocx 将题库中所有题目导出为 Word 文档。
func (p *Pipeline) ExportDocx(ctx context.Context, path string) error {
	questions, err := p.store.ListQuestions(ctx)
	if err != nil {
		return fmt.Errorf("获取题目列表失败: %w", err)
	}
	if len(questions) == 0 {
		return fmt.Errorf("题库为空")
	}

	if err := exporter.ExportToDocx(questions, path); err != nil {
		return fmt.Errorf("导出 docx 失败: %w", err)
	}
	fmt.Printf("已导出 %d 道题目到 %s\n", len(questions), path)
	return nil
}

// ClearQuestions 清空题库中所有题目。
func (p *Pipeline) ClearQuestions(ctx context.Context) error {
	questions, err := p.store.ListQuestions(ctx)
	if err != nil {
		return err
	}
	for _, q := range questions {
		if err := p.store.DeleteQuestion(ctx, q.ID); err != nil {
			return err
		}
	}
	fmt.Printf("已清空 %d 道题目\n", len(questions))
	return nil
}

// ClearKnowledgePoints 清空所有知识点。
func (p *Pipeline) ClearKnowledgePoints(ctx context.Context) error {
	points, err := p.kpSvc.ListAll(ctx)
	if err != nil {
		return err
	}
	for _, p2 := range points {
		if err := p.kpSvc.DeletePoint(ctx, p2.ID); err != nil {
			return err
		}
	}
	fmt.Printf("已清空 %d 个知识点\n", len(points))
	return nil
}

func printJSON(value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}
