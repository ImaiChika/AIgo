# 批量导入绕过生成配额

状态：**已修复**（提交 `9eb1543`，数据库迁移 35）。

## 复现与影响

修复前，在隔离 PostgreSQL 中将全站运行占用设为 20/20，再导入一项已完成、含 12 道题的批量任务。导入直接创建 12 个首次 AI 检查任务，页面占用变成 32/20。HTTP 在途闸门和生成任务准入不能阻止这条人工导入路径；多个已完成任务连续导入可继续增加首检积压。

## 修复

- 批量导入在 PostgreSQL 配额事务锁内先检查余量，抢占成功时按将导入的题目 ID 预留首检名额；额度不足返回 429 和 `Retry-After`，不保存题目。
- 首检任务入队后，相应预留名额不再计入，避免同一题重复占额。保存失败的题目不保留预留；并发导入共用同一全站上限。
- 管理页面示出“导入待首检预留”，并在批量页展示可重试的拒绝原因。

证据：`internal/storage/postgres/generation_quota_test.go` 中的 `TestCompletedBatchImportReservesFirstCheckQuota`、`TestConcurrentBatchImportClaimsShareQuota`、`TestBatchImportReservationKeepsOnlySavedUnqueuedQuestions`，以及 `internal/api/handler_generation_quota_test.go` 中的 `TestBatchImportQuotaRejectionAndRecoveryWithoutModelCall`。浏览器测试的 API 全部拦截，没有调用真实出题模型。

## 剩余边界

配额约束新任务与人工批量导入的准入。服务启动时对遗留 `ai_draft` 的首检补扫属于恢复流程，仍可能使实时首检数量短时高于配置上限；不能把页面上限表述为任何状态下绝不超出的硬容量保证。后续如压测证明补扫会造成资源压力，应对恢复队列加入分批背压，同时保留“每题只首检一次”的产品约束。
