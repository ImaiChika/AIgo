"""只读生产冒烟：登录、题库浏览、请求关联 ID 与管理员运行指标。"""

import os
import sys

from playwright.sync_api import sync_playwright


BASE = os.environ.get("AIGO_E2E_BASE_URL", "http://127.0.0.1:5173").rstrip("/")
USERNAME = os.environ.get("AIGO_E2E_ADMIN_USERNAME", "admin")
PASSWORD = os.environ.get("AIGO_E2E_ADMIN_PASSWORD", "")


def check(name, condition, detail=""):
    print(("PASS" if condition else "FAIL"), "|", name, "|", detail)
    if not condition:
        raise AssertionError(name)


if not PASSWORD:
    print("缺少 AIGO_E2E_ADMIN_PASSWORD", file=sys.stderr)
    raise SystemExit(2)


browser_errors = []
with sync_playwright() as playwright:
    browser = playwright.chromium.launch(headless=True, channel="chrome")
    context = browser.new_context(viewport={"width": 1440, "height": 900})
    page = context.new_page()
    page.on(
        "console",
        lambda message: browser_errors.append(f"console: {message.text}")
        if message.type == "error"
        else None,
    )
    page.on("pageerror", lambda error: browser_errors.append(f"pageerror: {error}"))
    page.on(
        "requestfailed",
        lambda request: browser_errors.append(f"requestfailed: {request.url}"),
    )
    try:
        page.goto(BASE + "/login", wait_until="networkidle")
        page.get_by_placeholder("请输入用户名").fill(USERNAME)
        page.get_by_placeholder("请输入密码").fill(PASSWORD)
        page.get_by_role("button", name="登录", exact=True).click()
        page.wait_for_url("**/my", timeout=20_000)
        page.wait_for_load_state("networkidle")
        check("管理员登录后进入我的数据", page.get_by_role("heading", name="我的数据", exact=True).count() == 1, page.url)

        token = page.evaluate("localStorage.getItem('aigo_token')")
        check("登录 token 已建立", bool(token))
        headers = {"Authorization": "Bearer " + token}

        metrics = page.request.get(BASE + "/api/system/runtime-metrics", headers=headers)
        check("超级管理员运行指标可读", metrics.status == 200, str(metrics.status))
        metrics_body = metrics.json()
        check(
            "运行指标字段完整",
            all(key in metrics_body for key in ("http", "process", "database_pool", "queues")),
        )

        health = page.request.get(BASE + "/health/live")
        request_id = health.headers.get("x-request-id", "")
        check("响应包含请求关联 ID", health.status == 200 and len(request_id) == 32, request_id[:4] + "...")

        page.goto(BASE + "/bank?scope=personal&tier=working", wait_until="networkidle")
        check("个人待审核题库可浏览", page.get_by_role("heading", name="题库", exact=True).count() == 1, page.url)
        check("浏览器控制台无错误", not browser_errors, "; ".join(browser_errors))
    finally:
        context.close()
        browser.close()
