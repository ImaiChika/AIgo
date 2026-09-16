# AIgo

面向国家执业医师考试 A2 型试题的 AI 辅助命题系统。

当前产品只处理纯文本 A2 单选题，题目唯一来源是 AI 单题生成。AI 生图、题目图片、多模态素材和图片审核永久不做；Web 批量任务拆成单题 API 调用并持久化进度，CLI 仍保留历史 DashScope 批量兼容命令。

> **当前阶段（2026-09-12）**：项目尚未验收。实时出题与 AI 检查使用用户自有的云端千问 API（百炼）；客户本地部署是最终交付目标，本地模型/GPU 资源尚未具备，OpenAI-compatible 切换接口已预留。本地开发环境与 ECS 服务器保持同一可用版本，代码变更经本地回归与 CI 验证后再同步服务器。

## 必读文件

- `功能需求文档.txt` — 产品需求基准
- `CLAUDE.md` — 开发约束
- `docs/README.md` — 专家材料、项目指南与分析文档索引
- `data/README.md` — 需求表、参考数据、样题与批量运行资料索引

## 快速启动

开发模式：

```bash
# 一键启动（PostgreSQL + 后端 + 前端）
./start.sh

# 一键停止
./end.sh
```

正式构建与前台运行（运行阶段不需要 Node/Vite，PostgreSQL 由外部管理）：

```bash
# 生成版本化发布目录 output/releases/<build-id>，并切换 output/production
./build-production.sh

# 正式运行默认不读取项目 .env；请由进程环境/Secrets 注入数据库和 JWT 配置。
# 实时 Qwen 地址、API Key 和两个模型可在超级管理员登录后的“AI 服务配置”页面填写。
export DB_DSN='postgres://localhost:5432/aigo?sslmode=disable'
export JWT_SECRET='请替换为随机强密钥'

# 先执行版本化迁移，再以前台单进程托管 API 和前端静态资源
./run-production.sh
```

`run-production.sh` 默认监听 `127.0.0.1:8080`、开启自助注册并禁用 dotenv。新注册账号不带角色、业务权限或题库范围，管理员会在「用户管理」自动看到并负责授权。也可以设置 `AIGO_ENV_FILE=/secure/path/production.env` 读取发布目录之外的受控配置文件；文件不存在或不可读时会拒绝启动。

单机构 Docker 交付使用 [deploy/README.md](deploy/README.md)：提供多阶段非 root 应用镜像、PostgreSQL、一次性迁移服务、Compose Secrets、Caddy HTTPS、带校验与恢复演练的逻辑备份，以及可选的 `age + S3` 客户端加密异机备份服务。该部署骨架不包含本地 Qwen 服务本身；客户实际对象存储的 Object Lock/凭证策略、集中监控和本地模型容量仍需部署验收。

手动启动：
```bash
# 1. 配置环境变量
cp configs/example.env .env
# 首次启动也可以用环境变量预置 Qwen，服务会加密导入数据库；上线后可从“AI 服务配置”页面切换。
# 两种模式都需配置 DB_DSN 和 JWT_SECRET；Batch 如继续使用百炼仍需保留独立 Batch 凭证。

# 2. 启动 PostgreSQL
brew services start postgresql@16

# 3. 启动后端
go run ./cmd/aigo serve

# 4. 启动前端（另一个终端）
cd web && npm install && npm run dev

# 5. 访问 http://localhost:5173
# 默认管理员：admin / admin
```

## 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `QWEN_DEPLOYMENT` | 实时文本推理位置：`cloud` / `local` | `cloud` |
| `DASHSCOPE_API_KEY` | 兼容导入/回退凭证；也可供独立 Batch 使用 | UI 配置后实时调用不再以它为主 |
| `QWEN_BASE_URL` | 首次启动兼容导入的实时 OpenAI-compatible 地址 | UI 配置后可留空；云端默认百炼地址 |
| `QWEN_MODEL` | 首次启动兼容导入的生成模型 | UI 配置后可留空；云端默认 `qwen3.5-flash` |
| `QWEN_API_KEY` | 首次启动兼容导入/回退的实时端点凭证 | UI 配置后可留空 |
| `QWEN_LOCAL_API_KEY` | 兼容导入的本地实时端点凭证；不会回退云 Key | 本地无鉴权时可空 |
| `QWEN_ENABLE_THINKING` | `true` / `false` / `auto` | `auto` |
| `QWEN_BATCH_BACKEND` | CLI 兼容命令的 `auto` / `dashscope` / `local` / `disabled`；Web 批量不使用 Files/Batches | `auto` |
| `QWEN_BATCH_API_KEY` | CLI 批量命令使用的独立百炼 Batch 凭证；空则使用 `DASHSCOPE_API_KEY` | 空 |
| `QWEN_BATCH_BASE_URL` | 百炼 Batch base URL；cloud 下空则继承实时端点 | 空 |
| `QWEN_BATCH_MODEL` | 百炼批量模型 | `qwen3.5-flash` |
| `QWEN_BATCH_PROFILE` | 脱敏配置档名称，用于审计；多 profile 路由待后续 registry | `dashscope-default` |
| `DB_DSN` | PostgreSQL 连接串 | `postgres://localhost:5432/aigo?sslmode=disable` |
| `JWT_SECRET` | JWT 签名密钥 | `aigo-jwt-secret-default`（生产环境请更换） |
| `AIGO_HTTP_ADDR` | 正式 HTTP 监听地址 | `127.0.0.1:8080` |
| `AIGO_WEB_DIST_DIR` | Vite 生产构建目录；空时只提供 API | 空 |
| `AIGO_ENV_FILE` | 外部 dotenv 路径；`-` 表示完全禁用 dotenv | `.env` |
| `AIGO_REGISTER_ENABLED` | 是否开放自助注册；设为 `0/false` 可紧急关闭 | 默认 `1` |
| `AIGO_TRUST_PROXY_HEADERS` | 是否信任 Caddy 写入的客户端IP头；直连部署不得开启 | 默认 `false`，Compose 内为 `true` |

### 渐进迁移到本地推理

实时出题和 AI 检查共用 `llm.Client`，可先迁到本地 OpenAI-compatible 服务；现有百炼 Batch 可独立保留：

```env
QWEN_DEPLOYMENT=local
QWEN_BASE_URL=http://qwen-vllm.internal:8000/v1
QWEN_MODEL=Qwen/Qwen3.5-35B-A3B
QWEN_LOCAL_API_KEY=

# 保留百炼批量时继续配置
DASHSCOPE_API_KEY=
QWEN_BATCH_BACKEND=dashscope
QWEN_BATCH_MODEL=qwen3.5-flash
```

`qwen3.5-flash` 是百炼托管模型 ID；本地服务应填写实际开放权重或 `--served-model-name`，不能在业务代码里写死。Web 批量任务已使用本地单题 API 队列，实时部署地址、密钥和模型可由超级管理员在 Web 的“AI 服务配置”页面维护；本地模型进程、GPU 参数和网络连通性仍由部署运维管理。Batch 配置仅供 CLI 兼容命令保留。

Web 管理端的批量推理页面使用本地单题 API 队列：每道题独立调用实时生成服务，任务进度、失败明细和结果快照持久化，导入后进入正常 AI 检查流程；上述 `QWEN_BATCH_*` 变量仅保留给 CLI 批量命令兼容使用。

## CLI 命令

```bash
go run ./cmd/aigo migrate            # 数据库迁移到当前版本
go run ./cmd/aigo serve              # 启动 HTTP 服务
go run ./cmd/aigo import <file.xlsx> # 导入题目
go run ./cmd/aigo kp-import <file.xlsx> # 导入知识点
go run ./cmd/aigo doctor             # 检查系统状态
```

## 功能概览

### 已完成

| 模块 | 功能 |
|------|------|
| **AI 出题** | 千问单题生成 A2 型题，结构化 JSON 输出，运行 ID 幂等与刷新恢复 |
| **AI 检查** | 题目首次生成后自动异步触发，LLM 检查科学性、答案、解析与 A2 格式；命题元数据只作提示词反馈；送审强制前置；检查结果、题目状态/淘汰与任务完成在同一 PostgreSQL 事务落库 |
| **知识点管理** | 年度大纲版本，Excel/CSV/Word 多文件导入，目录树、搜索及单条增删改 |
| **题目管理** | AI 生成题查询、筛选、退修编辑、版本记录和状态管理；无手动新建 |
| **批量推理** | Web 端使用本地单题 API 队列，任务可轮询、持久化和幂等导入；CLI 保留 DashScope 兼容路径 |
| **多轮审核** | 可配置流程、多轮审核、通过/驳回/退回修改；待审/决断任务固定分页；终态题按轮次并列展示全部专家意见和最终决断 |
| **题目通过** | 最终把关通过 → `published` 唯一成功终态 |
| **导出** | Excel (.xlsx)、Word (.docx) 导出；仅包含 `published/已通过` 题目，不输出命题人列 |
| **用户认证** | JWT 登录，注册默认低权限，管理员RBAC授权，账号/IP渐进限速与失败审计 |
| **个人中心** | 登录默认进入“我的数据”；按当前工作身份汇总待提交/审核/决断/修改/分享审批和本人三层题库数量；独立个人设置支持昵称与密码修改 |
| **操作日志** | 审计日志，按题目/操作人筛选 |
| **数据库** | PostgreSQL，独立版本化迁移、校验和与启动版本检查 |
| **持续集成** | GitHub Actions：Go 全量测试、真实 PostgreSQL 集成测试、竞态测试、govulncheck 漏洞扫描、前端构建与状态测试 |
| **前端** | Vue 3 管理端；开发使用 Vite，正式构建由 Go 同源托管 |

### 主流程

```text
AI 单题出题 → 落库（ai_draft，仅作为首次检查前的暂存态）
        ↓ 自动触发（后台异步，AIGO_AICHECK_AUTO）
AI 检查（持久化任务队列：抢占执行 → 超时重试 3 次 → 耗尽标记最终失败）
        ├─ 不通过 → 自动删除，原因留档并在生成页展示"本次生题具体情况"
        └─ 通过 → ai_reviewed（送审强制前置，可配置关闭）
专家多轮审核与决断
        ├─ 需修改 → 题目恢复 ai_reviewed，退回生成者修改（「待我修改」页），改完提交修改回库，由管理员重新送审
        ├─ 驳回 → rejected 终态锁定：禁止修改与重新提交
        └─ 通过（全轮 + 把关人决断）→ published 定稿（管理员可在题库「撤回」回 ai_reviewed 修订）
```

- 题目唯一来源是 AI 生成；无手动新建。编辑窗口仅限"审核退回修改"的题目（「待我修改」页），生成页为纯展示。
- 驳回与需修改语义分离：驳回是终态锁定；需修改是唯一的人工修订入口（无需 AI 检查，送审由管理员负责）。
- AI 检查仅在题目首次生成时执行一次；退回修改后的编辑不触发复检。
- AI 检查只对医学内容、选项逻辑、答案解析一致性和 A2 格式做硬性判断；难度、认知层次、考核要点等元数据问题只能作为 info 反馈给出题提示词，不能单独改变 verdict 或淘汰题目。
- 检查任务持久化在 `ai_check_tasks` 表：服务重启自动恢复，单次调用超时可配（默认 90s），失败自动重试（默认 3 次，指数退避），重试耗尽标记最终失败；卡死任务由租约到期自动回收。
- 进度可见：生成页显示“检查中 x/N”分段进度与淘汰明细（短轮询）；题库页提供检查概况统计。
- 分层可见性：手动补查按 `question:edit`（题目编辑）授权，强制通过/撤回按 `user:manage` 授权；`review:do`（审题）默认只见"AI 预审"折叠标识，可主动展开；`review:final`（最终把关，管理员天然具备，也可在用户管理中授予指定账户）在决断工作台可见 AI 报告与全部轮次专家评语。所有权限点均在权限矩阵中可分配。
- 单题出题与暂缓的批量模块仍使用独立的 `question:generate` / `batch:run` 权限，避免保留的批量模拟入口扩大单题权限；批量模块恢复开发前不作为正式能力承诺。
- 存量草稿可用 CLI 补查：`go run ./cmd/aigo ai-check --all-drafts`。
- 检查与生成一样走 `llm.Client`（OpenAI 兼容）；切换自部署只需改 `QWEN_*`，或用 `AIGO_AICHECK_*` 让检查走独立端点/模型。

### 技术栈

- **后端**: Go + PostgreSQL
- **前端**: Vue 3 + Vite
- **AI**: 阿里云百炼千问 (OpenAI 兼容接口)
