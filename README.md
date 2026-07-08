# AIgo

基于 PDF 开题报告建立的 Go 框架，用于后续实现“AI 医学 A2 型试题自动生成、评估与人机协同优化”系统原型。

后续开发者必须先阅读：

- `CLAUDE.md`

## 快速检查

```bash
go run ./cmd/aigo doctor
go run ./cmd/aigo evaluate
```

## 前端原型

当前 Vue 3 + Vite 前端原型位于 `web/`，参考 PDF 中 AI 智能出题 demo 页面制作。

```bash
cd web
npm install
npm run dev -- --port 5174
```

然后访问 `http://127.0.0.1:5174`。

调用千问文本接口前，先设置环境变量：

```bash
export DASHSCOPE_API_KEY="..."
export QWEN_BASE_URL="https://dashscope.aliyuncs.com/compatible-mode/v1"
export QWEN_MODEL="qwen-plus"
```

## 当前状态

- 已确定主语言：Go。
- 已建立文本 LLM 客户端、领域类型、生成服务、评估服务、知识点导入、图片候选、审核流程和 CLI 入口。
- 已建立 Vue 3 + Vite 前端原型。
- 暂未实现数据库落库、前后端 API 对接、真实生图模型接入和完整专家审核页面。
