package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	// 统一结构化日志：后台任务（生成/检查）以键值对字段记录 run/task/attempt 等
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if len(args) < 2 {
		printUsage()
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if len(args) > 1 && args[1] == "serve" && !cfg.AICheck.AutoEnabled {
		return fmt.Errorf("拒绝启动：AI 质量检查必须开启；题目仅允许在首次生成后自动检查一次")
	}
	if commandRequiresInferenceConfig(args[1]) {
		if err := cfg.ValidateInference(); err != nil {
			return err
		}
	}
	// 初始化 PostgreSQL 存储
	pgStore, err := postgres.New(cfg.DB.DSN)
	if err != nil {
		return fmt.Errorf("连接 PostgreSQL 失败: %w", err)
	}
	defer pgStore.Close()

	// 迁移是独立运维动作；其他命令只做版本检查，避免服务启动时静默改库。
	if args[1] == "migrate" {
		applied, err := pgStore.Migrate(ctx)
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			fmt.Printf("数据库 Schema 已是最新版本 %d，无需迁移\n", postgres.LatestSchemaVersion)
			return nil
		}
		for _, migration := range applied {
			fmt.Printf("已应用数据库迁移 %d: %s\n", migration.Version, migration.Name)
		}
		fmt.Printf("数据库 Schema 已升级到版本 %d\n", postgres.LatestSchemaVersion)
		return nil
	}
	if err := pgStore.CheckSchemaVersion(ctx); err != nil {
		return fmt.Errorf("数据库版本检查失败（请先执行 `go run ./cmd/aigo migrate`）: %w", err)
	}
	fmt.Println("数据库: PostgreSQL")
	// AI 配置密钥使用 JWT_SECRET 派生的 AES-GCM 密钥加密落库；JWT_SECRET 仍只
	// 通过部署 Secret/环境变量注入，不把 Qwen 凭证写回代码或配置模板。
	pgStore.SetLLMEncryptionKey(cfg.JWTSecret)
	if err := bootstrapAIProviderConfig(ctx, pgStore, cfg); err != nil {
		return fmt.Errorf("初始化 AI 服务配置失败: %w", err)
	}
	qwenResolver := databaseQwenResolver{
		store:              pgStore,
		generationFallback: cfg.Qwen,
		checkFallback:      cfg.AICheck.Client,
	}
	client := llm.NewDynamicQwenClient(qwenResolver, llm.PurposeGeneration, cfg.Qwen)

	// 初始化认证服务（JWT 密钥从配置读取，通过 JWT_SECRET 环境变量设置）
	authSvc := auth.NewService(pgStore.DB(), cfg.JWTSecret, 24*time.Hour)
	// 写入内置角色模板（超级管理员/管理员/审题老师/命题教师）
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
	// 送审强制前置：开启后仅 AI 检查通过（及人工回流状态）的题目可提交审核
	reviewSvc.RequireAICheck = true

	// 题库（分库）服务
	bankSvc := bank.NewService(pgStore, pgStore)

	llmInfo := client.Info()
	fmt.Printf("LLM: %s / %s\n", llmInfo.Deployment, llmInfo.Model)

	kpSvc := knowledge.NewService(pgStore)
	auditSvc := audit.NewService(pgStore)

	// 创建生成服务（完整模式）
	genSvc := generator.NewService(client)
	// genSvc.Brief = true  // 精简模式：解析限制200字，节省token

	// 批量任务复用单题生成 API：每道题独立调用 generator，结果和进度落库，
	// 不再绑定 Qwen 平台的 Files/Batches 批量协议；Web 与 CLI 共用同一执行器。
	var batchSvc batch.Executor = batch.NewLocalExecutor(genSvc, pgStore, pgStore, cfg.Qwen.Model)
	batchInfo := batchSvc.Capabilities()
	fmt.Printf("批量推理: %s / %s（可用=%v）\n", batchInfo.Backend, batchInfo.Model, batchInfo.Available)

	// 创建 AI 检查服务（使用 LLM 检查题目质量）。
	// 检查端点默认与实时推理共用；配置 AIGO_AICHECK_* 后可指向独立模型/端点
	// （如实时走云端、检查走自部署本地模型）。
	checkClient := llm.NewDynamicQwenClient(qwenResolver, llm.PurposeAICheck, cfg.AICheck.Client)
	aiCheckSvc := aicheck.NewService(checkClient, pgStore, pgStore, pgStore, cfg.AICheck.Client.Model)
	aiCheckSvc.SetAutoCheckEnabled(cfg.AICheck.AutoEnabled)
	aiCheckSvc.CheckTimeout = cfg.AICheck.Timeout
	aiCheckSvc.MaxAttempts = cfg.AICheck.MaxAttempts

	pipe := pipeline.New(genSvc, evaluator.NewService(), reviewSvc, kpSvc, pgStore)
	pipe.SetDraftChecker(aiCheckSvc)
	// 单题生成持久化任务：worker 执行时复用与原同步路径一致的题库归纳与审计行为
	pipe.SetBankAssigner(bankSvc)
	pipe.SetGenerationAuditor(auditSvc)

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
		addr := cfg.HTTPAddr
		if len(args) > 2 {
			addr = "127.0.0.1:" + args[2]
		}
		server := api.NewServer(pipe, kpSvc, reviewSvc, auditSvc, pgStore, authSvc, batchSvc, aiCheckSvc, bankSvc, cfg.CORSOrigins, cfg.RegisterEnabled, cfg.TrustProxyHeaders)
		handler := server.Handler()
		if cfg.WebDistDir != "" {
			handler, err = api.WithStaticFrontend(handler, cfg.WebDistDir)
			if err != nil {
				return fmt.Errorf("启用前端静态托管失败: %w", err)
			}
			fmt.Printf("前端静态资源: %s\n", cfg.WebDistDir)
		}
		fmt.Printf("AIgo HTTP 服务启动: http://%s\n", addr)

		// 启动 AI 检查后台 worker：执行生成/批量导入/编辑自动触发的质量检查
		workerCtx, stopWorkers := context.WithCancel(context.Background())
		defer stopWorkers()
		aiCheckSvc.StartWorkers(workerCtx, cfg.AICheck.Concurrency)
		// 启动单题生成后台 worker：消费 pending 命题运行（请求快照已落库，可恢复/重试）
		pipe.StartGenerationWorkers(workerCtx, 2)
		fmt.Printf("AI 质量检查: 自动触发=%v 并发=%d 模型=%s 送审强制前置=%v\n",
			cfg.AICheck.AutoEnabled, cfg.AICheck.Concurrency, cfg.AICheck.Client.Model, cfg.ReviewRequireAI)

		fmt.Println("API 文档:")
		fmt.Println("  GET    /health/live                 进程存活检查")
		fmt.Println("  GET    /health/ready                数据库与关键Schema就绪检查")
		fmt.Println("  GET    /api/stats                    统计信息")
		fmt.Println("  POST   /api/questions/generate        生成题目")
		fmt.Println("  GET    /api/generation-runs/{id}       查询命题运行状态")
		fmt.Println("  GET    /api/questions                  列出题目")
		fmt.Println("  GET    /api/questions/{id}             题目详情")
		fmt.Println("  GET    /api/knowledge-points           列出知识点")
		fmt.Println("  GET    /api/knowledge-points/search?q= 搜索知识点")
		fmt.Println("  POST   /api/knowledge-points/import    导入知识点(文件上传)")
		fmt.Println("  POST   /api/review/submit              提交审核")
		fmt.Println("  POST   /api/review/action              执行审核")
		fmt.Println("  GET    /api/review/task/{id}           审核任务详情")

		srv := &http.Server{
			Addr:              addr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,  // 防慢速头攻击
			ReadTimeout:       60 * time.Second,  // 读请求体超时
			WriteTimeout:      20 * time.Minute,  // 写响应超时（AI 生成耗时较长）
			IdleTimeout:       120 * time.Second, // 空闲连接超时
		}

		// 优雅关闭：收到 SIGINT/SIGTERM 后停止接收新请求，等现有请求完成
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sigCh
			fmt.Println("收到退出信号，正在优雅关闭...")
			stopWorkers()
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

	case "ai-check":
		return fmt.Errorf("AI 检查只允许在题目首次生成后由系统自动执行一次，不支持人工补查或复检")

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

	// ===== 批量推理（逐题调用单题生成 API）=====
	case "batch-run":
		// 提交批量任务到当前执行器
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
				p, err := kpSvc.ResolveForGeneration(ctx, "", id, "")
				if err != nil {
					p, err = kpSvc.ResolveForGeneration(ctx, "", "", id)
				}
				if err != nil {
					return err
				}
				if len(points) > 0 && points[0].VersionID != p.VersionID {
					return fmt.Errorf("一次批量任务只能使用一个大纲版本")
				}
				points = append(points, *p)
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
			existing := domain.ExistingKnowledgeKeys(questions)
			var filtered []domain.KnowledgePoint
			for _, p := range points {
				if !existing[domain.KnowledgePointKey(p)] {
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

		batchInfo := batchSvc.Capabilities()
		if !batchInfo.Available {
			return fmt.Errorf("批量生成不可用: %s", batchInfo.Message)
		}
		fmt.Printf("批量执行器: %s / %s\n", batchInfo.Backend, batchInfo.Model)
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
		fmt.Printf("\n%s。\n", batchInfo.Message)
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

		// 新路径按 AIgo job_id 导入；仍兼容历史 output_file_id 参数。
		id := args[2]
		job, statusErr := batchSvc.GetJobStatus(ctx, id)
		if statusErr == nil {
			fmt.Printf("任务状态: %s\n", job.Status)
		}

		// 获取知识点列表
		allKPs, err := kpSvc.ListAll(ctx)
		if err != nil {
			return err
		}

		if statusErr != nil {
			return statusErr
		}
		result, err := batchSvc.ImportResults(ctx, id, allKPs)
		if err != nil {
			return err
		}
		fmt.Printf("导入完成：成功入库 %d 道题目，失败请求 %d 个\n", result.Saved, result.Failed)
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
		fmt.Println("batch-run 使用当前部署配置的批量执行器。")
		fmt.Println("详见：go run ./cmd/aigo help")
		return nil

	case "batch-urls":
		batchInfo := batchSvc.Capabilities()
		fmt.Printf("批量执行器: %s / %s @ %s（可用=%v）\n", batchInfo.Backend, batchInfo.Model, batchInfo.Endpoint, batchInfo.Available)
		return nil

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

func commandRequiresInferenceConfig(command string) bool {
	switch command {
	case "serve", "doctor", "generate", "generate-all", "batch-run":
		return true
	default:
		return false
	}
}

// databaseQwenResolver 将数据库中的活动配置适配成 llm 所需的实时配置。
// 批量推理逐题复用实时生成配置，不再有独立的批量端点。
type databaseQwenResolver struct {
	store              storage.AIProviderConfigStore
	generationFallback llm.QwenConfig
	checkFallback      llm.QwenConfig
}

func (r databaseQwenResolver) ResolveQwenConfig(ctx context.Context, purpose llm.ConfigPurpose) (llm.QwenConfig, bool, error) {
	fallback := r.generationFallback
	if purpose == llm.PurposeAICheck {
		fallback = r.checkFallback
	}
	if r.store == nil {
		return fallback, false, nil
	}
	config, err := r.store.GetActiveAIProviderConfig(ctx)
	if err != nil {
		return fallback, false, err
	}
	if config == nil {
		return fallback, false, nil
	}
	model := config.GenerationModel
	baseURL := config.BaseURL
	apiKey := config.APIKey
	if purpose == llm.PurposeAICheck {
		model = config.CheckModel
	}
	if strings.TrimSpace(model) == "" {
		model = fallback.Model
	}
	return llm.QwenConfig{
		Deployment:     llm.DeploymentMode(config.Deployment),
		APIKey:         apiKey,
		BaseURL:        baseURL,
		Model:          model,
		EnableThinking: fallback.EnableThinking,
		HTTPTimeout:    fallback.HTTPTimeout,
	}, true, nil
}

// bootstrapAIProviderConfig 只在数据库还没有任何手动配置时把旧环境配置导入
// 一次。导入后实际请求优先使用数据库配置，保留环境变量仅作为兼容回退。
func bootstrapAIProviderConfig(ctx context.Context, store storage.AIProviderConfigStore, cfg config.Config) error {
	configs, err := store.ListAIProviderConfigs(ctx)
	if err != nil {
		return err
	}
	if len(configs) > 0 {
		return nil
	}
	// Config.Load 会提供云端默认地址和模型，因此额外判断原始环境变量，避免
	// 没有任何凭证/部署参数的新服务器自动生成一条空的伪配置。
	explicitEnv := strings.TrimSpace(cfg.Qwen.APIKey) != "" ||
		strings.TrimSpace(os.Getenv("QWEN_DEPLOYMENT")) != "" ||
		strings.TrimSpace(os.Getenv("QWEN_BASE_URL")) != "" ||
		strings.TrimSpace(os.Getenv("QWEN_MODEL")) != ""
	if !explicitEnv {
		return nil
	}
	checkModel := strings.TrimSpace(cfg.AICheck.Client.Model)
	if checkModel == "" {
		checkModel = cfg.Qwen.Model
	}
	return store.SaveAIProviderConfig(ctx, domain.AIProviderConfig{
		ID:              "qwen-env-bootstrap",
		Name:            "默认千问配置",
		Deployment:      string(cfg.Qwen.Deployment),
		BaseURL:         cfg.Qwen.BaseURL,
		APIKey:          cfg.Qwen.APIKey,
		GenerationModel: cfg.Qwen.Model,
		CheckModel:      checkModel,
		Active:          true,
		Source:          "env-bootstrap",
	})
}

func printUsage() {
	fmt.Print(`AIgo - AI医学A2型试题生成与评估系统

基础命令:
  go run ./cmd/aigo migrate                       将数据库迁移到当前程序版本
  go run ./cmd/aigo doctor                        检查框架状态
  go run ./cmd/aigo import <file.xlsx>            从 xlsx 导入题目
  go run ./cmd/aigo list                          列出题库摘要
  go run ./cmd/aigo show <题目ID>                 查看题目详情
  go run ./cmd/aigo generate                      调 API 生成新题(需 Key)
  go run ./cmd/aigo evaluate                      评估最后一道题
  go run ./cmd/aigo eval-id <题目ID>              评估指定题目

AI 质量检查（LLM 评分；serve 模式下生成/导入/编辑后自动执行）:

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

批量推理（逐题调用单题生成 API，任务落本地库，不使用云端批量协议）:
  go run ./cmd/aigo batch-run [选项]              提交批量任务
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

Environment:
	QWEN_DEPLOYMENT     实时推理部署方式: cloud（默认）/ local
	QWEN_BASE_URL       实时 OpenAI-compatible base URL
	QWEN_MODEL          云端模型 ID 或本地 served-model-name
	QWEN_API_KEY        云端/自建网关实时推理凭证
	QWEN_LOCAL_API_KEY  本地实时端点独立凭证；无鉴权时可留空
	DASHSCOPE_API_KEY   百炼凭证（云端实时、云端批量）
	QWEN_BATCH_BACKEND  auto（默认）/ dashscope / local（CLI兼容）/ disabled
		QWEN_BATCH_API_KEY  独立百炼 Batch 凭证（可选）
		AIGO_HTTP_ADDR       HTTP 监听地址（默认 127.0.0.1:8080）
		AIGO_WEB_DIST_DIR    Vite 生产构建目录（空=仅提供 API）
		AIGO_ENV_FILE        外部 dotenv 路径（-=禁用 dotenv）
`)
}
