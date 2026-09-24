"""Offline UI check: every /api request is intercepted; no model or DB is called."""
import json
from urllib.parse import urlparse

from playwright.sync_api import sync_playwright


POINTS = [
    {"id": f"kp-{i}", "version_id": "v-1", "category": "临床综合", "subject": "内科",
     "unit": "消化", "sub_item": "溃疡", "topic": f"模拟要点{i}", "outline_code": f"100.{i}"}
    for i in (1, 2)
]
QUOTA = {"single_max_questions": 20, "batch_max_questions": 100,
         "single_active_per_user": 2, "batch_active_per_user": 1,
         "global_pending_questions": 500}


def install_user(context, role):
    permissions = (["role:manage", "user:manage", "generation_quota:manage"] if role == "super_admin"
                   else ["generation_quota:manage"] if role == "admin"
                   else ["question:generate", "batch:run"])
    user = {"id": f"{role}-1", "username": role, "role": role,
            "roles": [role], "permissions": permissions, "display_name": role}
    context.add_init_script("""(() => {
      localStorage.setItem('aigo_token', 'offline-test-token');
      localStorage.setItem('aigo_user', JSON.stringify(USER));
    })()""".replace("USER", json.dumps(user, ensure_ascii=False)))
    return user


with sync_playwright() as playwright:
    browser = playwright.chromium.launch(headless=True)
    errors = []
    calls = []
    quota_reads = {"admin": 0, "teacher": 0, "super_admin": 0}
    single_rejections = [
        "单次最多生成 20 道题，请减少题数",
        "您已有 2 个未完成的单题任务，请等待完成后再提交",
        "全站未完成出题/检查量已达 500 道，请稍后重试",
    ]
    batch_rejections = [
        "批量任务最多生成 100 道题，请减少要点或每点题数",
        "您已有 1 个进行中的批量任务，请等待完成后再提交",
        "全站未完成出题/检查量已达 500 道，请稍后重试",
    ]

    def make_page(role):
        context = browser.new_context(viewport={"width": 1366, "height": 900})
        user = install_user(context, role)
        page = context.new_page()
        page.on("pageerror", lambda error: errors.append(str(error)))

        def fake_api(route):
            request = route.request
            path = urlparse(request.url).path
            calls.append((request.method, path))
            status, body = 200, {}
            if path == "/api/auth/me":
                body = user
            elif path == "/api/system/generation-quota":
                if role != "admin":
                    status, body = 403, {"error": "无权限"}
                elif request.method == "PUT":
                    QUOTA.update(json.loads(request.post_data))
                    body = {"quota": QUOTA, "usage": {"pending_question_units": 0,
                        "active_single_runs": 0, "active_batch_jobs": 0, "active_ai_check_tasks": 0}}
                else:
                    quota_reads[role] += 1
                    body = {"quota": QUOTA, "usage": {"pending_question_units": quota_reads[role],
                        "single_pending_questions": quota_reads[role], "batch_pending_questions": 0,
                        "overlap_questions": 0, "active_single_runs": 0,
                        "active_batch_jobs": 0, "active_ai_check_tasks": 0}}
            elif path == "/api/knowledge-versions":
                body = {"versions": [{"id": "v-1", "name": "测试大纲", "status": "published", "point_count": 2}],
                        "default_version_id": "v-1"}
            elif path == "/api/knowledge-points/search":
                body = {"points": POINTS, "total": len(POINTS), "page": 1}
            elif path == "/api/knowledge-points/tree":
                body = {"tree": []}
            elif path == "/api/batch/capabilities":
                body = {"available": True, "execution_mode": "local_single_api", "model": "offline-fake", "message": "模拟执行器"}
            elif path == "/api/questions/generate" and request.method == "POST":
                status, body = 429, {"error": single_rejections.pop(0)}
            elif path == "/api/batch/submit" and request.method == "POST":
                status, body = 429, {"error": batch_rejections.pop(0)}
            elif path == "/api/batch/history":
                body = {"jobs": [{"job_id":"fake-completed", "job_name":"离线导入任务", "owner_id":"teacher-1",
                    "status":"completed", "total_count":2, "completed":1, "failed":1}], "total": 1}
            elif path == "/api/batch/list":
                body = {"jobs": []}
            elif path == "/api/batch/status/fake-completed":
                body = {"job_id":"fake-completed", "job_name":"离线导入任务", "owner_id":"teacher-1",
                    "status":"completed", "total_count":2, "completed":1, "failed":1, "items":[]}
            elif path == "/api/batch/download/fake-completed" and request.method == "POST":
                status, body = 429, {"error":"批量结果需为 12 道题安排首次 AI 检查；全站当前占用 20/20 道，请等待队列释放后再导入"}
            elif path == "/api/roles":
                body = {"roles": []}
            elif path == "/api/permissions":
                body = {"permissions": [
                    {"code":"question:view","name":"查看题目","group":"题库","bank_scope":True},
                    {"code":"generation_quota:manage","name":"生成任务配额（仅内置管理员）","group":"系统","admin_only":True},
                ]}
            route.fulfill(status=status, content_type="application/json", body=json.dumps(body, ensure_ascii=False))

        page.route("**/api/**", fake_api)
        return context, page

    admin_context, admin = make_page("admin")
    admin.goto("http://127.0.0.1:5173/system/generation-quota")
    admin.wait_for_load_state("networkidle")
    assert admin.get_by_role("heading", name="生成任务配额", level=2).is_visible()
    assert admin.get_by_label("批量任务总量").input_value() == "100"
    admin.get_by_label("批量任务总量").fill("80")
    admin.get_by_role("button", name="保存并立即生效").click()
    admin.get_by_role("status").get_by_text("配额已保存", exact=False).wait_for()
    assert ("PUT", "/api/system/generation-quota") in calls
    # 页面可见时自动轮询；实时数字更新，但未保存的配置输入保持原样。
    admin.get_by_label("批量任务总量").fill("81")
    before = quota_reads["admin"]
    admin.wait_for_timeout(5600)
    assert quota_reads["admin"] > before, "visible page should poll automatically"
    assert admin.get_by_label("批量任务总量").input_value() == "81", "poll must not overwrite an unsaved edit"
    assert str(quota_reads["admin"]) in admin.locator(".quota-usage strong").first.inner_text()
    # 隐藏页暂停请求，再切回立即恢复。
    # Chromium headless 不保证切标签会改变 visibilityState，显式模拟隐藏事件。
    admin.evaluate("""() => { Object.defineProperty(document, 'hidden', { configurable: true, value: true }); document.dispatchEvent(new Event('visibilitychange')); }""")
    before = quota_reads["admin"]
    admin.wait_for_timeout(5600)
    assert quota_reads["admin"] == before, "hidden page must not poll"
    admin.evaluate("""() => { Object.defineProperty(document, 'hidden', { configurable: true, value: false }); document.dispatchEvent(new Event('visibilitychange')); }""")
    admin.wait_for_timeout(500)
    assert quota_reads["admin"] > before, "visible page should refresh immediately"
    assert admin.get_by_label("批量任务总量").input_value() == "81"
    admin.screenshot(path="/tmp/aigo-generation-quota-admin.png", full_page=True)
    admin_context.close()

    teacher_context, teacher = make_page("teacher")
    teacher.goto("http://127.0.0.1:5173/generate")
    teacher.wait_for_load_state("networkidle")
    teacher.locator(".kp-item input[type=radio]").first.check()
    teacher.locator(".count-input").fill("21")
    teacher.get_by_role("button", name="生成试题").click()
    teacher.get_by_role("status").get_by_text("单次最多生成 20 道题", exact=False).wait_for()
    teacher.locator(".count-input").fill("1")
    teacher.get_by_role("button", name="生成试题").click()
    teacher.get_by_role("status").get_by_text("您已有 2 个未完成的单题任务", exact=False).wait_for()
    teacher.locator(".count-input").fill("3")
    teacher.get_by_role("button", name="生成试题").click()
    teacher.get_by_role("status").get_by_text("全站未完成出题/检查量", exact=False).wait_for()
    assert ("POST", "/api/questions/generate") in calls
    teacher.goto("http://127.0.0.1:5173/generate/batch")
    teacher.wait_for_load_state("networkidle")
    teacher.locator(".kp-item input[type=checkbox]").first.check()
    teacher.get_by_role("button", name="提交生成任务", exact=False).click()
    teacher.get_by_text("批量任务最多生成 100 道题", exact=False).first.wait_for()
    teacher.get_by_role("button", name="提交生成任务", exact=False).click()
    teacher.get_by_text("您已有 1 个进行中的批量任务", exact=False).first.wait_for()
    teacher.get_by_role("button", name="提交生成任务", exact=False).click()
    teacher.get_by_text("全站未完成出题/检查量", exact=False).first.wait_for()
    assert ("POST", "/api/batch/submit") in calls
    teacher.goto("http://127.0.0.1:5173/batch-history")
    teacher.wait_for_load_state("networkidle")
    teacher.get_by_role("button", name="离线导入任务", exact=False).click()
    teacher.get_by_role("button", name="直接导入成功部分").click()
    teacher.get_by_text("全站当前占用 20/20 道", exact=False).first.wait_for()
    assert ("POST", "/api/batch/download/fake-completed") in calls
    teacher_context.close()

    super_context, super_page = make_page("super_admin")
    super_page.goto("http://127.0.0.1:5173/roles")
    super_page.wait_for_load_state("networkidle")
    super_page.get_by_role("button", name="新建角色", exact=False).click()
    assert super_page.locator(".perm-check").filter(has_text="生成任务配额").count() == 0
    assert super_page.get_by_text("生成任务配额仅内置超级管理员和管理员可管理", exact=False).is_visible()
    super_context.close()

    assert not errors, errors
    browser.close()
    print("PASS: admin save; teacher sees generation and import 429; role matrix hides admin-only grant; all API requests intercepted")
