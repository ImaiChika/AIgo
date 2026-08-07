# AIgo

面向国家执业医师考试 A2 型试题的 AI 辅助命题系统。

## 必读文件

- `功能需求文档.txt` — 产品需求基准
- `CLAUDE.md` — 开发约束
- `当前还缺的功能提示.txt` — 待开发功能清单
- `项目结构.txt` — 项目目录结构和阅读顺序

## 快速启动

```bash
# 1. 启动 PostgreSQL
brew services start postgresql@16

# 2. 配置环境变量（复制 .env 并填入 API Key）
cp configs/example.env .env
# 编辑 .env 填入 DASHSCOPE_API_KEY 和 DB_DSN

# 3. 启动后端
go run ./cmd/aigo serve

# 4. 启动前端（另一个终端）
cd web && npm install && npm run dev -- --port 5174

# 5. 访问 http://127.0.0.1:5174
# 默认管理员：admin / admin
```

## CLI 命令

```bash
go run ./cmd/aigo doctor              # 检查框架状态
go run ./cmd/aigo import <file.xlsx>  # 导入题目
go run ./cmd/aigo kp-import <file.xlsx> # 导入知识点
go run ./cmd/aigo serve               # 启动 HTTP 服务
```

## 当前状态

已完成：
- Go HTTP API 服务（20+ 个接口）
- PostgreSQL 持久化存储
- JWT 认证 + RBAC 权限（admin/expert/teacher）
- 千问出题（qwen3.6-flash）
- 知识点 Excel 导入 + 搜索
- 题目 CRUD + 搜索（支持ID/题干/答案）
- 专家库管理
- 多轮审核流程（可配置轮数和审核人）
- 审核操作（通过/驳回/需修改，管理员可审任意轮）
- 题目发布（审核通过 → 管理员发布 → 正式题库）
- 图片提示词生成 + Mock 生图
- 操作日志（审计）
- Vue 3 前端（8 个页面，全部对接后端 API）

未完成：
- 见 `当前还缺的功能提示.txt`
