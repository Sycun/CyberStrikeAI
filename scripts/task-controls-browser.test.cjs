const { test } = require('node:test');
const assert = require('node:assert/strict');
const { fixture, goal } = require('./helpers/task-ui-fixture.cjs');

// Each test uses a disposable HTTP fixture, real template/script references and
// the real router/auth/modal code. It never calls a running CSAI deployment.
test('goal task actions: retry, budget, resume, pause, answer, report, cancel and delete', async t => {
    const { page, state, select, refresh } = await fixture(t);
    await select('a');
    await page.locator('[data-lt-action=retry]').click();
    await page.locator('.lt-step .lt-status-pending').waitFor();
    assert.equal(state.tasks.get('a').steps[0].status, 'pending');
    await page.locator('[data-lt-action=budget]').click();
    await page.locator('#lt-modal [name=max_rounds]').fill('60'); await page.locator('#lt-modal [name=minutes]').fill('90');
    await page.locator('#lt-modal [type=submit]').click(); await page.waitForFunction(() => !document.getElementById('lt-modal'));
    assert.equal(state.tasks.get('a').remaining_seconds, 5400);
    await page.locator('[data-lt-action=start]').click(); await page.locator('#lt-state .lt-status-running').waitFor();
    await page.locator('[data-lt-action=pause]').click(); await page.locator('#lt-state .lt-status-paused').waitFor();
    state.tasks.get('a').status = 'waiting_input'; state.tasks.get('a').question = 'Which fixture?'; await refresh();
    await page.locator('#lt-guidance-input').fill('Use local fixture A'); await page.locator('#lt-guidance-send').click(); await page.locator('#lt-state .lt-status-running').waitFor();
    assert.deepEqual(state.tasks.get('a').guidance, ['Use local fixture A']);
    const downloaded = page.waitForEvent('download'); await page.locator('[data-lt-action=report]').click(); assert.equal((await downloaded).suggestedFilename(), 'task-a.md');
    state.confirm = false; await page.locator('[data-lt-action=cancel]').click(); assert.equal(state.tasks.get('a').status, 'running');
    state.confirm = true; await page.locator('[data-lt-action=cancel]').click(); await page.locator('#lt-state .lt-status-cancelled').waitFor();
    await page.locator('[data-lt-action=delete]').click(); await page.locator('#lt-empty-detail').waitFor(); assert.equal(state.tasks.has('a'), false);
    assert.deepEqual(state.pageErrors, []);
});

test('search responds while typing and expanded step instructions survive refresh', async t => {
    const { page, state, select, refresh } = await fixture(t);
    await select('a'); await page.locator('.lt-step details summary').click(); await refresh();
    assert.equal(await page.locator('.lt-step details').evaluate(e => e.open), true);
    await page.locator('#lt-search').fill('目标 b');
    await page.waitForFunction(() => document.querySelectorAll('.lt-card').length === 1 && document.querySelector('.lt-card').dataset.id === 'b');
    assert.ok(state.requests.some(r => r.query.q === '目标 b'));
});

test('cancelled tasks never offer a retry that the API rejects', async t => {
    const { page, state, select, refresh } = await fixture(t);
    await select('a'); state.tasks.get('a').status = 'cancelled'; await refresh();
    assert.equal(await page.locator('[data-lt-action=retry]').count(), 0);
});

test('a late creation-options response does not open a modal after leaving the page', async t => {
    const { page, state } = await fixture(t); let release;
    const gate = new Promise(resolve => { release = resolve; });
    state.hook = async req => { if (req.path.endsWith('/options')) await gate; };
    await page.locator('#task-new-goal').click(); await page.waitForTimeout(50);
    await page.evaluate(() => switchPage('dashboard')); release(); await page.waitForTimeout(150);
    assert.equal(await page.locator('#lt-modal').count(), 0);
});

test('deleting A while selecting B preserves B and report filenames stay attached to their task', async t => {
    const { page, state, select } = await fixture(t); let release;
    await select('a'); const gate = new Promise(resolve => { release = resolve; });
    state.hook = async req => { if (req.path === '/api/long-tasks/a' && req.method === 'DELETE') await gate; };
    await page.locator('[data-lt-action=delete]').click(); await select('b'); release(); await page.waitForTimeout(150);
    assert.equal(await page.locator('#lt-detail').isVisible(), true);
    assert.equal(await page.locator('#lt-name').textContent(), '目标 b');
    state.tasks.set('a', goal('a')); await page.evaluate(() => refreshTaskWorkspace()); await select('a');
    const reportGate = new Promise(resolve => { release = resolve; }); state.hook = async req => { if (req.path.endsWith('/a/report')) await reportGate; };
    const downloaded = page.waitForEvent('download'); await page.locator('[data-lt-action=report]').click(); await select('b'); release();
    assert.equal((await downloaded).suggestedFilename(), 'task-a.md');
});

test('goal paging, filters, refresh toggle, modal close and execution links work', async t => {
    const { page, state, select, refresh } = await fixture(t);
    for (let i = 0; i < 34; i++) state.tasks.set('extra-' + i, goal('extra-' + i));
    await refresh(); await page.locator('#lt-next').click(); await page.waitForFunction(() => document.getElementById('lt-page').textContent === '2');
    await page.locator('#lt-prev').click(); await page.waitForFunction(() => document.getElementById('lt-page').textContent === '1');
    await page.locator('#lt-filter').selectOption('paused'); await page.waitForFunction(() => document.querySelectorAll('.lt-card').length === 1);
    await page.locator('#lt-filter').selectOption(''); await page.waitForFunction(() => document.querySelectorAll('.lt-card').length === 30);
    await page.locator('#task-new-goal').click(); await page.locator('#lt-modal').waitFor(); await page.locator('#lt-modal [data-close]').last().click(); await page.waitForFunction(() => !document.getElementById('lt-modal'));
    await page.locator('#task-new-goal').click(); await page.locator('#lt-modal').waitFor(); await page.keyboard.press('Escape'); await page.waitForFunction(() => !document.getElementById('lt-modal'));
    assert.equal(state.requests.filter(r => r.method === 'POST').length, 0);
    await page.locator('#tasks-auto-refresh').check(); const count = state.requests.filter(r => r.path === '/api/tasks').length;
    await page.waitForTimeout(4200); assert.ok(state.requests.filter(r => r.path === '/api/tasks').length > count);
    await page.locator('#tasks-auto-refresh').uncheck(); await refresh(); const stopped = state.requests.length;
    await page.waitForTimeout(4200); assert.equal(state.requests.length, stopped);
    await select('a'); await page.locator('.lt-step a').click(); await page.waitForFunction(() => window.visitedConversation === 'conversation-a');
    assert.deepEqual(state.pageErrors, []);
});

test('slow submissions are single-flight and saved answers are not resubmitted on resume failure', async t => {
    const { page, state, select, refresh } = await fixture(t); await select('a');
    state.hook = async req => { if (req.path.endsWith('/control/start')) await new Promise(resolve => setTimeout(resolve, 300)); };
    await page.locator('[data-lt-action=start]').dblclick(); await page.locator('#lt-state .lt-status-running').waitFor();
    assert.equal(state.requests.filter(r => r.path.endsWith('/control/start')).length, 1);
    state.hook = null; state.tasks.get('a').status = 'waiting_input'; state.tasks.get('a').question = 'Choose a fixture'; state.failResume = 'All execution slots are busy'; await refresh();
    await page.locator('#lt-guidance-input').fill('Fixture A'); await page.locator('#lt-guidance-send').click();
    await page.waitForFunction(() => document.getElementById('lt-error').textContent.includes('All execution slots'));
    assert.equal(await page.locator('#lt-guidance-input').inputValue(), '');
    await refresh(); assert.match(await page.locator('#lt-error').textContent(), /答复已保存/);
    assert.deepEqual(state.tasks.get('a').guidance, ['Fixture A']);
});

test('creation failures keep form input, and budget/execution permissions match available actions', async t => {
    const { page, state, select, refresh } = await fixture(t);
    state.hook = req => req.path === '/api/long-tasks' && req.method === 'POST' ? { status: 503, body: { error: 'fixture unavailable' } } : null;
    await page.locator('#task-new-goal').click(); await page.locator('#lt-modal [name=name]').fill('Keep this draft');
    await page.locator('#lt-modal [name=objective]').fill('Local report'); await page.locator('#lt-modal [name=scope]').fill('Local only'); await page.locator('#lt-modal [name=criteria]').fill('Evidence');
    await page.locator('#lt-modal [type=submit]').click(); await page.waitForFunction(() => document.querySelector('.lt-form-error').textContent.includes('fixture unavailable'));
    assert.equal(await page.locator('#lt-modal [name=name]').inputValue(), 'Keep this draft');
    assert.equal(await page.locator('#lt-modal [type=submit]').isEnabled(), true);
    await page.locator('#lt-modal [data-close]').last().click(); state.hook = null; await select('a');
    state.tasks.get('a').status = 'budget_exhausted'; state.tasks.get('a').remaining_seconds = 0; await refresh();
    assert.equal(await page.locator('[data-lt-action=start]').isDisabled(), true); assert.equal(await page.locator('[data-lt-action=budget]').isEnabled(), true);
    state.tasks.get('a').rounds = 200; state.tasks.get('a').max_rounds = 200; await refresh(); assert.equal(await page.locator('[data-lt-action=budget]').isDisabled(), true);
    await page.evaluate(() => { saveAuth('fixture-token',new Date(Date.now()+3600000),{permissions:['tasks:read','tasks:write'],scope:'own'}); applyRBACToUI(); }); await refresh();
    assert.equal(await page.locator('[data-lt-action=start]').count(), 0);
});

test('batch queue controls: create, close, metadata, add/edit/delete steps, single run, start/pause/resume/rerun/delete', async t => {
    const { page, state } = await fixture(t);
    await page.evaluate(() => showBatchImportModal());
    await page.locator('#batch-import-modal').waitFor({ state: 'visible' });
    await page.locator('#batch-queue-title').fill('New batch'); await page.locator('#batch-tasks-input').fill('Read local A\nRead local B');
    await page.locator('[onclick="createBatchQueue()"]').click();
    await page.locator('[data-bq-detail-for="new-queue"]').waitFor();
    assert.equal(state.queues.get('new-queue').executeNow, false);
    await page.locator('[data-lt-action=select][data-id=a][data-mode=goal]').click();
    await page.locator('.lt-card[data-id=q1][data-mode=list]').click(); await page.locator('[data-bq-detail-for=q1]').waitFor();
    await page.locator('#bq-title-val .bq-inline-editable').click(); await page.locator('#bq-edit-title').fill('Renamed queue'); await page.locator('#bq-edit-title').press('Enter');
    await page.waitForFunction(() => document.getElementById('bq-title-val').textContent === 'Renamed queue'); assert.equal(state.queues.get('q1').title, 'Renamed queue');
    await page.locator('#bq-concurrency-val .bq-inline-editable').click(); await page.locator('#bq-edit-concurrency').fill('2'); await page.locator('#bq-edit-concurrency').press('Enter');
    await page.waitForFunction(() => !document.getElementById('bq-edit-concurrency')); assert.equal(state.queues.get('q1').concurrency, 2);
    await page.locator('#batch-queue-add-task-btn').click(); await page.locator('#add-task-message').fill('Added fixture step'); await page.locator('[onclick="saveAddBatchTask()"]').click();
    await page.locator('.batch-task-item[data-task-id=added]').waitFor();
    await page.locator('.batch-task-item[data-task-id=added] .batch-task-edit-btn').click(); await page.locator('#bq-task-edit-added').fill('Edited fixture step'); await page.locator('#bq-task-edit-added').press('Tab');
    await page.waitForFunction(() => document.querySelector('[data-task-id=added] .batch-task-message').textContent === 'Edited fixture step');
    await page.locator('.batch-task-item[data-task-id=added] .batch-task-delete-btn').click(); await page.locator('.batch-task-item[data-task-id=added]').waitFor({ state: 'detached' });
    await page.locator('.batch-task-item[data-task-id=b1] .batch-task-run-btn').click(); await page.waitForFunction(() => document.querySelector('[data-task-id=b1] .batch-task-status').textContent === '已完成');
    await page.locator('#batch-queue-start-btn').click(); await page.locator('#batch-queue-pause-btn').waitFor({ state: 'visible' }); assert.equal(state.queues.get('q1').status, 'running');
    await page.locator('#batch-queue-pause-btn').click(); await page.locator('#batch-queue-start-btn').waitFor({ state: 'visible' }); assert.equal(state.queues.get('q1').status, 'paused');
    await page.locator('#batch-queue-start-btn').click(); await page.locator('#batch-queue-pause-btn').waitFor({ state: 'visible' });
    state.queues.get('q1').status = 'completed'; await page.evaluate(() => showBatchQueueDetail('q1')); await page.locator('#batch-queue-rerun-btn').click(); await page.locator('#batch-queue-pause-btn').waitFor({ state: 'visible' });
    await page.locator('#batch-queue-pause-btn').click(); await page.locator('#batch-queue-delete-btn').waitFor({ state: 'visible' }); await page.locator('#batch-queue-delete-btn').click();
    await page.locator('#batch-queue-detail-modal').waitFor({ state: 'hidden' }); assert.equal(state.queues.has('q1'), false);
    await page.locator('.lt-card[data-id=new-queue][data-mode=list]').click(); await page.locator('[data-bq-detail-for=new-queue]').waitFor(); await page.locator('#batch-queue-delete-btn').click(); await page.locator('.lt-card[data-id=new-queue][data-mode=list]').waitFor({ state: 'detached' });
    assert.deepEqual(state.pageErrors, []);
});

test('batch create and add-step clicks cannot submit twice on a slow connection', async t => {
    const { page, state } = await fixture(t);
    state.hook = async req => { if (req.method === 'POST' && (req.path === '/api/batch-tasks' || req.path.endsWith('/tasks'))) await new Promise(resolve => setTimeout(resolve, 300)); };
    await page.evaluate(() => showBatchImportModal());
    await page.locator('#batch-tasks-input').fill('One local task'); await page.locator('[onclick="createBatchQueue()"]').dblclick();
    await page.locator('[data-bq-detail-for="new-queue"]').waitFor();
    assert.equal(state.requests.filter(r => r.method === 'POST' && r.path === '/api/batch-tasks').length, 1);
    await page.locator('#batch-queue-add-task-btn').click(); await page.locator('#add-task-message').fill('One new step');
    await page.locator('[onclick="saveAddBatchTask()"]').dblclick(); await page.locator('[data-task-id=added]').waitFor();
    assert.equal(state.requests.filter(r => r.method === 'POST' && r.path.endsWith('/new-queue/tasks')).length, 1);
});

test('late batch detail and creation lookups do not take over a newer selection or page', async t => {
    const { page, state } = await fixture(t);
    state.queues.set('q2', { ...state.queues.get('q1'), id: 'q2', title: 'Other queue' });
    await page.evaluate(() => refreshBatchQueues());
    let release; const gate = new Promise(resolve => { release = resolve; });
    state.hook = async req => { if (req.path === '/api/batch-tasks/q1' && req.method === 'GET') await gate; };
    await page.locator('.lt-card[data-id=q1][data-mode=list]').click(); await page.locator('[data-lt-action=select][data-id=a][data-mode=goal]').click();
    await page.locator('.lt-card[data-id=q2][data-mode=list]').click(); await page.locator('[data-bq-detail-for=q2]').waitFor();
    release(); await page.waitForTimeout(150); assert.equal(await page.locator('[data-bq-detail-for=q2]').count(), 1);
    await page.locator('[data-lt-action=select][data-id=a][data-mode=goal]').click();
    const createGate = new Promise(resolve => { release = resolve; });
    state.hook = async req => { if (req.path === '/api/projects') await createGate; };
    await page.evaluate(() => { void showBatchImportModal(); }); await page.evaluate(() => switchPage('dashboard')); release(); await page.waitForTimeout(150);
    assert.equal(await page.locator('#batch-import-modal').isVisible(), false);
});

test('one task list and one creation form support objectives, checklists and schedules', async t => {
    const { page, state } = await fixture(t);
    assert.equal(await page.locator('[data-task-tab]').count(), 0);
    assert.equal(await page.locator('#task-new-batch').count(), 0);
    assert.equal(await page.locator('#lt-list [data-mode=goal]').count(), 2);
    assert.equal(await page.locator('#lt-list [data-mode=list]').count(), 1);
    await page.locator('#lt-mode-filter').selectOption('list');
    await page.waitForFunction(() => document.querySelectorAll('.lt-card').length === 1);
    await page.locator('.lt-card[data-mode=list][data-id=q1]').click();
    await page.locator('#lt-batch-detail [data-bq-detail-for=q1]').waitFor();
    assert.equal(await page.locator('#batch-queue-detail-modal').evaluate(node => node.classList.contains('modal')), false);
    await page.screenshot({path:'/tmp/csai-unified-task-list-preview.png'});
    assert.equal(await page.locator('#lt-detail').isVisible(),false);
    await page.locator('#task-new-goal').click();
    await page.locator('#lt-modal [name=execution_mode][value=list]').check();
    await page.locator('#lt-modal [name=name]').fill('Unified checklist');
    await page.locator('#lt-modal [name=tasks_text]').fill('Read local A\nRead local B');
    await page.locator('#lt-modal [name=schedule_mode]').selectOption('cron');
    await page.locator('#lt-modal [name=cron_expr]').fill('0 2 * * *');
    assert.equal(await page.locator('#lt-modal [name=objective]').isDisabled(), true);
    assert.match(await page.locator('.lt-dialog-note').textContent(), /自动执行/);
    await page.locator('#lt-modal [type=submit]').click();
    await page.locator('#lt-batch-detail [data-bq-detail-for=new-queue]').waitFor();
    assert.equal(state.queues.get('new-queue').scheduleMode, 'cron');
    assert.equal(state.queues.get('new-queue').cronExpr, '0 2 * * *');
    assert.equal(state.queues.get('new-queue').executeNow, false);
    assert.equal(state.queues.get('new-queue').tasks.length, 2);
    assert.equal(await page.locator('#lt-list [data-mode=list]').count(), 2);
    await page.locator('#task-new-goal').click();
    await page.locator('#lt-modal [name=name]').fill('Unified objective');
    await page.locator('#lt-modal [name=objective]').fill('Summarize the local report');
    await page.locator('#lt-modal [name=scope]').fill('Supplied local evidence only');
    await page.locator('#lt-modal [name=criteria]').fill('Every conclusion has evidence');
    assert.equal(await page.locator('#lt-modal [name=tasks_text]').isDisabled(), true);
    await page.locator('#lt-modal [type=submit]').click();
    await page.waitForFunction(() => document.getElementById('lt-name').textContent === 'Unified objective');
    assert.equal(state.requests.filter(request => request.path.endsWith('/control/start')).length, 0);
    assert.deepEqual(state.pageErrors, []);
});

test('creation and budget dialogs stay centered with visible actions on desktop and narrow screens', async t => {
    const { page, state, select } = await fixture(t);
    for (const theme of ['light', 'dark']) {
        await page.evaluate(theme => document.documentElement.setAttribute('data-theme', theme), theme);
        for (const size of [{width:1440,height:900},{width:1280,height:720},{width:390,height:844},{width:640,height:400}]) {
            await page.setViewportSize(size);
            await page.locator('#task-new-goal').click(); await page.locator('#lt-modal').waitFor();
            const layout = await page.evaluate(() => {
                const rect = element => { const r=element.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height,bottom:r.bottom,right:r.right}; };
                return {dialog:rect(document.getElementById('lt-modal')),footer:rect(document.querySelector('.lt-dialog-footer')),submit:rect(document.querySelector('#lt-modal [type=submit]')),body:rect(document.querySelector('.lt-dialog-body')),scrollWidth:document.querySelector('.lt-dialog-body').scrollWidth};
            });
            assert.ok(Math.abs(layout.dialog.x+layout.dialog.width/2-size.width/2)<2, JSON.stringify(layout));
            assert.ok(Math.abs(layout.dialog.y+layout.dialog.height/2-size.height/2)<2, JSON.stringify(layout));
            assert.ok(layout.dialog.y>=10 && layout.dialog.bottom<=size.height-10, JSON.stringify(layout));
            assert.ok(layout.submit.bottom<=size.height-10 && layout.submit.y>=0, JSON.stringify(layout));
            assert.ok(layout.scrollWidth<=layout.body.width+1, 'dialog body must not overflow horizontally');
            await page.evaluate(() => {const body=document.querySelector('.lt-dialog-body');body.scrollTop=body.scrollHeight;});
            const footer = await page.locator('.lt-dialog-footer').boundingBox(); assert.ok(Math.abs(footer.y-layout.footer.y)<2, 'footer must remain fixed while fields scroll');
            await page.evaluate(() => {document.querySelector('.lt-dialog-body').scrollTop=0;});
            if(size.width===1440 || size.width===390) await page.screenshot({path:`/tmp/csai-unified-task-dialog-${theme}-${size.width}.png`});
            if(theme==='light' && size.width===1440) await page.locator('#lt-modal').screenshot({path:'/tmp/csai-unified-task-dialog-preview.png'});
            await page.locator('#lt-modal .lt-dialog-close').click();
        }
    }
    await page.setViewportSize({width:1280,height:720}); await select('a'); await page.locator('[data-lt-action=budget]').click();
    const box=await page.locator('#lt-modal').boundingBox(); assert.ok(Math.abs(box.x+box.width/2-640)<2 && box.width<=550);
    await page.locator('#lt-modal [data-close]').last().click(); assert.deepEqual(state.pageErrors, []);
});


test('the unified empty state stays usable and checklist details fit narrow screens', async t => {
    const { page,state,refresh }=await fixture(t);
    await page.setViewportSize({width:390,height:844});
    await page.locator('.lt-card[data-mode=list][data-id=q1]').click();await page.locator('[data-bq-detail-for=q1]').waitFor();
    assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=window.innerWidth+1));
    await page.locator('#batch-queue-start-btn').scrollIntoViewIfNeeded();
    const button=await page.locator('#batch-queue-start-btn').boundingBox();assert.ok(button.x>=0 && button.x+button.width<=390);
    await page.screenshot({path:'/tmp/csai-unified-task-list-mobile.png'});
    await page.evaluate(()=>{onTaskQueueDeleted('q1');closeBatchQueueDetailModal();});state.tasks.clear();state.queues.clear();await refresh();
    await page.locator('#lt-empty-state').waitFor();assert.equal(await page.locator('#lt-board').isVisible(),false);
    await page.locator('#lt-empty-state [data-lt-action=new]').click();await page.locator('#lt-modal').waitFor();
    await page.keyboard.press('Escape');assert.deepEqual(state.pageErrors,[]);
});
