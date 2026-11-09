#!/usr/bin/env python3
"""Opt-in C6 acceptance driver. Only accepts the Go-owned isolated fixture.

Run TestCustomerBrowserAcceptance rather than starting a daily database/server.
Screenshots, downloads and the result JSON remain inside the registered temporary
root for inspection; the outer owner removes that root after recording results.
"""
import argparse
import json
import os
import re
import time
import traceback
import zipfile
from pathlib import Path
from urllib.parse import urlparse
from urllib.request import urlopen
from playwright.sync_api import sync_playwright, expect


def validate_inputs(fixture_path, artifact_dir):
    fixture_path = Path(fixture_path).resolve(strict=True)
    fixture = json.loads(fixture_path.read_text())
    root = Path(fixture['root']).resolve(strict=True)
    registered = json.loads((root / 'resources.json').read_text())
    artifact_dir = Path(artifact_dir).resolve()
    if Path(registered['root']).resolve() != root or not registered['database'].startswith('aigo_c6'):
        raise ValueError('An owned C6 resource manifest is required')
    if fixture_path.parent != root or artifact_dir.parent != root or artifact_dir == root:
        raise ValueError('Fixture and artifacts must belong to the registered root')
    url = urlparse(fixture['base_url'])
    if fixture.get('protocol') != 'aigo-c6-browser-v1' or not re.fullmatch(r'[a-f0-9]{48}', fixture.get('nonce', '')):
        raise ValueError('Unsupported fixture protocol or missing ownership nonce')
    if url.scheme != 'http' or url.hostname != '127.0.0.1' or url.port not in registered['ports'] or url.username or url.password or url.path:
        raise ValueError('The acceptance app must use its registered loopback URL')
    if fixture.get('model') != 'controlled-client-no-provider-http':
        raise ValueError('This driver only runs against the controlled model fixture')
    with urlopen(fixture['base_url'] + '/__c6_fixture/' + fixture['nonce'], timeout=5) as response:
        handshake = json.load(response)
    if handshake != {k: fixture[k] for k in ('protocol', 'nonce', 'model')}:
        raise ValueError('The server does not own this controlled acceptance fixture')
    artifact_dir.mkdir(mode=0o700, exist_ok=True)
    return fixture, artifact_dir


class Acceptance:
    def __init__(self, fixture, artifacts):
        self.f = fixture
        self.root = artifacts
        self.base = fixture['base_url']
        self.steps = []
        self.console_errors = []
        self.expected_rejection_pages = set()
        self.page_errors = []
        self.http_errors = []
        self.dialog_accept = True
        self.contexts = []
        self.viewports = []
        self.review_ui = fixture.get("question_review_ui", True)
        self.legacy_paths = {"/review", "/review-decisions", "/review-results", "/review-restore", "/review-flows", "/my-revisions", "/share-requests"}

    def record(self, name, detail=None):
        self.steps.append({'name': name, 'passed': True, 'detail': detail})
        print('PASS', name, flush=True)

    def attach(self, page):
        page.set_default_timeout(15000)
        page.on('dialog', lambda d: d.accept() if self.dialog_accept else d.dismiss())
        page.on('pageerror', lambda e: self.page_errors.append(str(e)))
        page.on('console', lambda m: self.console_errors.append(m.text) if m.type == 'error' and not (id(page) in self.expected_rejection_pages and m.location.get('url','').endswith('/api/batch/submit') and '400' in m.text) else None)
        page.on('response', lambda r: self.http_errors.append({'status': r.status, 'url': r.url}) if r.status >= 400 and r.headers.get('x-c6-expected-error')!='submission-probe' else None)
        return page

    def goto(self, page, url):
        page.goto(url)
        page.wait_for_load_state('networkidle')

    def login(self, browser, key, viewport=None, device_scale_factor=1):
        ctx = browser.new_context(viewport=viewport or {'width': 1440, 'height': 900}, accept_downloads=True, device_scale_factor=device_scale_factor)
        self.contexts.append(ctx)
        p = self.attach(ctx.new_page())
        self.goto(p, self.base + '/login')
        acct = self.f['accounts'][key]
        p.get_by_placeholder('请输入用户名', exact=True).fill(acct['username'])
        p.get_by_placeholder('请输入密码', exact=True).fill(acct['password'])
        p.get_by_role('button', name='登录', exact=True).click()
        expect(p).to_have_url(self.base + '/my')
        expect(p.locator('main')).to_be_visible()
        if acct.get('id'):
            assert json.loads(p.evaluate('localStorage.getItem("aigo_user")'))['id'] == acct['id']
        return p

    def api(self, page, path, method='GET', body=None):
        if not path.startswith('/api/'):
            raise ValueError('Only local API paths may be requested')
        result = page.evaluate('''async ({path,method,body})=>{
          const r=await fetch(path,{method,headers:{'Content-Type':'application/json',Authorization:'Bearer '+localStorage.getItem('aigo_token')},...(body===null?{}:{body:JSON.stringify(body)})});
          const data=await r.json();return {status:r.status,data};
        }''', {'path': path, 'method': method, 'body': body})
        if result['status'] >= 400:
            raise AssertionError(f'{method} {path}: HTTP {result["status"]}: {result["data"]}')
        return result['data']

    def poll(self, callback, predicate, message, seconds=35):
        until = time.monotonic() + seconds
        last = None
        while time.monotonic() < until:
            last = callback()
            if predicate(last):
                return last
            time.sleep(.2)
        raise AssertionError(f'{message}: {last}')

    def select_source(self, p, text):
        self.goto(p, self.base + '/new-questions')
        row = p.locator('.new-question-card').filter(has_text=text)
        expect(row).to_have_count(1)
        row.click()
        return row

    def edit_source(self, p, text, revision_text, version=2):
        self.select_source(p, text)
        area = p.locator('.edit-textarea').first
        area.fill(area.input_value().replace('该患者最可能的诊断是', revision_text + '该患者最可能的诊断是'))
        p.get_by_role('button', name='保存微调', exact=True).click()
        expect(p.locator('.toast')).to_contain_text('已保存为 v'+str(version))

    def open_learning(self, p, analyze=False):
        p.get_by_role('button', name='比较原始版 / 总结经验', exact=True).click()
        expect(p.locator('.learning-content')).to_contain_text('原始版 v1')
        if analyze:
            p.get_by_role('button', name='AI总结修改经验', exact=True).click()
        expect(p.locator('.learning-candidate')).to_have_count(3, timeout=30000)

    def rule_input(self, page, cid):
        candidate=page.locator('.learning-candidate').filter(has=page.get_by_label('选择候选 '+cid,exact=True))
        return candidate.get_by_role('textbox',name=re.compile('^规则 ')).first

    def save_candidate(self, p, cid, text, active=False):
        p.get_by_label('选择候选 ' + cid, exact=True).check()
        self.rule_input(p,cid).fill(text)
        p.get_by_role('button', name='预览所选经验', exact=True).click()
        p.get_by_role('button', name='保存并启用' if active else '保存草稿', exact=True).click()
        expect(p.locator('.learning-notice')).to_contain_text('已保存 1 条')
        expect(p.get_by_label('选择候选 ' + cid, exact=True)).to_be_disabled()

    def take(self, p, name):
        p.screenshot(path=str(self.root / (name + '.png')), full_page=False)

    def ui_download(self, p, kind):
        with p.expect_download() as event:
            p.get_by_role('button', name='导出 ' + kind, exact=True).click()
        download = event.value
        path = self.root / ('export.' + ('xlsx' if kind == 'Excel' else 'docx'))
        download.save_as(path)
        with zipfile.ZipFile(path) as z:
            texts = '\n'.join(z.read(n).decode('utf-8') for n in z.namelist() if n.endswith('.xml'))
        assert '胃溃疡' in texts and '反复上腹痛' in texts
        assert 'C6已启用个人经验采用标记' not in texts
        self.record('physical_' + kind + '_export', {'bytes': path.stat().st_size, 'zip_xml_checked': True})

    def core(self, browser):
        p = self.login(browser, 'teacher')
        u = json.loads(p.evaluate('localStorage.getItem("aigo_user")'))
        assert not any(x.startswith('review:') for x in u['permissions'])
        self.edit_source(p, 'C6学习源题', '精简病例表达，保留已有决策线索。')
        self.open_learning(p, True)
        self.take(p, 'teacher-diff-candidates-1440')
        self.save_candidate(p, 'concise', 'C6个人经验草稿标记：只描述病例给出的线索。')
        self.save_candidate(p, 'detailed', 'C6已启用个人经验采用标记：按病例已有线索组织表达。', True)
        self.record('zero_review_v2_three_candidates_draft_and_activate')
        self.goto(p, self.base + '/my-experiences')
        expect(p.locator('.personal-experience-card')).to_have_count(2)
        assert 'C6已启用个人经验采用标记' in p.inner_text('body')
        self.goto(p, self.base + '/generate')
        p.locator('label.kp-item').filter(has_text='测试胃溃疡').locator('input').check()
        with p.expect_response(lambda r: r.request.method == 'POST' and r.url.endswith('/api/questions/generate')) as response:
            p.get_by_role('button', name='生成试题', exact=True).click()
        run = response.value.json()['run']['id']
        data = self.poll(lambda: self.api(p, '/api/generation-runs/' + run), lambda d: len(d.get('questions', [])) == 1, 'generated question did not pass mandatory check')
        generated = data['questions'][0]
        expect(p.locator('.readonly-stem').first).to_be_visible(timeout=15000)
        expect(p.get_by_role('button', name='填写或查看个人需求反馈', exact=True)).to_have_count(0)
        expect(p.get_by_text('我的需求反馈', exact=True)).to_have_count(0)
        p.get_by_role('button', name='查看执行记录', exact=True).click()
        expect(p.get_by_role('button', name='刷新记录', exact=True)).to_be_visible()
        self.record('generation_retired_feedback_absent_and_execution_diagnostics_ui')
        adoption = self.api(p, '/api/questions/' + generated['id'] + '/personal-experiences')['snapshot']
        assert len(adoption['entries']) == 1 and adoption['entries'][0]['revision'] == 1
        assert 'C6已启用个人经验采用标记' in json.dumps(adoption, ensure_ascii=False)
        self.select_source(p, '反复上腹痛2年，加重1天')
        p.locator('.confirm-current-button').click()
        self.poll(lambda: self.api(p, '/api/questions/' + generated['id']), lambda q: q['library_state'] == 'active', 'zero-round confirmation did not complete')
        self.goto(p, self.base + '/bank?scope=personal')
        row = p.locator('.question-item').filter(has_text='反复上腹痛2年，加重1天')
        expect(row).to_have_count(1)
        row.locator('.q-checkbox').check()
        self.ui_download(p, 'Excel')
        self.ui_download(p, 'Word')
        self.take(p, 'teacher-personal-library-1440')
        self.record('generated_experience_adoption_mandatory_check_zero_round_confirm')
        self.goto(p, self.base + '/my-experiences')
        card = p.locator('.personal-experience-card').filter(has_text='C6已启用个人经验采用标记')
        for width,height in [(1440,900),(390,844),(720,450)]:
            p.set_viewport_size({'width':width,'height':height})
            controls=p.locator('.experience-page-filters').locator('select,input,button')
            boxes=[controls.nth(i).bounding_box() for i in range(controls.count())]
            assert max(b['height'] for b in boxes)-min(b['height'] for b in boxes)<=1,boxes
            if width>700:assert max(b['y']+b['height'] for b in boxes)-min(b['y']+b['height'] for b in boxes)<=1,boxes
            assert p.evaluate('document.documentElement.scrollWidth<=innerWidth+1')
            self.take(p,'rule-filters-aligned-'+str(width))
        p.set_viewport_size({'width':1440,'height':900})
        self.record('rule_filter_controls_equal_height_and_aligned_responsive')
        card.get_by_role('button', name='停用', exact=True).click()
        expect(card.locator('.experience-state')).to_contain_text('草稿')
        expect(card.locator('.experience-actions .experience-success')).to_have_text('已停用')
        expect(p.locator('.panel > .experience-success:not(.experience-action-result)')).to_have_count(0)
        expect(card.locator('.experience-success')).to_have_count(0,timeout=6500)
        expect(card.locator('.experience-state')).to_contain_text('草稿')
        self.record('rule_operation_feedback_is_local_expires_and_preserves_current_status')
        history_button=card.get_by_role('button',name='查看历史',exact=True)
        history_button.scroll_into_view_if_needed()
        before_scroll=p.evaluate('scrollY')
        history_button.click()
        modal=p.locator('dialog[open]')
        expect(modal.get_by_role('heading',name='命题规则历史',exact=True)).to_be_visible()
        expect(modal.get_by_label('查看经验历史版本',exact=True)).to_be_visible()
        assert modal.evaluate('(el)=>el.matches(":modal") && el.contains(document.activeElement)')
        assert abs(p.evaluate('scrollY')-before_scroll)<=1
        history_button.evaluate('(el)=>el.focus()')
        assert modal.evaluate('(el)=>el.contains(document.activeElement)')
        for _ in range(8):
            p.keyboard.press('Tab')
            # Chrome permits Tab into browser chrome; background app controls stay inert.
            focus=modal.evaluate('(el)=>({inside:el.contains(document.activeElement),browser:document.activeElement===document.body&&!document.hasFocus()})')
            assert focus['inside'] or focus['browser'],focus
        if not p.evaluate('document.hasFocus()'):
            p.keyboard.press('Tab')
        self.take(p,'rule-history-modal-1440')
        p.keyboard.press('Escape')
        expect(modal).to_have_count(0)
        expect(history_button).to_be_focused()
        self.record('rule_history_modal_blocks_background_focus_escape_and_trigger_focus_restore')
        card.get_by_role('button',name='修订',exact=True).click()
        expect(modal.get_by_role('heading',name='修订命题规则',exact=True)).to_be_visible()
        p.get_by_label('经验标题',exact=True).fill('C6个人经验修订标题')
        self.dialog_accept=False
        p.keyboard.press('Escape')
        expect(modal).to_be_visible()
        expect(p.get_by_label('经验标题',exact=True)).to_have_value('C6个人经验修订标题')
        self.dialog_accept=True
        for width,height in [(1440,900),(390,844),(720,450)]:
            p.set_viewport_size({'width':width,'height':height})
            body=modal.locator('.experience-dialog-body')
            before=modal.locator('.experience-dialog-heading').bounding_box()['y']
            body.evaluate('(el)=>el.scrollTop=el.scrollHeight')
            assert abs(modal.locator('.experience-dialog-heading').bounding_box()['y']-before)<=1
            close=modal.get_by_role('button',name='关闭命题规则窗口',exact=True).bounding_box()
            assert close['y']>=0 and close['y']+close['height']<=height
            assert p.evaluate('document.documentElement.scrollWidth<=innerWidth+1')
            box=modal.bounding_box();assert box['width']<=width-20 and box['height']<=height-20,box
            self.take(p,'rule-revision-modal-'+str(width))
        self.record('rule_revision_modal_responsive_fixed_close_and_dirty_escape_guard')
        p.set_viewport_size({'width':1440,'height':900})
        p.get_by_role('button',name='保存为草稿',exact=True).click()
        expect(modal).to_have_count(0)
        card=p.locator('.personal-experience-card').filter(has_text='C6个人经验修订标题')
        card.get_by_role('button',name='查看历史',exact=True).click()
        expect(p.get_by_label('查看经验历史版本')).to_be_visible()
        p.get_by_role('button',name='关闭历史',exact=True).click()
        card.get_by_role('button',name='启用',exact=True).click()
        expect(modal.get_by_role('heading',name='启用命题规则',exact=True)).to_be_visible()
        p.get_by_role('button',name='确认启用',exact=True).click()
        expect(card.locator('.experience-state')).to_have_text('已启用')
        rule=next(c for c in self.api(p,'/api/personal-experiences')['experiences'] if c['title']=='C6个人经验修订标题')
        card.get_by_role('button',name='归档',exact=True).click()
        expect(card.locator('.experience-state')).to_have_text('已归档')
        p.get_by_label('筛选经验状态',exact=True).select_option('archived')
        expect(card).to_be_visible()
        card.get_by_role('button',name='恢复为草稿',exact=True).click()
        expect(card).to_have_count(0)
        p.get_by_label('筛选经验状态',exact=True).select_option('draft')
        expect(card.locator('.experience-state')).to_have_text('草稿 / 已停用')
        card.get_by_role('button',name='启用',exact=True).click()
        p.get_by_role('button',name='确认启用',exact=True).click()
        p.get_by_label('筛选经验状态',exact=True).select_option('active')
        expect(card.locator('.experience-state')).to_have_text('已启用')
        restored=next(c for c in self.api(p,'/api/personal-experiences')['experiences'] if c['id']==rule['id'])
        assert restored['revision']==rule['revision']+3
        versions=self.api(p,'/api/personal-experiences/'+rule['id']+'/history')['versions']
        assert any(c['status']=='archived' for c in versions)
        p.get_by_label('筛选经验状态',exact=True).select_option('')
        self.record('rule_archive_filter_restore_and_reactivate_preserve_existing_lifecycle')
        frozen = self.api(p, '/api/questions/' + generated['id'] + '/personal-experiences')['snapshot']
        assert frozen['entries'][0]['revision'] == 1
        self.record('experience_stop_edit_reactivate_keeps_previous_adoption')
        return p, generated

    def race_journeys(self, p):
        self.select_source(p, 'C6学习源题')
        self.open_learning(p)
        p.get_by_label('选择候选 checklist', exact=True).check()
        self.rule_input(p,'checklist').fill('C6本窗口尚未保存，不能被另一窗口导入覆盖。')
        self.dialog_accept = False
        other_row = p.locator('.new-question-card').filter(has_text='C6第二题')
        other_row.locator('input').click()
        expect(other_row.locator('input')).not_to_be_checked()
        expect(self.rule_input(p,'checklist')).to_have_value('C6本窗口尚未保存，不能被另一窗口导入覆盖。')
        self.dialog_accept = True
        p2 = self.attach(p.context.new_page())
        self.select_source(p2, 'C6学习源题')
        self.open_learning(p2)
        self.save_candidate(p2, 'checklist', '另一窗口明确保存的经验。')
        p.bring_to_front()
        p.evaluate('window.dispatchEvent(new Event("focus"))')
        expect(p.get_by_role('button', name='放弃本地副本', exact=True)).to_be_visible(timeout=15000)
        expect(self.rule_input(p,'checklist')).to_have_value('C6本窗口尚未保存，不能被另一窗口导入覆盖。')
        assert self.rule_input(p,'checklist').get_attribute('readonly') is not None
        self.rule_input(p,'checklist').scroll_into_view_if_needed()
        self.take(p, 'two-window-import-conflict')
        p.get_by_role('button', name='放弃本地副本', exact=True).click()
        self.record('two_windows_checkbox_cancel_and_import_conflict_retains_local_copy')
        self.edit_source(p, 'C6第二题', '只保留清楚的决策线索。')
        self.open_learning(p, True)
        p.get_by_label('选择候选 concise', exact=True).check()
        self.rule_input(p,'concise').fill('C6确认移出列表仍应保留的学习编辑。')
        q = self.api(p2, '/api/questions/' + self.f['question_ids']['c6-ui-second'])
        axes = {'question_id': q['id'], 'expected_version': q['version'], 'expected_library_revision': q['library_revision'], 'expected_review_revision': q['review_revision']}
        self.api(p2, '/api/questions/confirm', 'POST', {'items': [axes]})
        p.evaluate('window.dispatchEvent(new Event("aigo:refresh-navigation-badges"))')
        expect(p.locator('[role=status]').filter(has_text='当前源题已移出待整理列表')).to_be_visible(timeout=15000)
        expect(self.rule_input(p,'concise')).to_have_value('C6确认移出列表仍应保留的学习编辑。')
        expect(p.locator('.edit-textarea').first).to_be_disabled()
        p.get_by_role('button', name='重新读取版本', exact=True).click()
        p.get_by_role('button', name='预览所选经验', exact=True).click()
        p.get_by_role('button', name='保存草稿', exact=True).click()
        expect(p.locator('.learning-notice')).to_contain_text('已保存 1 条')
        p2.close()
        self.record('background_zero_round_confirmation_retains_learning_and_refreshes_axes')

    def random_batch(self, p):
        self.goto(p, self.base + '/generate/batch')
        p.get_by_role('button', name=re.compile('全选匹配的全部 3 条')).click()
        expect(p.locator('label.kp-item.checked')).to_have_count(3)
        p.locator('input[type=radio][value=random_total]').check()
        p.locator('#batch-question-count').fill('3')
        skip = p.locator('label.checkbox-row').filter(has_text='跳过已有题目').locator('input')
        if skip.is_checked():
            skip.uncheck()
        expect(p.locator('.batch-candidate-summary')).to_contain_text('可参与出题 3 个要点')
        assert p.get_by_role('button', name='预览候选范围', exact=True).count() == 0
        boxes = [p.locator(i).bounding_box() for i in ['#batch-question-count', '#batch-job-name']]
        assert all(boxes) and abs(boxes[0]['y']-boxes[1]['y']) <= 1, boxes
        assert p.locator('fieldset.batch-mode-row').count() == 0
        p.locator('.batch-candidate-summary').evaluate("el=>el.scrollIntoView({block:'start'})")
        self.take(p, 'unified-batch-aligned-auto-count-1440')
        p.get_by_placeholder('大纲代码前缀',exact=True).fill('wf.')
        p.wait_for_timeout(450)
        companion=p.context.new_page()
        try:
            companion.goto('about:blank')
            for _ in range(3):
                companion.bring_to_front();p.bring_to_front();p.wait_for_timeout(250)
                expect(p.locator('label.kp-item.checked')).to_have_count(3)
                expect(p.get_by_placeholder('大纲代码前缀',exact=True)).to_have_value('wf.')
        finally:companion.close()
        self.record('native_tab_focus_preserves_selected_points_and_code_filter')
        self.goto(p, self.base + '/my')
        self.goto(p, self.base + '/generate/batch')
        expect(p.locator('label.kp-item.checked')).to_have_count(3)
        with p.expect_response(lambda r: r.request.method == 'POST' and r.url.endswith('/api/batch/submit')) as response:
            p.get_by_role('button', name=re.compile('提交随机生成任务')).click()
        job_id = response.value.json()['job_id']
        job = self.poll(lambda: self.api(p, '/api/batch/status/' + job_id), lambda j: j['status'] == 'completed', 'random batch did not settle', 40)
        assert job['total_count'] == 3 and job['completed'] == 3 and job['failed'] == 0
        self.record('random_total_three_candidates_three_units_restores_selection', {'total_count': job['total_count'], 'question_ids': job.get('pipeline_result', {}).get('question_ids', [])})
        def reject_submission(route):route.fulfill(status=400,content_type='application/json',headers={'x-c6-expected-error':'submission-probe'},body=json.dumps({'error':'缺少可用的已启用考点知识卡'}))
        p.route('**/api/batch/submit',reject_submission)
        self.expected_rejection_pages.add(id(p))
        try:
            p.get_by_role('button',name=re.compile('提交随机生成任务')).click()
            expect(p.locator('.batch-submission-error')).to_contain_text('缺少可用的已启用考点知识卡')
            p.wait_for_timeout(3300)
            expect(p.locator('.batch-submission-error')).to_be_visible()
            self.record('batch_submission_error_remains_after_toast_expires')
        finally:
            self.expected_rejection_pages.discard(id(p))
            p.unroute('**/api/batch/submit',reject_submission)


    def sharing_and_review(self, browser, p):
        self.goto(p, self.base + '/bank?scope=personal')
        row = p.locator('.question-item').filter(has_text='C6公共原始快照')
        row.locator('.q-checkbox').check()
        p.get_by_role('button', name='提交所选至全局库', exact=True).click()
        expect(p.locator('dialog[open]')).to_be_visible()
        p.locator('dialog[open]').get_by_role('button', name=re.compile('确认|提交')).last.click()
        expect(p.locator('.bulk-share-notice')).to_contain_text('已提交 1 道')
        admin = self.login(browser, 'admin')
        self.goto(admin, self.base + '/share-requests')
        expect(admin.locator('.question-stem').filter(has_text='C6公共原始快照')).to_be_visible()
        admin.get_by_role('button', name='批准进入全局正式库', exact=True).click()
        expect(admin.get_by_role('button', name='批准进入全局正式库', exact=True)).to_have_count(0)
        q = self.api(p, '/api/questions/' + self.f['question_ids']['c6-ui-share'])
        self.api(p, '/api/questions/' + q['id'], 'PUT', {'expected_version': q['version'], 'expected_library_revision': q['library_revision'], 'expected_review_revision': q['review_revision'], 'clinical_stem': q['clinical_stem'].replace('C6公共原始快照', 'C6作者私有修订v2')})
        self.goto(admin, self.base + '/bank?scope=global')
        expect(admin.locator('.question-item').filter(has_text='C6公共原始快照')).to_be_visible()
        assert 'C6作者私有修订v2' not in admin.inner_text('body')
        self.record('share_ui_admin_approval_fixed_public_v1_private_v2')
        author = self.login(browser, 'review_author')
        self.select_source(author, 'C6可选一轮审核')
        author.locator('.new-question-detail select').last.select_option(self.f['flow_id'])
        author.locator('.submit-current-button').click()
        self.poll(lambda: self.api(author, '/api/questions/' + self.f['question_ids']['c6-ui-review']), lambda q: q['status'] == 'reviewing', 'review submission')
        expert = self.login(browser, 'expert')
        assert 'review:final' not in json.loads(expert.evaluate('localStorage.getItem("aigo_user")'))['permissions']
        self.goto(expert, self.base + '/review')
        expert.locator('.my-task-card').filter(has_text='C6可选一轮审核').click()
        expert.get_by_role('button', name='提交审核', exact=True).click()
        self.poll(lambda: self.api(author, '/api/questions/' + self.f['question_ids']['c6-ui-review']), lambda q: q['library_state'] == 'active' and q['status'] == 'published', 'last round did not confirm library')
        self.record('optional_one_round_expert_without_final_permission_finishes')
        return admin

    def review_isolation(self,browser,teacher):
        admin=self.login(browser,'admin')
        for key in ['admin','business_admin','teacher','expert','review_author','dual','knowledge_reviewer','zero','legacy']:
            p=admin if key=='admin' else self.login(browser,key)
            for path in sorted(self.legacy_paths):
                self.goto(p,self.base+path+'?legacy=1#task')
                expect(p).to_have_url(self.base+'/my')
            aside=p.locator('aside')
            assert not any(x in aside.inner_text() for x in ['审核工作','待我审核','待我决断','审核还原','流程配置','分享管理','我的分享','分享审批','待我修改'])
            if key=='expert':
                expect(aside.get_by_text('AI 出题',exact=True)).to_have_count(0)
        self.goto(admin,self.base+'/REVIEW/')
        expect(admin).to_have_url(self.base+'/my')
        self.record('nine_account_types_seven_hidden_routes_redirect_without_review_navigation')
        self.goto(teacher,self.base+'/new-questions')
        expect(teacher.locator('.submit-current-button')).to_have_count(0)
        expect(teacher.locator('.submit-batch-button')).to_have_count(0)
        assert '提交审核' not in teacher.locator('main').inner_text()
        self.goto(teacher,self.base+'/bank?scope=personal')
        row=teacher.locator('.question-item').filter(has_text='C6公共原始快照')
        row.locator('.q-content').click()
        expect(teacher.get_by_role('button',name='修订题目',exact=True)).to_be_visible()
        expect(teacher.locator('.share-btn')).to_have_count(0)
        expect(teacher.locator('.review-info-field,.review-loading')).to_have_count(0)
        self.goto(admin,self.base+'/stats')
        assert '审核流程现状' not in admin.locator('main').inner_text()
        assert '审核状态分布' not in admin.locator('main').inner_text()
        expect(admin.get_by_role('heading',name=re.compile('AI 质量检查'))).to_be_visible()
        self.goto(admin,self.base+'/roles')
        assert '审题老师' not in admin.locator('main').inner_text()
        assert 'review:' not in admin.locator('main').inner_text()
        admin.get_by_role('button',name=re.compile('新建角色')).click()
        assert not any(x in admin.locator('.perm-picker').inner_text() for x in ['执行审核','提交审核','流程配置','专家库管理'])
        self.take(admin,'isolated-permissions')
        self.goto(admin,self.base+'/users')
        assert self.f['accounts']['expert']['username'] not in admin.locator('main').inner_text()
        assert self.f['accounts']['knowledge_reviewer']['username'] in admin.locator('main').inner_text()
        for select in admin.locator('main select').all():
            assert '审题老师' not in select.inner_text()
        self.take(admin,'isolated-accounts')
        # Real UI saves must preserve grants/identities which the matrix hides.
        author=self.f['accounts']['review_author']
        row=admin.locator('tbody > tr').filter(has_text=author['username'])
        row.get_by_role('button',name='分配权限',exact=True).click()
        matrix=row.locator('xpath=following-sibling::tr[1]').locator('.perm-matrix')
        check=matrix.get_by_label('查看知识库',exact=True)
        original=check.is_checked()
        for value in [not original,original]:
            with admin.expect_response(lambda r:r.request.method=='PUT' and r.url.endswith('/api/users/'+author['id'])):
                check.set_checked(value)
            saved=next(u for u in self.api(admin,'/api/users')['users'] if u['id']==author['id'])
            assert 'review:submit' in saved['direct_permissions'], saved
        dual=self.f['accounts']['dual']
        role=admin.locator('tbody > tr').filter(has_text=dual['username']).locator('select')
        for value in ['admin','teacher']:
            with admin.expect_response(lambda r:r.request.method=='PUT' and r.url.endswith('/api/users/'+dual['id'])):
                role.select_option(value)
            saved=next(u for u in self.api(admin,'/api/users')['users'] if u['id']==dual['id'])
            assert 'expert' in saved['roles'] and saved['role']==value, saved
        self.record('real_user_permission_and_primary_role_saves_preserve_isolated_grants_and_identity')
        self.record('normal_authoring_bank_statistics_permissions_and_accounts_isolated')
        requests=[]
        capture=lambda r:requests.append(r.url)
        admin.on('request',capture)
        try:
            self.goto(admin,self.base+'/my')
            assert any('/api/my/summary?view=authoring' in url for url in requests),requests
            assert not any('/api/questions?' in url for url in requests),requests
            light=self.api(admin,'/api/my/summary?view=authoring')
            assert 'library_active_count' in light and 'pending_review_count' not in light,light
        finally:admin.remove_listener('request',capture)
        self.record('authoring_dashboard_uses_count_only_summary_without_question_list_reads')
        return admin

    def quiet_knowledge_checks(self, page, selector, label, shared=False, focus_only=False, private_cards=False):
        reads = []
        def delayed(route):
            response = route.fetch()
            time.sleep(.2)
            reads.append(1)
            route.fulfill(response=response)
        route_pattern = '**/api/private-knowledge?*' if private_cards else '**/api/knowledge-base?*' if shared else '**/api/personal-experiences?*'
        page.route(route_pattern, delayed)
        # Let a user's explicit smooth-scroll settle before measuring later polling.
        # The observation still spans actual periodic requests after this setup.
        page.evaluate("""selector=>new Promise(resolve=>{
          let last,stable=0;
          function sample(){const y=document.querySelector(selector).getBoundingClientRect().top;stable=y===last?stable+1:0;last=y;if(stable>=6)resolve();else requestAnimationFrame(sample);}
          sample();
        })""",selector)
        page.evaluate("""({selector,panel,loadingText,filters}) => {
          const anchor=document.querySelector(selector);
          const top=anchor.getBoundingClientRect().top;
          const initialDisabled=new Map([...document.querySelectorAll(filters+' button')].map(b=>[b,b.disabled]));
          window.__knowledgeProbe={frames:0,loading:0,missing:0,shift:0,disabled:0,done:false};
          const probe=window.__knowledgeProbe;
          function sample(){
            if(probe.done)return;
            probe.frames++;
            const current=document.querySelector(selector);
            if(current!==anchor)probe.missing++;
            if(current)probe.shift=Math.max(probe.shift,Math.abs(current.getBoundingClientRect().top-top));
            if([...document.querySelectorAll(panel+' [role="status"]')].some(n=>n.textContent===loadingText))probe.loading++;
            if([...document.querySelectorAll(filters+' button')].some(b=>!initialDisabled.has(b)||b.disabled!==initialDisabled.get(b)))probe.disabled++;
            requestAnimationFrame(sample);
          }
          sample();
        }""", {'selector':selector,'panel':'.private-cards' if private_cards else '.kb-library' if shared else '.personal-experiences-page','loadingText':'加载中…' if shared or private_cards else '读取中…','filters':'.collection-toolbar' if private_cards else '.kb-filters' if shared else '.experience-page-filters'})
        try:
            if focus_only:
                for _ in range(2):
                    page.evaluate("window.dispatchEvent(new Event('focus'))")
                    page.wait_for_timeout(1000)
            else:
                page.wait_for_timeout(61000 if shared else 11000)
            result = page.evaluate('window.__knowledgeProbe.done=true;window.__knowledgeProbe')
            assert len(reads) >= 2, ('periodic checks did not execute', len(reads))
            assert result['frames'] > 50 and result['loading']==0 and result['missing']==0 and result['disabled']==0 and result['shift'] <= 1, result
            self.record(label, {**result, 'list_reads': len(reads)})
        finally:
            page.evaluate('window.__knowledgeProbe.done=true')
            page.unroute(route_pattern, delayed)

    def admin_and_views(self, browser, admin, teacher):
        routes = ['/my', '/settings', '/generate', '/generate/batch', '/my-experiences', '/new-questions', '/batch-history', '/knowledge', '/knowledge-base', '/review', '/review-restore', '/my-revisions', '/share-requests', '/review-decisions', '/review-results', '/bank', '/stats', '/review-flows', '/audit', '/users', '/roles', '/system/ai-providers', '/system/generation-quota']
        routes = [p for p in routes if self.review_ui or p not in self.legacy_paths]
        for path in routes:
            self.goto(admin, self.base + path)
            expect(admin.locator('main')).to_be_visible()
            assert urlparse(admin.url).path == path, 'unexpected route redirect: ' + admin.url
        self.record('admin_all_visible_authorized_routes_loaded', {'paths': routes})
        self.goto(admin,self.base+'/system/ai-providers')
        admin.get_by_role('button',name='＋ 新增配置',exact=True).click()
        expect(admin.get_by_label('保存后立即启用此配置',exact=True)).not_to_be_checked()
        admin.get_by_role('button',name='取消',exact=True).click()
        self.record('provider_creation_requires_explicit_activation')
        generation_before=self.api(admin,'/api/system/ai-providers')
        admin.get_by_role('tab',name='AI总结',exact=True).click()
        expect(admin.get_by_label('总结温度',exact=True)).to_have_value('0.6')
        admin.get_by_label('独立配置',exact=True).check()
        admin.get_by_label('总结 API 地址',exact=True).fill('https://summary.invalid/v1')
        admin.get_by_label('总结模型',exact=True).fill('qwen3.8-flash')
        admin.locator('.learning-config input[type=password]').fill('browser-summary-fixture-key')
        admin.get_by_role('button',name='保存总结配置',exact=True).click()
        expect(admin.locator('.learning-config').get_by_role('status')).to_contain_text('总结配置已保存')
        expect(admin.locator('.learning-config input[type=password]')).to_have_value('')
        summary=self.api(admin,'/api/system/ai-learning')
        assert summary['independent'] and summary['model']=='qwen3.8-flash' and summary['temperature']==0.6
        assert 'browser-summary-fixture-key' not in json.dumps(summary)
        assert self.api(admin,'/api/system/ai-providers')==generation_before
        self.take(admin,'summary-provider-desktop')
        admin.set_viewport_size({'width':390,'height':844})
        metrics=admin.evaluate('({width:innerWidth,scroll:document.documentElement.scrollWidth})')
        assert metrics['scroll']<=metrics['width']+1,metrics
        self.take(admin,'summary-provider-mobile')
        admin.set_viewport_size({'width':1440,'height':900})
        admin.get_by_label('沿用出题服务',exact=True).check()
        admin.get_by_role('button',name='保存总结配置',exact=True).click()
        expect(admin.locator('.learning-config').get_by_role('status')).to_contain_text('总结配置已保存')
        assert not self.api(admin,'/api/system/ai-learning')['independent']
        admin.get_by_role('tab',name='出题与首检',exact=True).click()
        self.record('summary_provider_separate_key_model_temperature_mobile_and_generation_isolation')

        summary_before=self.api(admin,'/api/system/ai-learning')
        admin.get_by_role('tab',name='调用参数',exact=True).click()
        expect(admin.get_by_label('出题温度',exact=True)).to_have_value('0.4')
        expect(admin.get_by_label('首检温度',exact=True)).to_have_value('0.2')
        admin.get_by_label('出题温度',exact=True).fill('0.6')
        admin.get_by_label('首检温度',exact=True).fill('0.6')
        admin.get_by_role('button',name='保存调用参数',exact=True).click()
        expect(admin.locator('.sampling-settings').get_by_role('status')).to_contain_text('调用参数已保存')
        sampling=self.api(admin,'/api/system/ai-sampling')
        assert sampling['generation_temperature']==0.6 and sampling['check_temperature']==0.6
        assert self.api(admin,'/api/system/ai-providers')==generation_before
        assert self.api(admin,'/api/system/ai-learning')==summary_before
        for width in [1440,768,390]:
            admin.set_viewport_size({'width':width,'height':900})
            metrics=admin.evaluate('({width:innerWidth,scroll:document.documentElement.scrollWidth})')
            assert metrics['scroll']<=metrics['width']+1,metrics
            expect(admin.get_by_label('首检温度',exact=True)).to_be_visible()
            self.take(admin,'sampling-controls-'+str(width))
        admin.get_by_label('出题温度',exact=True).fill('0.4')
        admin.get_by_label('首检温度',exact=True).fill('0.2')
        admin.get_by_role('button',name='保存调用参数',exact=True).click()
        expect(admin.locator('.sampling-settings').get_by_role('status')).to_contain_text('调用参数已保存')
        admin.set_viewport_size({'width':1440,'height':900})
        self.record('generation_check_sampling_controls_persist_separately_desktop_tablet_mobile')

        self.goto(admin, self.base + '/my-experiences')
        self.quiet_knowledge_checks(admin, '.experience-empty', 'admin_empty_personal_knowledge_stable_across_two_poll_cycles')
        admin.get_by_role('button', name='新增个人知识', exact=True).click()
        admin.get_by_label('知识标题', exact=True).fill('管理端手工命题知识')
        admin.get_by_label('命题要求或经验', exact=True).fill('保留病例中实际给出的事实，不添加未知检查结果。')
        admin.get_by_role('button', name='保存并启用', exact=True).click()
        expect(admin.locator('.personal-experience-card').filter(has_text='管理端手工命题知识')).to_be_visible()
        admin.get_by_role('button', name='修订', exact=True).click()
        admin.get_by_label('经验标题', exact=True).fill('管理端未保存标题')
        self.quiet_knowledge_checks(admin, '.personal-experience-card', 'admin_personal_card_and_editor_stable_across_two_poll_cycles')
        expect(admin.get_by_label('经验标题', exact=True)).to_have_value('管理端未保存标题')
        admin.get_by_role('button', name='取消修订', exact=True).click()
        self.goto(admin, self.base + '/system/generation-quota')
        learning_limit = admin.get_by_label('AI总结并发', exact=True)
        expect(learning_limit).to_have_value('2')
        learning_limit.fill('3')
        admin.get_by_role('button', name='保存并立即生效', exact=True).click()
        expect(admin.get_by_role('button', name='保存并立即生效', exact=True)).to_be_disabled()
        admin.reload()
        expect(learning_limit).to_have_value('3')
        learning_limit.fill('2')
        admin.get_by_role('button', name='保存并立即生效', exact=True).click()
        expect(admin.get_by_role('button', name='保存并立即生效', exact=True)).to_be_disabled()
        self.record('bounded_learning_quota_saved_and_restored')
        search = admin.get_by_label('允许联网搜索', exact=True)
        before = search.is_checked()
        search.set_checked(not before)
        admin.get_by_role('button', name='保存并立即生效', exact=True).click()
        expect(admin.get_by_role('button', name='保存并立即生效', exact=True)).to_be_disabled()
        search.set_checked(before)
        admin.get_by_role('button', name='保存并立即生效', exact=True).click()
        expect(admin.get_by_role('button', name='保存并立即生效', exact=True)).to_be_disabled()
        self.goto(admin, self.base + '/knowledge-base')
        kb = admin.locator('.kb-card').filter(has_text='冻结测试卡')
        kb.get_by_role('button', name='编辑', exact=True).click()
        admin.locator('dialog[open]').get_by_label('标题', exact=True).fill('C6共享知识卡维护验证')
        admin.locator('dialog[open]').get_by_label('临床与鉴别特征', exact=True).fill('C6多行特征第一行\nC6多行特征第二行')
        admin.locator('dialog[open]').get_by_role('button', name='保存草稿', exact=True).click()
        kb = admin.locator('.kb-card').filter(has_text='C6共享知识卡维护验证')
        kb.get_by_role('button', name='启用', exact=True).click()
        admin.locator('dialog[open]').get_by_role('button', name='确认', exact=True).click()
        expect(kb.locator('.kb-state')).to_have_text('已启用')
        kb.get_by_role('button', name='查看与历史', exact=True).click()
        expect(admin.locator('dialog[open] select option')).to_have_count(4)
        lines=admin.locator('dialog[open] .kb-text').filter(has_text='C6多行特征第一行')
        assert '\n' in lines.text_content() and lines.evaluate('e=>getComputedStyle(e).whiteSpace')=='pre-wrap'
        self.record('knowledge_card_multiline_detail_preserves_newlines')
        admin.get_by_role('button', name='关闭详情', exact=True).click()
        admin.get_by_role('button', name='新增知识卡', exact=True).click()
        dialog=admin.locator('dialog[open]')
        dialog.get_by_label('知识卡类型', exact=True).select_option('authoring_guideline')
        expect(dialog.get_by_role('heading', name='关联知识点', exact=True)).to_have_count(0)
        expect(dialog.get_by_role('button', name='添加事实', exact=True)).to_have_count(0)
        dialog.get_by_label('标题', exact=True).fill('C6全局通用命题规范')
        dialog.get_by_label('自定义内容', exact=True).fill('C6全局规范真实采用：五项按同一决策维度核对，不混入个人信息。')
        dialog.get_by_label('来源与版本', exact=True).fill('隔离浏览器验收材料')
        dialog.get_by_role('button', name='保存草稿', exact=True).click()
        global_card=admin.locator('.kb-card').filter(has_text='C6全局通用命题规范')
        expect(global_card).to_contain_text('全局适用')
        global_card.get_by_role('button', name='启用', exact=True).click()
        expect(admin.locator('dialog[open]')).to_contain_text('所有人的新单题和批量任务')
        admin.locator('dialog[open]').get_by_role('button', name='确认', exact=True).click()
        expect(global_card).to_contain_text('已启用')
        global_card.get_by_role('button', name='查看与历史', exact=True).click()
        expect(admin.locator('dialog[open]')).to_contain_text('C6全局规范真实采用')
        admin.get_by_role('button', name='关闭详情', exact=True).click()
        admin.evaluate('window.scrollTo(0,0)')
        self.quiet_knowledge_checks(admin, '.kb-card', 'shared_kb_stable_across_two_real_30_second_cycles', shared=True)
        admin.get_by_label('搜索知识卡', exact=True).fill('C6_shared_empty_marker_no_match')
        admin.locator('.kb-filters').get_by_role('button', name='查询', exact=True).click()
        expect(admin.locator('.kb-empty')).to_be_visible()
        self.quiet_knowledge_checks(admin, '.kb-empty', 'shared_kb_empty_stable_across_focus_checks', shared=True, focus_only=True)
        admin.get_by_label('搜索知识卡', exact=True).fill('')
        admin.locator('.kb-filters').get_by_role('button', name='查询', exact=True).click()
        expect(admin.locator('.kb-card').first).to_be_visible()
        self.record('global_authoring_method_created_without_point_binding_and_activated')
        self.record('admin_manual_knowledge_quota_tool_round_trip_and_shared_card_history')
        for width, height in [(1440, 900), (1366, 768), (390, 844)]:
            teacher.set_viewport_size({'width': width, 'height': height})
            for path in ['/new-questions', '/my-experiences', '/bank?scope=personal', '/generate', '/generate/batch']:
                self.goto(teacher, self.base + path)
                expect(teacher.locator('main')).to_be_visible()
                if path == '/new-questions':
                    teacher.locator('.new-question-card').filter(has_text='长内容不应遮住').click()
                if path == '/generate/batch':
                    teacher.locator('.batch-candidate-summary').evaluate("el=>el.scrollIntoView({block:'start'})")
                    if width >= 1000:
                        fields=[teacher.locator(i).bounding_box() for i in ['#batch-question-count','#batch-job-name']]
                        assert all(fields) and abs(fields[0]['y']-fields[1]['y'])<=1, fields
                teacher.keyboard.press('Tab')
                focus = teacher.evaluate('document.activeElement?.tagName')
                assert focus not in ('BODY', 'HTML', None)
                metrics = teacher.evaluate('({width:innerWidth,scroll:document.documentElement.scrollWidth})')
                assert metrics['scroll'] <= metrics['width'] + 1, (width, path, metrics)
                self.take(teacher, f'view-{width}-{path.split("?")[0].strip("/").replace("/","-")}')
                self.viewports.append({'width': width, 'height': height, 'path': path, 'document_width': metrics['scroll'], 'keyboard_focus': focus})
                teacher.mouse.wheel(0, 550)
            self.record('responsive_and_keyboard_' + str(width))
        review_author_mobile = self.login(browser, 'dual', {'width':390,'height':844})
        def long_flow_name(route):
            response=route.fetch();data=response.json()
            for flow in data.get('flows',[]):flow['name']='临床执业医师综合考试内外妇儿科第一轮联合审题流程'
            route.fulfill(response=response,json=data)
        review_author_mobile.route('**/api/review/available-flows',long_flow_name)
        self.goto(review_author_mobile,self.base+'/new-questions')
        review_author_mobile.locator('.new-question-card').filter(has_text='长内容应在小屏幕').click()
        metrics=review_author_mobile.evaluate('({width:innerWidth,scroll:document.documentElement.scrollWidth})')
        self.take(review_author_mobile,'mobile-editor-with-review-permission')
        assert metrics['scroll'] <= metrics['width']+1, ('review-author-mobile',metrics)
        self.record('mobile_editing_with_optional_review_permission_fits',metrics)
        # A 1440x900 desktop at 200% has a 720x450 CSS viewport. DPR2
        # preserves the physical screenshot size while exercising real reflow.
        zoom = self.login(browser, 'teacher', {'width': 720, 'height': 450}, 2)
        self.goto(zoom, self.base + '/new-questions')
        zoom.locator('.new-question-card').filter(has_text='长内容不应遮住').click(position={'x': 20, 'y': 15})
        metrics = zoom.evaluate('({width:innerWidth,scroll:document.documentElement.scrollWidth,dpr:devicePixelRatio})')
        assert metrics['scroll'] <= metrics['width'] + 1, metrics
        zoom.locator('.edit-textarea').first.scroll_into_view_if_needed()
        self.take(zoom, 'long-question-200-percent-equivalent')
        assert zoom.locator('.edit-textarea').first.is_visible()
        self.record('long_content_200_percent_equivalent_reflow', {'native_browser_zoom': False, 'css_viewport': [720, 450], 'device_scale_factor': 2})

    def manual_knowledge_generation(self, browser):
        p = self.login(browser, 'manual_author')
        user = json.loads(p.evaluate('localStorage.getItem("aigo_user")'))
        assert 'learning:use' not in user['permissions'] and 'learning:analyze' not in user['permissions']
        self.goto(p, self.base + '/my-experiences')
        p.get_by_role('button', name='新增个人知识', exact=True).click()
        p.get_by_label('知识标题', exact=True).fill('手工维护的命题参考')
        p.get_by_label('命题要求或经验', exact=True).fill('C6手工知识真实采用：只使用已给出的病例线索组织解析。')
        p.get_by_role('button', name='保存并启用', exact=True).click()
        expect(p.locator('.personal-experience-card').filter(has_text='手工维护的命题参考')).to_be_visible()
        self.goto(p, self.base + '/generate')
        expect(p.get_by_label('应用命题规则', exact=True)).to_be_checked()
        p.locator('label.kp-item').filter(has_text='测试胃溃疡').locator('input').check()
        with p.expect_response(lambda r: r.request.method=='POST' and r.url.endswith('/api/questions/generate')) as response:
            p.get_by_role('button', name='生成试题', exact=True).click()
        run=response.value.json()['run']['id']
        result=self.poll(lambda:self.api(p, '/api/generation-runs/'+run),lambda d:len(d.get('questions',[]))==1,'manual knowledge generation did not pass first check')
        generated=result['questions'][0]
        adoption=self.api(p,'/api/questions/'+generated['id']+'/personal-experiences')['snapshot']
        assert len(adoption['entries'])==1 and 'C6手工知识真实采用' in json.dumps(adoption,ensure_ascii=False)
        assert 'C6已启用个人经验采用标记' not in json.dumps(adoption,ensure_ascii=False)
        self.goto(p,self.base+'/new-questions')
        p.locator('.new-question-card').first.click()
        p.locator('.confirm-current-button').click()
        self.poll(lambda:self.api(p,'/api/questions/'+generated['id']),lambda q:q['library_state']=='active','manual knowledge author could not confirm without review')
        self.record('manual_knowledge_real_ui_generation_adoption_without_learning_permission')

    def manual_and_login_defaults(self, browser):
        p = self.login(browser, 'manual')
        self.goto(p, self.base + '/my-experiences')
        expect(p.get_by_role('heading', name='个人知识库', level=2, exact=True)).to_be_visible()
        p.get_by_role('button', name='新增个人知识', exact=True).click()
        p.get_by_label('知识标题', exact=True).fill('无需题目和AI权限的手工知识')
        p.get_by_label('命题要求或经验', exact=True).fill('题目解析分段表达，每段说明一个判断。')
        p.get_by_role('button', name='保存草稿', exact=True).click()
        card = p.locator('.personal-experience-card').filter(has_text='无需题目和AI权限的手工知识')
        expect(card).to_be_visible()
        data = self.api(p, '/api/personal-experiences')
        saved = next(c for c in data['experiences'] if c['title']=='无需题目和AI权限的手工知识')
        assert saved['source']['kind']=='manual' and not saved['source'].get('job_id') and not saved['source'].get('question_id')
        self.take(p, 'manual-only-private-knowledge')
        dual = self.login(browser, 'dual')
        user = json.loads(dual.evaluate('localStorage.getItem("aigo_user")'))
        assert user['role']=='teacher', user['role']
        self.goto(dual, self.base + '/generate/batch')
        expect(dual.locator('.batch-mode-options')).to_be_visible()
        if not self.review_ui:
            expect(dual.locator('.role-switch-trigger')).to_have_count(0)
            # Simulate an already signed-in old identity through the preserved API.
            data=self.api(dual,'/api/auth/switch-role','POST',{'role':'expert'})
            dual.evaluate('(d)=>{localStorage.setItem("aigo_token",d.token);localStorage.setItem("aigo_user",JSON.stringify(d.user))}',data)
            dual.reload();dual.wait_for_load_state('networkidle')
            expect(dual.locator('.user-info')).to_contain_text('已保留身份')
            assert '审题老师' not in dual.locator('aside').inner_text()
            self.goto(dual,self.base+'/my-experiences')
            dual.get_by_role('button',name='新增个人知识',exact=True).click()
            expect(dual.locator('dialog[open]').get_by_role('heading',name='新增命题规则',exact=True)).to_be_visible()
            dual.get_by_label('知识标题',exact=True).fill('Expert身份的本人口径')
            dual.get_by_label('命题要求或经验',exact=True).fill('仅整理本人使用的表达习惯。')
            dual.get_by_role('button',name='保存草稿',exact=True).click()
            expert_card=dual.locator('.personal-experience-card').filter(has_text='Expert身份的本人口径')
            expert_card.get_by_role('button',name='查看历史',exact=True).click()
            expect(dual.locator('dialog[open]')).to_contain_text('Expert身份的本人口径')
            expect(dual.locator('dialog[open]')).not_to_contain_text('管理端手工命题知识')
            dual.get_by_role('button',name='关闭历史',exact=True).click()
            self.record('expert_identity_own_rule_creation_and_history_share_the_modal')
            dual.locator('.role-switch-trigger').click()
            dual.get_by_role('option',name=re.compile('命题')).click()
            dual.wait_for_function('JSON.parse(localStorage.getItem("aigo_user")).role === "teacher"')
            self.record('isolated_old_identity_can_switch_to_teacher_without_automatic_privilege_change')
            self.record('manual_private_knowledge_without_question_or_ai_permission_and_teacher_fresh_login')
            return
        dual.locator('.role-switch-trigger').click()
        with dual.expect_response(lambda r: r.request.method=='POST' and r.url.endswith('/api/auth/switch-role')):
            dual.get_by_role('option', name=re.compile('审题')).click()
        dual.wait_for_function('JSON.parse(localStorage.getItem("aigo_user")).role === "expert"')
        self.goto(dual, self.base + '/my')
        assert json.loads(dual.evaluate('localStorage.getItem("aigo_user")'))['role']=='expert'
        newer = self.login(browser, 'dual')
        assert json.loads(newer.evaluate('localStorage.getItem("aigo_user")'))['role']=='teacher'
        dual.reload();dual.wait_for_load_state('networkidle')
        assert json.loads(dual.evaluate('localStorage.getItem("aigo_user")'))['role']=='expert'
        self.goto(dual, self.base + '/my-experiences')
        permissions = json.loads(dual.evaluate('localStorage.getItem("aigo_user")'))['permissions']
        assert 'experience:view' in permissions and 'experience:manage' in permissions
        assert 'question:generate' not in permissions and 'learning:analyze' not in permissions
        dual.get_by_role('button', name='新增个人知识', exact=True).click()
        dual.get_by_label('知识标题', exact=True).fill('审题身份维护的本人知识')
        dual.get_by_label('命题要求或经验', exact=True).fill('整理自己审题时积累的表达经验。')
        dual.get_by_role('button', name='保存草稿', exact=True).click()
        expect(dual.locator('.personal-experience-card').filter(has_text='审题身份维护的本人知识')).to_be_visible()
        assert '管理端手工命题知识' not in dual.inner_text('body')
        self.record('reviewer_template_can_manage_own_knowledge_without_paid_analysis_or_cross_owner_access')
        self.record('manual_private_knowledge_without_question_or_ai_permission_and_teacher_fresh_login')

    def help_hints_and_frontend_routes(self, browser):
        admin = self.login(browser, 'admin')
        paths=['/my','/settings','/knowledge','/knowledge-base','/my-experiences','/new-questions','/generate','/generate/batch','/batch-history','/bank?scope=personal','/review','/review-restore','/my-revisions','/share-requests','/review-decisions','/review-results','/stats','/review-flows','/audit','/users','/roles','/system/ai-providers','/system/generation-quota']
        paths=[p for p in paths if self.review_ui or p not in self.legacy_paths]
        checked=[]
        for path in paths:
            self.goto(admin,self.base+path)
            expect(admin.locator('main')).to_be_visible()
            assert admin.url.split('?')[0]==(self.base+path).split('?')[0], (path,admin.url)
            guide_expected=path.split('?')[0] in ['/my','/generate','/generate/batch','/new-questions','/bank']
            expect(admin.locator('.authoring-guide')).to_have_count(1 if guide_expected else 0)
            expect(admin.get_by_text('可选支线',exact=True)).to_have_count(0)
            expect(admin.get_by_role('button',name='可选操作',exact=True)).to_have_count(0)
            expect(admin.get_by_text('我的需求反馈',exact=True)).to_have_count(0)
            hints=admin.locator('.ui-help-button:visible')
            labels=[]
            for i in range(hints.count()):
                hint=hints.nth(i)
                hint.hover()
                popup=admin.get_by_role('tooltip')
                expect(popup).to_be_visible()
                assert popup.inner_text().strip(), (path,i)
                rect=popup.bounding_box()
                assert rect and rect['x']>=11 and rect['x']+rect['width']<=1440-11, (path,rect)
                labels.append(hint.get_attribute('aria-label'))
                admin.mouse.move(0,0)
                expect(popup).to_have_count(0)
            checked.append({'path':path,'hints':labels})
        self.record('all_visible_frontend_routes_and_hover_leave_help',checked)
        self.goto(admin,self.base+'/users')
        dates=admin.locator('.time-cell').all_inner_texts()
        assert dates and all(re.fullmatch(r'\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}',date.strip()) for date in dates),dates
        self.record('user_creation_times_have_stable_chinese_interface_format')
        group=admin.locator('.navigation-group-button').filter(has_text=re.compile('^题库$'))
        for _ in range(20):
            before=group.get_attribute('aria-expanded')
            admin.evaluate("window.dispatchEvent(new Event('aigo:refresh-navigation-badges'))")
            group.click()
            expect(group).to_have_attribute('aria-expanded','false' if before=='true' else 'true')
        self.record('bank_navigation_group_toggles_on_each_single_click_during_background_refresh')
        self.record('guidance_is_limited_to_main_workflow_pages_without_optional_branch_banners')
        self.goto(admin,self.base+'/generate')
        second=admin.locator('.guide-steps li').nth(1)
        expect(second).to_contain_text('需有出题任务')
        expect(second.get_by_role('button')).to_be_disabled()
        self.take(admin,'guide-no-task-desktop')
        self.record('generation_step_requires_a_task_and_explains_its_unavailable_state')
        self.goto(admin,self.base+'/my-experiences')
        button_layout=[]
        for width,height in [(1440,900),(390,844),(720,450)]:
            admin.set_viewport_size({'width':width,'height':height})
            heading=admin.locator('.experience-page-heading')
            add=heading.get_by_role('button',name='新增个人知识',exact=True)
            back=heading.get_by_role('link',name='返回题目整理',exact=True)
            expect(add).to_be_visible();expect(back).to_be_visible()
            a,b=add.bounding_box(),back.bounding_box()
            assert abs(a['height']-b['height'])<=1 and abs(a['y']-b['y'])<=1,(width,a,b)
            style=back.evaluate('(el)=>{const s=getComputedStyle(el);return {decoration:s.textDecorationLine,display:s.display,align:s.alignItems}}')
            # A flex parent's inline-flex child is blockified to computed flex.
            assert style['decoration']=='none' and style['display'] in ['flex','inline-flex'] and style['align']=='center',style
            back.hover();add.focus();admin.keyboard.press('Tab')
            expect(back).to_be_focused()
            assert back.evaluate('(el)=>getComputedStyle(el).outlineStyle')=='solid'
            assert admin.evaluate('document.documentElement.scrollWidth<=innerWidth+1')
            button_layout.append({'width':width,'height':a['height'],'aligned':True})
            self.take(admin,f'personal-knowledge-heading-actions-{width}')
        admin.set_viewport_size({'width':1440,'height':900})
        back.click();expect(admin).to_have_url(self.base+'/new-questions')
        self.record('personal_knowledge_return_link_matches_buttons_and_supports_keyboard_navigation',button_layout)
        self.goto(admin,self.base+'/knowledge-base')
        admin.get_by_role('button',name='新增知识卡',exact=True).click()
        dialog=admin.locator('dialog[open]')
        hint=dialog.get_by_role('button',name='知识卡维护说明',exact=True)
        hint.hover()
        expect(dialog.get_by_role('tooltip')).to_be_visible()
        hint.press('Escape')
        expect(admin.get_by_role('tooltip')).to_have_count(0)
        expect(dialog).to_be_visible()
        hint.focus()
        admin.keyboard.press('Tab')
        admin.keyboard.press('Shift+Tab')
        expect(admin.get_by_role('tooltip')).to_be_visible()
        admin.keyboard.press('Escape')
        expect(dialog).to_be_visible()
        admin.get_by_role('button',name='关闭编辑',exact=True).click()
        self.record('native_dialog_help_above_modal_keyboard_escape_preserves_editor')
        admin.set_viewport_size({'width':390,'height':844})
        self.goto(admin,self.base+'/system/generation-quota')
        for hint in admin.locator('.ui-help-button:visible').all():
            hint.hover()
            popup=admin.get_by_role('tooltip')
            expect(popup).to_be_visible()
            rect=popup.bounding_box()
            assert rect['x']>=11 and rect['x']+rect['width']<=379 and rect['y']>=11 and rect['y']+rect['height']<=833, rect
            if hint.get_attribute('aria-label')=='在途配额计数说明':
                self.take(admin,'mobile-quota-help-open')
            admin.mouse.move(0,0)
            expect(popup).to_have_count(0)
        self.take(admin,'mobile-quota-help')
        self.record('small_viewport_help_clamped_to_visible_edges')
        ctx=browser.new_context(viewport={'width':390,'height':844},is_mobile=True,has_touch=True)
        self.contexts.append(ctx)
        touch=self.attach(ctx.new_page())
        touch.goto(self.base+'/login')
        touch.evaluate('data=>{localStorage.setItem("aigo_token",data.token);localStorage.setItem("aigo_user",data.user)}',admin.evaluate('({token:localStorage.getItem("aigo_token"),user:localStorage.getItem("aigo_user")})'))
        self.goto(touch,self.base+'/knowledge-base')
        hint=touch.get_by_role('button',name='知识卡使用说明',exact=True)
        hint.tap();expect(touch.get_by_role('tooltip')).to_be_visible()
        hint.tap();expect(touch.get_by_role('tooltip')).to_have_count(0)
        hint.tap();expect(touch.get_by_role('tooltip')).to_be_visible()
        touch.get_by_role('heading',name='知识库',level=2,exact=True).tap()
        expect(touch.get_by_role('tooltip')).to_have_count(0)
        self.record('touch_help_toggle_and_outside_dismiss')

    def bind_primary_point(self,dialog):
        binding=dialog.locator('.point-binding')
        binding.locator('.binding-trigger').click()
        expect(binding.get_by_label('绑定大纲版本',exact=True)).to_have_value(self.f['points'][0]['version_id'])
        path=[self.f['points'][0].get(k,'') for k in ['category','subject','unit','sub_item']]
        directory=binding.locator('.binding-tree')
        names=list(filter(None,path))
        for index,name in enumerate(names):
            label=directory.locator(':scope > .directory-node > .directory-row > .directory-label[title='+json.dumps(name,ensure_ascii=False)+']')
            expect(label).to_be_visible()
            if index+1<len(names):
                expand=label.locator('..').locator('button.directory-expand')
                expect(expand).to_be_visible()
                if expand.get_attribute('aria-expanded')!='true':expand.click()
                directory=label.locator('../..').locator(':scope > .directory-children')
            else:label.click()
        binding.get_by_label('绑定 '+self.f['points'][0]['topic']+'（'+self.f['points'][0]['outline_code']+'）',exact=True).check()
        binding.get_by_role('button',name='完成选择',exact=True).click()
        expect(binding.locator('.binding-workspace')).to_have_count(0)

    def manual_card_tree_binding(self,browser,teacher,admin):
        self.goto(teacher,self.base+'/my-experiences')
        teacher.get_by_role('button',name='知识卡',exact=True).click()
        teacher.get_by_role('button',name='新增知识卡',exact=True).click()
        dialog=teacher.locator('dialog[open]')
        expect(dialog.locator('.binding-workspace')).to_have_count(0)
        dialog.get_by_label('标题',exact=True).fill('C6手工自用知识卡')
        dialog.get_by_label('知识卡类型',exact=True).select_option('custom')
        dialog.get_by_label('自定义内容',exact=True).fill('C6手工卡树形绑定采用标记：解析按选项分段，不添加未提供的检查结果。')
        self.bind_primary_point(dialog)
        dialog.get_by_role('button',name='保存并启用',exact=True).click()
        row=teacher.locator('.private-knowledge-card').filter(has_text='C6手工自用知识卡')
        expect(row).to_be_visible()
        manual=next(c for c in self.api(teacher,'/api/private-knowledge')['cards'] if c['title']=='C6手工自用知识卡')
        assert manual['status']=='active' and not manual['selected'] and not manual.get('origin')
        assert manual['knowledge_points'][0]['id']==self.f['points'][0]['id']
        def own_preview():return self.api(teacher,'/api/knowledge-base/prepare','POST',{'knowledge_points':[self.f['points'][0]]})
        assert all(c['id']!=manual['id'] for b in own_preview()['bundles'] for c in b['cards'])
        with teacher.expect_response(lambda r:r.request.method=='PUT' and r.url.endswith('/api/knowledge-selections')):
            row.get_by_label('用于出题 C6手工自用知识卡',exact=True).check()
        assert any(c['id']==manual['id'] for b in own_preview()['bundles'] for c in b['cards'])
        self.record('manual_private_card_tree_bind_enable_explicit_self_use_without_AI_summary')
        for width,height in [(1440,900),(390,844),(720,450)]:
            teacher.set_viewport_size({'width':width,'height':height})
            row.get_by_role('button',name='编辑',exact=True).click()
            dialog=teacher.locator('dialog[open]');binding=dialog.locator('.point-binding')
            binding.locator('.binding-trigger').click()
            binding.get_by_label('绑定大纲版本',exact=True).select_option(self.f['binding_version'])
            expect(binding.locator('.binding-pagination')).to_contain_text('45 个考点')
            expect(binding.locator('.binding-chips')).to_contain_text(self.f['points'][0]['topic'])
            if width==390:binding.get_by_role('button',name='考点列表',exact=True).click()
            # Select across two pages up to the existing 20-point card limit.
            checks=binding.locator('.binding-point input')
            for i in range(18):checks.nth(i).check()
            binding.get_by_role('button',name='绑定考点下一页',exact=True).click()
            expect(binding.locator('.binding-pagination')).to_contain_text('2 / 3 页')
            checks.first.check()
            expect(checks.nth(1)).to_be_disabled()
            expect(binding.locator('.binding-footer')).to_contain_text('已达20个上限')
            checks.first.uncheck()
            expect(checks.nth(1)).not_to_be_disabled()
            binding.get_by_role('button',name='绑定考点上一页',exact=True).click()
            expect(checks.first).to_be_checked()
            if width==390:binding.get_by_role('button',name='大纲目录',exact=True).click()
            binding.locator('.binding-directory').get_by_role('button',name='展开临床综合',exact=True).click()
            metrics=binding.evaluate("""el=>{const box=el.querySelector('.binding-workspace'),dir=el.querySelector('.binding-directory .binding-scroll'),list=el.querySelector('.binding-results .binding-scroll');return {width:innerWidth,height:innerHeight,pageWidth:document.documentElement.scrollWidth,panel:box.getBoundingClientRect().height,panelWidth:box.getBoundingClientRect().width,directoryScroll:dir.scrollHeight>dir.clientHeight,listScroll:list.scrollHeight>list.clientHeight,bodyOverflow:document.body.scrollHeight>innerHeight}}""")
            assert metrics['pageWidth']<=width+1 and metrics['panel']<=max(280,height*.58)+1 and metrics['panelWidth']<=width-28,metrics
            assert metrics['directoryScroll'],metrics
            self.take(teacher,'binding-tree-'+str(width)+'x'+str(height))
            if width==390:
                binding.get_by_role('button',name='考点列表',exact=True).click()
                self.take(teacher,'binding-mobile-point-list')
                assert binding.locator('.binding-results .binding-scroll').evaluate('(el)=>el.scrollHeight>el.clientHeight')
            else:assert metrics['listScroll'],metrics
            # Closing the unsaved card exercises the existing draft guard; the
            # exploratory draft-version bindings must not reach the saved card.
            dialog.get_by_role('button',name='关闭个人卡编辑',exact=True).click()
            saved=next(c for c in self.api(teacher,'/api/private-knowledge')['cards'] if c['id']==manual['id'])
            assert len(saved['knowledge_points'])==1 and saved['revision']==manual['revision']
        teacher.set_viewport_size({'width':1440,'height':900})
        self.record('bounded_tree_mobile_tabs_pagination_twenty_limit_cross_version_and_dirty_guard')
        self.goto(admin,self.base+'/knowledge-base')
        admin.get_by_role('button',name='新增知识卡',exact=True).click()
        dialog=admin.locator('dialog[open]')
        dialog.get_by_label('标题',exact=True).fill('C6共享树形绑定资料')
        dialog.get_by_label('知识卡类型',exact=True).select_option('custom')
        dialog.get_by_label('自定义内容',exact=True).fill('共享编辑器同样支持目录树绑定。')
        self.bind_primary_point(dialog)
        dialog.get_by_role('button',name='保存草稿',exact=True).click()
        expect(admin.locator('.kb-card').filter(has_text='C6共享树形绑定资料')).to_be_visible()
        saved=next(c for c in self.api(admin,'/api/knowledge-base')['cards'] if c['title']=='C6共享树形绑定资料')
        assert saved['knowledge_points'][0]['id']==self.f['points'][0]['id']
        self.record('shared_card_editor_uses_same_tree_binding_and_persists_real_ID')
        return manual

    def knowledge_collection_journey(self,browser,teacher,admin):
        def grant(page,key,perms):
            user=self.api(page,'/api/auth/me')
            self.api(admin,'/api/users/'+user['id'],'PUT',{'display_name':user['display_name'],'role':user.get('role',''),'roles':user.get('roles',[]),'permissions':list(dict.fromkeys(user['permissions']+perms)),'enabled':True})
        grant(teacher,'teacher',['kb:view','kb:use','kb:submit'])
        teacher.reload();teacher.wait_for_load_state('networkidle')
        self.edit_source(teacher,'C6学习源题','新增用于知识卡整理的修订。',3)
        self.open_learning(teacher,True)
        teacher.get_by_label('选择候选 checklist',exact=True).check()
        self.rule_input(teacher,'checklist').fill('C6审核共享采用标记：保留病例已有证据，解析按选项分段。')
        teacher.get_by_role('button',name='预览所选经验',exact=True).click()
        teacher.get_by_label('候选保存位置',exact=True).select_option('cards')
        teacher.get_by_label('将候选作为表达要求保存',exact=True).check()
        incomplete_pattern='**/api/learning-analyses/*/knowledge-cards'
        teacher.route(incomplete_pattern,lambda route:route.fulfill(status=200,content_type='application/json',body=json.dumps({'cards':[{'id':'incomplete-response','content':{'custom_text':'旧服务响应'}}]})))
        teacher.get_by_role('button',name='保存草稿',exact=True).click()
        expect(teacher.locator('.learning-error')).to_contain_text('未保留表达要求')
        expect(teacher.get_by_label('选择候选 checklist',exact=True)).to_be_checked()
        expect(teacher.get_by_label('将候选作为表达要求保存',exact=True)).to_be_checked()
        teacher.unroute(incomplete_pattern)
        self.record('incomplete_expression_conversion_response_retains_candidate_and_preview')
        teacher.get_by_role('button',name='保存草稿',exact=True).click()
        expect(teacher.locator('.learning-notice')).to_contain_text('个人知识卡')
        self.goto(teacher,self.base+'/my-experiences')
        teacher.get_by_role('button',name='知识卡',exact=True).click()
        card=teacher.locator('.private-knowledge-card').filter(has_text='表达经验checklist')
        expect(card).to_be_visible()
        card.get_by_role('button',name='编辑',exact=True).click()
        dialog=teacher.locator('dialog[open]')
        dialog.get_by_label('标题',exact=True).fill('C6候选共享知识卡')
        dialog.get_by_label('维护备注',exact=True).fill('C6私人备注不得公开')
        expect(dialog.get_by_label('此卡包含表达要求',exact=True)).to_be_checked()
        expect(dialog.get_by_label('表达要求',exact=True)).to_have_value('C6审核共享采用标记：保留病例已有证据，解析按选项分段。')
        expression_note='C6教学提示：先核对本例证据。'
        dialog.get_by_label('解析附言',exact=True).fill(expression_note)
        dialog.get_by_role('button',name='保存并启用',exact=True).click()
        card=teacher.locator('.private-knowledge-card').filter(has_text='C6候选共享知识卡')
        card.get_by_label('用于出题 C6候选共享知识卡',exact=True).check()
        self.quiet_knowledge_checks(teacher,'.private-knowledge-card','private_cards_stable_across_two_actual_poll_cycles',private_cards=True)
        data=self.api(teacher,'/api/private-knowledge')
        private=next(c for c in data['cards'] if c['title']=='C6候选共享知识卡')
        assert private['selected'] and private['origin']['candidate_id']=='checklist'
        assert private['content']['expression']['explanation_note']==expression_note
        self.record('AI_candidate_explicit_expression_conversion_editor_and_private_roundtrip')
        self.take(teacher,'private-knowledge-card-selected')
        def generate_knowledge(page,label):
            self.goto(page,self.base+'/generate')
            page.locator('label.kp-item').filter(has_text='测试胃溃疡').locator('input').check()
            page.get_by_label('使用知识库工作流',exact=True).check()
            with page.expect_response(lambda r:r.request.method=='POST' and r.url.endswith('/api/questions/generate')) as response:
                page.get_by_role('button',name='生成试题',exact=True).click()
            run=response.value.json()['run']['id']
            result=self.poll(lambda:self.api(page,'/api/generation-runs/'+run),lambda d:len(d.get('questions',[]))==1,label+' did not pass mandatory check')
            question=result['questions'][0]
            context=self.api(page,'/api/questions/'+question['id']+'/generation-context')
            return question,context
        manual=self.manual_card_tree_binding(browser,teacher,admin)
        own,own_context=generate_knowledge(teacher,'private knowledge')
        assert any(c['id']==manual['id'] for c in own_context['bundle']['cards'])
        assert 'C6手工卡树形绑定采用标记' in json.dumps(own_context,ensure_ascii=False)
        self.record('manual_private_card_adopted_by_real_generation_without_extra_model_calls')
        assert any(c['id']==private['id'] for c in own_context['bundle']['cards'])
        assert own_context['workflow_version']=='knowledge-v5' and own['explanation'].count(expression_note)==1
        self.record('private_explicit_expression_frozen_and_note_rendered_before_first_check')
        self.record('ai_candidate_to_private_card_and_real_generation_snapshot')
        self.goto(teacher,self.base+'/my-experiences')
        teacher.get_by_role('button',name='知识卡',exact=True).click()
        teacher.get_by_role('button',name='新增知识卡',exact=True).click()
        dialog=teacher.locator('dialog[open]')
        dialog.get_by_label('标题',exact=True).fill('C6待驳回知识卡')
        dialog.get_by_label('知识卡类型',exact=True).select_option('custom')
        dialog.get_by_label('自定义内容',exact=True).fill('此样例供测试驳回，不应进入共享知识库。')
        self.bind_primary_point(dialog)
        dialog.get_by_role('button',name='保存草稿',exact=True).click()
        teacher.get_by_label('选择共享 C6候选共享知识卡',exact=True).check()
        teacher.get_by_label('选择共享 C6待驳回知识卡',exact=True).check()
        teacher.get_by_role('button',name='提交所选共享审核（2）',exact=True).click()
        dialog=teacher.locator('dialog[open]')
        expect(dialog).not_to_contain_text('C6私人备注不得公开')
        expect(dialog).to_contain_text(expression_note)
        dialog.get_by_role('button',name='确认提交（2 张）',exact=True).click()
        expect(teacher.locator('.collection-success')).to_contain_text('已核对 2 张申请')
        reviewer=self.login(browser,'knowledge_reviewer')
        self.goto(reviewer,self.base+'/knowledge-base')
        reviewer.get_by_role('button',name='知识卡审核',exact=True).click()
        reject=reviewer.locator('.knowledge-submission').filter(has_text='C6待驳回知识卡')
        reject.get_by_role('button',name='驳回',exact=True).click()
        dialog=reviewer.locator('dialog[open]')
        expect(dialog.get_by_role('button',name='确认驳回',exact=True)).to_be_disabled()
        dialog.get_by_label('知识卡审核意见',exact=True).fill('来源不足，不公开此条。')
        dialog.get_by_role('button',name='确认驳回',exact=True).click()
        approve=reviewer.locator('.knowledge-submission').filter(has_text='C6候选共享知识卡')
        approve.get_by_role('button',name='通过',exact=True).click()
        reviewer.locator('dialog[open]').get_by_role('button',name='确认通过',exact=True).click()
        expect(reviewer.locator('.collection-empty')).to_be_visible()
        self.record('delegated_one_step_review_batch_submission_approve_or_reject_only')
        self.goto(teacher,self.base+'/my-experiences')
        teacher.get_by_role('button',name='我的申请',exact=True).click()
        expect(teacher.locator('.knowledge-submission').filter(has_text='C6候选共享知识卡')).to_contain_text('已通过')
        expect(teacher.locator('.knowledge-submission').filter(has_text='C6待驳回知识卡')).to_contain_text('已驳回')
        self.take(teacher,'knowledge-submission-two-results')
        peer=self.login(browser,'other')
        grant(peer,'other',['kb:view','kb:use','batch:run'])
        peer.reload();peer.wait_for_load_state('networkidle')
        self.goto(peer,self.base+'/knowledge-base')
        public=peer.locator('.kb-card').filter(has_text='C6候选共享知识卡')
        expect(public).to_be_visible()
        expect(public).to_contain_text('表达要求')
        expect(peer.locator('.kb-card').filter(has_text='C6待驳回知识卡')).to_have_count(0)
        expect(public.get_by_label('用于出题 C6候选共享知识卡',exact=True)).not_to_be_checked()
        shared=next(c for c in self.api(peer,'/api/knowledge-base')['cards'] if c['title']=='C6候选共享知识卡')
        def preview():return self.api(peer,'/api/knowledge-base/prepare','POST',{'knowledge_points':[self.f['points'][0]]})
        assert all(c['id']!=shared['id'] for b in preview()['bundles'] for c in b['cards'])
        public.get_by_label('用于出题 C6候选共享知识卡',exact=True).check()
        assert any(c['id']==shared['id'] for b in preview()['bundles'] for c in b['cards'])
        question,context=generate_knowledge(peer,'reviewed shared knowledge')
        assert any(c['id']==shared['id'] and c['revision']==1 for c in context['bundle']['cards'])
        assert context['workflow_version']=='knowledge-v5' and question['explanation'].count(expression_note)==1
        assert 'C6私人备注不得公开' not in json.dumps(context,ensure_ascii=False)
        self.record('shared_expression_one_step_approval_explicit_peer_choice_and_single_note')
        self.record('other_teacher_explicit_shared_choice_and_real_generation_prompt_adoption')
        self.goto(peer,self.base+'/generate/batch')
        peer.locator('label.kp-item').filter(has_text='测试胃溃疡').locator('input').check()
        peer.locator('#batch-question-count').fill('1')
        peer.get_by_label('使用知识库工作流',exact=True).check()
        with peer.expect_response(lambda r:r.request.method=='POST' and r.url.endswith('/api/batch/submit')) as response:
            peer.get_by_role('button',name=re.compile('提交随机生成任务')).click()
        job=response.value.json()['job_id']
        data=self.poll(lambda:self.api(peer,'/api/batch/status/'+job),lambda d:len((d.get('pipeline_result') or {}).get('question_ids') or [])==1,'reviewed batch card not adopted')
        self.poll(lambda:self.api(peer,'/api/ai-check/progress','POST',{'question_ids':data['pipeline_result']['question_ids']}),lambda d:d.get('counts',{}).get('passed')==1,'batch mandatory first check did not pass')
        batch_context=self.api(peer,'/api/questions/'+data['pipeline_result']['question_ids'][0]+'/generation-context')
        assert any(c['id']==shared['id'] for c in batch_context['bundle']['cards'])
        assert batch_context['workflow_version']=='knowledge-v5' and batch_context['basis']['case_plan']['explanation_notes']==[expression_note]
        self.record('reviewed_shared_card_real_batch_frozen_adoption')
        self.goto(admin,self.base+'/knowledge-base')
        row=admin.locator('.kb-card').filter(has_text='C6候选共享知识卡')
        row.get_by_role('button',name='归档',exact=True).click()
        admin.locator('dialog[open]').get_by_role('button',name='确认',exact=True).click()
        expect(row.locator('.kb-state')).to_have_text('已归档')
        assert all(c['id']!=shared['id'] for b in preview()['bundles'] for c in b['cards'])
        frozen=self.api(peer,'/api/questions/'+question['id']+'/generation-context')
        assert any(c['id']==shared['id'] and c['revision']==1 for c in frozen['bundle']['cards'])
        self.record('shared_archive_stops_new_adoption_preserves_existing_frozen_context')
        for width in [1440,390]:
            teacher.set_viewport_size({'width':width,'height':844})
            self.goto(teacher,self.base+'/my-experiences')
            teacher.get_by_role('button',name='知识卡',exact=True).click()
            teacher.locator('.private-knowledge-card').filter(has_text='C6候选共享知识卡').get_by_role('button',name='编辑',exact=True).click()
            expect(teacher.locator('dialog[open]')).to_be_visible()
            expect(teacher.locator('dialog[open]').get_by_label('解析附言',exact=True)).to_have_value(expression_note)
            metrics=teacher.evaluate('({width:innerWidth,scroll:document.documentElement.scrollWidth,dialog:document.querySelector("dialog[open]").getBoundingClientRect().width})')
            assert metrics['scroll']<=width+1 and metrics['dialog']<=width-20,metrics
            self.take(teacher,'private-card-editor-'+str(width))
            teacher.get_by_role('button',name='关闭个人卡编辑',exact=True).click()
        self.record('structured_private_card_editor_desktop_mobile_and_dirty_guard')

    def knowledge_summary_journey(self,teacher,admin):
        teacher.set_viewport_size({'width':1440,'height':900})
        with teacher.expect_response(lambda r:'/api/private-knowledge?' in r.url and 'view=summary' in r.url) as response:
            self.goto(teacher,self.base+'/my-experiences')
            teacher.get_by_role('button',name='知识卡',exact=True).click()
        private_list=response.value.json()
        assert private_list['view']=='summary' and 'cards' not in private_list
        assert all(c['is_summary'] and not any(k in c for k in ['content','note','origin']) for c in private_list['items'])
        private=next(c for c in private_list['items'] if c['title']=='C6候选共享知识卡')
        before=self.api(teacher,'/api/private-knowledge/'+private['id'])
        row=teacher.locator('.private-knowledge-card').filter(has_text=private['title'])
        with teacher.expect_response(lambda r:r.request.method=='GET' and r.url.endswith('/api/private-knowledge/'+private['id'])):
            row.get_by_role('button',name='编辑',exact=True).click()
        dialog=teacher.locator('dialog[open]')
        expect(dialog.get_by_label('维护备注',exact=True)).to_have_value('C6私人备注不得公开')
        expect(dialog.get_by_label('自定义内容',exact=True)).to_have_value(before['content']['custom_text'])
        dialog.get_by_role('button',name='关闭个人卡编辑',exact=True).click()
        for action,next_action in [('停用','启用'),('启用','停用')]:
            row.get_by_role('button',name=action,exact=True).click()
            expect(row.get_by_role('button',name=next_action,exact=True)).to_be_visible()
        after=self.api(teacher,'/api/private-knowledge/'+private['id'])
        assert after['revision']==before['revision']+2
        for field in ['content','source','note','origin','knowledge_points']:
            assert after.get(field)==before.get(field),field
        self.take(teacher,'private-summary-list-and-preserved-full-body')
        self.record('private_summary_edit_and_state_changes_preserve_full_body_note_and_AI_evidence')
        with admin.expect_response(lambda r:'/api/knowledge-base?' in r.url and 'view=summary' in r.url) as response:
            self.goto(admin,self.base+'/knowledge-base')
        shared_list=response.value.json()
        assert shared_list['view']=='summary' and 'cards' not in shared_list
        assert all(c['is_summary'] and not any(k in c for k in ['content','note','origin','selected']) for c in shared_list['items'])
        self.record('private_and_shared_real_list_responses_are_explicit_metadata_projections')
        shared=next(c for c in shared_list['items'] if c['title']=='C6共享树形绑定资料')
        before=self.api(admin,'/api/knowledge-base/'+shared['id'])
        with admin.expect_response(lambda r:r.request.method=='GET' and r.url.endswith('/api/knowledge-base/'+shared['id'])):
            admin.locator('.kb-card').filter(has_text=shared['title']).get_by_role('button',name='编辑',exact=True).click()
        dialog=admin.locator('dialog[open]')
        expect(dialog.get_by_label('自定义内容',exact=True)).to_have_value(before['content']['custom_text'])
        dialog.get_by_label('维护备注',exact=True).fill('摘要编辑保留标记')
        dialog.get_by_role('button',name='保存草稿',exact=True).click()
        expect(dialog).to_have_count(0)
        after=self.api(admin,'/api/knowledge-base/'+shared['id'])
        assert after['revision']==before['revision']+1 and after['content']==before['content'] and after['source']==before['source']
        assert after['note']=='摘要编辑保留标记'
        self.record('shared_summary_editor_reads_full_card_and_preserves_original_content_on_save')
        admin.locator('.kb-card').filter(has_text=shared['title']).get_by_role('button',name='查看与历史',exact=True).click()
        history=admin.locator('dialog[open]')
        expect(history.get_by_role('heading',level=2)).to_have_text(shared['title'])
        dates=history.locator('option').all_inner_texts()
        assert dates and all(re.search(r'\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}',date) for date in dates),dates
        history.get_by_role('button',name='关闭详情',exact=True).click()
        self.record('shared_card_history_uses_stable_date_format_independent_of_browser_locale')

    def authoring_guide_journey(self,teacher):
        teacher.set_viewport_size({'width':1440,'height':900})
        self.goto(teacher,self.base+'/my')
        task=teacher.evaluate('JSON.parse(sessionStorage.getItem("aigo_journey:"+JSON.parse(localStorage.aigo_user).id+":"+JSON.parse(localStorage.aigo_user).role))')
        result=self.api(teacher,'/api/generation-runs/'+task['id'])
        qid=result['questions'][0]['id']
        teacher.get_by_role('button',name='去题目整理',exact=True).click()
        expect(teacher).to_have_url(self.base+'/new-questions')
        def arrangement():
            data=self.api(teacher,'/api/questions/my-new?page=1&page_size=20')
            expect(teacher.locator('.new-question-card')).to_have_count(len(data['questions']))
            index=next(i for i,q in enumerate(data['questions']) if q['id']==qid)
            teacher.locator('.new-question-card').nth(index).click()
        arrangement()
        assert self.api(teacher,'/api/questions/my-new?page=1&page_size=20')['total']>1
        teacher.locator('.confirm-current-button').click()
        expect(teacher.locator('.authoring-confirmed')).to_contain_text('已确认 1 题')
        teacher.get_by_role('link',name='去个人题库导出 →',exact=True).click()
        expect(teacher).to_have_url(self.base+'/bank?scope=personal')
        def bank_row():
            data=self.api(teacher,'/api/questions?page=1&page_size=100&scope=personal&library=active')
            expect(teacher.locator('.question-item')).to_have_count(len(data['questions']))
            return teacher.locator('.question-item').nth(next(i for i,q in enumerate(data['questions']) if q['id']==qid))
        row=bank_row()
        row.locator('.q-checkbox').check()
        self.ui_download(teacher,'Excel')
        with zipfile.ZipFile(self.root/'export.xlsx') as z:
            texts='\n'.join(z.read(n).decode('utf-8') for n in z.namelist() if n.endswith('.xml'))
        assert texts.count('C6教学提示：先核对本例证据。')==1
        self.record('explicit_expression_note_survives_confirmation_and_physical_excel_export')
        self.take(teacher,'authoring-guide-all-questions-desktop')
        self.record('teacher_guide_opens_all_arrangement_and_personal_bank_without_hidden_task_filter')
        row.locator('.q-content').click()
        teacher.get_by_role('button',name='修订题目',exact=True).click()
        stem=teacher.get_by_label('修订题干',exact=True)
        stem.fill('临床病例记录：'+stem.input_value()+'？')
        teacher.get_by_label('修订解析',exact=True).fill('教师说明：A符合本例胃溃疡，其余诊断请结合病史排除。')
        teacher.get_by_role('button',name='保存修订',exact=True).click()
        expect(teacher.locator('.authoring-confirmed')).to_contain_text('修订已保存')
        teacher.get_by_role('link',name='回题目整理继续确认 →',exact=True).click()
        expect(teacher).to_have_url(self.base+'/new-questions')
        arrangement()
        teacher.locator('.confirm-current-button').click()
        teacher.get_by_role('link',name='去个人题库导出 →',exact=True).click()
        row=bank_row()
        self.record('saved_revision_has_persistent_return_to_arrangement_without_new_first_check')
        row.locator('.q-checkbox').check()
        self.ui_download(teacher,'Word')
        self.record('teacher_independent_wording_survives_reconfirmation_and_document_export')
        teacher.reload();teacher.wait_for_load_state('networkidle')
        expect(teacher).to_have_url(self.base+'/bank?scope=personal')
        bank_row()
        teacher.set_viewport_size({'width':390,'height':844})
        expect(teacher.locator('.authoring-guide .guide-steps li:visible')).to_have_count(1)
        teacher.get_by_role('button',name='展开四步',exact=True).click()
        expect(teacher.locator('.authoring-guide .guide-steps li:visible')).to_have_count(4)
        metrics=teacher.evaluate('({width:innerWidth,scroll:document.documentElement.scrollWidth})')
        assert metrics['scroll']<=metrics['width']+1,metrics
        self.take(teacher,'authoring-guide-mobile-expanded')
        teacher.get_by_role('button',name='收起步骤',exact=True).click()
        expect(teacher.locator('.authoring-guide .guide-steps li:visible')).to_have_count(1)
        self.record('guide_restores_progress_without_filtering_lists_and_mobile_steps_fit')
        teacher.set_viewport_size({'width':1440,'height':900})

    def optional_knowledge_and_reset(self,teacher):
        no_card=self.f['points'][2]
        previews=[]
        teacher.on('request',lambda r:previews.append(r.url) if '/api/knowledge-base/prepare' in r.url else None)
        self.goto(teacher,self.base+'/generate')
        teacher.locator('label.kp-item').filter(has_text=no_card['topic']).locator('input').check()
        teacher.get_by_label('使用知识库工作流',exact=True).check()
        assert teacher.get_by_role('button',name='查看将用的资料',exact=True).count()==0
        with teacher.expect_response(lambda r:r.request.method=='POST' and r.url.endswith('/api/questions/generate')) as response:
            teacher.get_by_role('button',name='生成试题',exact=True).click()
        run=response.value.json()['run']['id']
        result=self.poll(lambda:self.api(teacher,'/api/generation-runs/'+run),lambda d:len(d.get('questions',[]))==1,'no-card single did not pass')
        assert not result['questions'][0].get('workflow_version')
        self.record('knowledge_checkbox_with_empty_lookup_uses_ordinary_generation_and_mandatory_check_without_preview')
        self.goto(teacher,self.base+'/generate/batch')
        for checkbox in teacher.locator('label.kp-item input').all():
            if checkbox.is_checked():checkbox.uncheck()
        for point in [self.f['points'][0],no_card]:
            teacher.locator('label.kp-item').filter(has_text=point['topic']).locator('input').check()
        teacher.get_by_role('radio',name=re.compile('每点 K 题')).check()
        teacher.get_by_label('使用知识库工作流',exact=True).check()
        teacher.get_by_label('跳过已有题目的大纲要点',exact=True).uncheck()
        with teacher.expect_response(lambda r:r.request.method=='POST' and r.url.endswith('/api/batch/submit')) as response:
            teacher.get_by_role('button',name=re.compile('提交生成任务')).click()
        job_id=response.value.json()['job_id']
        job=self.poll(lambda:self.api(teacher,'/api/batch/status/'+job_id),lambda j:j['status']=='completed','mixed batch did not finish')
        assert job['completed']==2 and job['failed']==0
        expect(teacher.get_by_role('link',name='去题目整理',exact=True)).to_be_visible()
        teacher.get_by_role('link',name='去题目整理',exact=True).click()
        expect(teacher).to_have_url(self.base+'/new-questions')
        self.record('mixed_card_and_no_card_batch_completes_both_points_and_opens_all_questions')
        self.goto(teacher,self.base+'/generate/batch?job_id='+job_id)
        expect(teacher.get_by_role('button',name='清空本次记录',exact=True)).to_be_visible()
        teacher.get_by_role('button',name='清空本次记录',exact=True).click()
        expect(teacher.locator('.authoring-guide .guide-steps li.done')).to_have_count(0)
        expect(teacher.locator('.authoring-guide .guide-steps li').first).to_have_class(re.compile('here'))
        assert 'job_id=' not in teacher.url
        teacher.reload();teacher.wait_for_load_state('networkidle')
        expect(teacher.locator('.authoring-guide .guide-steps li.done')).to_have_count(0)
        assert teacher.locator('.job-status-panel').count()==0
        assert not previews
        self.take(teacher,'empty-workflow-reset-desktop')
        self.record('clearing_completed_batch_resets_guide_and_stays_reset_after_reload')

    def run(self):
        failure = None
        with sync_playwright() as pw:
            browser = pw.chromium.launch(channel='chrome', headless=True)
            try:
                if os.environ.get('AIGO_C6_BROWSER_FOCUS')=='binding':
                    teacher=self.login(browser,'teacher');admin=self.login(browser,'admin')
                    user=self.api(teacher,'/api/auth/me')
                    user=user.get('user',user)
                    self.api(admin,'/api/users/'+user['id'],'PUT',{'display_name':user['display_name'],'role':user.get('role',''),'roles':user.get('roles',[]),'permissions':list(dict.fromkeys(user['permissions']+['kb:view','kb:use','kb:submit'])),'enabled':True})
                    teacher.reload();teacher.wait_for_load_state('networkidle')
                    self.manual_card_tree_binding(browser,teacher,admin)
                else:
                    teacher, generated = self.core(browser)
                    self.race_journeys(teacher)
                    self.random_batch(teacher)
                    admin = self.sharing_and_review(browser, teacher) if self.review_ui else self.review_isolation(browser,teacher)
                    self.admin_and_views(browser, admin, teacher)
                    self.manual_and_login_defaults(browser)
                    self.manual_knowledge_generation(browser)
                    self.help_hints_and_frontend_routes(browser)
                    self.knowledge_collection_journey(browser,teacher,admin)
                    self.knowledge_summary_journey(teacher,admin)
                    self.authoring_guide_journey(teacher)
                    self.optional_knowledge_and_reset(teacher)
                    for key in ['other', 'zero', 'legacy', 'dual']:
                        page = self.login(browser, key)
                        assert 'C6本窗口尚未保存' not in page.inner_text('body')
                    self.record('other_zero_legacy_and_dual_identity_logins')
                    assert not self.page_errors, self.page_errors
                    assert not self.http_errors, self.http_errors
                    assert not self.console_errors, self.console_errors
            except Exception as exc:
                failure = repr(exc)
                for i, ctx in enumerate(self.contexts):
                    for j, page in enumerate(ctx.pages):
                        try:
                            self.take(page, f'failure-{i}-{j}')
                        except Exception:
                            pass
                traceback.print_exc()
            finally:
                for ctx in self.contexts:
                    for page in ctx.pages:
                        try:
                            page.evaluate('localStorage.clear();sessionStorage.clear()')
                        except Exception:
                            pass
                    try:
                        ctx.clear_cookies()
                        ctx.close()
                    except Exception:
                        pass
                browser.close()
                result = {'protocol': self.f['protocol'], 'steps': self.steps, 'viewports': self.viewports, 'page_errors': self.page_errors, 'console_errors': self.console_errors, 'unexpected_http': self.http_errors, 'failure': failure, 'provider_http': 0, 'notes': ['LLM fixture proves engineering behavior, not medical/model quality.', '200% equivalent uses a 720x450 CSS viewport at DPR2 (1440x900 physical screenshot); native browser zoom is not claimed.']}
                (self.root / 'result.json').write_text(json.dumps(result, ensure_ascii=False, indent=2))
        if failure is not None:
            raise SystemExit(1)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--fixture-json', required=True)
    parser.add_argument('--artifact-dir', required=True)
    args = parser.parse_args()
    fixture, artifacts = validate_inputs(args.fixture_json, args.artifact_dir)
    Acceptance(fixture, artifacts).run()


if __name__ == '__main__':
    main()
