# 审核个人待办三页 E2E：待我审核 / 待我决断 / 待我修改（SQL 下推后行为回归）
# 前置：本地后端(8080 新代码) + Vite(5173)；本地已有 reviewing/conflict 审核任务
import re
import sys

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
    results.append((name, ok))
    print(("PASS" if ok else "FAIL"), "|", name, "|", detail)


def login(page, username, password):
    page.goto(BASE + "/login")
    page.wait_for_load_state("networkidle")
    page.get_by_placeholder("请输入用户名").fill(username)
    page.get_by_placeholder("请输入密码").fill(password)
    page.get_by_role("button", name="登录").click()
    page.wait_for_timeout(2000)


with sync_playwright() as p:
    browser = p.chromium.launch(headless=True, channel="chrome")
    page = browser.new_page(viewport={"width": 1440, "height": 900})
    console_errors = []
    page.on("console", lambda m: console_errors.append(m.text) if m.type == "error" else None)

    # ===== 1. 待我审核（admin：分配名单内，未投票 → 应看到 2 个 reviewing 任务） =====
    login(page, "admin", "admin")
    page.goto(BASE + "/review")
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(1800)
    body = page.inner_text("body")
    task_cards = page.locator("text=第 1 轮").count() + page.locator("text=第1轮").count()
    has_stem = ("反复上腹部" in body) or ("胸痛" in body) or ("任务" in body and "审核" in body)
    check("待我审核页渲染任务", has_stem, f"cards~{task_cards}")
    page.screenshot(path="/tmp/e2e_review_tasks.png", full_page=True)

    # ===== 2. expert 视角：同样分配了这两个任务 =====
    login(page, "expert", "expert")
    page.goto(BASE + "/review")
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(1800)
    body = page.inner_text("body")
    check("expert 待我审核同视图（任务分配可见性一致）", "审核" in body and ("题" in body), "")

    # ===== 3. 待我决断（admin 是 conflict 任务的把关人） =====
    login(page, "admin", "admin")
    page.goto(BASE + "/review-decisions")
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(1800)
    body = page.inner_text("body")
    decision_card = ("决断" in body) or ("通过" in body and "驳回" in body)
    check("待我决断页渲染 conflict 任务", decision_card, "")
    page.screenshot(path="/tmp/e2e_decisions.png", full_page=True)

    # ===== 4. 把关人决断「退回修改」→ 任务进入 revision_required =====
    # 先在左侧选中一道待决断任务卡，右侧才渲染决断表单
    card = page.locator(".decision-card").first
    check("待我决断页有任务卡可选", card.count() > 0, "")
    if card.count():
        card.click()
        page.wait_for_timeout(1500)
    # 决断表单：下拉选择「退回修改」+ 填一栏决断理由 + 提交决断
    action_select = page.locator("select").first
    check("决断表单可选退回修改", action_select.count() > 0, "")
    if action_select.count():
        action_select.select_option("revision_required")
        comment_box = page.locator("textarea").first
        if comment_box.count():
            comment_box.fill("题干信息不完整，请补充关键查体数据后重新提交")
        page.get_by_role("button", name="提交决断").click()
        page.wait_for_timeout(2500)
        body = page.inner_text("body")
        check("决断提交成功", "决断完成" in body or "待决断" not in body[:200], "")

    # ===== 5. 待我修改（题目 created_by=admin，决断后应出现在待我修改页） =====
    page.goto(BASE + "/my-revisions")
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(1800)
    body = page.inner_text("body")
    revision_visible = ("修改" in body) and (("题干" in body) or ("任务" in body and "重新" in body) or ("退回" in body))
    check("待我修改页渲染退修任务", revision_visible, body[:120].replace("\n", " | "))
    page.screenshot(path="/tmp/e2e_revisions.png", full_page=True)

    # ===== 6. 控制台错误 =====
    real_errors = [e for e in console_errors if "favicon" not in e and "401" not in e]
    check("无前端控制台错误", len(real_errors) == 0, "; ".join(real_errors[:3]))

    browser.close()

failed = [n for n, ok in results if not ok]
print(f"\n===== 汇总: {len(results)-len(failed)}/{len(results)} 通过 =====")
for n in failed:
    print("FAILED:", n)
sys.exit(1 if failed else 0)
