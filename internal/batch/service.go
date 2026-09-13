// Package batch 提供 DashScope 批量推理功能。
// 使用 OpenAI 兼容格式的批量 API。
// 参考文档：https://platform.qianwenai.com/docs/developer-guides/text-generation/batch
package batch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aigo/internal/domain"
	"aigo/internal/generator"
	"aigo/internal/storage"
)

// Service 批量推理服务。
type Service struct {
	apiKey         string
	baseURL        string // https://dashscope.aliyuncs.com/compatible-mode/v1
	model          string // 云端批量模型，禁止在 JSONL 中硬编码
	profile        string // 脱敏端点配置档名称，用于审计
	enableThinking bool   // 是否为批量请求开启思考模式
	questionStore  storage.QuestionStore
	batchJobStore  storage.BatchJobStore // 批量任务持久化存储
	httpClient     *http.Client
}

// DashScopeConfig 配置百炼 Files + Batches 执行器。
type DashScopeConfig struct {
	APIKey         string
	BaseURL        string
	Model          string
	Profile        string
	EnableThinking bool
}

// NewService 保留旧构造函数兼容，默认使用 qwen3.5-flash 和思考模式。
func NewService(apiKey, baseURL string, questionStore storage.QuestionStore, batchJobStore storage.BatchJobStore) *Service {
	return NewDashScopeService(DashScopeConfig{
		APIKey:         apiKey,
		BaseURL:        baseURL,
		Model:          "qwen3.5-flash",
		Profile:        "dashscope-default",
		EnableThinking: true,
	}, questionStore, batchJobStore)
}

// NewDashScopeService 创建百炼批量执行器。
func NewDashScopeService(cfg DashScopeConfig, questionStore storage.QuestionStore, batchJobStore storage.BatchJobStore) *Service {
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = "qwen3.5-flash"
	}
	profile := strings.TrimSpace(cfg.Profile)
	if profile == "" {
		profile = "dashscope-default"
	}
	return &Service{
		apiKey:         strings.TrimSpace(cfg.APIKey),
		baseURL:        baseURL,
		model:          model,
		profile:        profile,
		enableThinking: cfg.EnableThinking,
		questionStore:  questionStore,
		batchJobStore:  batchJobStore,
		httpClient:     &http.Client{Timeout: 120 * time.Second},
	}
}

// Capabilities 返回脱敏后的执行器能力。
func (s *Service) Capabilities() Capabilities {
	available := s.apiKey != "" && s.baseURL != "" && s.model != ""
	message := "使用百炼 Files + Batches 异步执行，任务可在服务关闭后继续运行"
	if !available {
		message = "DashScope 批量执行器缺少 API Key、模型或端点配置"
	}
	return Capabilities{
		Backend:       "dashscope",
		Available:     available,
		ExecutionMode: "remote_file_batch",
		Model:         s.model,
		Message:       message,
		Endpoint:      s.baseURL,
	}
}

func (s *Service) ensureAvailable() error {
	info := s.Capabilities()
	if !info.Available {
		return fmt.Errorf("%w: %s", ErrUnavailable, info.Message)
	}
	return nil
}

// BatchJob 批量任务状态。
type BatchJob struct {
	JobID        string `json:"job_id"`
	OwnerID      string `json:"-"` // 提交任务的用户，仅服务端隔离使用
	Backend      string `json:"backend,omitempty"`
	Model        string `json:"model,omitempty"`
	JobName      string `json:"job_name"` // 自定义任务名称
	Status       string `json:"status"`   // validating/in_progress/finalizing/completed/expired/cancelling/cancelled
	TotalCount   int    `json:"total_count"`
	Completed    int    `json:"completed"`
	Failed       int    `json:"failed"`
	OutputFileID string `json:"output_file_id"`
	CreatedAt    int64  `json:"created_at"`
	Error        string `json:"error,omitempty"`
	// ImportedAt 非空表示结果已完成导入（导入幂等）；前端据此决定是否触发自动导入。
	ImportedAt string `json:"imported_at,omitempty"`
	// Tracked 表示该任务在本地 batch_jobs 有记录（导入幂等可用）。
	// 仅云端列表补齐的历史任务没有本地记录，结果多半早已按旧流程入库，
	// 前端不得对它们自动导入，否则会把历史结果重复写进题库。
	Tracked bool `json:"tracked,omitempty"`
}

// ImportResult 导入结果详情。
type ImportResult struct {
	Saved       int          `json:"saved"`        // 实际成功入库题目数
	Failed      int          `json:"failed"`       // 失败请求/JSONL 行数（不是题目数）
	Items       []ImportItem `json:"items"`        // 每条导入详情
	QuestionIDs []string     `json:"question_ids"` // 本次入库的题目 ID，用于触发 AI 自动检查
	Simulated   bool         `json:"simulated,omitempty"`
	Message     string       `json:"message,omitempty"`
}

// ImportItem 单条导入结果。
type ImportItem struct {
	OutlineCode string `json:"outline_code"` // 知识点代码
	Count       int    `json:"count"`        // 导入题目数
	Status      string `json:"status"`       // "ok" / "failed"
	Error       string `json:"error"`        // 失败原因
}

// GenerateAndSubmit 批量生成并提交到 DashScope。
// 返回 job_id，可用于后续查询状态和下载结果。
// jobName 为自定义任务名称，显示在 DashScope 控制台。
func (s *Service) GenerateAndSubmit(ctx context.Context, points []domain.KnowledgePoint, countPerPoint int, jobName string) (string, int, error) {
	if err := s.ensureAvailable(); err != nil {
		return "", 0, err
	}
	if len(points) == 0 {
		return "", 0, fmt.Errorf("知识点列表为空")
	}
	if countPerPoint <= 0 {
		countPerPoint = 1
	}

	// 1. 生成 JSONL 文件
	tmpDir := "output/batch_tmp"
	os.MkdirAll(tmpDir, 0755)
	jsonlPath := filepath.Join(tmpDir, fmt.Sprintf("batch_%d.jsonl", time.Now().UnixNano()))

	count, err := s.generateJSONL(points, countPerPoint, jsonlPath)
	if err != nil {
		return "", 0, fmt.Errorf("生成JSONL失败: %w", err)
	}

	// 2. 上传文件
	fmt.Println("正在上传文件到 DashScope...")
	fileID, err := s.uploadFile(ctx, jsonlPath)
	if err != nil {
		return "", 0, fmt.Errorf("上传文件失败: %w", err)
	}
	fmt.Printf("文件已上传, file_id: %s\n", fileID)

	// 3. 提交批量任务
	fmt.Println("正在提交批量任务...")
	jobID, err := s.submitJob(ctx, fileID, jobName)
	if err != nil {
		return "", 0, fmt.Errorf("提交任务失败: %w", err)
	}
	fmt.Printf("任务已提交, job_id: %s\n", jobID)

	// 4. 保存到数据库
	if s.batchJobStore != nil {
		pointsJSON, _ := json.Marshal(points)
		if err := s.batchJobStore.SaveBatchJob(ctx, storage.BatchJobRecord{
			ID:             jobID,
			OwnerID:        storage.QuestionChangeFromContext(ctx).OwnerID,
			Backend:        "dashscope",
			BackendProfile: s.profile,
			Model:          s.model,
			JobName:        jobName,
			Status:         "validating",
			TotalCount:     count,
			PointsJSON:     string(pointsJSON),
		}); err != nil {
			// DB 写入失败不阻塞提交，但记录警告（任务已在 DashScope 云端）
			fmt.Printf("⚠ 警告: 任务 %s 已提交到 DashScope，但本地保存失败: %v\n", jobID, err)
		}
	}

	// 清理临时文件
	os.Remove(jsonlPath)

	return jobID, count, nil
}

// GetJobStatus 查询批量任务状态。
func (s *Service) GetJobStatus(ctx context.Context, jobID string) (*BatchJob, error) {
	if err := s.ensureAvailable(); err != nil {
		return nil, err
	}
	var stored *storage.BatchJobRecord
	if s.batchJobStore != nil {
		stored, _ = s.batchJobStore.GetBatchJob(ctx, jobID)
	}
	url := fmt.Sprintf("%s/batches/%s", s.baseURL, jobID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		OutputFileID  string `json:"output_file_id"`
		ErrorFileID   string `json:"error_file_id"`
		CreatedAt     int64  `json:"created_at"`
		RequestCounts struct {
			Total     int `json:"total"`
			Completed int `json:"completed"`
			Failed    int `json:"failed"`
		} `json:"request_counts"`
		Metadata map[string]string `json:"metadata"`
		Error    interface{}       `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("查询失败: HTTP %d", resp.StatusCode)
	}

	jobName := ""
	model := ""
	profile := ""
	if result.Metadata != nil {
		jobName = result.Metadata["ds_name"]
		model = result.Metadata["aigo_model"]
		profile = result.Metadata["aigo_profile"]
	}
	if stored != nil {
		if jobName == "" {
			jobName = stored.JobName
		}
		if model == "" {
			model = stored.Model
		}
		if profile == "" {
			profile = stored.BackendProfile
		}
	}
	job := &BatchJob{
		JobID:        result.ID,
		Backend:      "dashscope",
		Model:        model,
		JobName:      jobName,
		Status:       result.Status,
		TotalCount:   result.RequestCounts.Total,
		Completed:    result.RequestCounts.Completed,
		Failed:       result.RequestCounts.Failed,
		OutputFileID: result.OutputFileID,
		CreatedAt:    result.CreatedAt,
	}
	if stored != nil {
		job.OwnerID = stored.OwnerID
		job.ImportedAt = stored.ImportedAt
		job.Tracked = true
	}

	// 更新数据库
	if s.batchJobStore != nil {
		if err := s.batchJobStore.UpdateBatchJob(ctx, storage.BatchJobRecord{
			ID:             result.ID,
			OwnerID:        job.OwnerID,
			Backend:        "dashscope",
			BackendProfile: profile,
			Model:          model,
			Status:         result.Status,
			TotalCount:     result.RequestCounts.Total,
			Completed:      result.RequestCounts.Completed,
			Failed:         result.RequestCounts.Failed,
			OutputFileID:   result.OutputFileID,
		}); err != nil {
			fmt.Printf("⚠ 警告: 更新任务 %s 本地状态失败: %v\n", result.ID, err)
		}
	}

	return job, nil
}

// ImportResults 按 AIgo 任务 ID 获取并导入结果，不向上层泄漏 provider file ID。
// 导入幂等：任务结果只允许入库一次。已导入的任务直接重放上次结果，
// 并发/重复触发由存储层抢占标记兜底，不会产生重复题目。
func (s *Service) ImportResults(ctx context.Context, jobID string, points []domain.KnowledgePoint) (*ImportResult, error) {
	if s.batchJobStore != nil {
		stored, err := s.batchJobStore.GetBatchJob(ctx, jobID)
		if err != nil {
			return nil, fmt.Errorf("读取任务记录失败: %w", err)
		}
		if stored != nil {
			if stored.ImportedAt != "" {
				// 已导入过：优先重放上次导入结果，避免重新下载和重复入库
				if stored.ImportResult != "" {
					var replay ImportResult
					if json.Unmarshal([]byte(stored.ImportResult), &replay) == nil {
						return &replay, nil
					}
				}
				return nil, fmt.Errorf("该任务的结果已于 %s 导入过，不能重复导入", stored.ImportedAt)
			}
			claimed, err := s.batchJobStore.ClaimBatchJobImport(ctx, jobID)
			if err != nil {
				return nil, fmt.Errorf("标记导入状态失败: %w", err)
			}
			if !claimed {
				return nil, fmt.Errorf("该任务的结果已导入过或正在导入，不能重复导入")
			}
			// Stored submission snapshots are authoritative even if the syllabus has since
			// been edited, replaced or deleted. Legacy direct imports retain their explicit input.
			if stored.PointsJSON != "" {
				if err := json.Unmarshal([]byte(stored.PointsJSON), &points); err != nil {
					_ = s.batchJobStore.ReleaseBatchJobImport(ctx, jobID)
					return nil, fmt.Errorf("任务知识点快照无效: %w", err)
				}
			}
		}
	}
	result, err := s.importJobResult(ctx, jobID, points)
	if err != nil {
		// 下载/状态校验等前置失败尚未写入任何题目，释放标记以便重试
		if s.batchJobStore != nil {
			_ = s.batchJobStore.ReleaseBatchJobImport(ctx, jobID)
		}
		return nil, err
	}
	if s.batchJobStore != nil {
		if payload, mErr := json.Marshal(result); mErr == nil {
			if saveErr := s.batchJobStore.SaveBatchJobImportResult(ctx, jobID, string(payload)); saveErr != nil {
				fmt.Printf("⚠ 警告: 保存任务 %s 导入结果失败: %v\n", jobID, saveErr)
			}
		}
	}
	return result, nil
}

// importJobResult 校验任务状态后下载结果并导入题库（不做幂等控制，见 ImportResults）。
func (s *Service) importJobResult(ctx context.Context, jobID string, points []domain.KnowledgePoint) (*ImportResult, error) {
	job, err := s.GetJobStatus(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if job.Status != "completed" && job.Status != "complete" {
		return nil, fmt.Errorf("%w，当前状态: %s", ErrNotReady, job.Status)
	}
	if job.OutputFileID == "" {
		return nil, errors.New("任务没有输出文件")
	}
	if len(points) == 0 {
		return nil, fmt.Errorf("任务缺少知识点快照，无法安全关联导入结果")
	}
	return s.DownloadAndImport(ctx, job.OutputFileID, points)
}

// DownloadAndImport 按 DashScope output_file_id 下载结果并导入题库。
// 仅为历史 CLI 兼容保留；新路径应使用 ImportResults(jobID)。
func (s *Service) DownloadAndImport(ctx context.Context, outputFileID string, points []domain.KnowledgePoint) (*ImportResult, error) {
	if err := s.ensureAvailable(); err != nil {
		return nil, err
	}
	// 1. 下载结果文件
	fmt.Println("正在下载结果...")
	url := fmt.Sprintf("%s/files/%s/content", s.baseURL, outputFileID)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(bodyBytes))
		if len(message) > 500 {
			message = message[:500]
		}
		return nil, fmt.Errorf("下载批量结果失败: HTTP %d %s", resp.StatusCode, message)
	}

	// 2. 建立知识点映射
	kpMap := make(map[string]domain.KnowledgePoint)
	for _, kp := range points {
		kpMap[batchPointID(kp)] = kp
	}

	// 3. 解析并导入
	scanner := bufio.NewScanner(bytes.NewReader(bodyBytes))
	// 百炼允许单行请求接近 1MB；默认 64KB Scanner 缓冲会把长题误判为文件结束。
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	result := &ImportResult{}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var resp struct {
			CustomID string `json:"custom_id"`
			Response struct {
				StatusCode int `json:"status_code"`
				Body       struct {
					Choices []struct {
						Message struct {
							Content string `json:"content"`
						} `json:"message"`
					} `json:"choices"`
				} `json:"body"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			result.Items = append(result.Items, ImportItem{
				OutlineCode: "?",
				Status:      "failed",
				Error:       fmt.Sprintf("响应JSON解析失败: %v", err),
			})
			result.Failed++
			continue
		}

		item := ImportItem{OutlineCode: resp.CustomID}

		if resp.Response.StatusCode != 200 || len(resp.Response.Body.Choices) == 0 {
			item.Status = "failed"
			item.Error = fmt.Sprintf("API返回异常: status=%d, choices=%d", resp.Response.StatusCode, len(resp.Response.Body.Choices))
			result.Items = append(result.Items, item)
			result.Failed++
			continue
		}

		// 查找知识点
		kp, ok := kpMap[resp.CustomID]
		if !ok {
			item.Status = "failed"
			item.Error = "结果无法匹配提交时的知识点快照"
			result.Items = append(result.Items, item)
			result.Failed++
			continue
		}

		item.OutlineCode = kp.OutlineCode
		// 解析题目
		content := strings.TrimSpace(resp.Response.Body.Choices[0].Message.Content)
		if content == "" {
			item.Status = "failed"
			item.Error = "AI返回内容为空"
			result.Items = append(result.Items, item)
			result.Failed++
			continue
		}
		questions, err := generator.ParseQuestionsFromRaw(content)
		if err != nil {
			item.Status = "failed"
			item.Error = fmt.Sprintf("题目解析失败（可能内容被截断）: %v", err)
			result.Items = append(result.Items, item)
			result.Failed++
			continue
		}
		if len(questions) == 0 {
			item.Status = "failed"
			item.Error = "解析结果为空"
			result.Items = append(result.Items, item)
			result.Failed++
			continue
		}

		// 保存到数据库
		savedCount := 0
		// 生成者记录为提交批量任务的用户（handler 已把 actor 塞入 ctx）；CLI 路径无用户时兜底 "batch"
		batchActor := strings.TrimSpace(storage.QuestionChangeFromContext(ctx).Actor)
		if batchActor == "" {
			batchActor = "batch"
		}
		for _, q := range questions {
			question := buildQuestion(q, kp)
			question.NormalizeGeneratedA2()
			if err := question.ValidateGeneratedA2(); err != nil {
				fmt.Printf("[import] 题目未通过A2专家规范，跳过: %v\n", err)
				continue
			}
			questionCtx := storage.WithQuestionChange(ctx, storage.QuestionChange{Actor: batchActor, OwnerID: storage.QuestionChangeFromContext(ctx).OwnerID, ChangeType: "batch_generate", ChangeNote: "批量推理生成题目"})
			if err := s.questionStore.SaveQuestion(questionCtx, question); err != nil {
				fmt.Printf("[import] 保存题目失败: %v\n", err)
				continue
			}
			result.QuestionIDs = append(result.QuestionIDs, question.ID)
			savedCount++
		}

		if savedCount > 0 {
			item.Status = "ok"
			item.Count = savedCount
			result.Saved += savedCount
		} else {
			item.Status = "failed"
			item.Error = "保存到数据库失败"
			result.Failed++
		}
		result.Items = append(result.Items, item)
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("扫描批量结果失败: %w", err)
	}

	fmt.Printf("导入完成: 成功 %d, 失败 %d\n", result.Saved, result.Failed)
	return result, nil
}

// WaitForCompletion 等待任务完成。
func (s *Service) WaitForCompletion(ctx context.Context, jobID string, interval, timeout time.Duration) (*BatchJob, error) {
	deadline := time.Now().Add(timeout)

	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等待超时")
		}

		job, err := s.GetJobStatus(ctx, jobID)
		if err != nil {
			return nil, err
		}

		fmt.Printf("  状态: %s, 进度: %d/%d (完成:%d, 失败:%d)\n",
			job.Status, job.Completed+job.Failed, job.TotalCount, job.Completed, job.Failed)

		switch job.Status {
		case "completed", "complete": // DashScope 文档写 completed，实际可能返回 complete
			return job, nil
		case "failed":
			return job, fmt.Errorf("任务失败")
		case "expired":
			return job, fmt.Errorf("任务已过期")
		case "cancelled":
			return job, fmt.Errorf("任务已取消")
		}

		time.Sleep(interval)
	}
}

// generateJSONL 生成 JSONL 文件。
func (s *Service) generateJSONL(points []domain.KnowledgePoint, countPerPoint int, outputPath string) (int, error) {
	file, err := os.Create(outputPath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	writer := bufio.NewWriter(file)
	defer writer.Flush()

	count := 0
	for _, kp := range points {
		prompt := generator.BuildPrompt(kp, countPerPoint)

		req := map[string]interface{}{
			"custom_id": batchPointID(kp),
			"method":    "POST",
			"url":       "/v1/chat/completions",
			"body": map[string]interface{}{
				"model": s.model,
				"messages": []map[string]string{
					{"role": "system", "content": generator.GetSystemPrompt()},
					{"role": "user", "content": prompt},
				},
				"temperature":     0.4,
				"enable_thinking": s.enableThinking,
			},
		}

		line, _ := json.Marshal(req)
		writer.Write(line)
		writer.WriteByte('\n')
		count++
	}

	return count, nil
}

// uploadFile 上传文件到 DashScope。
func (s *Service) uploadFile(ctx context.Context, filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", err
	}
	writer.WriteField("purpose", "batch")
	writer.Close()

	url := fmt.Sprintf("%s/files", s.baseURL)

	req, err := http.NewRequestWithContext(ctx, "POST", url, &buf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.httpClient.Do(req)
	if err != nil {
		fmt.Printf("  上传错误: %v\n", err)
		return "", err
	}
	defer resp.Body.Close()

	// 读取响应
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %w", err)
	}

	var result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "", fmt.Errorf("解析响应失败: %s", string(bodyBytes))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("上传失败: HTTP %d %s", resp.StatusCode, result.Error)
	}

	fmt.Printf("  上传成功: %s\n", result.ID)

	return result.ID, nil
}

// submitJob 提交批量任务。
func (s *Service) submitJob(ctx context.Context, fileID string, jobName string) (string, error) {
	url := fmt.Sprintf("%s/batches", s.baseURL)
	body := map[string]interface{}{
		"input_file_id":     fileID,
		"endpoint":          "/v1/chat/completions",
		"completion_window": "24h",
		"metadata": map[string]string{
			"ds_name":      jobName,
			"aigo_model":   s.model,
			"aigo_profile": s.profile,
		},
	}
	payload, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("提交失败: HTTP %d %s", resp.StatusCode, result.Error)
	}

	return result.ID, nil
}

// buildQuestion 从 map 构建 A2Question。
func buildQuestion(item map[string]interface{}, kp domain.KnowledgePoint) domain.A2Question {
	idPrefix := generator.SanitizeIDPrefix(kp.OutlineCode)
	if idPrefix == "" {
		idPrefix = generator.SanitizeIDPrefix(kp.Topic)
	}
	if idPrefix == "" {
		idPrefix = "custom"
	}
	profession, system := generator.QuestionMetadataForKnowledgePoint(kp, "")
	q := domain.A2Question{
		ID:              fmt.Sprintf("q-%s-%d", idPrefix, time.Now().UnixNano()),
		OutlineCode:     kp.OutlineCode,
		Profession:      profession,
		System:          system,
		KnowledgePoints: []domain.KnowledgePoint{kp},
		Status:          domain.StatusAIDraft,
		Version:         1,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if v, ok := item["clinical_stem"].(string); ok {
		q.ClinicalStem = v
	}
	if v, ok := item["answer"].(string); ok {
		q.Answer = v
	}
	if v, ok := item["explanation"].(string); ok {
		q.Explanation = v
	}
	if v, ok := item["difficulty"].(string); ok {
		q.Difficulty = domain.Difficulty(v)
	}
	if v, ok := item["cognitive_level"].(string); ok {
		q.CognitiveLevel = v
	}
	if v, ok := item["exam_points"].(string); ok {
		q.ExamPoints = v
	}
	if opts, ok := item["options"].([]interface{}); ok {
		for _, o := range opts {
			if m, ok := o.(map[string]interface{}); ok {
				label, _ := m["label"].(string)
				text, _ := m["text"].(string)
				if label != "" && text != "" {
					q.Options = append(q.Options, domain.Option{Label: label, Text: text})
				}
			}
		}
	}
	return q
}

// ListJobs 查询批量任务列表。
// 合并本地数据库和 DashScope 云端的任务列表，确保所有任务都能看到。
func (s *Service) ListJobs(ctx context.Context, name, status string, limit int) ([]BatchJob, error) {
	if err := s.ensureAvailable(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}

	// 用 map 去重，以 jobID 为 key
	jobMap := make(map[string]BatchJob)

	// 1. 从数据库获取本地保存的任务
	if s.batchJobStore != nil {
		var localJobs []storage.BatchJobRecord
		var err error
		if name != "" {
			localJobs, err = s.batchJobStore.SearchBatchJobs(ctx, name, limit)
		} else {
			localJobs, err = s.batchJobStore.ListBatchJobs(ctx, limit)
		}
		if err == nil {
			for _, j := range localJobs {
				backend := j.Backend
				if backend == "" {
					backend = "dashscope"
				}
				if backend != "dashscope" {
					continue // 共享任务表中其他 provider 的记录由对应 adapter 返回
				}
				jobMap[j.ID] = BatchJob{
					JobID:        j.ID,
					OwnerID:      j.OwnerID,
					Backend:      backend,
					Model:        j.Model,
					JobName:      j.JobName,
					Status:       j.Status,
					TotalCount:   j.TotalCount,
					Completed:    j.Completed,
					Failed:       j.Failed,
					OutputFileID: j.OutputFileID,
					ImportedAt:   j.ImportedAt,
					Tracked:      true,
				}
			}
		}
	}

	// 2. 从 DashScope 查询云端任务（始终查询，补充本地可能缺失的旧任务）
	url := fmt.Sprintf("%s/batches?limit=%d", s.baseURL, limit)
	if status != "" {
		url += "&status=" + status
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		// DashScope 查询失败不影响本地结果
		fmt.Printf("⚠ 查询DashScope任务列表失败: %v\n", err)
	} else {
		defer resp.Body.Close()

		var result struct {
			Data []struct {
				ID            string `json:"id"`
				Status        string `json:"status"`
				CreatedAt     int64  `json:"created_at"`
				RequestCounts struct {
					Total     int `json:"total"`
					Completed int `json:"completed"`
					Failed    int `json:"failed"`
				} `json:"request_counts"`
				OutputFileID string            `json:"output_file_id"`
				Metadata     map[string]string `json:"metadata"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			fmt.Printf("⚠ 解析DashScope响应失败: %v\n", err)
		} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			fmt.Printf("⚠ DashScope返回错误: HTTP %d\n", resp.StatusCode)
		} else {
			for _, item := range result.Data {
				jobName := ""
				model := ""
				if item.Metadata != nil {
					jobName = item.Metadata["ds_name"]
					model = item.Metadata["aigo_model"]
				}

				// 按名称过滤
				if name != "" && !strings.Contains(strings.ToLower(jobName), strings.ToLower(name)) {
					continue
				}

				// 如果本地已有该任务，用本地的 job_name（可能用户自定义过）
				// 但用 DashScope 的最新状态覆盖
				if existing, ok := jobMap[item.ID]; ok {
					existing.Status = item.Status
					existing.TotalCount = item.RequestCounts.Total
					existing.Completed = item.RequestCounts.Completed
					existing.Failed = item.RequestCounts.Failed
					existing.OutputFileID = item.OutputFileID
					existing.CreatedAt = item.CreatedAt
					if model != "" {
						existing.Model = model
					}
					jobMap[item.ID] = existing
				} else {
					jobMap[item.ID] = BatchJob{
						JobID:        item.ID,
						Backend:      "dashscope",
						Model:        model,
						JobName:      jobName,
						Status:       item.Status,
						TotalCount:   item.RequestCounts.Total,
						Completed:    item.RequestCounts.Completed,
						Failed:       item.RequestCounts.Failed,
						OutputFileID: item.OutputFileID,
						CreatedAt:    item.CreatedAt,
					}
				}
			}
		}
	}

	// 3. 转为切片并按创建时间排序（最新的在前）
	jobs := make([]BatchJob, 0, len(jobMap))
	for _, j := range jobMap {
		jobs = append(jobs, j)
	}
	// 按 CreatedAt 降序排列
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt > jobs[j].CreatedAt
	})

	return jobs, nil
}

// Preserve old provider IDs for legacy jobs; versioned jobs use globally unique IDs.
func batchPointID(p domain.KnowledgePoint) string {
	if p.OutlineCode != "" && (p.VersionID == "" || p.VersionID == domain.LegacyKnowledgeVersion) {
		return p.OutlineCode
	}
	return p.ID
}
