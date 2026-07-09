package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"aigo/internal/api"
	"aigo/internal/auth"
	"aigo/internal/config"
	"aigo/internal/domain"
	"aigo/internal/evaluator"
	"aigo/internal/generator"
	"aigo/internal/image"
	"aigo/internal/knowledge"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
	"aigo/internal/review"
	"aigo/internal/storage"
	"aigo/internal/storage/postgres"

	_ "github.com/lib/pq"
)

func main() {
	if err := run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) < 2 {
		printUsage()
		return nil
	}

	cfg := config.FromEnv()
	client := llm.NewQwenClient(cfg.Qwen)

	// 初始化存储
	var questionStore storage.QuestionStore
	var expertStore storage.ExpertStore
	var reviewStore storage.ReviewStore
	var imageStore storage.ImageStore
	var kpStore storage.KnowledgeStore
	var pgStore *postgres.Store

	if cfg.DB.Driver == "postgres" {
		var err error
		pgStore, err = postgres.New(cfg.DB.DSN)
		if err != nil {
			return fmt.Errorf("连接 PostgreSQL 失败: %w", err)
		}
		defer pgStore.Close()

		// 初始化表结构
		schemaBytes, err := os.ReadFile("internal/storage/postgres/schema.sql")
		if err != nil {
			return fmt.Errorf("读取 schema 文件失败: %w", err)
		}
		if err := pgStore.InitSchema(string(schemaBytes)); err != nil {
			return fmt.Errorf("初始化数据库表失败: %w", err)
		}

		questionStore = pgStore
		expertStore = pgStore
		reviewStore = pgStore
		imageStore = pgStore
		kpStore = pgStore
		fmt.Println("数据库: PostgreSQL")
	} else {
		questionStore = storage.NewMemoryStore()
		expertStore = storage.NewMemoryExpertStore()
		reviewStore = storage.NewMemoryReviewStore()
		imageStore = storage.NewMemoryImageStore()
		kpStore = storage.NewMemoryKnowledgeStore()
		fmt.Println("数据库: 内存存储（重启丢失）")
	}

	reviewSvc := review.NewService(expertStore, reviewStore, questionStore)
	mockImgGen := image.NewMockImageGenerator("output/images")
	imageSvc := image.NewService(questionStore, imageStore, client, mockImgGen)
	kpSvc := knowledge.NewService(kpStore)
	pipe := pipeline.New(generator.NewService(client), evaluator.NewService(), reviewSvc, imageSvc, kpSvc, questionStore)

	// 初始化认证服务
	var authSvc *auth.Service
	if pgStore != nil {
		authSvc = auth.NewService(pgStore.DB(), "aigo-jwt-secret-2025", 24*time.Hour)
		if err := authSvc.InitAdmin("admin", "admin123", "系统管理员"); err != nil {
			fmt.Printf("初始化管理员账号失败: %v\n", err)
		} else {
			fmt.Println("默认管理员: admin / admin123")
		}
	}

	switch args[1] {
	case "doctor":
		return pipe.Doctor(ctx)

	case "serve":
		port := "8080"
		if len(args) > 2 {
			port = args[2]
		}
		server := api.NewServer(pipe, kpSvc, imageSvc, reviewSvc, questionStore, authSvc)
		addr := "127.0.0.1:" + port
		fmt.Printf("AIgo HTTP 服务启动: http://%s\n", addr)
		fmt.Println("API 文档:")
		fmt.Println("  GET    /api/stats                    统计信息")
		fmt.Println("  POST   /api/questions/generate        生成题目")
		fmt.Println("  GET    /api/questions                  列出题目")
		fmt.Println("  GET    /api/questions/{id}             题目详情")
		fmt.Println("  GET    /api/knowledge-points           列出知识点")
		fmt.Println("  GET    /api/knowledge-points/search?q= 搜索知识点")
		fmt.Println("  POST   /api/knowledge-points/import    导入知识点(文件上传)")
		fmt.Println("  POST   /api/images/prompt              生成生图提示词")
		fmt.Println("  POST   /api/images/generate            生成候选图")
		fmt.Println("  GET    /api/images/{questionId}        列出候选图")
		fmt.Println("  POST   /api/images/review              审核图片")
		fmt.Println("  POST   /api/review/submit              提交审核")
		fmt.Println("  POST   /api/review/action              执行审核")
		fmt.Println("  GET    /api/review/task/{id}           审核任务详情")
		return http.ListenAndServe(addr, server.Handler())

	case "import":
		if len(args) < 3 {
			return fmt.Errorf("用法: aigo import <xlsx文件路径>")
		}
		return pipe.ImportXlsx(ctx, args[2])

	case "list":
		return pipe.ListQuestions(ctx)

	case "generate":
		return pipe.GenerateSample(ctx)

	case "evaluate":
		return pipe.EvaluateSample(ctx)

	case "eval-id":
		if len(args) < 3 {
			return fmt.Errorf("用法: aigo eval-id <题目ID>")
		}
		return pipe.EvaluateByID(ctx, args[2])

	// ===== 知识点管理 =====
	case "kp-import":
		if len(args) < 3 {
			return fmt.Errorf("用法: aigo kp-import <知识点xlsx文件路径>")
		}
		return pipe.ImportKnowledgePoints(ctx, args[2])

	case "kp-list":
		system := ""
		if len(args) > 2 {
			system = args[2]
		}
		return pipe.ListKnowledgePoints(ctx, system)

	case "kp-search":
		if len(args) < 3 {
			return fmt.Errorf("用法: aigo kp-search <关键词>")
		}
		return pipe.SearchKnowledgePoints(ctx, args[2])

	case "kp-stats":
		return pipe.KnowledgePointStats(ctx)

	// ===== 专家管理 =====
	case "expert-add":
		if len(args) < 4 {
			return fmt.Errorf(`用法: aigo expert-add <ID> <姓名> [科室] [职称]

示例:
  aigo expert-add E001 张三 内科 副主任医师`)
		}
		expert := domain.Expert{
			ID:         args[2],
			Name:       args[3],
			Enabled:    true,
		}
		if len(args) > 4 {
			expert.Department = args[4]
		}
		if len(args) > 5 {
			expert.Title = args[5]
		}
		if err := pipe.CreateExpert(ctx, expert); err != nil {
			return err
		}
		fmt.Printf("专家 %s (%s) 已添加\n", expert.ID, expert.Name)
		return nil

	case "expert-list":
		return pipe.ListExperts(ctx)

	// ===== 审核流程 =====
	case "flow-load":
		if len(args) < 3 {
			return fmt.Errorf("用法: aigo flow-load <配置文件路径>\n示例: aigo flow-load configs/review_flows.json")
		}
		return pipe.LoadFlows(ctx, args[2])

	case "flow-list":
		return pipe.ListFlows(ctx)

	// ===== 审核操作 =====
	case "submit":
		if len(args) < 4 {
			return fmt.Errorf("用法: aigo submit <题目ID> <流程ID>")
		}
		return pipe.SubmitQuestion(ctx, args[2], args[3])

	case "review":
		if len(args) < 5 {
			return fmt.Errorf(`用法: aigo review <任务ID> <专家ID> <动作> [意见]

动作: approved / rejected / revision_required

示例:
  aigo review task-xxx E001 approved "题目质量良好"
  aigo review task-xxx E001 revision_required "题干需要补充病史"`)
		}
		req := review.ReviewRequest{
			TaskID:   args[2],
			ExpertID: args[3],
			Action:   domain.QuestionStatus(args[4]),
		}
		if len(args) > 5 {
			req.Opinion = args[5]
		}
		if err := pipe.Review(ctx, req); err != nil {
			return err
		}
		fmt.Printf("审核完成: 任务=%s 专家=%s 结论=%s\n", req.TaskID, req.ExpertID, req.Action)
		return nil

	case "task":
		if len(args) < 3 {
			return fmt.Errorf("用法: aigo task <任务ID>")
		}
		return pipe.GetReviewTask(ctx, args[2])

	case "task-by-question":
		if len(args) < 3 {
			return fmt.Errorf("用法: aigo task-by-question <题目ID>")
		}
		return pipe.GetReviewTaskByQuestion(ctx, args[2])

	case "records":
		if len(args) < 3 {
			return fmt.Errorf("用法: aigo records <任务ID>")
		}
		return pipe.ListReviewRecords(ctx, args[2])

	case "publish":
		if len(args) < 3 {
			return fmt.Errorf("用法: aigo publish <题目ID>")
		}
		return pipe.PublishQuestion(ctx, args[2])

	// ===== 图片配图 =====
	case "img-prompt":
		if len(args) < 3 {
			return fmt.Errorf("用法: aigo img-prompt <题目ID>")
		}
		return pipe.GenerateImagePrompt(ctx, args[2])

	case "img-generate":
		if len(args) < 3 {
			return fmt.Errorf("用法: aigo img-generate <题目ID> [数量3-5]")
		}
		count := 3
		if len(args) > 3 {
			fmt.Sscanf(args[3], "%d", &count)
		}
		return pipe.GenerateImages(ctx, args[2], count)

	case "img-list":
		if len(args) < 3 {
			return fmt.Errorf("用法: aigo img-list <题目ID>")
		}
		return pipe.ListImages(ctx, args[2])

	case "img-review":
		if len(args) < 5 {
			return fmt.Errorf(`用法: aigo img-review <图片ID> <专家ID> <approved|rejected> [意见]`)
		}
		return pipe.ReviewImage(ctx, args[2], args[3], domain.ImageStatus(args[4]), func() string {
			if len(args) > 5 {
				return args[5]
			}
			return ""
		}())

	case "img-show":
		if len(args) < 3 {
			return fmt.Errorf("用法: aigo img-show <题目ID>")
		}
		return pipe.GetImagePrompt(ctx, args[2])

	case "show":
		if len(args) < 3 {
			return fmt.Errorf("用法: aigo show <题目ID>")
		}
		q, err := questionStore.GetQuestion(ctx, args[2])
		if err != nil {
			return err
		}
		if q == nil {
			return fmt.Errorf("未找到题目 %s", args[2])
		}
		encoded, _ := json.MarshalIndent(q, "", "  ")
		fmt.Println(string(encoded))
		return nil

	default:
		printUsage()
		return fmt.Errorf("unknown command %q", args[1])
	}
}

func printUsage() {
	fmt.Print(`AIgo - AI医学A2型试题生成与评估系统

基础命令:
  go run ./cmd/aigo doctor                        检查框架状态
  go run ./cmd/aigo import <file.xlsx>            从 xlsx 导入题目
  go run ./cmd/aigo list                          列出题库摘要
  go run ./cmd/aigo show <题目ID>                 查看题目详情
  go run ./cmd/aigo generate                      调 API 生成新题(需 Key)
  go run ./cmd/aigo evaluate                      评估最后一道题
  go run ./cmd/aigo eval-id <题目ID>              评估指定题目

知识点管理:
  go run ./cmd/aigo kp-import <file.xlsx>         导入知识点
  go run ./cmd/aigo kp-list [系统名]              列出知识点(可选按系统筛选)
  go run ./cmd/aigo kp-search <关键词>            搜索知识点
  go run ./cmd/aigo kp-stats                      知识点统计

专家管理:
  go run ./cmd/aigo expert-add <ID> <姓名> [科室] [职称]  添加专家
  go run ./cmd/aigo expert-list                           列出专家

审核流程:
  go run ./cmd/aigo flow-load <配置文件>           从JSON文件加载审核流程
  go run ./cmd/aigo flow-list                     列出审核流程

审核操作:
  go run ./cmd/aigo submit <题目ID> <流程ID>      提交题目到审核流程
  go run ./cmd/aigo review <任务ID> <专家ID> <动作> [意见]
  go run ./cmd/aigo task <任务ID>                 查看审核任务
  go run ./cmd/aigo task-by-question <题目ID>     按题目查审核任务
  go run ./cmd/aigo records <任务ID>              查看审核记录
  go run ./cmd/aigo publish <题目ID>              发布审核通过的题目

图片配图:
  go run ./cmd/aigo img-prompt <题目ID>           为题目生成结构化生图提示词(需Key)
  go run ./cmd/aigo img-generate <题目ID> [数量]   生成候选图(3-5张)
  go run ./cmd/aigo img-list <题目ID>             查看候选图列表
  go run ./cmd/aigo img-review <图片ID> <专家ID> <approved|rejected> [意见]
  go run ./cmd/aigo img-show <题目ID>             查看题目生图提示词

Environment:
  DASHSCOPE_API_KEY   阿里云百炼/千问 API Key
  QWEN_BASE_URL       OpenAI兼容接口Base URL
  QWEN_MODEL          默认 qwen-plus
`)
}
