// Package batch 提供 DashScope 批量推理功能。
// 使用 OpenAI 兼容格式的批量 API。
// 参考文档：https://platform.qianwenai.com/docs/developer-guides/text-generation/batch
package batch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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
	apiKey        string
	baseURL       string               // https://dashscope.aliyuncs.com/compatible-mode/v1
	questionStore storage.QuestionStore
	batchJobStore storage.BatchJobStore // 批量任务持久化存储
	httpClient    *http.Client
}

// NewService 创建批量推理服务。
func NewService(apiKey, baseURL string, questionStore storage.QuestionStore, batchJobStore storage.BatchJobStore) *Service {
	return &Service{
		apiKey:        apiKey,
		baseURL:       strings.TrimRight(baseURL, "/"),
		questionStore: questionStore,
		batchJobStore: batchJobStore,
		httpClient:    &http.Client{Timeout: 120 * time.Second},
	}
}

// BatchJob 批量任务状态。
type BatchJob struct {
	JobID        string `json:"job_id"`
	JobName      string `json:"job_name"`      // 自定义任务名称
	Status       string `json:"status"`        // validating/in_progress/completed/failed/cancelled
	TotalCount   int    `json:"total_count"`
	Completed    int    `json:"completed"`
	Failed       int    `json:"failed"`
	OutputFileID string `json:"output_file_id"`
	CreatedAt    int64  `json:"created_at"`
	Error        string `json:"error,omitempty"`
}

// ImportResult 导入结果详情。
type ImportResult struct {
	Saved  int            `json:"saved"`  // 成功导入数
	Failed int            `json:"failed"` // 失败数
	Items  []ImportItem   `json:"items"`  // 每条导入详情
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
			ID:         jobID,
			JobName:    jobName,
			Status:     "validating",
			TotalCount: count,
			PointsJSON: string(pointsJSON),
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
	if result.Metadata != nil {
		jobName = result.Metadata["ds_name"]
	}

	job := &BatchJob{
		JobID:        result.ID,
		JobName:      jobName,
		Status:       result.Status,
		TotalCount:   result.RequestCounts.Total,
		Completed:    result.RequestCounts.Completed,
		Failed:       result.RequestCounts.Failed,
		OutputFileID: result.OutputFileID,
		CreatedAt:    result.CreatedAt,
	}

	// 更新数据库
	if s.batchJobStore != nil {
		if err := s.batchJobStore.UpdateBatchJob(ctx, storage.BatchJobRecord{
			ID:           result.ID,
			Status:       result.Status,
			TotalCount:   result.RequestCounts.Total,
			Completed:    result.RequestCounts.Completed,
			Failed:       result.RequestCounts.Failed,
			OutputFileID: result.OutputFileID,
		}); err != nil {
			fmt.Printf("⚠ 警告: 更新任务 %s 本地状态失败: %v\n", result.ID, err)
		}
	}

	return job, nil
}

// DownloadAndImport 下载结果并导入题库，返回详细导入结果。
func (s *Service) DownloadAndImport(ctx context.Context, outputFileID string, points []domain.KnowledgePoint) (*ImportResult, error) {
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

	// 2. 建立知识点映射
	kpMap := make(map[string]domain.KnowledgePoint)
	for _, kp := range points {
		kpMap[kp.OutlineCode] = kp
	}

	// 3. 解析并导入
	scanner := bufio.NewScanner(bytes.NewReader(bodyBytes))
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
			kp = domain.KnowledgePoint{OutlineCode: resp.CustomID}
		}

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
		for _, q := range questions {
			question := buildQuestion(q, kp)
			if err := s.questionStore.SaveQuestion(ctx, question); err != nil {
				fmt.Printf("[import] 保存题目失败: %v\n", err)
				continue
			}
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
		prompt := buildPrompt(kp, countPerPoint)

		req := map[string]interface{}{
			"custom_id": kp.OutlineCode,
			"method":    "POST",
			"url":       "/v1/chat/completions",
			"body": map[string]interface{}{
				"model": "qwen3.5-flash",
				"messages": []map[string]string{
					{"role": "system", "content": getSystemPrompt()},
					{"role": "user", "content": prompt},
				},
				"temperature":    0.4,
				"max_tokens":     16000, // 从8000增大到16000，避免生成内容被截断
				"enable_thinking": true,
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
	fmt.Printf("  上传URL: %s\n", url)
	if len(s.apiKey) >= 10 {
		fmt.Printf("  API Key: %s...%s\n", s.apiKey[:6], s.apiKey[len(s.apiKey)-4:])
	} else {
		fmt.Printf("  API Key: （未配置或长度异常）\n")
	}

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
		"input_file_id":    fileID,
		"endpoint":         "/v1/chat/completions",
		"completion_window": "24h",
		"metadata": map[string]string{
			"ds_name": jobName,
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

// buildPrompt 构建完整 prompt（与项目一致，不可删减）。
func buildPrompt(kp domain.KnowledgePoint, count int) string {
	var b strings.Builder
	b.WriteString("请根据以下考试大纲知识点，生成国家执业医师考试A2型单选题。\n\n")
	b.WriteString("【知识点信息】\n")
	b.WriteString(fmt.Sprintf("大纲代码：%s\n", kp.OutlineCode))
	b.WriteString(fmt.Sprintf("分类：%s\n", kp.Category))
	b.WriteString(fmt.Sprintf("专业/系统：%s\n", kp.Subject))
	if kp.Unit != "" {
		b.WriteString(fmt.Sprintf("单元：%s\n", kp.Unit))
	}
	if kp.SubItem != "" {
		b.WriteString(fmt.Sprintf("细目：%s\n", kp.SubItem))
	}
	b.WriteString(fmt.Sprintf("要点：%s\n", kp.Topic))
	b.WriteString(fmt.Sprintf("数量：%d道\n\n", count))
	b.WriteString("【输出格式】\n")
	b.WriteString("输出一个JSON数组，每个元素包含以下字段：\n")
	b.WriteString(`{
  "clinical_stem": "临床情境题干（按病历顺序书写）",
  "options": [
    {"label": "A", "text": "选项文本"},
    {"label": "B", "text": "选项文本"},
    {"label": "C", "text": "选项文本"},
    {"label": "D", "text": "选项文本"},
    {"label": "E", "text": "选项文本"}
  ],
  "answer": "正确答案标签",
  "explanation": "完整解析：说明正确答案依据和干扰项错误原因",
  "difficulty": "0.65",
  "cognitive_level": "记忆/理解/简单应用/综合应用 选一",
  "exam_points": "考核要点，如：诊断与鉴别诊断，临床表现"
}`)
	b.WriteString("\n\n只输出JSON数组，不要输出其他任何内容。")
	return b.String()
}

// getSystemPrompt 返回完整系统提示词（与项目一致，不可删减）。
func getSystemPrompt() string {
	return `你是医学考试命题助手，负责生成国家执业医师考试A2型单选题。

【一、命题总原则】
1. 以《考试大纲》为依据，以人卫社统编教材为内容基础
2. 执业医师命题以本科生毕业后培训一年的水平为标准
3. 所命试题应是新编原创试题，采用新的素材或临床情景，避免照搬书本现成实例

【二、A2型题格式】
题干按病历书写顺序：一般情况→主诉→现病史→既往史→查体→辅助检查→提问
- 一般情况：男/女，年龄
- 主诉：主要症状或体征+时间
- 现病史：发病诱因、症状特点、伴随症状、诊治经过
- 既往史：根据病例需要编写
- 查体：按生命征→一般情况→头颈→肺→心→腹→脊柱→四肢→神经系统顺序
- 辅助检查：常用检查执业医师以英文表示
- 提问：该患者最可能的诊断是 / 最有价值的检查是 / 治疗原则是

【三、内容要求】
1. 试题内容科学、正确
2. 正确答案唯一、且无学术上的争议
3. 内容取样有较好的代表性，避免出偏题、怪题
4. 试题必须有明确的主题，题干和备选答案必须围绕同一知识点，避免在一道题中考查多个知识点
5. 避免存在性别、种族、地域、文化的不公平或歧视
6. 必须使用规范的医学术语，名词术语、药物名称、化验数值和计量单位须准确、规范

【四、题干要求】
1. 题干叙述简明扼要，包含回答问题所必需的全部要素
2. 题干提出的问题具体明确，使应试者一看到题干就能明确要考察的知识点和具体内容
3. 题干中不能包括备选答案或对回答问题有所暗示
4. 题干尽量以叙述式书写

【五、备选答案要求】
1. 备选答案之间不能有相互重叠、相互依赖的内容
2. 备选答案应在性质上、类别上相同，在逻辑、语法和内容长短上也应基本一致
3. 备选答案中避免无意义或无用的干扰答案
4. 不使用"以上都是"和"以上都不是"作为备选答案
5. 备选答案与题干符合逻辑性
6. 备选答案按逻辑顺序排列
7. 备选答案中的相同表述，统一合理地放到题干中

【六、文字要求】
1. 试题所用文字简明、扼要，避免生僻、艰涩、洋化用语
2. 避免暗示或模糊性用语，杜绝错别字
3. 尽量避免使用否定式，必须使用否定句时用黑体加粗强调否定词，杜绝使用双重否定

【七、输出要求】
- 严格输出JSON数组，不要输出任何其他文字
- 选项五选一
- 每道题必须包含完整解析，说明正确答案依据和干扰项错误原因
- 难度以0-1之间两位小数表示（0.05的倍数），如0.65
- 认知层次：记忆/理解/简单应用/综合应用 选一
- 考核要点标注具体，如：诊断与鉴别诊断，临床表现，辅助检查
- 大纲代码标到最后一级`
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
	q := domain.A2Question{
		ID:              fmt.Sprintf("q-%s-%d", idPrefix, time.Now().UnixNano()),
		OutlineCode:     kp.OutlineCode,
		Profession:      kp.Subject,
		System:          kp.Category,
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
				jobMap[j.ID] = BatchJob{
					JobID:        j.ID,
					JobName:      j.JobName,
					Status:       j.Status,
					TotalCount:   j.TotalCount,
					Completed:    j.Completed,
					Failed:       j.Failed,
					OutputFileID: j.OutputFileID,
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
				if item.Metadata != nil {
					jobName = item.Metadata["ds_name"]
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
					jobMap[item.ID] = existing
				} else {
					jobMap[item.ID] = BatchJob{
						JobID:        item.ID,
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

// GetBaseURLs 返回当前使用的 URL（用于调试）。
func (s *Service) GetBaseURLs() string {
	return s.baseURL
}
