import json
import os
import shlex
import subprocess
import time
import uuid

from playwright.sync_api import sync_playwright


BASE = os.environ.get("AIGO_E2E_BASE_URL", "http://127.0.0.1:5173")
DSN = os.environ.get("AIGO_E2E_DB_DSN", "postgres://localhost:5432/aigo?sslmode=disable")
ADMIN_USERNAME = os.environ.get("AIGO_E2E_ADMIN_USERNAME", "admin")
ADMIN_PASSWORD = os.environ.get("AIGO_E2E_ADMIN_PASSWORD", "admin")
SSH_HOST = os.environ.get("AIGO_E2E_SSH_HOST", "")
suffix = uuid.uuid4().hex[:10]
username = f"ze2e_dual_{suffix}"
password = f"Ze2e-{suffix}-password"
flow_id = f"ze2e-flow-{suffix}"
question_id = f"ze2e-question-{suffix}"
stem_marker = f"端到端无分类审核题-{suffix}"
initial_stem = f"男，50岁。{stem_marker}，出现持续胸痛2小时。最可能的诊断是？"


def psql(sql):
    if SSH_HOST:
        command = ["docker", "exec", "aigo-postgres-1", "psql", "-U", "aigo", "-d", "aigo", "-v", "ON_ERROR_STOP=1", "-c", sql]
        subprocess.run(["ssh", "-o", "BatchMode=yes", SSH_HOST, " ".join(shlex.quote(value) for value in command)], check=True, capture_output=True, text=True)
        return
    subprocess.run(["psql", DSN, "-v", "ON_ERROR_STOP=1", "-c", sql], check=True, capture_output=True, text=True)


def login(page, username_value, password_value):
    page.goto(BASE + "/login", wait_until="networkidle")
    page.get_by_placeholder("请输入用户名").fill(username_value)
    page.get_by_placeholder("请输入密码").fill(password_value)
    page.get_by_role("button", name="登录", exact=True).click()
    page.wait_for_url("**/my")
    page.wait_for_load_state("networkidle")


def auth_headers(page):
    return {"Authorization": "Bearer " + page.evaluate("localStorage.getItem('aigo_token')")}


def switch_role(page, label, role_id):
    page.locator(".role-switch-trigger").click()
    page.locator(".role-option").filter(has_text=label).click()
    page.wait_for_function(f"JSON.parse(localStorage.getItem('aigo_user')).role === '{role_id}'")
    page.wait_for_load_state("networkidle")


def pass_current_review(page):
    page.goto(BASE + "/review", wait_until="networkidle")
    card = page.locator(".my-task-card").filter(has_text=stem_marker).first
    card.wait_for(timeout=15000)
    card.click()
    page.get_by_role("button", name="提交审核", exact=True).click()
    page.wait_for_timeout(700)


def main():
    user_id = ""
    with sync_playwright() as playwright:
        browser = playwright.chromium.launch(headless=True, channel="chrome")
        admin = browser.new_page(viewport={"width": 1440, "height": 1000})
        teacher = browser.new_page(viewport={"width": 1440, "height": 1000})
        try:
            login(admin, ADMIN_USERNAME, ADMIN_PASSWORD)
            admin_user = admin.evaluate("JSON.parse(localStorage.getItem('aigo_user'))")
            created = admin.request.post(
                BASE + "/api/users",
                headers=auth_headers(admin),
                data={
                    "username": username,
                    "password": password,
                    "display_name": "E2E双身份老师",
                    "role": "teacher",
                    "roles": ["teacher", "expert"],
                    "permissions": [],
                    "bank_ids": [],
                },
            )
            assert created.status == 201, created.text()
            user = created.json()
            user_id = user["id"]
            flow = admin.request.post(
                BASE + "/api/review/flows",
                headers=auth_headers(admin),
                data={
                    "id": flow_id,
                    "name": "E2E无分类双轮流程",
                    "description": "自动化后清理",
                    "bank_id": "should-be-ignored",
                    "final_reviewer_ids": [admin_user["id"]],
                    "rounds": [
                        {"round_number": 1, "name": "初审", "expert_ids": [user_id], "required_count": 1},
                        {"round_number": 2, "name": "复审", "expert_ids": [user_id], "required_count": 1},
                    ],
                },
            )
            assert flow.status == 201, flow.text()
            assert not flow.json().get("bank_id"), flow.text()

            safe_stem = initial_stem.replace("'", "''")
            psql(f"""
                INSERT INTO questions (
                    id, clinical_stem, options, answer, explanation, knowledge_points,
                    difficulty, outline_code, profession, status, version, created_by,
                    owner_id, created_at, updated_at, search_text
                ) VALUES (
                    '{question_id}', '{safe_stem}',
					'[{{"label":"A","text":"甲"}},{{"label":"B","text":"乙"}},{{"label":"C","text":"丙"}},{{"label":"D","text":"丁"}},{{"label":"E","text":"戊"}}]'::jsonb,
                    'A', '', '[]'::jsonb, '0.65', 'E2E', '测试专业',
                    'ai_reviewed', 1, '{username}', '{user_id}', NOW(), NOW(), LOWER('{safe_stem}')
                );
                INSERT INTO question_versions (id, question_id, version, snapshot, actor, change_type, created_at)
                SELECT '{question_id}-v1', id, version, to_jsonb(q), '{username}', 'create', NOW()
                FROM questions q WHERE id='{question_id}';
            """)

            login(teacher, username, password)
            workspace_key = "aigo_generation_workspace_v1:" + user_id
            teacher.evaluate(
                "([key,id]) => localStorage.setItem(key, JSON.stringify({version:1,userId:id,marker:'preserve-role-switch'}))",
                [workspace_key, user_id],
            )
            switch_role(teacher, "审题老师", "expert")
            assert "preserve-role-switch" in (teacher.evaluate("key => localStorage.getItem(key)", workspace_key) or "")
            switch_role(teacher, "命题教师", "teacher")

            teacher.goto(BASE + "/new-questions", wait_until="networkidle")
            teacher.locator(".new-question-card").filter(has_text=stem_marker).click()
            textarea = teacher.locator(".new-question-detail textarea").first
            textarea.fill(initial_stem + "（已微调）")
            leave_dialog = []
            teacher.once("dialog", lambda dialog: (leave_dialog.append(dialog.message), dialog.dismiss()))
            teacher.locator('a[href="/my"]').first.click()
            teacher.wait_for_timeout(250)
            assert leave_dialog and "未保存修改" in leave_dialog[0] and teacher.url.endswith("/new-questions")
            teacher.get_by_role("button", name="保存微调", exact=True).click()
            teacher.locator(".toast.show,.error-modal").first.wait_for(timeout=10000)
            if teacher.locator(".error-modal").count():
                raise AssertionError(teacher.locator(".error-modal").inner_text())
            assert "已保存为 v2" in teacher.locator(".toast.show").inner_text()
            teacher.locator(".submit-box select").select_option(flow_id)
            teacher.once("dialog", lambda dialog: dialog.accept())
            teacher.get_by_role("button", name="提交当前题", exact=True).click()
            teacher.get_by_text("已提交 1 道题进入审核流程", exact=False).wait_for(timeout=10000)

            switch_role(teacher, "审题老师", "expert")
            todo_count = teacher.locator('.todo-row[data-kind="review"] strong')
            todo_count.wait_for(timeout=10000)
            teacher.wait_for_function("document.querySelector('.todo-row[data-kind=\"review\"] strong')?.textContent.trim() === '1'", timeout=10000)
            assert int(todo_count.inner_text()) == 1, f"dashboard review count={todo_count.inner_text()}"

            pass_current_review(teacher)
            pass_current_review(teacher)

            admin.goto(BASE + "/review-decisions", wait_until="networkidle")
            decision = admin.locator(".decision-card,.my-task-card").filter(has_text=stem_marker).first
            decision.wait_for(timeout=15000)
            decision.click()
            admin.locator(".finalize-form select").select_option("approved")
            admin.get_by_role("button", name="提交决断", exact=True).click()
            admin.wait_for_timeout(900)

            switch_role(teacher, "命题教师", "teacher")
            result = teacher.request.get(
                BASE + "/api/questions?page=1&page_size=20&tier=formal&scope=personal",
                headers=auth_headers(teacher),
            )
            assert result.status == 200, result.text()
            assert any(q["id"] == question_id and q["status"] == "published" for q in result.json().get("questions", [])), result.text()
            print("PASS | 无分类新题 → 两轮审核 → 最终决断 → 正式题库")
            print("PASS | 身份切换保留命题工作区，待审数字无需手动刷新")
            print("PASS | 新题微调未保存时离页拦截")
        finally:
            browser.close()
            if user_id:
                psql(f"""
                    DELETE FROM questions WHERE id='{question_id}';
                    DELETE FROM review_flows WHERE id='{flow_id}';
                    DELETE FROM experts WHERE id='{user_id}';
                    DELETE FROM users WHERE id='{user_id}';
                    DELETE FROM audit_logs WHERE actor IN ('{username}', 'admin')
                      AND created_at > NOW() - INTERVAL '30 minutes'
                      AND (detail LIKE '%{question_id}%' OR detail LIKE '%{flow_id}%' OR detail LIKE '%{username}%');
                """)


if __name__ == "__main__":
    main()
