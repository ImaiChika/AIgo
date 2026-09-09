# Claude Code 必读：AIgo 当前开发约束

## 1. 必须先读

后续开发必须以根目录 `功能需求文档.txt` 为产品基准。

如果本文与 `功能需求文档.txt` 冲突，以 `功能需求文档.txt` 为准。

## 2. 当前项目定位

AIgo 当前版本是面向国家执业医师考试 A2 型试题的 AI 辅助命题系统。

当前主流程：

```text
知识点 Excel / CSV / Word 表格
        ↓
千问生成 A2 试题草稿
        ↓
基础质量初评
        ↓
多轮专家审核
        ↓
正式题库
```

当前重点是做一个可落地、可审核、可裁剪的系统，不要扩展成复杂研究平台。

## 3. 技术路线

- 主系统优先使用 Go。
- Python/conda 只作为后续算法实验的辅助方案。
- 文本出题直接调用阿里云百炼千问 API。
- 当前不接知识图谱。
- 当前不强依赖历史真题。
- 当前不要求解析和试题来源为必填字段。

千问相关环境变量：

- `DASHSCOPE_API_KEY`
- `QWEN_BASE_URL`
- `QWEN_MODEL`

不得把真实 API Key 写入代码或文档。

## 4. 当前必须实现或保留的方向

### 4.1 知识点管理

知识点管理要简单。

只做：

- Excel 批量导入知识点。
- 知识点搜索。

不要做：

- 复杂的单个知识点新增、编辑、删除流程。
- 知识点与指南、教材、文献的复杂关联。
- 知识图谱关系维护。

### 4.2 AI 出题

当前直接用千问生成 A2 题。

题目首期核心字段：

- 题干。
- 选项。
- 正确答案。
- 知识点。
- 难度。
- 审核状态。
- 版本记录。

解析和试题来源是可选字段，首期不要作为必填项，也不要在质量校验里强制要求。

### 4.3 多轮审核

审核流程必须可配置。

需要支持：

- 专家库。
- 审核流程配置。
- 审核轮数配置。
- 每轮审核人或专家组配置。
- 审核意见记录。
- 修改记录。
- 通过、驳回、退回修改。

## 5. 当前不要做的内容

以下内容目前不要实现：

- 知识图谱。
- 题目附带图片、多模态素材、AI 生图及图片审核。
- Neo4j。
- 混淆网络。
- IRT 或深度 IRT。
- Bloom 分类。
- 强化学习优化。
- 医学模型微调。
- 复杂来源资料管理。
- 历史真题强依赖。
- 把解析或试题来源做成必填。

如果后续需要使用历史真题，只能先做成轻量参考加载：

```text
选择知识点
        ↓
检索少量相关历史真题或样例
        ↓
加载到提示词上下文
        ↓
千问参考样例生成新题
```

不要为了历史真题提前设计复杂知识库或知识图谱。

## 6. 代码风格约束

- 保持 Go 为主语言。
- 优先使用标准库，除非收益明确。
- 新增依赖前必须说明原因。
- 不要大规模重构当前骨架。
- 不要把审核流程写死在代码里。
- 不要把知识点管理做复杂。
- AI 生成结果默认是草稿。
- 未通过审核的题目不能进入正式题库。
- 测试命令优先使用 `go test ./...`。

## 7. 当前工程骨架

### 后端 Go

- `cmd/aigo/main.go`：CLI 入口，支持 serve/import/generate 等命令。
- `internal/config/config.go`：从 .env 文件和环境变量加载配置。
- `internal/llm/`：千问 OpenAI 兼容客户端。
- `internal/domain/`：题目、知识点、审核等领域类型。
- `internal/generator/`：千问出题服务（prompt 构建 + JSON 解析）。
- `internal/evaluator/`：规则评估（选项数、答案、解析）。
- `internal/knowledge/`：知识点服务（导入、搜索、筛选）。
- `internal/importer/`：xlsx 导入器（题目 + 知识点）。
- `internal/review/`：审核服务（多轮流程、状态流转）。
- `internal/audit/`：审计日志服务。
- `internal/auth/`：JWT 认证 + RBAC 权限。
- `internal/api/`：HTTP REST API（handler 按资源拆分）。
- `internal/pipeline/`：业务编排层，串联各服务。
- `internal/storage/interfaces.go`：所有存储接口定义。
- `internal/storage/postgres/`：PostgreSQL 实现（schema.sql + store.go）。
- `internal/storage/testutil/`：内存实现（仅供测试）。

### 前端 Vue 3

- `web/src/App.vue`：根组件（侧边栏、用户信息、昵称编辑）。
- `web/src/api.js`：API 服务层（封装所有后端接口）。
- `web/src/auth.js`：认证状态管理（token、用户信息）。
- `web/src/router.js`：路由配置 + 登录守卫。
- `web/src/pages/`：8 个页面组件。

### 配置文件

- `.env`：环境变量（API Key、数据库连接），不提交 git。
- `configs/example.env`：环境变量示例。
- `configs/review_flows.json`：审核流程配置。

后续开发应围绕 `功能需求文档.txt` 的树状图继续，不要主动增加无关模块。
