# AIgo

面向国家执业医师考试 A2 型试题的 AI 辅助命题系统。

## 必读文件

- `功能需求文档.txt` — 产品需求基准
- `CLAUDE.md` — 开发约束

## 快速启动

```bash
# 一键启动（PostgreSQL + 后端 + 前端）
./start.sh

# 一键停止
./end.sh
```

手动启动：
```bash
# 1. 配置环境变量
cp configs/example.env .env
# 编辑 .env 填入 DASHSCOPE_API_KEY 和 DB_DSN

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
| `DASHSCOPE_API_KEY` | 阿里云百炼 API Key | 必填 |
| `QWEN_BASE_URL` | 千问 API 地址 | `https://dashscope.aliyuncs.com/compatible-mode/v1` |
| `QWEN_MODEL` | 出题模型 | `qwen3.5-flash` |
| `DB_DSN` | PostgreSQL 连接串 | `postgres://localhost:5432/aigo?sslmode=disable` |
| `JWT_SECRET` | JWT 签名密钥 | `aigo-jwt-secret-default`（生产环境请更换） |

## CLI 命令

```bash
go run ./cmd/aigo serve              # 启动 HTTP 服务
go run ./cmd/aigo import <file.xlsx> # 导入题目
go run ./cmd/aigo kp-import <file.xlsx> # 导入知识点
go run ./cmd/aigo doctor             # 检查系统状态
```

## 功能概览

### 已完成

| 模块 | 功能 |
|------|------|
| **AI 出题** | 千问生成 A2 型题，结构化 JSON 输出，批量生成 |
| **AI 检查** | LLM 检查题目质量（科学性、答案、解析），四维评分 |
| **知识点管理** | Excel 批量导入、搜索、筛选 |
| **题目管理** | CRUD、搜索、筛选、状态管理 |
| **批量推理** | DashScope 批量 API，云端执行，任务持久化 |
| **多轮审核** | 可配置流程、多轮审核、通过/驳回/退回修改 |
| **题目发布** | 审核通过 → 管理员发布 → 正式题库 |
| **生图** | z-image-turbo 真实生图，提示词生成，候选图审核 |
| **导出** | Excel (.xlsx)、Word (.docx) 导出 |
| **用户认证** | JWT 登录，RBAC 三种角色 (admin/expert/teacher) |
| **操作日志** | 审计日志，按题目/操作人筛选 |
| **数据库** | PostgreSQL，12 张数据表，启动自动建表 |
| **前端** | Vue 3，9 个页面，全部对接后端 API |

### 技术栈

- **后端**: Go + PostgreSQL
- **前端**: Vue 3 + Vite
- **AI**: 阿里云百炼千问 (OpenAI 兼容接口)
- **生图**: z-image-turbo
