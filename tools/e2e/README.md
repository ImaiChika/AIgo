# E2E 浏览器回归脚本（本地开发环境）

本目录固化关键业务流程的 Playwright 网页模拟测试。它们是真实浏览器驱动的
端到端回归：登录、生成、审核、题库可见性、归档删除等链路在每次后端/前端
改动后应全数通过。

## 前置条件

1. 本地完整栈已启动（仓库根 `./start.sh`：PostgreSQL + 后端 8080 + Vite 5173）；
2. Python + Playwright（Chromium/Chrome headless），安装：
   `pip install playwright && playwright install chromium`（或用系统 Chrome 通道）；
3. `psql` 可用（脚本会直接种入/清理测试数据）；
4. 本地库存在演示账号 `admin/admin`、`expert/expert`（见进展指引 §14）。

## 运行

```bash
cd tools/e2e
python3 visibility_e2e.py        # 题目可见性三档（expert 仅本人 / admin 全量）
python3 full_regression.py       # 生成全周期：提交→pending→刷新恢复→AI检查→题库命中
python3 review_todo_e2e.py       # 审核三页：待我审核→决断→待我修改（会真实决断本地任务）
python3 archive_deletion_e2e.py  # 删除即归档：published 题删除→淘汰题库→记录保留
python3 export_e2e.py            # Excel/Word 导出：字段完整、无命题人占位符、文件清理
python3 personal_center_e2e.py    # 个人工作台、零权限新用户、昵称/密码、移动端与清理
python3 review_pagination_e2e.py  # 待审/决断分页，终态题全部评论与分享页按需展开
python3 current_workflow_e2e.py   # 当前主链路：无分类送审→双轮审核→最终决断；身份切换状态回归
```

环境变量（均可选）：

- `AIGO_E2E_BASE_URL`：前端地址，默认 `http://127.0.0.1:5173`
- `AIGO_E2E_DB_DSN`：本地库 DSN，默认 `postgres://localhost:5432/aigo`

## 副作用与数据说明

- 脚本会向本地库写入测试数据并在结束时清理（`zdiag_*` 账号、`e2e-*`/`gen-*`
  运行记录、admin 名下近 30 分钟的测试题）；
- `review_todo_e2e.py` 会对本地 conflict 任务做出**真实决断**（退回修改），
  重复运行前需重置该任务或重新造数——这是有状态的审核流测试；
- `full_regression.py` 每次运行通过真实千问 API 生成 1 道题（计费）；
- 请勿对生产库运行：脚本假设演示账号与本地数据形态。

## 约定

- 新增端到端回归时：断言优先用**业务结果**（计数、状态、按题目 ID 命中），
  避免依赖界面文案细节；对带 `confirm()` 的按钮需注册 `page.on("dialog", ...)`;
- 管理员账号在题库页默认进入全局题库视图（按分享状态过滤），针对个人轴的
  断言须先切回「我的题库」。
