import os
import sys

from playwright.sync_api import sync_playwright

BASE = os.environ.get("AIGO_E2E_BASE_URL", "http://127.0.0.1:5173")

results = []
def check(name, ok, detail=""):
    results.append((name, ok))
    print(("PASS" if ok else "FAIL"), "|", name, "|", detail)

with sync_playwright() as p:
    browser = p.chromium.launch(headless=True, channel="chrome")
    page = browser.new_page(viewport={"width": 1440, "height": 900})

    # ===== expert 视角：题库仅本人题目，旧分类目录不泄露 =====
    page.goto(BASE + "/login")
    page.wait_for_load_state("networkidle")
    page.get_by_placeholder("请输入用户名").fill("expert")
    page.get_by_placeholder("请输入密码").fill("expert")
    page.get_by_role("button", name="登录").click()
    page.wait_for_timeout(2000)
    print("expert login URL:", page.url)
    page.goto(BASE + "/bank")
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(1800)
    body = page.inner_text("body")
    check("expert 题库页不显示旧分类子题库", "分类子题库" not in body and "管理员第一次测试" not in body, "")
    # 全局题库入口对 expert 不可见（无 view_global）
    check("expert 不显示全局题库切换", "全局题库" not in body, "")
    page.screenshot(path="/tmp/e2e_expert_bank.png", full_page=True)

    # ===== admin（super_admin）视角：未送审题保留，旧分类数据为空 =====
    page.goto(BASE + "/login")
    page.wait_for_load_state("networkidle")
    # 退出 expert
    page.goto(BASE + "/bank")
    page.wait_for_timeout(500)
    logout = page.get_by_text("退出", exact=True)
    if logout.count():
        logout.first.click()
        page.wait_for_timeout(1200)
    page.goto(BASE + "/login")
    page.wait_for_load_state("networkidle")
    page.get_by_placeholder("请输入用户名").fill("admin")
    page.get_by_placeholder("请输入密码").fill("admin")
    page.get_by_role("button", name="登录").click()
    page.wait_for_timeout(2000)
    page.goto(BASE + "/bank")
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(1800)
    token = page.evaluate("localStorage.getItem('aigo_token')")
    headers = {"Authorization": "Bearer " + token}
    questions = page.request.get(BASE + "/api/questions?page=1&page_size=1&scope=personal", headers=headers)
    banks = page.request.get(BASE + "/api/banks", headers=headers)
    check("admin 未送审题仍保留", questions.status == 200 and questions.json().get("total", 0) > 0, f"total={questions.json().get('total', 0)}")
    check("旧分类子题库数据已清空", banks.status == 200 and banks.json().get("total", 0) == 0, banks.text()[:160])

    browser.close()

failed = [n for n, ok in results if not ok]
print(f"===== 汇总: {len(results)-len(failed)}/{len(results)} 通过 =====")
for n in failed:
    print("FAILED:", n)
sys.exit(1 if failed else 0)
