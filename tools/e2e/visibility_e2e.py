from playwright.sync_api import sync_playwright

results = []
def check(name, ok, detail=""):
    results.append((name, ok))
    print(("PASS" if ok else "FAIL"), "|", name, "|", detail)

with sync_playwright() as p:
    browser = p.chromium.launch(headless=True, channel="chrome")
    page = browser.new_page(viewport={"width": 1440, "height": 900})

    # ===== expert 视角：题库仅本人题目，子题库目录不泄露 =====
    page.goto("http://127.0.0.1:5173/login")
    page.wait_for_load_state("networkidle")
    page.get_by_placeholder("请输入用户名").fill("expert")
    page.get_by_placeholder("请输入密码").fill("expert")
    page.get_by_role("button", name="登录").click()
    page.wait_for_timeout(2000)
    print("expert login URL:", page.url)
    page.goto("http://127.0.0.1:5173/bank")
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(1800)
    body = page.inner_text("body")
    check("expert 题库页不显示管理员测试子题库", "管理员第一次测试" not in body, "")
    # 全局题库入口对 expert 不可见（无 view_global）
    check("expert 不显示全局题库切换", "全局题库" not in body, "")
    page.screenshot(path="/tmp/e2e_expert_bank.png", full_page=True)

    # ===== admin（super_admin）视角：仍然看到全部 =====
    page.goto("http://127.0.0.1:5173/login")
    page.wait_for_load_state("networkidle")
    # 退出 expert
    page.goto("http://127.0.0.1:5173/bank")
    page.wait_for_timeout(500)
    logout = page.get_by_text("退出", exact=True)
    if logout.count():
        logout.first.click()
        page.wait_for_timeout(1200)
    page.goto("http://127.0.0.1:5173/login")
    page.wait_for_load_state("networkidle")
    page.get_by_placeholder("请输入用户名").fill("admin")
    page.get_by_placeholder("请输入密码").fill("admin")
    page.get_by_role("button", name="登录").click()
    page.wait_for_timeout(2000)
    page.goto("http://127.0.0.1:5173/bank")
    page.wait_for_load_state("networkidle")
    page.wait_for_timeout(1800)
    body = page.inner_text("body")
    check("admin（super_admin）仍可见全部题库列表", "男，38岁" in body or "男，38 岁" in body, "legacy 题可见")

    browser.close()

failed = [n for n, ok in results if not ok]
print(f"===== 汇总: {len(results)-len(failed)}/{len(results)} 通过 =====")
for n in failed:
    print("FAILED:", n)
