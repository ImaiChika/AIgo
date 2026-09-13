"""个人中心 E2E：管理员概览、零权限新用户、昵称/密码与清理。"""

import json
import os
import subprocess
import sys
import time
import uuid
from datetime import datetime, timezone

from playwright.sync_api import sync_playwright


BASE = os.environ.get("AIGO_E2E_BASE_URL", "http://127.0.0.1:5173")
ADMIN_USERNAME = os.environ.get("AIGO_E2E_ADMIN_USERNAME", "admin")
ADMIN_PASSWORD = os.environ.get("AIGO_E2E_ADMIN_PASSWORD", "admin")
DB_DSN = os.environ.get("AIGO_E2E_DB_DSN", "")
stamp = time.strftime("%Y%m%d%H%M%S") + "-" + uuid.uuid4().hex[:6]
username = f"personal_uat_{stamp}"
display_name = f"个人中心测试-{stamp}"
updated_name = f"昵称已更新-{stamp}"
old_password = f"Personal-{stamp}-old"
new_password = f"Personal-{stamp}-new"
started_at = datetime.now(timezone.utc).isoformat()
results = []
browser_errors = []
admin_token = ""
created_user_id = ""


def check(name, ok, detail=""):
    results.append((name, ok, detail))
    print(("PASS" if ok else "FAIL"), "|", name, "|", detail)


def listen(page, label):
    page.on("console", lambda message: browser_errors.append(f"{label}: {message.text}") if message.type == "error" else None)
    page.on("pageerror", lambda error: browser_errors.append(f"{label}: {error}"))
    page.on("requestfailed", lambda request: browser_errors.append(f"{label}: {request.url}"))


def login(page, username_value, password_value):
    page.goto(BASE + "/login")
    page.wait_for_load_state("networkidle")
    page.get_by_placeholder("请输入用户名").fill(username_value)
    page.get_by_placeholder("请输入密码").fill(password_value)
    page.get_by_role("button", name="登录", exact=True).click()
    page.wait_for_url("**/my", timeout=20000)
    page.wait_for_timeout(500)
    return page.evaluate("localStorage.getItem('aigo_token')")


def admin_users(page):
    response = page.request.get(BASE + "/api/users", headers={"Authorization": "Bearer " + admin_token})
    return response.json().get("users", []) if response.ok else []


def cleanup_local_traces():
    if not DB_DSN:
        return
    safe_started_at = started_at.replace("'", "''")
    safe_username = username.replace("'", "''")
    sql = f"""
        DELETE FROM audit_logs
        WHERE created_at >= '{safe_started_at}'::timestamptz
          AND (detail LIKE '%{safe_username}%' OR actor='{safe_username}' OR action IN ('auth_login_failed','auth_rate_limited'));
        DELETE FROM auth_login_limits WHERE updated_at >= '{safe_started_at}'::timestamptz;
    """
    subprocess.run(
        ["psql", DB_DSN, "-v", "ON_ERROR_STOP=1", "-c", sql],
        capture_output=True,
        text=True,
        timeout=15,
    )


with sync_playwright() as playwright:
    browser = playwright.chromium.launch(headless=True, channel="chrome")
    admin_context = browser.new_context(viewport={"width": 1440, "height": 900})
    admin_page = admin_context.new_page()
    listen(admin_page, "admin")
    user_context = None
    try:
        admin_token = login(admin_page, ADMIN_USERNAME, ADMIN_PASSWORD)
        check("登录后默认进入我的工作台", admin_page.url.endswith("/my"), admin_page.url)
        personal_group = admin_page.locator(".navigation-group.contains-active")
        check("个人中心导航独立显示", personal_group.locator(".navigation-group-button").filter(has_text="个人中心").count() == 1 and admin_page.locator('a[href="/my"]').count() == 1)
        check("管理员工作台加载待办与题库", "我的待办" in admin_page.inner_text("body") and "我的题库" in admin_page.inner_text("body") and "部分数据暂不可用" not in admin_page.inner_text("body"))
        headers = {"Authorization": "Bearer " + admin_token}
        expected_review = admin_page.request.get(BASE + "/api/review/my-tasks", headers=headers).json().get("total", 0)
        expected_formal = admin_page.request.get(BASE + "/api/questions?page=1&page_size=1&tier=formal&scope=personal", headers=headers).json().get("total", 0)
        displayed_review = int(admin_page.locator('.todo-row[data-kind="review"] strong').inner_text())
        displayed_formal = int(admin_page.locator('.metric-card[data-kind="formal"] strong').inner_text())
        check("工作台待办数量与接口一致", displayed_review == expected_review, f"page={displayed_review} api={expected_review}")
        check("工作台题库数量与接口一致", displayed_formal == expected_formal, f"page={displayed_formal} api={expected_formal}")

        formal_link = admin_page.locator("a.metric-card.green")
        formal_link.click()
        admin_page.wait_for_url("**/bank?scope=personal&tier=formal")
        admin_page.wait_for_timeout(600)
        check("题库数量卡直达个人正式题库", admin_page.locator(".scope-tab.active").inner_text() == "我的题库" and admin_page.locator(".tier-tab.active").inner_text() == "正式题库")

        admin_page.locator(".user-detail").click()
        admin_page.wait_for_url("**/settings")
        check("侧栏账号入口打开个人设置", admin_page.get_by_role("heading", name="账号信息").count() == 1 and admin_page.locator("#personal-display-name").count() == 1)

        admin_page.set_viewport_size({"width": 390, "height": 844})
        for route in ["/my", "/settings"]:
            admin_page.goto(BASE + route)
            admin_page.wait_for_load_state("networkidle")
            dimensions = admin_page.evaluate("({width: document.documentElement.scrollWidth, viewport: innerWidth})")
            check("移动端" + route + "无横向溢出", dimensions["width"] <= dimensions["viewport"] + 2, json.dumps(dimensions))
        admin_page.set_viewport_size({"width": 1440, "height": 900})

        user_context = browser.new_context(viewport={"width": 1280, "height": 820})
        user_page = user_context.new_page()
        listen(user_page, "new-user")
        user_page.goto(BASE + "/login")
        user_page.wait_for_load_state("networkidle")
        user_page.get_by_text("注册新账号", exact=True).click()
        user_page.get_by_placeholder("登录用户名").fill(username)
        user_page.get_by_placeholder("真实姓名（可选）").fill(display_name)
        user_page.get_by_placeholder("至少8位").fill(old_password)
        user_page.get_by_placeholder("再次输入密码").fill(old_password)
        user_page.get_by_role("button", name="注册", exact=True).click()
        user_page.get_by_text("注册成功，请等待管理员分配权限后登录", exact=True).wait_for(timeout=15000)
        check("新用户注册成功", True, username)

        users = admin_users(admin_page)
        created = next((user for user in users if user.get("username") == username), None)
        created_user_id = created.get("id", "") if created else ""
        check("新用户保持零权限", bool(created) and not created.get("role") and not created.get("permissions") and not created.get("bank_ids"), created_user_id)

        login(user_page, username, old_password)
        body = user_page.inner_text("body")
        check("零权限用户仍可使用个人中心", "暂无待办" in body and "暂无题库权限" in body and user_page.get_by_role("link", name="个人设置", exact=True).count() >= 1)
        check("零权限导航不泄露业务入口", user_page.get_by_text("系统管理", exact=True).count() == 0 and user_page.get_by_role("link", name="题库", exact=True).count() == 0)

        user_page.goto(BASE + "/settings")
        user_page.wait_for_load_state("networkidle")
        user_page.locator("#personal-display-name").fill(updated_name)
        user_page.get_by_role("button", name="保存", exact=True).click()
        user_page.get_by_text("已保存", exact=True).wait_for(timeout=10000)
        check("个人设置修改昵称", updated_name in user_page.locator(".user-detail").inner_text())

        user_page.locator("#current-password").fill(old_password)
        user_page.locator("#new-password").fill(new_password)
        user_page.locator("#confirm-password").fill(new_password + "x")
        user_page.get_by_role("button", name="修改密码", exact=True).click()
        check("密码不一致前端拦截", user_page.get_by_text("两次输入不一致", exact=True).count() == 1)
        user_page.locator("#confirm-password").fill(new_password)
        user_page.get_by_role("button", name="修改密码", exact=True).click()
        user_page.get_by_text("密码已修改", exact=True).wait_for(timeout=10000)
        check("个人设置修改密码", True)

        user_page.get_by_role("button", name="退出", exact=True).click()
        user_page.wait_for_url("**/login")
        user_page.get_by_placeholder("请输入用户名").fill(username)
        user_page.get_by_placeholder("请输入密码").fill(old_password)
        user_page.get_by_role("button", name="登录", exact=True).click()
        user_page.get_by_text("用户名或密码错误", exact=True).wait_for(timeout=10000)
        check("旧密码立即失效", True)
        login(user_page, username, new_password)
        check("新密码重新登录", user_page.url.endswith("/my"))

        admin_page.goto(BASE + "/users")
        admin_page.wait_for_load_state("networkidle")
        row = admin_page.locator("tbody tr").filter(has_text=username).first
        admin_page.on("dialog", lambda dialog: dialog.accept())
        row.get_by_role("button", name="删除", exact=True).click()
        admin_page.wait_for_timeout(800)
        check("临时账号清理", not any(user.get("id") == created_user_id for user in admin_users(admin_page)))
    finally:
        if created_user_id and admin_token and any(user.get("id") == created_user_id for user in admin_users(admin_page)):
            admin_page.request.delete(BASE + "/api/users/" + created_user_id, headers={"Authorization": "Bearer " + admin_token})
        cleanup_local_traces()
        if user_context:
            user_context.close()
        admin_context.close()
        browser.close()

real_errors = [error for error in browser_errors if "favicon" not in error and "401 (Unauthorized)" not in error]
check("无前端控制台或请求错误", not real_errors, "; ".join(real_errors[:4]))
failed = [item for item in results if not item[1]]
print(f"\n===== 汇总: {len(results) - len(failed)}/{len(results)} 通过 =====")
for name, _, detail in failed:
    print("FAILED:", name, "|", detail)
sys.exit(1 if failed else 0)
