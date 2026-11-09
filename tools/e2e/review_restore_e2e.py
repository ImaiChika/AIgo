"""审核还原多人浏览器验收。仅允许显式指定 loopback + 本轮专用数据库。

模型必须由调用方配置为本机假模型；本脚本通过正常生成、首检和审核 API 构造任务，
SQL 仅用于读取不变量。数据库/服务/截图生命周期由本轮运行器负责精确清理。
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import time
import uuid
from urllib.parse import urlparse

from playwright.sync_api import sync_playwright, expect

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--base-url', required=True)
parser.add_argument('--dsn', required=True)
parser.add_argument('--artifact-dir', required=True)
parser.add_argument('--fixture-json', help='将非敏感夹具ID写入本轮临时文件，供视图测试复用')
args = parser.parse_args()
assert urlparse(args.base_url).hostname in ('127.0.0.1', 'localhost'), 'Only local browser tests are supported'
assert 'aigo_review_restore_' in args.dsn, 'Use an explicitly isolated review-restore database'
PASSWORD = os.environ['AIGO_E2E_ADMIN_PASSWORD']
BASE = args.base_url.rstrip('/')
ARTIFACT = Path(args.artifact_dir)
ARTIFACT.mkdir(parents=True, exist_ok=True)
SUFFIX = uuid.uuid4().hex[:8]
results = []


def check(name, condition=True):
    assert condition, name
    results.append(name)
    print('PASS | ' + name, flush=True)


def sql(statement):
    response = subprocess.run(['psql', args.dsn, '-XAt', '-v', 'ON_ERROR_STOP=1', '-c', statement], check=True, capture_output=True, text=True)
    return response.stdout.strip()


def literal(value):
    return "'" + str(value).replace("'", "''") + "'"


def poll(callback, timeout=90):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        last = callback()
        if last:
            return last
        time.sleep(.15)
    raise AssertionError('Timed out waiting for state: ' + repr(last))


def headers(page):
    return {'Authorization': 'Bearer ' + page.evaluate("localStorage.getItem('aigo_token')")}


def call(page, method, path, body=None, expected=(200, 201)):
    response = page.request.fetch(BASE + '/api' + path, method=method, headers=headers(page), data=body)
    assert response.status in expected, f'{method} {path}: {response.status} {response.text()}'
    return response.json()


def go(page, path):
    page.goto(BASE + path, wait_until='networkidle')


def login(browser, username, password):
    context = browser.new_context(viewport={'width': 1440, 'height': 1000})
    page = context.new_page()
    page.on('pageerror', lambda e: javascript_errors.append(str(e)))
    page.on('dialog', lambda dialog: dialog.accept())
    go(page, '/login')
    page.get_by_placeholder('请输入用户名').fill(username)
    page.get_by_placeholder('请输入密码').fill(password)
    page.get_by_role('button', name='登录', exact=True).click()
    page.wait_for_url('**/my')
    page.wait_for_load_state('networkidle')
    return page


def generate(page, kp, count):
    ids = []
    while count:
        take = min(count, 20)
        rid = 'restore-e2e-' + uuid.uuid4().hex
        call(page, 'POST', '/questions/generate', {'run_id': rid, 'count': take, 'knowledge_point_id': kp['id'], 'version_id': kp['version_id']})
        def finished():
            run = call(page, 'GET', '/generation-runs/' + rid)['run']
            assert run['status'] != 'failed', run
            return run if run['status'] == 'succeeded' else None
        run = poll(finished)
        qids = run['question_ids']
        assert len(qids) == take, run
        def checked():
            items = call(page, 'POST', '/ai-check/progress', {'question_ids': qids})['items']
            return len(items) == take and all(x.get('verdict') == 'pass' for x in items)
        poll(checked)
        ids.extend(qids)
        count -= take
    return ids


def question(page, qid):
    return call(page, 'GET', '/questions/' + qid)


def submit(page, qid, fid):
    q = question(page, qid)
    return call(page, 'POST', '/review/submit', {'question_id': qid, 'flow_id': fid, 'expected_version': q['version'], 'expected_review_revision': q.get('review_revision', 0)})


def vote(page, task, action='approved', opinion=''):
    return call(page, 'POST', '/review/action', {'task_id': task['id'], 'expected_round': task['current_round'], 'action': action, 'opinion': opinion})


def active_task(page, qid):
    return call(page, 'GET', '/review/task-by-question/' + qid)


def restore_ui(page, qid):
    go(page, '/review-restore')
    row = page.locator('.restore-row').filter(has=page.locator(f'input[aria-label="选择题目 {qid}"]'))
    row.get_by_role('button', name='查看详情').click()
    expect(page.locator('.restore-detail')).to_contain_text(qid)
    page.get_by_role('button', name='还原当前题', exact=True).click()
    expect(page.locator('dialog')).to_be_visible()
    page.locator('dialog textarea').fill('本地多人验收，重新安排审核流程')
    page.get_by_role('button', name='确认还原', exact=True).click()
    expect(page.locator('dialog')).to_have_count(0)
    expect(page.locator('.restore-message.success')).to_contain_text('已还原 1 道题')


def fingerprint(qid):
    key = literal(qid)
    return sql("SELECT md5(jsonb_build_object('q',(to_jsonb(q)-'status'-'review_revision'-'updated_at'), 'versions',(SELECT jsonb_agg(to_jsonb(v) ORDER BY version) FROM question_versions v WHERE v.question_id=q.id),'ai',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM ai_review_results a WHERE a.question_id=q.id),'checks',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM ai_check_tasks c WHERE c.question_id=q.id),'banks',(SELECT jsonb_agg(to_jsonb(b) ORDER BY bank_id) FROM question_bank_members b WHERE b.question_id=q.id))::text) FROM questions q WHERE id=" + key)


javascript_errors = []
with sync_playwright() as playwright:
    browser = playwright.chromium.launch(headless=True, channel='chrome')
    try:
        root = login(browser, 'admin', PASSWORD)
        users, pages = {}, {}
        for key, role, roles, permissions in [('author', 'teacher', ['teacher', 'expert'], []), ('author_b', 'teacher', ['teacher'], []), ('e1', 'expert', ['expert'], []), ('e2', 'expert', ['expert'], []), ('e3', 'expert', ['expert'], []), ('manager', 'admin', ['admin'], [])]:
            name = 'restore_' + key + '_' + SUFFIX
            user = call(root, 'POST', '/users', {'username': name, 'password': PASSWORD, 'display_name': '本地验收' + key, 'role': role, 'roles': roles, 'permissions': permissions})
            users[key] = user
            pages[key] = login(browser, name, PASSWORD)
        author, manager = pages['author'], pages['manager']
        experts = [pages['e1'], pages['e2'], pages['e3']]
        version = call(root, 'POST', '/knowledge-versions', {'name': '审核还原验收大纲' + SUFFIX, 'year': 2026})
        kp = call(root, 'POST', '/knowledge-points', {'version_id': version['id'], 'category': '临床综合', 'subject': '内科学', 'unit': '循环系统', 'sub_item': '心肌梗死', 'topic': '心肌梗死的诊断', 'outline_code': 'RESTORE-' + SUFFIX})
        call(root, 'POST', '/knowledge-versions/' + version['id'] + '/publish')
        flow = call(root, 'POST', '/review/flows', {'id': 'restore-three-' + SUFFIX, 'name': '本地三轮全票流程' + SUFFIX, 'rounds': [{'round_number': n, 'name': '第' + str(n) + '轮', 'expert_ids': [users[x]['id'] for x in ['e1', 'e2', 'e3']], 'required_count': 3} for n in range(1, 4)], 'final_reviewer_ids': [users['manager']['id']]})
        replacement = call(root, 'POST', '/review/flows', {'id': 'restore-new-' + SUFFIX, 'name': '本地新流程' + SUFFIX, 'rounds': [{'round_number': 1, 'expert_ids': [users['e3']['id']], 'required_count': 1}], 'final_reviewer_ids': [users['manager']['id']]})
        qid = generate(author, kp, 1)[0]
        task = submit(author, qid, flow['id'])
        go(author, '/new-questions')
        for expert in experts:
            go(expert, '/review')
        experts[0].locator('.my-task-card').first.click()
        experts[0].locator('.sc-field textarea').first.fill('保留这份尚未提交的专家意见')
        vote(experts[1], task, opinion='已核对关键诊断依据')
        before = fingerprint(qid)
        go(manager, '/review-restore')
        manager.get_by_role('button', name='查看详情', exact=True).first.click()
        expect(manager.locator('.restore-detail')).to_contain_text('已核对关键诊断依据')
        manager.screenshot(path=str(ARTIFACT / 'restore-detail-desktop.png'), full_page=True)
        manager.get_by_role('button', name='还原当前题', exact=True).click()
        manager.screenshot(path=str(ARTIFACT / 'restore-confirm-desktop.png'), full_page=True)
        manager.locator('dialog textarea').fill('重新配置流程')
        started = time.monotonic()
        manager.get_by_role('button', name='确认还原', exact=True).click()
        expect(manager.locator('.restore-message.success')).to_contain_text('已还原 1 道题')
        expect(author.locator('.new-question-card')).to_have_count(1, timeout=8000)
        expect(experts[0].locator('.my-task-card')).to_have_count(0, timeout=8000)
        expect(experts[0].locator('[role="alert"]')).to_contain_text('已被还原', timeout=8000)
        expect(experts[0].locator('.sc-field textarea').first).to_have_value('保留这份尚未提交的专家意见')
        expect(experts[0].get_by_role('button', name='提交审核', exact=True)).to_be_disabled()
        check('跨账号还原自动收敛，保留专家未提交意见并禁止旧提交；%.2f秒' % (time.monotonic() - started))
        check('内容、版本快照、AI结果、首检任务与分类归属不变', fingerprint(qid) == before)
        q = question(author, qid)
        check('原出题人恢复未提交v1且审核修订递增', q['status'] == 'ai_reviewed' and q['version'] == 1 and q['review_revision'] == 1)
        check('全部人工任务与意见被删除', sql('SELECT (SELECT count(*) FROM review_tasks WHERE question_id=' + literal(qid) + ')+(SELECT count(*) FROM review_records WHERE question_id=' + literal(qid) + ')') == '0')
        logs = call(root, 'GET', '/audit-logs/question/' + qid)
        check('旧审核日志删除并保留新还原事件', any(x['action'] == 'review_restore' for x in logs.get('logs', [])) and not any(x['action'] in ['review', 'submit'] for x in logs.get('logs', [])))
        call(author, 'POST', '/review/submit', {'question_id': qid, 'flow_id': flow['id'], 'expected_version': 1, 'expected_review_revision': 0}, expected=(409,))
        check('相同内容版本的旧送审请求被拦截')
        author.locator('.submit-box select').select_option(replacement['id'])
        author.get_by_role('button', name='提交当前题', exact=True).click()
        expect(author.locator('.new-question-card')).to_have_count(0)
        poll(lambda: len(call(experts[2], 'GET', '/review/my-tasks')['tasks']) == 1)
        go(experts[2], '/review')
        experts[2].locator('.my-task-card').first.click()
        experts[2].get_by_role('button', name='提交审核', exact=True).click()
        go(manager, '/review-decisions')
        expect(manager.locator('.decision-card')).to_have_count(1, timeout=8000)
        manager.locator('.decision-card').first.click()
        manager.get_by_role('button', name='提交决断', exact=True).click()
        poll(lambda: question(author, qid)['status'] == 'published')
        check('还原题可选择新流程送审、轮审和最终通过，进入正式题库')
        # 形成第三轮中途、待最终决断、待修改三种独立状态。
        additional = generate(author, kp, 3)
        third, final, revision = additional
        for target in [third, final]:
            current = submit(author, target, flow['id'])
            for _round in [1, 2]:
                for expert in experts:
                    vote(expert, current)
                current = active_task(root, target)
            for expert in experts[:2 if target == third else 3]:
                vote(expert, current)
        go(manager, '/review-decisions')
        manager.locator('.decision-card').first.click()
        manager.locator('.sc-field textarea').first.fill('这份决断意见尚未提交')
        restore_ui(root, final)
        expect(manager.locator('.decision-card')).to_have_count(0, timeout=8000)
        expect(manager.locator('[role="alert"]')).to_contain_text('已被还原', timeout=8000)
        expect(manager.locator('.sc-field textarea').first).to_have_value('这份决断意见尚未提交')
        expect(manager.get_by_role('button', name='提交决断', exact=True)).to_be_disabled()
        check('待决断还原清除九条轮审意见，把关旧待办自动失效且保留未提交输入')
        restore_ui(root, third)
        check('第三轮部分投票仍可还原并清除以前全部轮次', sql('SELECT count(*) FROM review_records WHERE question_id=' + literal(third)) == '0')
        current = submit(author, revision, flow['id'])
        vote(experts[0], current, 'revision_required', '补充胸痛伴随的症状')
        vote(experts[1], current)
        vote(experts[2], current)
        check('待修改不进入可还原列表', not any(x['question']['id'] == revision for x in call(root, 'GET', '/review/restore-candidates')['items']))
        go(author, '/my-revisions')
        expect(author.locator('.revision-card')).to_have_count(1)
        author.locator('.revision-card').first.click()
        expect(author.locator('.reason-box')).to_contain_text('补充胸痛伴随的症状')
        expect(author.get_by_role('button', name='重新送审', exact=True)).to_be_disabled()
        original = author.locator('.edit-field textarea').first.input_value()
        author.locator('.edit-field textarea').first.fill(original.replace('伴大汗', '伴明显大汗'))
        author.get_by_role('button', name='保存修改', exact=True).click()
        expect(author.locator('.version-strip')).to_contain_text('v2')
        author.get_by_label('重新送审流程').select_option(replacement['id'])
        author.get_by_role('button', name='重新送审', exact=True).click()
        expect(author.locator('.revision-card')).to_have_count(0)
        latest = active_task(root, revision)
        history = call(author, 'GET', '/questions/' + revision + '/review-history')['items']
        check('退修保存新版本可换流程，旧task血缘和原因保留', latest['flow_id'] == replacement['id'] and latest['previous_task_id'] == current['id'] and any('补充胸痛伴随的症状' in r['opinion'] for h in history for r in h['records']))
        call(root, 'DELETE', '/review/flows/' + flow['id'])
        check('原流程所有在途/退修占用清空后可以删除归档')
        # 移动端实测布局与详情；被还原题重送产生新的在途对象。
        mobile_task = submit(author, third, replacement['id'])
        go(root, '/review-restore')
        root.set_viewport_size({'width': 390, 'height': 844})
        root.locator('.restore-row').first.get_by_role('button', name='查看详情').click()
        expect(root.locator('.restore-detail')).to_contain_text(third)
        root.screenshot(path=str(ARTIFACT / 'restore-detail-mobile.png'), full_page=True)
        check('移动端无页面横向溢出', root.evaluate('document.documentElement.scrollWidth <= window.innerWidth'))
        root.set_viewport_size({'width': 1440, 'height': 1000})
        # 真正生成101题，跨两位作者、六页；操作的是新建独立流程，其他题作控制组。
        bulk_flow = call(root, 'POST', '/review/flows', {'id': 'restore-bulk-' + SUFFIX, 'name': '跨页批量流程' + SUFFIX, 'rounds': [{'round_number': 1, 'expert_ids': [users['e1']['id']], 'required_count': 1}]})
        baseline_new = {key: call(pages[key], 'GET', '/my/summary')['new_questions_count'] for key in ['author', 'author_b']}
        bulk_ids = {'author': generate(author, kp, 50), 'author_b': generate(pages['author_b'], kp, 51)}
        controls = {target: fingerprint(target) for target in [qid, revision, third]}
        for key, qids in bulk_ids.items():
            expectations = {target: {'expected_version': 1, 'expected_review_revision': 0} for target in qids}
            submitted = call(pages[key], 'POST', '/review/submit-batch', {'question_ids': qids, 'flow_id': bulk_flow['id'], 'expectations': expectations})
            assert submitted['submitted'] == len(qids), submitted
        go(root, '/review-restore')
        root.get_by_label('按审核流程筛选').select_option(bulk_flow['id'])
        expect(root.locator('.restore-batchbar')).to_contain_text('共 101 道')
        root.get_by_label('全选本页可还原题').check()
        first_page_question = root.locator('.restore-row input[type="checkbox"]').first.get_attribute('aria-label')
        root.get_by_role('button', name='下一页', exact=True).click()
        expect(root.get_by_role('button', name='2', exact=True)).to_have_attribute('aria-current', 'page')
        expect(root.locator('.restore-row input[type="checkbox"]').first).not_to_have_attribute('aria-label', first_page_question)
        root.get_by_label('全选本页可还原题').check()
        expect(root.locator('.restore-batchbar')).to_contain_text('已选 40 道')
        root.get_by_role('button', name='还原所选 40 道', exact=True).click()
        expect(root.locator('.restore-confirm-count')).to_contain_text('40')
        root.locator('dialog textarea').fill('跨页选中的四十道题')
        root.get_by_role('button', name='确认还原', exact=True).click()
        expect(root.locator('.restore-message.success')).to_contain_text('已还原 40 道题')
        expect(root.locator('.restore-batchbar')).to_contain_text('共 61 道')
        check('101题跨页选择保留，选中40题全批成功且其余61题保持在途')
        root.get_by_role('button', name='还原此流程全部', exact=True).click()
        expect(root.locator('.restore-confirm-count')).to_contain_text('61')
        root.locator('dialog textarea').fill('整流程所有剩余分页')
        root.get_by_role('button', name='确认还原', exact=True).click()
        expect(root.locator('.restore-message.success')).to_contain_text('已还原 61 道题')
        expect(root.locator('.restore-row')).to_have_count(0)
        check('整流程全部还原覆盖所有分页，不会只还原当前20题')
        for key, qids in bulk_ids.items():
            now = call(pages[key], 'GET', '/my/summary')['new_questions_count']
            check(key + '新题计数与回流数逐题对应', now == baseline_new[key] + len(qids))
        check('跨流程与正式对照题内容、版本、AI结果均未改变', all(fingerprint(target) == value for target, value in controls.items()))
        # 未来轮次占用、清空后的资格释放和失效模板预检。
        future_flow = call(root, 'POST', '/review/flows', {'id': 'restore-future-' + SUFFIX, 'name': '未来轮次守卫' + SUFFIX, 'rounds': [{'round_number': 1, 'expert_ids': [users['e1']['id']], 'required_count': 1}, {'round_number': 2, 'expert_ids': [users['e2']['id']], 'required_count': 1}]})
        future_task = submit(author, final, future_flow['id'])
        revoke = {'display_name': users['e2']['display_name'], 'role': 'teacher', 'roles': ['teacher'], 'permissions': [], 'bank_ids': [], 'enabled': True}
        call(root, 'PUT', '/users/' + users['e2']['id'], revoke, expected=(409,))
        check('尚未开始的未来轮审专家仍受真实任务占用保护')
        restore_ui(root, final)
        call(root, 'PUT', '/users/' + users['e2']['id'], revoke)
        available = call(author, 'GET', '/review/available-flows')['flows']
        check('清空任务后可撤审题权，引用该专家的旧模板显示不可用', next(f for f in available if f['id'] == future_flow['id'])['available'] is False)
        q = question(author, final)
        call(author, 'POST', '/review/submit', {'question_id': final, 'flow_id': future_flow['id'], 'expected_version': q['version'], 'expected_review_revision': q['review_revision']}, expected=(409,))
        call(root, 'DELETE', '/users/' + users['e2']['id'])
        check('仅模板引用不阻止删除专家；不合资格模板拒绝新送审')
        # 只有新权限的独立授权身份，以及在线收权清除管理详情/预览。
        restore_user = call(root, 'POST', '/users', {'username': 'restore_only_' + SUFFIX, 'password': PASSWORD, 'display_name': '仅还原授权', 'role': 'expert', 'permissions': ['review:restore']})
        only_restore = login(browser, restore_user['username'], PASSWORD)
        go(only_restore, '/review-restore')
        selected_row = only_restore.locator('.restore-row').filter(has=only_restore.locator(f'input[aria-label="选择题目 {third}"]'))
        selected_row.get_by_role('button', name='查看详情').click()
        expect(only_restore.locator('.restore-detail')).to_contain_text(third)
        call(only_restore, 'GET', '/users', expected=(403,))
        call(only_restore, 'GET', '/questions/' + third, expected=(403,))
        only_restore.get_by_role('button', name='还原当前题', exact=True).click()
        expect(only_restore.locator('dialog')).to_be_visible()
        call(root, 'PUT', '/users/' + restore_user['id'], {'display_name': '仅还原授权', 'role': 'expert', 'roles': ['expert'], 'permissions': [], 'bank_ids': [], 'enabled': True})
        expect(only_restore.locator('dialog')).to_have_count(0, timeout=8000)
        expect(only_restore.locator('.restore-detail')).to_have_count(0)
        call(only_restore, 'GET', '/review/restore-candidates', expected=(403,))
        check('独立还原授权不扩大用户/普通题库权限；在线收权会清除详情和确认凭证')
        go(author, '/review-restore')
        expect(author).to_have_url(BASE + '/my')
        check('普通出题身份直达审核还原页面被拒绝')
        # 断网期间保留页面，联网自动收敛个人数据和导航角标。
        offline_task = submit(author, final, bulk_flow['id'])
        go(author, '/my')
        new_before = call(author, 'GET', '/my/summary')['new_questions_count']
        expect(author.locator('.todo-row[data-kind="newQuestions"] strong')).to_have_text(str(new_before))
        author.context.set_offline(True)
        restore_ui(root, final)
        author.context.set_offline(False)
        expect(author.locator('.todo-row[data-kind="newQuestions"] strong')).to_have_text(str(new_before + 1), timeout=8000)
        expect(author.locator('a[href="/new-questions"] .navigation-badge').first).to_have_text(str(new_before + 1) if new_before + 1 <= 99 else '99+', timeout=8000)
        check('离线不丢已有页面，联网后个人数据与导航待办自动同步')
        # 同账号跨标签身份变化不能混用命题与审核权限。
        other_tab = author.context.new_page()
        go(other_tab, '/my')
        author.locator('.role-switch-trigger').click()
        author.locator('.role-option').filter(has_text='审题老师').click()
        poll(lambda: other_tab.evaluate("JSON.parse(localStorage.getItem('aigo_user')).role") == 'expert')
        expect(other_tab.locator('.business-navigation a[href="/new-questions"]')).to_have_count(0)
        author.locator('.role-switch-trigger').click()
        author.locator('.role-option').filter(has_text='命题教师').click()
        poll(lambda: other_tab.evaluate("JSON.parse(localStorage.getItem('aigo_user')).role") == 'teacher')
        expect(other_tab.locator('.business-navigation a[href="/new-questions"]')).to_have_count(1)
        other_tab.close()
        check('同账号多标签身份切换同步，权限菜单正确切换')
        # 管理员批量能力和配额边界的控制组。
        manager_me = call(manager, 'GET', '/auth/me')
        check('管理员保留批量生成权限，新功能未开放生成配额管理', 'batch:run' in manager_me['permissions'] and 'generation_quota:manage' not in manager_me['permissions'])
        call(manager, 'GET', '/system/generation-quota', expected=(403,))
        batch = call(manager, 'POST', '/batch/submit', {'knowledge_point_ids': [kp['id']], 'version_id': kp['version_id'], 'count': 2, 'job_name': '还原功能批量控制组' + SUFFIX})
        def batch_finished():
            job = call(manager, 'GET', '/batch/status/' + batch['job_id'])
            assert job['status'] != 'failed', job
            return job if job['status'] == 'completed' else None
        job = poll(batch_finished)
        imported = call(manager, 'POST', '/batch/download/' + batch['job_id'])
        manager_ids = imported.get('question_ids', [])
        assert len(manager_ids) == 2, imported
        poll(lambda: all(i.get('verdict') == 'pass' for i in call(manager, 'POST', '/ai-check/progress', {'question_ids': manager_ids})['items']))
        replayed = call(manager, 'POST', '/batch/download/' + batch['job_id'])
        check('管理员实际批量出题、首次检查和结果重复导入保持幂等', replayed.get('question_ids') == manager_ids)
        # 正式题导出保持可用，未通过题不能导出；文件只在运行器工作目录产生。
        for extension in ['xlsx', 'docx']:
            exported = call(author, 'POST', '/export/' + extension, {'question_ids': [qid], 'scope': 'personal'})
            downloaded = author.request.get(BASE + '/api/export/download/' + exported['filename'], headers=headers(author))
            assert downloaded.status == 200 and downloaded.body().startswith(b'PK'), extension
        failed_export = author.request.post(BASE + '/api/export/xlsx', headers=headers(author), data={'question_ids': [third], 'scope': 'personal'})
        check('正式题Excel/Word导出正常，进行中题仍被导出门禁拦截', failed_export.status in (400, 403))
        go(root, '/system/rag')
        expect(root).to_have_url(BASE + '/my')
        check('RAG入口继续隐藏，后台总开关保持关闭', not call(root, 'GET', '/system/rag/settings')['enabled'])
        # 可选：由本轮运行器显式提供假模型控制端，验证质量淘汰和故障重试。
        control = os.environ.get('AIGO_E2E_MODEL_CONTROL_URL')
        if control:
            assert urlparse(control).hostname in ('127.0.0.1', 'localhost')
            unchanged = fingerprint(qid)
            count_before = call(author, 'GET', '/my/summary')['new_questions_count']
            controlled = root.request.post(control, data={'reject_next': True})
            assert controlled.status == 200
            reject_run = 'restore-reject-' + uuid.uuid4().hex
            payload = {'run_id': reject_run, 'count': 1, 'knowledge_point_id': kp['id'], 'version_id': kp['version_id']}
            call(author, 'POST', '/questions/generate', payload)
            def rejected_run_finished():
                run = call(author, 'GET', '/generation-runs/' + reject_run)['run']
                assert run['status'] != 'failed', run
                return run if run['status'] == 'succeeded' else None
            rejected_run = poll(rejected_run_finished)
            poll(lambda: call(author, 'POST', '/ai-check/progress', {'question_ids': rejected_run['question_ids']}).get('counts', {}).get('discarded') == 1)
            after_discard = call(author, 'GET', '/my/summary')['new_questions_count']
            check('首次检查质量不通过仍被自动淘汰，不进入新题或任何题库', after_discard == count_before)
            state = root.request.get(control).json()
            root.request.post(control, data={'check_failures': 1})
            recovered = generate(author, kp, 1)
            state_after = root.request.get(control).json()
            check('临时模型故障能内部重试恢复，已通过题不会重复首检', len(recovered) == 1 and state_after['check_calls'] >= state['check_calls'] + 2 and fingerprint(qid) == unchanged)
            # 生成正在等待模型时，另一题的还原不能锁住生成或重新检查旧题。
            overlapping = submit(author, final, bulk_flow['id'])
            root.request.post(control, data={'generation_delay': 4})
            concurrent_run = 'restore-overlap-' + uuid.uuid4().hex
            call(author, 'POST', '/questions/generate', {'run_id': concurrent_run, 'count': 2, 'knowledge_point_id': kp['id'], 'version_id': kp['version_id']})
            poll(lambda: root.request.get(control).json()['active_generation'] > 0)
            preview = call(root, 'POST', '/review/restore-preview', {'task_ids': [overlapping['id']]})
            restoring_at = time.monotonic()
            overlap_result = call(root, 'POST', '/review/restore', {'operation_id': 'overlap-' + uuid.uuid4().hex, 'preview_token': preview['token'], 'reason': '出题在途时还原其他题目'})
            restore_duration = time.monotonic() - restoring_at
            root.request.post(control, data={'generation_delay': 0})
            concurrent_result = poll(lambda: (lambda r: r if r['status'] == 'succeeded' else None)(call(author, 'GET', '/generation-runs/' + concurrent_run)['run']))
            poll(lambda: all(i.get('verdict') == 'pass' for i in call(author, 'POST', '/ai-check/progress', {'question_ids': concurrent_result['question_ids']})['items']))
            check('出题在途与其他题还原可并行，两道新题继续完成首检；还原%.2f秒' % restore_duration, overlap_result['restored'] == 1 and len(concurrent_result['question_ids']) == 2 and restore_duration < 3)
            replay = call(author, 'POST', '/questions/generate', payload)
            check('相同生成运行ID重放不重复产生被淘汰题', replay['run']['question_ids'] == rejected_run['question_ids'])
        check('无浏览器JavaScript异常', not javascript_errors)
        summary = {'passed': len(results), 'cases': results, 'suffix': SUFFIX, 'question_ids': [qid] + additional, 'users': {k: v['id'] for k, v in users.items()}, 'flow_ids': [flow['id'], replacement['id']]}
        if args.fixture_json:
            Path(args.fixture_json).write_text(json.dumps(summary, ensure_ascii=False))
        print(json.dumps(summary, ensure_ascii=False), flush=True)
    finally:
        for context in browser.contexts:
            context.close()
        browser.close()
