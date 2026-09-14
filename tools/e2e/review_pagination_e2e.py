"""审核待办分页与终态完整评语展示 E2E（只读）。"""

import os
import subprocess
import sys

from playwright.sync_api import sync_playwright


BASE = os.environ.get("AIGO_E2E_BASE_URL", "http://127.0.0.1:5173")
ADMIN_USERNAME = os.environ.get("AIGO_E2E_ADMIN_USERNAME", "admin")
ADMIN_PASSWORD = os.environ.get("AIGO_E2E_ADMIN_PASSWORD", "admin")
DB_DSN = os.environ.get("AIGO_E2E_DB_DSN", "")
QUESTION_ID = os.environ.get("AIGO_E2E_REVIEWED_QUESTION_ID", "")
EXPECT_SHARE = os.environ.get("AIGO_E2E_EXPECT_SHARE_HISTORY", "") == "1"
results = []
errors = []


def check(name, ok, detail=""):
    results.append((name, ok, detail))
    print(("PASS" if ok else "FAIL"), "|", name, "|", detail, flush=True)


def resolve_question_id():
    if QUESTION_ID:
        return QUESTION_ID
    if not DB_DSN:
        return ""
    query = """
        SELECT q.id
        FROM questions q
        JOIN review_tasks t ON t.question_id=q.id
        JOIN review_records r ON r.task_id=t.id
        WHERE q.status='published'
        GROUP BY q.id
        ORDER BY (bool_or(r.conclusion='approved') AND bool_or(r.conclusion='rejected')) DESC,
                 COUNT(NULLIF(r.opinion,'')) DESC, COUNT(r.id) DESC, q.id
        LIMIT 1
    """
    proc = subprocess.run(["psql", DB_DSN, "-Atc", query], capture_output=True, text=True, timeout=10)
    return proc.stdout.strip() if proc.returncode == 0 else ""


with sync_playwright() as playwright:
    browser = playwright.chromium.launch(headless=True, channel="chrome")
    page = browser.new_page(viewport={"width": 1440, "height": 900})
    page.on("console", lambda message: errors.append(message.text) if message.type == "error" else None)
    page.on("pageerror", lambda error: errors.append(str(error)))
    page.goto(BASE + "/login")
    page.wait_for_load_state("networkidle")
    page.get_by_placeholder("请输入用户名").fill(ADMIN_USERNAME)
    page.get_by_placeholder("请输入密码").fill(ADMIN_PASSWORD)
    page.get_by_role("button", name="登录", exact=True).click()
    page.wait_for_url("**/my", timeout=20000)
    token = page.evaluate("localStorage.getItem('aigo_token')")
    headers = {"Authorization": "Bearer " + token}

    task_total = page.request.get(BASE + "/api/review/my-tasks", headers=headers).json().get("total", 0)
    page.goto(BASE + "/review")
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(500)
    cards = page.locator(".my-task-card")
    pager = page.locator(".compact-pager")
    check("待审任务每页最多20条", cards.count() == min(20, task_total), f"cards={cards.count()} total={task_total}")
    check("待审任务分页栏固定显示", pager.count() == (1 if task_total else 0))
    if task_total > 20:
        first = cards.first.inner_text()
        before = pager.bounding_box()["y"]
        page.locator(".my-task-list").evaluate("element => element.scrollTop = element.scrollHeight")
        after = pager.bounding_box()["y"]
        page.locator(".compact-pager button").filter(has_text="2").click()
        page.wait_for_timeout(250)
        check("待审任务页码切换", cards.first.inner_text() != first and page.locator(".compact-pager button.active").inner_text() == "2")
        check("待审分页不随列表滚动", abs(before - after) < 1, f"y={before}/{after}")

    decision_total = page.request.get(BASE + "/api/review/my-decisions", headers=headers).json().get("total", 0)
    page.goto(BASE + "/review-decisions")
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(400)
    check("最终决断每页最多20条", page.locator(".decision-card").count() == min(20, decision_total), f"total={decision_total}")
    check("最终决断使用同一分页栏", page.locator(".compact-pager").count() == (1 if decision_total else 0))

    question_id = resolve_question_id()
    if question_id:
        task_response = page.request.get(BASE + f"/api/review/task-by-question/{question_id}", headers=headers)
        task = task_response.json()
        records = page.request.get(BASE + f"/api/review/records/{task['id']}", headers=headers).json().get("records", [])

        page.goto(BASE + "/bank?scope=global&tier=formal")
        page.wait_for_load_state("networkidle")
        page.locator(".search-input").fill(question_id)
        page.get_by_role("button", name="搜索", exact=True).click()
        page.wait_for_timeout(500)
        row = page.locator(".question-item")
        check("正式题库定位终态题目", row.count() == 1, question_id)
        if row.count():
            row.first.locator(".q-content").click()
            page.locator(".review-history").wait_for(timeout=10000)
            displayed = page.locator(".review-info-field .rc-card")
            text = page.locator(".review-info-field").inner_text()
            check("题库详情展示全部审核记录", displayed.count() == len(records), f"page={displayed.count()} api={len(records)}")
            written = [record.get("opinion", "").strip() for record in records if record.get("opinion", "").strip()]
            if written:
                check("全部书面评论文字可见", all(opinion in text for opinion in written), f"written={len(written)}")
            if {record.get("review_status") for record in records}.issuperset({"approved", "rejected"}):
                check("通过与反对意见并列可见", "通过" in text and "驳回" in text)

        page.goto(BASE + "/review-results")
        page.wait_for_load_state("networkidle")
        page.get_by_role("button", name="全局题库", exact=True).click()
        page.wait_for_timeout(350)
        page.get_by_placeholder("搜索题干、选项、解析、专业、系统、知识点或ID...").fill(question_id)
        page.get_by_role("button", name="搜索", exact=True).click()
        page.wait_for_timeout(500)
        result = page.locator(".result-card")
        if result.count():
            result.first.locator(".result-head").click()
            page.wait_for_timeout(250)
        check("审核记录页展示全部终态评论", result.count() == 1 and result.first.locator(".rc-card").count() == len(records))

        page.goto(BASE + "/share-requests")
        page.wait_for_load_state("networkidle")
        all_button = page.get_by_role("button", name="全部记录", exact=True)
        if all_button.count():
            all_button.click()
            page.wait_for_timeout(500)
        share_card = page.locator(".request-card").filter(has_text=question_id).first
        if share_card.count():
            share_card.get_by_role("button", name="查看审核意见", exact=True).click()
            share_card.locator(".review-history").wait_for(timeout=10000)
            check("分享页按需展示全部终态评论", share_card.locator(".rc-card").count() == len(records))
        elif EXPECT_SHARE:
            check("分享页按需展示全部终态评论", False, "未找到指定分享记录")
    else:
        print("SKIP | 终态评论展示 | 未提供或未找到带审核记录的已通过题目", flush=True)

    real_errors = [error for error in errors if "favicon" not in error]
    check("无前端控制台错误", not real_errors, "; ".join(real_errors[:3]))
    browser.close()

failed = [result for result in results if not result[1]]
print(f"\n===== 汇总: {len(results) - len(failed)}/{len(results)} 通过 =====")
for name, _, detail in failed:
    print("FAILED:", name, "|", detail)
sys.exit(1 if failed else 0)
