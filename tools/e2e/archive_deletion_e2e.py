# 归档删除 E2E：published 题删除 → 归档 → 淘汰题库可见（"已归档"标签）
import subprocess
import sys

from playwright.sync_api import sync_playwright

import os as _os

BASE = _os.environ.get("AIGO_E2E_BASE_URL", "http://127.0.0.1:5173")
DB_DSN = _os.environ.get("AIGO_E2E_DB_DSN", "postgres://localhost:5432/aigo")


def _psql(query, db=_os.environ.get("AIGO_E2E_DB_DSN", "postgres://localhost:5432/aigo")):
    """执行一条 psql 命令（本地开发库），返回 stdout.strip()。"""
    import subprocess
    r = subprocess.run(["psql", db, "-tAc", query], capture_output=True, text=True, timeout=10)
    if r.returncode != 0:
        raise RuntimeError(f"psql failed: {r.stderr.strip()}")
    return r.stdout.strip()

STEM = "归档删除端到端测试题干"
results = []


def check(name, ok, detail=""):
    results.append((name, ok))
    print(("PASS" if ok else "FAIL"), "|", name, "|", detail)


def psql(query):
    return _psql(query)


# ===== 准备：published 题 + 审核任务 + 记录（归档后验证保留） =====
import subprocess

QID = "e2e-arch-0001"

def psqlx(query):
    # 带错误上抛的写库（种子失败必须立刻暴露，不允许静默继续）
    r = subprocess.run(["psql", DB_DSN, "-c", query],
                       capture_output=True, text=True, timeout=10)
    if r.returncode != 0:
        raise RuntimeError(f"seed SQL failed: {r.stderr.strip()}")

psqlx("DELETE FROM review_records WHERE task_id='e2e-arch-task'")
psqlx("DELETE FROM review_tasks WHERE id='e2e-arch-task'")
psqlx(f"DELETE FROM questions WHERE id='{QID}'")
psqlx(f"""INSERT INTO questions (id, clinical_stem, options, answer, status, version, owner_id, created_by, search_text, created_at, updated_at)
SELECT '{QID}', '归档删除端到端测试题干', '[{{"label":"A","text":"选项"}}]'::jsonb, 'A', 'published', 1, id, 'admin', lower('{QID}'), NOW(), NOW() FROM users WHERE username='admin'""")
psqlx(f"""INSERT INTO review_tasks (id, question_id, flow_id, status, current_round, assigned_to, question_version, attempt, created_at, updated_at)
SELECT 'e2e-arch-task', '{QID}', id, 'published', 1, ARRAY[(SELECT id FROM users WHERE username='expert')::text], 1, 1, NOW(), NOW() FROM review_flows LIMIT 1""")
psqlx(f"""INSERT INTO review_records (id, task_id, question_id, round_number, expert_id, conclusion, comment, created_at)
SELECT 'e2e-arch-rec', 'e2e-arch-task', '{QID}', 1, (SELECT id FROM users WHERE username='expert'), 'approved', '{{"stem":"题干合格"}}'::jsonb, NOW()""")
before_count = psql("SELECT COUNT(*) FROM questions")

with sync_playwright() as p:
    browser = p.chromium.launch(headless=True, channel="chrome")
    page = browser.new_page(viewport={"width": 1440, "height": 900})
    page.on("dialog", lambda d: d.accept())  # confirm 删除对话框自动确认

    page.goto(BASE + "/login")
    page.wait_for_load_state("networkidle")
    page.get_by_placeholder("请输入用户名").fill("admin")
    page.get_by_placeholder("请输入密码").fill("admin")
    page.get_by_role("button", name="登录").click()
    page.wait_for_timeout(2000)

    # ===== 1. 正式题库：搜到该题并删除 =====
    page.goto(BASE + "/bank")
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(1500)
    scope_btn = page.locator(".scope-tab", has_text="我的题库")
    if scope_btn.count():
        scope_btn.first.click()
        page.wait_for_timeout(1000)
    page.get_by_text("正式题库", exact=True).first.click()
    page.wait_for_timeout(1000)
    page.locator(".search-input").first.fill(QID)
    with page.expect_response(lambda r: "/api/questions/search" in r.url, timeout=20000) as resp_info:
        page.get_by_role("button", name="搜索", exact=True).click()
    resp = resp_info.value
    print("  [diag] search url=", resp.url, "status=", resp.status)
    search_body = resp.json()
    print("  [diag] search total=", search_body.get("total"))
    page.wait_for_timeout(500)
    print("  [diag] stem in body after response:", STEM in page.inner_text("body"))
    print("  [diag] delete-btn count:", page.locator("button.delete-btn").count())
    # 卡片不渲染题目 ID：以题干文本等待结果
    try:
        page.wait_for_selector(f"text={STEM}", timeout=8000)
        check("正式题库可搜到待删题", True, "")
    except Exception:
        page.screenshot(path="/tmp/e2e_fail_arch_search.png", full_page=True)
        check("正式题库可搜到待删题", False, page.inner_text("body")[:200].replace(chr(10), " | "))
        sys.exit(1)
    card = page.locator("button.delete-btn").first
    check("删除按钮可见", card.count() > 0 and card.is_visible(), "")
    if card.count():
        card.click()
        # 归档 toast 出现即确认删除/归档动作生效
        try:
            page.wait_for_selector("text=已归档，题目移入淘汰题库", timeout=6000)
            check("删除提示为归档语义", True, "")
        except Exception:
            body = page.inner_text("body")
            check("删除提示为归档语义", False, body[:120].replace(chr(10), " | "))
        page.wait_for_timeout(800)

    # ===== 2. 淘汰题库：出现该题且标注“已归档” =====
    page.get_by_text("淘汰题库", exact=True).first.click()
    page.wait_for_timeout(1200)
    page.locator(".search-input").first.fill(QID)
    page.get_by_role("button", name="搜索", exact=True).click()
    page.wait_for_timeout(1500)
    body = page.inner_text("body")
    check("淘汰题库可见归档题", STEM in body, "")
    check("归档题标注「已归档」", STEM in body and "已归档" in body, "")

    # ===== 3. 待审核层不再显示该题 =====
    scope_btn2 = page.locator(".scope-tab", has_text="我的题库")
    if scope_btn2.count():
        scope_btn2.first.click()
        page.wait_for_timeout(800)
    page.get_by_text("待审核题库", exact=True).first.click()
    page.wait_for_timeout(1200)
    page.locator(".search-input").first.fill(QID)
    page.get_by_role("button", name="搜索", exact=True).click()
    page.wait_for_timeout(1500)
    body = page.inner_text("body")
    check("待审核层不再显示归档题", STEM not in body, "")

    # ===== 4. 后端状态：题目保留、审核记录保留 =====
    status = psql(f"SELECT status FROM questions WHERE id='{QID}'")
    check("数据库状态为 archived", status == "archived", f"status={status}")
    rec = psql("SELECT COUNT(*) FROM review_records WHERE question_id='" + QID + "'")
    check("审核记录完整保留", rec == "1", f"records={rec}")

    browser.close()

# ===== 清理 =====
psql("DELETE FROM review_records WHERE task_id='e2e-arch-task'")
psql("DELETE FROM review_tasks WHERE id='e2e-arch-task'")
psql(f"DELETE FROM questions WHERE id='{QID}'")

failed = [n for n, ok in results if not ok]
print(f"\n===== 汇总: {len(results)-len(failed)}/{len(results)} 通过 =====")
for n in failed:
    print("FAILED:", n)
sys.exit(1 if failed else 0)
