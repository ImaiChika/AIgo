package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"aigo/internal/domain"
	"aigo/internal/evaluator"
	"aigo/internal/generator"
	"aigo/internal/image"
	"aigo/internal/importer"
	"aigo/internal/knowledge"
	"aigo/internal/review"
	"aigo/internal/storage"
)

type Pipeline struct {
	generator *generator.Service
	evaluator *evaluator.Service
	review    *review.Service
	imageSvc  *image.Service
	kpSvc     *knowledge.Service
	store     storage.QuestionStore
}

func New(generator *generator.Service, evaluator *evaluator.Service, review *review.Service, imageSvc *image.Service, kpSvc *knowledge.Service, store storage.QuestionStore) *Pipeline {
	return &Pipeline{
		generator: generator,
		evaluator: evaluator,
		review:    review,
		imageSvc:  imageSvc,
		kpSvc:     kpSvc,
		store:     store,
	}
}

func (p *Pipeline) Doctor(ctx context.Context) error {
	fmt.Println("framework: ok")
	fmt.Println("language: go")
	fmt.Println("llm: qwen openai-compatible text-only client")
	fmt.Println("multimodal: interfaces reserved; current cli path is text-only")
	return nil
}

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
	return questions, nil
}

func (p *Pipeline) GenerateSample(ctx context.Context) error {
	req := domain.GenerationRequest{
		Subject:    "临床医学",
		Difficulty: domain.DifficultyMedium,
		KnowledgePoints: []domain.KnowledgePoint{{
			ID:      "demo-kp-001",
			Subject: "临床医学",
			Topic:   "常见症状鉴别诊断",
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
	p.store.SaveQuestion(ctx, q)

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

	count, err := p.store.SaveQuestions(ctx, questions)
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
	p.store.SaveQuestion(ctx, *q)

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
	count, err := p.kpSvc.ImportFromXlsx(ctx, path)
	if err != nil {
		return err
	}
	total, _ := p.kpSvc.Count(ctx)
	fmt.Printf("导入 %d 个知识点，当前总量 %d\n", count, total)
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
		fmt.Printf("  %s | %s | %s | %s\n", pt.ID, pt.System, pt.Topic, kw)
	}
	return nil
}

// ListKnowledgePoints 列出知识点（可选按系统筛选）。
func (p *Pipeline) ListKnowledgePoints(ctx context.Context, system string) error {
	var points []domain.KnowledgePoint
	var err error
	if system != "" {
		points, err = p.kpSvc.ListBySystem(ctx, system)
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
		fmt.Printf("  %s | %s | %s | %s\n", pt.ID, pt.System, pt.Topic, kw)
	}
	return nil
}

// KnowledgePointStats 知识点统计。
func (p *Pipeline) KnowledgePointStats(ctx context.Context) error {
	systems, err := p.kpSvc.ListSystems(ctx)
	if err != nil {
		return err
	}
	total, _ := p.kpSvc.Count(ctx)
	fmt.Printf("知识点总量: %d\n", total)
	fmt.Printf("\n各系统分布:\n")
	for sys, count := range systems {
		fmt.Printf("  %s: %d\n", sys, count)
	}
	return nil
}

// ===== 图片相关 =====

// GenerateImagePrompt 为题目生成结构化生图提示词。
func (p *Pipeline) GenerateImagePrompt(ctx context.Context, questionID string) error {
	prompt, err := p.imageSvc.GeneratePrompt(ctx, questionID)
	if err != nil {
		return err
	}
	fmt.Printf("已为题目 %s 生成生图提示词\n", questionID)
	return printJSON(prompt)
}

// GenerateImages 为题目生成候选图。
func (p *Pipeline) GenerateImages(ctx context.Context, questionID string, count int) error {
	images, err := p.imageSvc.GenerateImages(ctx, questionID, count)
	if err != nil {
		return err
	}
	fmt.Printf("已为题目 %s 生成 %d 张候选图\n", questionID, len(images))
	return printJSON(images)
}

// ListImages 列出题目的候选图。
func (p *Pipeline) ListImages(ctx context.Context, questionID string) error {
	images, err := p.imageSvc.ListImages(ctx, questionID)
	if err != nil {
		return err
	}
	if len(images) == 0 {
		fmt.Printf("题目 %s 暂无候选图\n", questionID)
		return nil
	}
	fmt.Printf("题目 %s 共 %d 张候选图:\n", questionID, len(images))
	for _, img := range images {
		fmt.Printf("  %s | %s | 状态:%s | 路径:%s\n", img.ID, img.ModelName, img.Status, img.ImagePath)
	}
	return nil
}

// ReviewImage 审核图片。
func (p *Pipeline) ReviewImage(ctx context.Context, imageID string, expertID string, action domain.ImageStatus, opinion string) error {
	return p.imageSvc.ReviewImage(ctx, imageID, expertID, action, opinion)
}

// GetImagePrompt 获取题目的生图提示词。
func (p *Pipeline) GetImagePrompt(ctx context.Context, questionID string) error {
	prompt, err := p.imageSvc.GetPrompt(ctx, questionID)
	if err != nil {
		return err
	}
	if prompt == nil {
		fmt.Printf("题目 %s 尚未生成提示词\n", questionID)
		return nil
	}
	return printJSON(prompt)
}

func printJSON(value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}
