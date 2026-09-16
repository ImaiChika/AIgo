# AIgo 全面 UI 回归测试（本轮改动：知识点缓存、生成持久任务、异步提交 UI）
# 前置：本地后端(8080, 新代码) + Vite(5173) + 本地 PostgreSQL v24
import re
import sys
import time

from playwright.sync_api import sync_playwright

import os as _os

BASE = _os.environ.get("AIGO_E2E_BASE_URL", "http://127.0.0.1:5173")


def _psql(query, db=_os.environ.get("AIGO_E2E_DB_DSN", "postgres://localhost:5432/aigo")):
    """执行一条 psql 命令（本地开发库），返回 stdout.strip()。"""
    import subprocess
    r = subprocess.run(["psql", db, "-tAc", query], capture_output=True, text=True, timeout=10)
    if r.returncode != 0:
        raise RuntimeError(f"psql failed: {r.stderr.strip()}")
    return r.stdout.strip()

results = []


def check(name, ok, detail=""):
    results.append((name, ok, detail))
    print(("PASS" if ok else "FAIL"), "|", name, "|", detail)


with sync_playwright() as p:
    browser = p.chromium.launch(headless=True, channel="chrome")
    page = browser.new_page(viewport={"width": 1440, "height": 900})
    console_errors = []
    page.on("console", lambda m: console_errors.append(m.text) if m.type == "error" else None)

    # ===== 1. 登录 =====
    page.goto(BASE + "/login")
    page.wait_for_load_state("networkidle")
    page.get_by_placeholder("请输入用户名").fill("admin")
    page.get_by_placeholder("请输入密码").fill("admin")
    page.get_by_role("button", name="登录").click()
    page.wait_for_url("**/my", timeout=20000)
    check("登录并进入我的数据", True, page.url)

    # ===== 2. 知识点页回归（快照缓存改动） =====
    page.goto(BASE + "/knowledge")
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(1200)
    body = page.inner_text("body")
    has_total = re.search(r"共 \d+ 条", body) is not None
    rows = page.locator("tbody tr").count()
    check("知识点页加载列表", has_total and rows > 0, f"total_text={has_total} rows={rows}")
    # 分页
    next_btn = page.locator("button[aria-label='下一页']")
    if next_btn.count() and next_btn.is_enabled():
        first_topic = page.locator("tbody tr td .topic-title").first.inner_text()
        next_btn.click()
        page.wait_for_timeout(1000)
        second_topic = page.locator("tbody tr td .topic-title").first.inner_text()
        check("知识点分页翻页", second_topic != first_topic, f"{first_topic[:16]} -> {second_topic[:16]}")
        page.locator("button[aria-label='上一页']").click()
        page.wait_for_timeout(600)
    # 搜索
    page.get_by_placeholder(re.compile("搜索")).first.fill("消化性溃疡")
    page.keyboard.press("Enter")
    page.wait_for_timeout(1500)
    body = page.inner_text("body")
    m = re.search(r"共 (\d+) 条", body)
    check("知识点关键词搜索", m is not None and int(m.group(1)) > 0, f"total={m.group(1) if m else '?'}")
    page.get_by_placeholder(re.compile("搜索")).first.fill("")
    page.keyboard.press("Enter")
    page.wait_for_timeout(800)

    # ===== 3. 生成异步流程（用户报告的场景） =====
    page.goto(BASE + "/generate")
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(1000)
    body = page.inner_text("body")
    check("生成页已移除「目标题库」选择器", "目标题库" not in body, "")
    picker = page.get_by_placeholder(re.compile("搜索大纲"))
    picker.fill("消化性溃疡")
    page.wait_for_timeout(1200)
    items = page.locator(".kp-item")
    if items.count() == 0:
        picker.fill("溃疡")
        page.wait_for_timeout(1500)
        items = page.locator(".kp-item")
    ok_kp = items.count() > 0
    check("考试大纲选择器出结果", ok_kp, f"items={items.count()}")
    if not ok_kp:
        page.screenshot(path="/tmp/e2e_fail_picker.png", full_page=True)
        sys.exit(1)
    items.first.locator("input[type=radio], input[type=checkbox]").check()
    page.wait_for_timeout(400)
    page.get_by_text("1道", exact=True).click()
    page.wait_for_timeout(300)

    t0 = time.time()
    page.get_by_role("button", name="生成试题").click()
    # A: 立即出现“任务已提交”（pending 分支生效）
    try:
        page.wait_for_selector("text=任务已提交", timeout=4000)
        check("提交后立即显示「任务已提交」（pending 视为进行中）", True, f"elapsed={time.time()-t0:.1f}s")
    except Exception:
        check("提交后立即显示「任务已提交」（pending 视为进行中）", False, page.inner_text("body")[:200])
        page.screenshot(path="/tmp/e2e_fail_submit.png", full_page=True)
        sys.exit(1)
    # B: “已生成 0 道题”永远不允许出现
    saw_zero = "已生成 0 道题" in page.inner_text("body")
    check("未出现「已生成 0 道题」", not saw_zero, "")

    # C: 中途刷新 → 恢复流程（用户实际遇到的路径）
    page.wait_for_timeout(5000)
    page.reload()
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(1500)
    body = page.inner_text("body")
    recovering = ("已恢复命题任务" in body) or ("正在生成" in body) or ("质量初检" in body and "执行中" in body)
    zero_after_reload = "已生成 0 道题" in body
    check("刷新后进入恢复流程且无误判", recovering and not zero_after_reload,
          f"recovering={recovering} zero={zero_after_reload}")

    # D: 等待生成完成，题目出现在预览区
    deadline = time.time() + 240
    gen_done = False
    step1_duration = ""
    while time.time() < deadline:
        body = page.inner_text("body")
        if "已生成 0 道题" in body:
            check("生成完成前未误报 0 道题", False, "检测到 0 道题")
            break
        m = re.search(r"题目生成[\s\S]{0,120}?处理耗时\s*\n?\s*([^\n]+)", body)
        if m and m.group(1).strip() not in ("—", ""):
            step1_duration = m.group(1).strip()
        if "已生成 1 道题" in body:
            gen_done = True
            break
        page.wait_for_timeout(2000)
    check("异步生成完成并展示题目", gen_done, f"耗时{time.time()-t0:.0f}s 步骤1用时={step1_duration!r}")
    check("第一步耗时不为 0s", bool(step1_duration) and step1_duration not in ("0s", "0秒", "—"), step1_duration)

    # E: 等待 AI 质量检查完成（第二步 已完成）
    deadline = time.time() + 300
    check_done = False
    while time.time() < deadline:
        body = page.inner_text("body")
        if re.search(r"质量初检[\s\S]{0,160}?已完成", body):
            check_done = True
            break
        page.wait_for_timeout(2500)
    check("AI 质量检查完成（第二步已完成）", check_done, f"等待{time.time()-t0:.0f}s")
    page.screenshot(path="/tmp/e2e_generation_done.png", full_page=True)

    # F: 题库页可见新题目（工作层）；题干从数据库取（预览区为只读元素）
    stem_snippet = ""
    try:
        qid = _psql("SELECT (question_ids)[1] FROM generation_runs WHERE status='succeeded' ORDER BY started_at DESC LIMIT 1")
        if qid:
            stem_snippet = qid  # 用题目 ID 精确搜索，避免相似题干干扰
    except Exception as e:
        print("取题目ID失败:", e)
    page.goto(BASE + "/bank")
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(1500)
    check("题库页已移除分类子题库筛选", page.locator(".bank-filter-row").count() == 0, "")
    if stem_snippet:
        # 管理员默认进入全局题库作用域；新题在个人题库的待审核层，先切回“我的题库”
        scope_btn = page.locator(".scope-tab", has_text="我的题库")
        if scope_btn.count():
            scope_btn.first.click()
            page.wait_for_timeout(1000)
        tier_btn = page.locator(".tier-tab", has_text="待审核题库")
        if tier_btn.count():
            tier_btn.first.click()
            page.wait_for_timeout(1000)
        page.locator(".search-input").first.fill(stem_snippet)
        page.get_by_role("button", name="搜索", exact=True).click()
        page.wait_for_timeout(2000)
        body = page.inner_text("body")
        found = re.search(r"1 道", body) is not None
        if not found:
            page.screenshot(path="/tmp/e2e_fail_bank.png", full_page=True)
            print("  [diag] input value=", page.locator(".search-input").first.input_value())
            print("  [diag] body head=", body[:260].replace(chr(10), " | "))
        check("题库页可搜到新题（我的题库·待审核层，按题目ID搜索恰好1道）", found, f"qid={stem_snippet!r}")
    else:
        check("题库页可搜到新题（待审核层）", False, "未能取到题干片段")

    # G: 控制台错误
    real_errors = [e for e in console_errors if "favicon" not in e and "401" not in e]
    check("无前端控制台错误", len(real_errors) == 0, "; ".join(real_errors[:3]))

    # ===== 清理测试数据 =====
    dsn = _os.environ.get("AIGO_E2E_DB_DSN", "postgres://localhost:5432/aigo")
    import subprocess
    subprocess.run(["psql", dsn, "-c",
        "DELETE FROM questions WHERE status IN ('ai_draft','ai_reviewed') AND created_by='admin' AND created_at > NOW() - INTERVAL '30 minutes'"], capture_output=True)
    subprocess.run(["psql", dsn, "-v", "ON_ERROR_STOP=1", "-c",
        "DELETE FROM generation_runs WHERE started_at > NOW() - INTERVAL '30 minutes'"], capture_output=True, check=True)
    print("cleanup done")

    browser.close()

failed = [r for r in results if not r[1]]
print(f"\n===== 汇总: {len(results)-len(failed)}/{len(results)} 通过 =====")
for name, ok, detail in failed:
    print("FAILED:", name, "|", detail)
sys.exit(1 if failed else 0)
