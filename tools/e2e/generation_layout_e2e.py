"""Offline layout regression; all API calls are mocked, no model/database access.

Serve web/dist locally, set AIGO_E2E_BASE_URL and AIGO_E2E_DIST to that build,
then run with Python Playwright. Optional AIGO_E2E_ARTIFACT_DIR keeps QA images.
"""
import json
import os
from pathlib import Path
from urllib.parse import urlparse

from playwright.sync_api import sync_playwright

BASE = os.environ.get('AIGO_E2E_BASE_URL', 'http://127.0.0.1:5173')
DIST = os.environ.get('AIGO_E2E_DIST')
ARTIFACTS = os.environ.get('AIGO_E2E_ARTIFACT_DIR')
USER = {'id': 'layout-user', 'username': 'layout-mock', 'display_name': '离线布局回归',
        'role': 'teacher', 'roles': ['teacher'],
        'permissions': ['question:generate', 'question:view', 'batch:run'], 'bank_ids': []}
POINT = {'id': 'layout-kp', 'version_id': 'layout-v', 'category': '临床综合',
         'subject': '呼吸系统', 'unit': '肺炎', 'sub_item': '概述',
         'topic': '诊断与鉴别诊断', 'outline_code': '110.4.1.5.1.5'}
STEM = '患者，男，65岁。突发高热、寒战3天，伴咳嗽、咳铁锈色痰，右侧胸痛。T 39.2℃，P 108次/分，R 24次/分。'


def check_case(browser, width, state, long_code=False, short=False):
    context = browser.new_context(viewport={'width': width, 'height': 1000})
    page = context.new_page()
    errors, unknown, calls = [], [], []
    page.on('pageerror', lambda error: errors.append(str(error)))
    point = dict(POINT, outline_code='110.' + '4.1.' * 10 if long_code else POINT['outline_code'])
    question = {'id': 'layout-q', 'clinical_stem': STEM, 'answer': 'A', 'status': 'ai_reviewed',
                'options': [{'label': x, 'text': '选项' + x} for x in 'ABCDE'],
                'explanation': '正确答案为A。受控显示内容。故选A。', 'version': 1,
                'knowledge_points': [point], 'outline_code': point['outline_code']}
    progress = {'question_id': 'layout-q', 'question_version': 1, 'max_attempts': 3,
                'outline_code': point['outline_code'], 'stem_summary': '患者，男，65岁。' if short else STEM,
                'check_started_at': '2026-10-22T02:48:20+08:00'}
    if state == 'discarded':
        counts = {'discarded': 1, 'issues': 1}
        progress.update(discarded=True, verdict='issues_found', task_status='', attempts=0,
                        scores={'scientific': 85, 'logic': 75, 'a2_fit': 85, 'answer': 70},
                        question=question, suggestion='受控选项层级问题',
                        check_completed_at='2026-10-22T02:48:37+08:00')
    elif state == 'passed':
        counts = {'passed': 1}
        progress.update(task_status='succeeded', question_status='ai_reviewed', verdict='pass', attempts=1,
                        check_completed_at='2026-10-22T02:48:37+08:00')
    elif state == 'failed':
        counts = {'exhausted': 1}
        progress.update(task_status='exhausted', discarded=True, check_failed=True, attempts=3,
                        suggestion='模型请求超时，已达到检查次数上限，本题已丢弃。',
                        check_completed_at='2026-10-22T02:48:37+08:00')
    else:
        counts = {'running': 1}
        progress.update(task_status='running', question_status='ai_draft', attempts=1)
    workspace = {'version': 1, 'userId': USER['id'], 'runId': 'layout-run',
                 'questionIds': ['layout-q'], 'workspaceStatus': 'checking',
                 'selectedKPs': [point], 'runKnowledgePoints': [point],
                 'selectedCount': 1, 'selectedDifficulty': '0.65'}
    context.add_init_script('localStorage.setItem("aigo_token","offline-token");'
                           + 'localStorage.setItem("aigo_user",' + json.dumps(json.dumps(USER)) + ');'
                           + 'localStorage.setItem("aigo_generation_workspace_v1:layout-user",'
                           + json.dumps(json.dumps(workspace)) + ');')

    def fake_api(route):
        path = urlparse(route.request.url).path
        calls.append(path)
        if path == '/api/auth/me':
            body = USER
        elif path == '/api/knowledge-versions':
            body = {'versions': [{'id': 'layout-v', 'name': '离线大纲', 'year': 2024,
                                  'status': 'published', 'point_count': 1}], 'default_version_id': 'layout-v'}
        elif path == '/api/knowledge-points/search':
            body = {'points': [point], 'total': 1, 'page': 1}
        elif path == '/api/knowledge-points/tree':
            body = {'tree': []}
        elif path == '/api/knowledge-points/meta':
            body = {'categories': [], 'subjects': []}
        elif path == '/api/stats':
            body = {'question_count': int(state == 'passed'), 'knowledge_count': 1}
        elif path == '/api/generation-runs/layout-run':
            body = {'run': {'id': 'layout-run', 'status': 'succeeded', 'requested_count': 1,
                            'question_ids': ['layout-q'], 'attempts': 1, 'max_attempts': 3,
                            'started_at': '2026-10-22T02:47:42+08:00',
                            'completed_at': '2026-10-22T02:48:20+08:00'},
                    'questions': [question] if state == 'passed' else [], 'count': 1,
                    'request_summary': {'knowledge_points': [point]}}
        elif path == '/api/ai-check/progress':
            body = {'total': 1, 'counts': counts, 'items': [progress]}
        elif path in ['/api/my/summary', '/api/generation-rules/preview']:
            body = {}
        else:
            unknown.append(path)
            body = {}
        route.fulfill(status=200, content_type='application/json', body=json.dumps(body, ensure_ascii=False))

    page.route('**/api/**', fake_api)
    if DIST:
        page.route(BASE + '/generate', lambda r: r.fulfill(path=str(Path(DIST) / 'index.html'), content_type='text/html'))
    try:
        page.goto(BASE + '/generate')
        page.wait_for_load_state('networkidle')
        page.locator('.kp-item').wait_for()
        page.get_by_text({'discarded': '质量淘汰', 'passed': '已通过', 'failed': '检查失败，已丢弃',
                          'checking': '首次检查中'}[state], exact=False).first.wait_for()
        bounds = page.evaluate('''() => {
          const box = s => { const e=document.querySelector(s),r=e.getBoundingClientRect();
            return {x:r.x,right:r.right,top:r.top,bottom:r.bottom,width:r.width,scroll:e.scrollWidth}; };
          const left=box('.editor-column'),right=box('.config-column');
          const panels=[...document.querySelectorAll('.editor-column > *')].map(e=>{
            const r=e.getBoundingClientRect();return {x:r.x,right:r.right,top:r.top,bottom:r.bottom};});
          return {viewport:innerWidth,scroll:document.documentElement.scrollWidth,left,right,panels};
        }''')
        assert bounds['scroll'] <= width + 1, (width, state, long_code, 'horizontal overflow', bounds)
        for panel in bounds['panels']:
            assert panel['right'] <= bounds['left']['right'] + 1, (width, state, long_code, 'panel escaped column', bounds)
            if panel['bottom'] > bounds['right']['top'] and panel['top'] < bounds['right']['bottom']:
                overlap = min(panel['right'], bounds['right']['right']) - max(panel['x'], bounds['right']['x'])
                assert overlap <= 1, (width, state, long_code, 'columns overlap', bounds)
        if state == 'discarded':
            page.locator('.discard-item').click()
            dialog = page.get_by_role('dialog', name='淘汰题目详情', exact=True)
            dialog.wait_for()
            assert point['outline_code'] in dialog.inner_text()
            assert STEM in dialog.inner_text()
            assert dialog.evaluate('(e) => e.scrollWidth <= e.clientWidth + 1'), (width, long_code, 'modal content overflow')
            page.get_by_role('button', name='关闭', exact=True).click()
            dialog.wait_for(state='hidden')
        if ARTIFACTS and width in [1440, 390] and state == 'discarded':
            target = Path(ARTIFACTS)
            target.mkdir(parents=True, exist_ok=True)
            page.screenshot(path=str(target / f'generation-{width}-longcode-{long_code}-short-{short}.png'), full_page=True)
        assert not errors, errors
        assert not unknown, unknown
        return {'width': width, 'state': state, 'long_code': long_code, 'scroll': bounds['scroll']}
    finally:
        context.close()


if __name__ == '__main__':
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True, channel='chrome')
        try:
            results = [check_case(browser, width, state) for width in [1920, 1440, 1366, 1320, 1280, 390]
                       for state in ['discarded', 'passed', 'checking', 'failed']]
            results.append(check_case(browser, 1440, 'discarded', short=True))
            results.extend(check_case(browser, width, 'discarded', long_code=True) for width in [1440, 390])
            print('PASS:', len(results), 'offline browser layout/state/modal scenarios')
        finally:
            browser.close()
