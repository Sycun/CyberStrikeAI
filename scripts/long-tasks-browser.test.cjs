/* Offline browser regression against the real task page, auth.js and styles.
 * NODE_PATH=/path/to/playwright/node_modules node --test scripts/long-tasks-browser.test.cjs
 */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const { chromium } = require('playwright');
const root = path.join(__dirname, '..');
const read = f => fs.readFileSync(path.join(root, f), 'utf8');

test('goal task page: authenticated controls, stable polling, XSS, report and responsive layout', async () => {
    const template = read('web/templates/index.html');
    const pageHTML = template.slice(template.indexOf('<div id="page-tasks"'), template.indexOf('<!-- C2 监听器管理页面 -->')).replace('class="page"','class="page active"');
    const locale = JSON.parse(read('web/static/i18n/zh-CN.json'));
    const task = { id: 'fixture', name: '本地材料整理 <img src=x onerror="window.bad=1">', status: 'draft', revision: 0, objective: 'Produce a verified report', scope: 'Local fixture only', criteria: 'Evidence and gaps', strategy: '', summary: '', steps: [], guidance: [], question: '', rounds: 0, max_rounds: 40, remaining_seconds: 3600, updated_at: new Date().toISOString() };
    const authenticated = [], actions = [];
        const server = http.createServer(async (req, res) => {
        if (req.url.startsWith('/static/')) { const file = path.join(root, 'web', req.url); res.setHeader('Content-Type', file.endsWith('.css') ? 'text/css; charset=utf-8' : 'text/javascript; charset=utf-8'); return res.end(fs.readFileSync(file)); }
        if (req.url === '/') {
            res.setHeader('Content-Type', 'text/html; charset=utf-8');
            const html = `<!doctype html><html data-theme="light"><head><meta charset="UTF-8"><link rel="stylesheet" href="/static/css/style.css"><link rel="stylesheet" href="/static/css/mobile.css"><link rel="stylesheet" href="/static/css/long-tasks.css"><style>.page-content{padding:16px}*{box-sizing:border-box}</style></head><body><div class="container"><main class="main-layout"><section class="content-area">${pageHTML}</section></main></div><script>window.t=(key,opts={})=>{let v=key.split('.').reduce((o,k)=>o&&o[k],${JSON.stringify(locale).replaceAll("<", "\\u003c")});return String(v||key).replace(/{{(\\w+)}}/g,(_,k)=>String(opts[k]??''))};window.closeAppModal=()=>{};window.openAppModal=()=>{};window.initializeChatUI=()=>{};window.refreshAppData=async()=>{};window.refreshBatchQueues=()=>{};window.toggleTasksAutoRefresh=()=>{};</script><script src="/static/js/auth.js"></script><script>document.removeEventListener('DOMContentLoaded', initializeApp);saveAuth('fixture-token',new Date(Date.now()+3600000),{permissions:['tasks:read','tasks:write','tasks:delete'],scope:'own'});</script><script src="/static/js/long-tasks.js"></script><script>initLongTasks();</script></body></html>`; return res.end(html);
        }
        if (req.url === '/api/auth/validate') { res.setHeader('Content-Type','application/json'); return res.end('{}'); }
        if (req.url === '/favicon.ico') { res.statusCode=204; return res.end(); }
        if (req.url.startsWith('/api/long-tasks')) {
            authenticated.push(req.headers.authorization);
            if (req.headers.authorization !== 'Bearer fixture-token') { res.statusCode = 401; return res.end('{}'); }
            let body = ''; for await (const c of req) body += c;
            const url = new URL(req.url, 'http://fixture');
            let response;
            if (url.pathname === '/api/long-tasks/options') response = { models: [], projects: [], roles: [] };
            else if (url.pathname === '/api/long-tasks' && req.method === 'POST') { Object.assign(task, JSON.parse(body), { id: 'fixture', revision: 0 }); response = { task }; res.statusCode = 201; actions.push('create'); }
            else if (url.pathname === '/api/long-tasks') response = { tasks: [task], has_more: false };
            else if (url.pathname.endsWith('/events')) response = { events: Number(url.searchParams.get('after')) ? [] : [{ id: 1, kind: 'created', message: '已保存 <script>window.bad=1</script>', created_at: task.updated_at }] };
            else if (url.pathname.endsWith('/report')) { actions.push('report'); res.setHeader('Content-Type', 'text/markdown'); return res.end('# fixture'); }
            else if (url.pathname.includes('/control/')) {
                const action = url.pathname.split('/').pop(); actions.push(action); const payload = JSON.parse(body); assert.equal(payload.revision, task.revision);
                if (action === 'start') { task.status = 'running'; task.steps = [{ id: 's1', title: '读取材料', instruction: 'Inspect fixture', status: 'running', summary: '已取得本地证据', depends_on: [], conversation_id: 'conversation-1', attempts: 1 }]; }
                if (action === 'pause') { task.status = 'paused'; task.steps[0].status = 'interrupted'; }
                if (action === 'guidance') task.guidance.push(payload.message);
                task.revision++; response = { task };
            } else response = { task };
            res.setHeader('Content-Type', 'application/json'); return res.end(JSON.stringify(response));
        }
        res.statusCode = 404; res.end('{}');
    });
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    const executablePath = process.env.CHROME_PATH || (process.platform === 'darwin' ? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : undefined);
    const browser = await chromium.launch(executablePath ? { executablePath } : {});
    try {
        const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, acceptDownloads: true });
        const errors = []; page.on('pageerror', e => { errors.push(e.message); process.stderr.write('PAGE: ' + e.stack + '\n'); });
        page.on('console', m => { if (m.type() === 'error' || m.type() === 'warning') process.stderr.write('CONSOLE: ' + m.text() + '\n'); });
        page.setDefaultTimeout(10000);
        await page.goto('http://127.0.0.1:' + server.address().port);
        await page.locator('[data-lt-action=select]').click();
        await page.locator('#lt-name').waitFor();
        await page.waitForFunction(() => document.getElementById('lt-name').textContent.includes('本地材料'));
        assert.equal(await page.evaluate(() => window.bad), undefined);
        assert.equal(await page.locator('#lt-name img').count(), 0);
        await page.locator('[data-lt-action=start]').click();
        await page.locator('#lt-state .lt-status-running').waitFor();
        await page.locator('#lt-guidance-input').fill('保留这个尚未提交的补充');
        await page.evaluate(() => refreshTaskWorkspace());
        assert.equal(await page.locator('#lt-guidance-input').inputValue(), '保留这个尚未提交的补充');
        await page.locator('[data-lt-action=pause]').click();
        await page.locator('#lt-state .lt-status-paused').waitFor();
        await page.locator('#lt-guidance-send').click();
        await page.waitForFunction(() => document.getElementById('lt-guidance-input').value === '');
        assert.deepEqual(task.guidance, ['保留这个尚未提交的补充']);
        const download = page.waitForEvent('download'); await page.locator('[data-lt-action=report]').click(); await download;
        for (const theme of ['light', 'dark']) {
            await page.evaluate(theme => document.documentElement.setAttribute('data-theme', theme), theme);
            for (const width of [1440, 390]) {
                await page.setViewportSize({ width, height: 1000 });
                const dimensions = await page.evaluate(() => ({ full: document.documentElement.scrollWidth, width: window.innerWidth }));
                assert.ok(dimensions.full <= dimensions.width + 1, `${theme} ${width}: horizontal overflow ${JSON.stringify(dimensions)}`);
                await page.locator('#lt-events').scrollIntoViewIfNeeded();
                const eventsBox=await page.locator('#lt-events').boundingBox(); assert.ok(eventsBox && eventsBox.y < 1000, 'activity must remain reachable');
                await page.locator('#lt-name').scrollIntoViewIfNeeded();
                const screenshot = `/tmp/csai-long-tasks-${theme}-${width}.png`; await page.screenshot({ path: screenshot, fullPage: true });
            }
        }
        await page.setViewportSize({ width: 1440, height: 1000 });
        await page.locator('#task-new-goal').click();
        await page.locator('#lt-modal [name=name]').fill('New draft');
        await page.locator('#lt-modal [name=objective]').fill('Review local evidence');
        await page.locator('#lt-modal [name=scope]').fill('Fixture only');
        await page.locator('#lt-modal [name=criteria]').fill('List verified facts and gaps');
        await page.locator('#lt-modal [type=submit]').click();
        await page.waitForFunction(() => !document.getElementById('lt-modal'));
        assert.equal(actions.filter(a => a === 'start').length, 1, 'creating a draft must not execute');
        await page.locator('[data-task-tab=batch]').click();
        assert.equal(await page.locator('#long-task-workspace').isVisible(), false);
        assert.equal(await page.locator('#task-batch-workspace').isVisible(), true);
        await page.evaluate(() => cleanupLongTasks());
        assert.equal(authenticated.every(a => a === 'Bearer fixture-token'), true);
        assert.ok(actions.includes('create') && actions.includes('report') && actions.includes('pause'));
        assert.deepEqual(errors, []);
    } finally { await browser.close(); await new Promise(resolve => server.close(resolve)); }
});
