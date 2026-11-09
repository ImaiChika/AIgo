package postgres

// Schema57 为批量任务增加显式计划与提交幂等键：plan_json 保存冻结的单元清单
// （随机任务恢复/重跑不重抽），request_key + request_hash 提供“同键同输入重放、
// 同键不同输入 409”的提交幂等；唯一索引限定同 owner 范围，旧任务不受影响。
func batchPlanStatements() []string {
	return []string{
		`ALTER TABLE batch_jobs ADD COLUMN IF NOT EXISTS plan_json TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE batch_jobs ADD COLUMN IF NOT EXISTS request_key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE batch_jobs ADD COLUMN IF NOT EXISTS request_hash TEXT NOT NULL DEFAULT ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_batch_jobs_request_key
		ON batch_jobs(owner_id, request_key) WHERE request_key <> ''`,
	}
}
