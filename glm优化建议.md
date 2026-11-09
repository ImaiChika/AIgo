# AIgo 生产化优化建议（GLM 评估报告）

> 评估日期：2026-09-05
> 评估方式：后端/前端全量代码走查（含 file:line 证据）+ 国内同类命题平台与 LLM 生产流水线实践调研
> 结论先行：**本项目不是"AI 生成的玩具"，而是按"单机 + 几千题 + 少量熟人用户"规模认真做的系统。**
> 真正的差距在：伸缩性（全内存分页）、可观测性（零结构化日志/无 CI）、以及**业务运营闭环**（查重、任务分配通知、成本度量）——这三层才是"玩具"与"可上市产品"的分界线。

---

## 一、总体结论

这个项目最容易出事的地方（事务、并发审核、数据库迁移、部署加固）做得比很多真人团队的第一版都严谨：

- 审核并发安全三件套：ctx 事务传播 + PG advisory lock（try-lock 轮询防连接池占满，`internal/storage/postgres/store.go:163`）+ `FOR UPDATE`/版本号乐观锁，配双实例并发测试
- 教科书级迁移器：版本化 + SHA256 校验 + 篡改拒绝 + 启动强校验（`internal/storage/postgres/migrations.go`）
- 知识点快照语义：批量任务用提交时快照导入，大纲事后修改不影响归属（`internal/batch/service.go:314`）
- 登录限流（账号/IP 双桶、指数退避、标识符哈希）、bcrypt + 防时序侧信道 dummy-hash
- 生产部署骨架：digest 固定镜像、read-only 容器、Caddy HTTPS、每日备份 + 异地加密备份

**地基不要大重构。** 问题是整体按单机小规模设计，且缺少生产系统赖以运转的"运营层"。当题库到万级、用户到几十人时，会先在性能、可观测性和协作流程上崩掉，而不是在功能上。

---

## 二、后端现状（50 个 Go 文件，约 2.1 万行）

### 硬伤（按优先级）

1. **数据层无 SQL 分页**——`ListQuestions` 全表加载（`internal/storage/postgres/store.go:450`），列表/搜索/统计/审核汇总全部在内存过滤。题库上万后每个请求都是 O(N) 内存扫描，这是最先撞上的天花板
2. **docx 导出在容器内必然 500**——`internal/exporter/docx.go:26` 调 `pandoc`，但 Dockerfile runtime 镜像只装 ca-certificates + tzdata（已核实）
3. **零可观测性**——77 处 `fmt.Printf`，无结构化日志、无请求 ID、无 metrics；线上排障只能翻 stdout
4. **无 CI**——约 90 个测试（含 PG 集成测试）没有任何自动执行入口；对 AI 辅助快速迭代的项目这是最大的质量风险放大器
5. **审核只读端点横向越权**——`GET /api/review/task/{id}`、`/records/{taskId}` 等只要求登录（`internal/api/server.go:199-201`，已核实），任意登录用户可跨题库读专家评语；与列表接口的 bank scope 403 语义不一致
6. **LLM 调用无重试/退避/并发上限/配额**（`internal/llm/qwen.go:61`），无 token 用量记账——既影响可用性，也没有成本可见性
7. **仓库卫生**——18MB 的 `aigo` 和 9.7MB 的 `verify` 二进制被 git 追踪（已核实），根目录留有 `tools/` 数据手术脚本和 `8.19/820/824` 生产数据 symlink
8. 次要但真实：`store.go:831` 的 `pqArray` 未转义（值含逗号即损坏）；JWT 手写 HS256 无 jti/吊销/refresh

### 保留资产

事务/迁移/审核并发/知识点快照/部署加固全部保留，不做大重构。

---

## 三、前端现状（31 个文件，约 1.3 万行）

### 亮点

- BatchPage 轮询竞态防护（序号+双标志+生命周期钩子，`web/src/pages/BatchPage.vue:142-184`）是全仓库质量最高的片段
- 导航系统：纯函数 + 单测（`web/src/navigation.js` + `navigation.test.js`），权限映射单一来源
- 知识点组件组（原生 `<dialog>`、防抖、竞态 ticket、404 自愈）质量不错
- KeepAlive 保留 generate/batch 表单状态的语义精确满足需求

### 硬伤（按优先级）

1. **token 存 localStorage**（`web/src/auth.js:41`）——XSS 一次即全线失守
2. **`auth.js:4` 裸 `JSON.parse(localStorage)` 无容错**——一条脏数据整个应用白屏；`api.me()` 定义了但从未调用，权限改动后用户无感知
3. **系统性复制粘贴**：`showToast` 复制 13 份、`statusText` 映射 8 份（键集不一致）、分页窗口逻辑 3 份——零 composable、零 UI 库、零 lint
4. **`api.js:24` 先 `res.json()` 后判 `res.ok`**——网关返回 502 HTML 时真实错误被吞成 SyntaxError；全仓库无请求超时/取消（无 AbortController）
5. 22 处 catch 只 `console.error` 静默吞错；`GeneratePage.vue:85` console.log 打印完整业务参数
6. 5 个核心审核页无移动端适配，两套视觉主题并存（全站蓝色 vs KnowledgePage 绿色）
7. 无 ESLint/Prettier/TS/CI，唯一测试未接入 npm scripts

---

## 四、与真实同类系统的对比

| 能力 | 真实平台的做法 | AIgo 现状 |
|---|---|---|
| 全流程 | 诺码信：征题→命题→审题→入库→组卷→发布→**分析**全流程打通 | 只到"入库"为止，入库后使用分析和组卷没有 |
| 质量闭环 | 广东省AI题库试点：AI 生成→人工校对→专家终审→**合规审查（查重、超纲检测）** | 有 AI 初评+专家审核，但无查重/相似题检测、无超纲检测——真实题库最刚需的两道闸 |
| 专家协作 | 考试星：专家一对一打磨、质量把关、署名认证 | 有专家库和投票，但无任务分配/认领、无通知提醒、无审核超时催办——靠审核人自己刷任务列表 |
| 内容形态 | 启明泰和：文字、图片、表格、公式、音频多格式录入审核 | 当前聚焦纯文本 A2 题；化验单表格、公式等形态不在本期范围 |
| LLM 流水线 | 生产实践：重试退避、任务队列、成本记账、prompt 版本管理、LLM-as-judge 评估闭环 | 批量链路不错（快照+可恢复），实时链路单发无重试，无 token 用量统计 |

**核心洞察：真实系统与"玩具"的分界线，不是代码风格，而是三样东西——运营闭环（通知/分配/催办）、质量闸门（查重/合规）、成本与质量的可度量（用量报表/评估回归）。**

参考来源：
- 考试星 AI 出题系统：https://www.kaoshixing.com/news/n4131
- 考阅题库命题组卷系统：https://www.kaoyue.com/
- 诺码信人事考试产品：https://www.nomax.cn/ncms/m_rskscp.shtml
- 启明泰和智能题库：https://www.qmth.com.cn/prod_info_zntk.html
- 广东省级AI题库建设试点：https://m.itouchtv.cn/article/692284a31215102b198e243a2b3cf3f1
- Building Production-Ready AI Pipelines：https://dev.to/synsun/building-production-ready-ai-pipelines-lessons-from-running-10k-generations-2cee

---

## 五、优化路线图

### P0 — 上线前必须修（正确性/安全，工作量小）

1. 修 docx 导出（Dockerfile 装 pandoc，或改用纯 Go docx 生成去掉 pandoc 依赖——推荐后者）
2. 收口审核只读端点的 bank scope 校验（`internal/api/server.go:199-201`）
3. 前端：token 去 localStorage 化（httpOnly cookie）；修裸 `JSON.parse`；删 console.log
4. 修 `web/src/api.js` 错误处理顺序 + `store.go:831` pqArray 转义
5. 仓库卫生：二进制出库（gitignore/LFS）、清理 tools 手术脚本和 symlink

### P1 — 规模与可运维（决定能否真实投产）

1. **SQL 层分页/过滤**（`ListQuestions` 加 WHERE/LIMIT/COUNT，review 汇总查询同改）——最大单项工程
2. 结构化日志 + 请求 ID 中间件（slog，标准库，符合"优先标准库"约束）
3. CI：GitHub Actions 跑 `go test ./...`（PG 用 service container）+ 前端 lint/build——90 个测试立刻变现
4. LLM 调用加固：重试退避、并发信号量、按用户生成配额、token 用量记账表
5. 前端抽 3 个 composable（`useToast`/`useStatusText`/`usePagination`）+ 接入 ESLint——先消复制粘贴再谈新功能

### P2 — 向真实业务靠拢（业务价值增量）

1. **查重/相似题检测**：题干向量或全文相似度，生成时和入库前各查一次（真实题库最刚需的闸门）
2. **审核协作闭环**：任务分配/认领、站内通知、审核超时提醒、审核进度看板
3. **成本报表**：按用户/批量任务统计 token 用量与费用
4. **prompt 版本管理**：提示词入库带版本号，题目记录生成所用 prompt 版本——质量回归的前提
5. 前端统一确认弹窗、补 5 个审核页移动端适配、统一双视觉主题

---

## 六、建议执行顺序

地基不动。先 P0 清雷（约 1-2 天工作量），P1 里优先做 **SQL 分页 + CI + 结构化日志** 三件（决定后续所有迭代的成本），P2 里 **查重和审核通知** 是让它"像真业务"性价比最高的两件事。
