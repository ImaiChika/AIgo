package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"aigo/internal/aicheck"
	"aigo/internal/api"
	"aigo/internal/audit"
	"aigo/internal/auth"
	"aigo/internal/bank"
	"aigo/internal/batch"
	"aigo/internal/config"
	"aigo/internal/domain"
	"aigo/internal/evaluator"
	"aigo/internal/generator"
	"aigo/internal/image"
	"aigo/internal/knowledge"
	"aigo/internal/llm"
	"aigo/internal/pipeline"
	"aigo/internal/review"
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

	// 初始化 PostgreSQL 存储
	pgStore, err := postgres.New(cfg.DB.DSN)
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
	fmt.Println("数据库: PostgreSQL")

	// 初始化认证服务（JWT 密钥从配置读取，通过 JWT_SECRET 环境变量设置）
	authSvc := auth.NewService(pgStore.DB(), cfg.JWTSecret, 24*time.Hour)
	// 写入内置角色模板（管理员/审题专家/命题教师，可自由修改）
	if err := authSvc.InitBuiltinRoles(ctx); err != nil {
		fmt.Printf("初始化内置角色失败: %v\n", err)
	}
	adminPassword := os.Getenv("AIGO_ADMIN_PASSWORD")
	created, usedPassword, err := authSvc.InitAdmin("admin", adminPassword, "系统管理员")
	if err != nil {
		fmt.Printf("初始化管理员账号失败: %v\n", err)
	} else if created {
		if usedPassword == adminPassword && len(adminPassword) >= 8 {
			fmt.Printf("默认管理员: admin（密码来自 AIGO_ADMIN_PASSWORD）\n")
		} else {
			fmt.Printf("⚠⚠ 已创建默认管理员 admin，请立即保存并修改密码：%s\n", usedPassword)
		}
	}
	// 审核服务：依赖认证服务解析审核人（审题权限 + 题库范围）
	reviewSvc := review.NewService(pgStore, pgStore, pgStore, authSvc)

	// 题库（分库）服务
	bankSvc := bank.NewService(pgStore, pgStore)

	// 生图服务：始终用真实生图器，无 API Key 时调用会报错
	zimgGen := image.NewZImageGenerator(image.ZImageConfig{
		APIKey:    cfg.Qwen.APIKey,
		OutputDir: "output/images",
	})
	imageSvc := image.NewService(pgStore, pgStore, client, zimgGen)
	fmt.Println("生图: z-image-turbo")
	fmt.Printf("LLM: %s @ %s\n", cfg.Qwen.Model, cfg.Qwen.BaseURL)

	kpSvc := knowledge.NewService(pgStore)
	auditSvc := audit.NewService(pgStore)

	// 创建生成服务（完整模式）
	genSvc := generator.NewService(client)
	// genSvc.Brief = true  // 精简模式：解析限制200字，节省token

	// 创建批量推理服务（DashScope 批量 API，云端执行）
	batchSvc := batch.NewService(cfg.Qwen.APIKey, cfg.Qwen.BaseURL, pgStore, pgStore)

	// 创建 AI 检查服务（使用 LLM 检查题目质量）
	aiCheckSvc := aicheck.NewService(client, pgStore, pgStore, cfg.Qwen.Model)

	pipe := pipeline.New(genSvc, evaluator.NewService(), reviewSvc, imageSvc, kpSvc, pgStore)

	// 自动加载审核流程配置（仅在数据库为空时导入，避免覆盖管理员在 UI 上的修改）
	existingFlows, _ := reviewSvc.ListFlows(ctx)
	if len(existingFlows) == 0 {
		if _, err := os.Stat("configs/review_flows.json"); err == nil {
			if n, err := reviewSvc.LoadFlowsFromFile(ctx, "configs/review_flows.json"); err != nil {
				fmt.Printf("加载审核流程失败: %v\n", err)
			} else if n > 0 {
				fmt.Printf("已加载 %d 个审核流程\n", n)
			}
		}
	}

	// 同步有审题权限的用户到专家库
	if err := authSvc.SyncReviewUsers(ctx, pgStore); err != nil {
		fmt.Printf("同步审题用户到专家库失败: %v\n", err)
	}

	switch args[1] {
	case "doctor":
		return pipe.Doctor(ctx)

	// ===== 批量生成和导出 =====
	case "generate-all":
		countPerPoint := 1
		if len(args) > 2 {
			fmt.Sscanf(args[2], "%d", &countPerPoint)
		}
		total, err := pipe.GenerateAll(ctx, countPerPoint)
		if err != nil {
			return err
		}
		fmt.Printf("成功生成 %d 道题目\n", total)
		return nil

	case "export-xlsx":
		path := "output/题目.xlsx"
		if len(args) > 2 {
			path = args[2]
		}
		return pipe.ExportXlsx(ctx, path)

	case "export-docx":
		path := "output/题目.docx"
		if len(args) > 2 {
			path = args[2]
		}
		return pipe.ExportDocx(ctx, path)

	case "clear-questions":
		return pipe.ClearQuestions(ctx)

	case "clear-kp":
		return pipe.ClearKnowledgePoints(ctx)

	case "serve":
		if cfg.IsDefaultJWTSecret() {
			return fmt.Errorf("拒绝启动：请通过 JWT_SECRET 环境变量设置强密钥（当前为默认密钥 %q，存在被接管风险）", config.DefaultJWTSecret)
		}
		port := "8080"
		if len(args) > 2 {
			port = args[2]
		}
		server := api.NewServer(pipe, kpSvc, imageSvc, reviewSvc, auditSvc, pgStore, authSvc, batchSvc, aiCheckSvc, bankSvc, cfg.CORSOrigins, cfg.RegisterEnabled)
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

		srv := &http.Server{
			Addr:              addr,
			Handler:           server.Handler(),
			ReadHeaderTimeout: 10 * time.Second,  // 防慢速头攻击
			ReadTimeout:       60 * time.Second,  // 读请求体超时
			WriteTimeout:      20 * time.Minute,  // 写响应超时（AI 生成/生图耗时长）
			IdleTimeout:       120 * time.Second, // 空闲连接超时
		}

		// 优雅关闭：收到 SIGINT/SIGTERM 后停止接收新请求，等现有请求完成
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sigCh
			fmt.Println("收到退出信号，正在优雅关闭...")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				fmt.Printf("优雅关闭超时: %v\n", err)
			}
		}()

		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return err
		}
		fmt.Println("服务已关闭")
		return nil

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
			ID:      args[2],
			Name:    args[3],
			Enabled: true,
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

	// ===== 批量推理（DashScope 批量 API，云端执行）=====
	case "batch-run":
		// 提交批量任务到 DashScope 云端
		// 用法: aigo batch-run [选项]
		//   --limit N          只处理前 N 个知识点
		//   --skip-existing    跳过已有题目的知识点
		//   --ids id1,id2      只处理指定知识点
		//   --count N          每个知识点生成几道题（默认1）
		//   --from CODE        起始大纲代码（含）
		//   --to CODE          结束大纲代码（含）
		limit := 0
		skipExisting := true
		ids := ""
		countPerPoint := 1
		fromCode := ""
		toCode := ""

		for i := 2; i < len(args); i++ {
			switch args[i] {
			case "--limit":
				if i+1 < len(args) {
					fmt.Sscanf(args[i+1], "%d", &limit)
					i++
				}
			case "--no-skip":
				skipExisting = false
			case "--ids":
				if i+1 < len(args) {
					ids = args[i+1]
					i++
				}
			case "--count":
				if i+1 < len(args) {
					fmt.Sscanf(args[i+1], "%d", &countPerPoint)
					i++
				}
			case "--from":
				if i+1 < len(args) {
					fromCode = args[i+1]
					i++
				}
			case "--to":
				if i+1 < len(args) {
					toCode = args[i+1]
					i++
				}
			}
		}

		// 获取知识点
		var points []domain.KnowledgePoint
		if ids != "" {
			for _, id := range strings.Split(ids, ",") {
				id = strings.TrimSpace(id)
				if p, err := kpSvc.GetByID(ctx, id); err == nil && p != nil {
					points = append(points, *p)
				}
			}
		} else {
			all, err := kpSvc.ListAll(ctx)
			if err != nil {
				return err
			}
			points = all
		}

		// 跳过已有题目
		if skipExisting {
			questions, _ := pgStore.ListQuestions(ctx)
			existing := make(map[string]bool)
			for _, q := range questions {
				if q.OutlineCode != "" {
					existing[q.OutlineCode] = true
				}
			}
			var filtered []domain.KnowledgePoint
			for _, p := range points {
				if !existing[p.OutlineCode] {
					filtered = append(filtered, p)
				}
			}
			fmt.Printf("跳过已有题目: %d → %d 个知识点\n", len(points), len(filtered))
			points = filtered
		}

		// 按范围筛选
		if fromCode != "" || toCode != "" {
			var filtered []domain.KnowledgePoint
			for _, kp := range points {
				if fromCode != "" && kp.OutlineCode < fromCode {
					continue
				}
				if toCode != "" && kp.OutlineCode > toCode {
					continue
				}
				filtered = append(filtered, kp)
			}
			points = filtered
		}

		// 限制数量
		if limit > 0 && limit < len(points) {
			points = points[:limit]
		}

		if len(points) == 0 {
			return fmt.Errorf("没有符合条件的知识点")
		}

		fmt.Printf("DashScope 批量 API: %s\n", batchSvc.GetBaseURLs())
		fmt.Printf("知识点: %d 个, 每点 %d 题\n", len(points), countPerPoint)
		fmt.Println("正在提交任务...")

		// 提交任务
		jobID, count, err := batchSvc.GenerateAndSubmit(ctx, points, countPerPoint, "")
		if err != nil {
			return err
		}

		fmt.Printf("\n任务已提交！\n")
		fmt.Printf("job_id: %s\n", jobID)
		fmt.Printf("请求数: %d\n", count)
		fmt.Println("\n任务在 DashScope 云端执行，关闭电脑不影响。")
		fmt.Println("使用以下命令查看状态和下载结果：")
		fmt.Printf("  go run ./cmd/aigo batch-status %s\n", jobID)
		fmt.Printf("  go run ./cmd/aigo batch-download %s\n", jobID)
		return nil

	case "batch-status":
		// 查询批量任务状态
		// 用法: aigo batch-status <job_id>
		if len(args) < 3 {
			return fmt.Errorf("用法: aigo batch-status <job_id>")
		}
		job, err := batchSvc.GetJobStatus(ctx, args[2])
		if err != nil {
			return err
		}
		fmt.Printf("任务ID: %s\n", job.JobID)
		fmt.Printf("状态: %s\n", job.Status)
		fmt.Printf("总数: %d, 成功: %d, 失败: %d\n", job.TotalCount, job.Completed, job.Failed)
		if job.OutputFileID != "" {
			fmt.Printf("结果文件ID: %s\n", job.OutputFileID)
		}
		if job.Error != "" {
			fmt.Printf("错误: %s\n", job.Error)
		}
		return nil

	case "batch-download":
		// 下载批量任务结果并导入题库
		// 用法: aigo batch-download <job_id 或 output_file_id>
		if len(args) < 3 {
			return fmt.Errorf("用法: aigo batch-download <job_id 或 output_file_id>")
		}

		// 判断是 job_id 还是 output_file_id
		id := args[2]
		var outputFileID string

		// 先尝试作为 job_id 查询
		job, err := batchSvc.GetJobStatus(ctx, id)
		if err == nil && job.OutputFileID != "" {
			outputFileID = job.OutputFileID
			fmt.Printf("任务状态: %s\n", job.Status)
			if job.Status != "completed" && job.Status != "complete" {
				return fmt.Errorf("任务尚未完成，当前状态: %s", job.Status)
			}
		} else {
			// 直接作为 output_file_id 使用
			outputFileID = id
		}

		// 获取知识点列表
		allKPs, err := kpSvc.ListAll(ctx)
		if err != nil {
			return err
		}

		result, err := batchSvc.DownloadAndImport(ctx, outputFileID, allKPs)
		if err != nil {
			return err
		}
		fmt.Printf("已导入 %d 道题目（成功 %d, 失败 %d）\n", result.Saved+result.Failed, result.Saved, result.Failed)
		for _, item := range result.Items {
			if item.Status == "ok" {
				fmt.Printf("  ✅ %s: 导入 %d 题\n", item.OutlineCode, item.Count)
			} else {
				fmt.Printf("  ❌ %s: %s\n", item.OutlineCode, item.Error)
			}
		}
		return nil

	// ===== 旧版批量推理（已废弃，请使用 batch-run）=====
	case "batch":
		fmt.Println("提示：旧版 batch 命令已废弃，请使用 batch-run 命令：")
		fmt.Println("  go run ./cmd/aigo batch-run [选项]")
		fmt.Println("")
		fmt.Println("batch-run 使用 DashScope 批量 API，云端执行，不会超时。")
		fmt.Println("详见：go run ./cmd/aigo help")
		return nil

	case "batch-urls":
		fmt.Printf("DashScope 批量 API: %s\n", batchSvc.GetBaseURLs())
		return nil

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
		q, err := pgStore.GetQuestion(ctx, args[2])
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

批量生成与导出:
  go run ./cmd/aigo generate-all [每知识点题数]    遍历所有知识点生成题目(默认1)
  go run ./cmd/aigo export-xlsx [路径]            导出题库为 xlsx (默认 output/题目.xlsx)
  go run ./cmd/aigo export-docx [路径]            导出题库为 docx (默认 output/题目.docx)
  go run ./cmd/aigo clear-questions               清空题库
  go run ./cmd/aigo clear-kp                      清空知识点

知识点管理:
  go run ./cmd/aigo kp-import <file.xlsx>         导入考试大纲知识点
  go run ./cmd/aigo kp-list [系统名]              列出知识点(可选按系统筛选)
  go run ./cmd/aigo kp-search <关键词>            搜索知识点
  go run ./cmd/aigo kp-stats                      知识点统计

批量推理（batch.dashscope 域名，5折优惠，同步等待）:
  go run ./cmd/aigo batch [选项]                  批量生成题目
    --limit N          只处理前 N 个知识点
    --skip-existing    跳过已有题目的知识点
    --ids id1,id2      只处理指定知识点
    --count N          每个知识点生成几道题（默认1）
  go run ./cmd/aigo batch-urls                    显示批量推理 URL

批量推理（DashScope 批量 API，云端执行，5折优惠）:
  go run ./cmd/aigo batch-run [选项]              提交批量任务到云端
    --limit N          只处理前 N 个知识点
    --skip-existing    跳过已有题目的知识点（默认开启）
    --ids id1,id2      只处理指定知识点
    --count N          每个知识点生成几道题（默认1）
    --from CODE        起始大纲代码（含）
    --to CODE          结束大纲代码（含）
  go run ./cmd/aigo batch-status <job_id>         查询任务状态
  go run ./cmd/aigo batch-download <job_id>       下载结果并导入题库

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
  QWEN_BASE_URL       DashScope API 地址 (默认 https://dashscope.aliyuncs.com/api/v1)
  QWEN_MODEL          模型名称 (默认 qwen3.5-flash)
`)
}
