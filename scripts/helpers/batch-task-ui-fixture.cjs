const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const { chromium } = require('playwright');
const root = path.join(__dirname, '../..');
const read = file => fs.readFileSync(path.join(root, file), 'utf8');
const template = read('web/templates/index.html');
function div(id) {
    const start = template.indexOf(`<div id="${id}"`);
    if (start < 0) throw new Error(`missing template region ${id}`);
    const tags = /<\/?div\b[^>]*>/g; tags.lastIndex = start;
    let depth = 0, match;
    while ((match = tags.exec(template))) { depth += match[0].startsWith('</') ? -1 : 1; if (!depth) return template.slice(start, tags.lastIndex); }
    throw new Error(`unclosed region ${id}`);
}
function queue(id = 'q1') {
    return { id, title: '批量样例', status: 'pending', role: '', agentMode: 'single', hitlPolicy: '', scheduleMode: 'manual', scheduleEnabled: true, concurrency: 1, createdAt: new Date().toISOString(), currentIndex: 0, tasks: [{ id: 'b1', message: 'first local task', status: 'pending', conversationId: 'batch-conversation' }, { id: 'b2', message: 'second local task', status: 'pending' }] };
}
async function fixture(t) {
    const state = { queues: new Map([['q1', queue()]]), requests: [], hook: null, confirm: true, pageErrors: [] };
    const scripts = [...template.matchAll(/<script\b[^>]*\bsrc=["']([^"']+)["'][^>]*><\/script>/g)].filter(m => /^\/static\/js\/(router|auth|modal|tasks|rbac-guards)\.js(?:\?|$)/.test(m[1])).map(m => m[0]).join('');
    const styles = [...template.matchAll(/<link\b[^>]*\bhref=["']([^"']+)["'][^>]*>/g)].filter(m => /^\/static\/css\/(style|mobile)\.css(?:\?|$)/.test(m[1])).map(m => m[0]).join('');
    const locale = JSON.stringify(JSON.parse(read('web/static/i18n/zh-CN.json'))).replaceAll('<', '\\u003c');
    const html = `<!doctype html><html><head><meta charset="UTF-8">${styles}</head><body><div class="container"><main class="main-layout"><section class="content-area">${div('page-tasks')}<div class="page" id="page-dashboard"><button onclick="switchPage('tasks')">Back to tasks</button></div><div class="page" id="page-chat"></div></section></main></div>${div('batch-import-modal')}${div('batch-queue-detail-modal')}${div('add-batch-task-modal')}<script>window.t=(key,opts={})=>{let v=key.split('.').reduce((o,k)=>o&&o[k],${locale});return String(v||key).replace(/{{(\\w+)}}/g,(_,k)=>String(opts[k]??''))};window.loadRoles=async()=>[];window.loadConversation=async id=>{window.visitedConversation=id};window.initializeChatUI=()=>{};window.refreshAppData=async()=>{};</script>${scripts}<script>document.removeEventListener('DOMContentLoaded',initializeApp);saveAuth('fixture-token',new Date(Date.now()+3600000),{user:{id:'fixture',username:'fixture'},permissions:['tasks:read','tasks:write','tasks:delete','agent:execute','chat:read','chat:write'],scope:'own'});</script></body></html>`;
    const server = http.createServer(async (req, res) => {
        const url = new URL(req.url, 'http://fixture');
        const send = (status, value) => { res.statusCode = status; res.setHeader('Content-Type', 'application/json; charset=utf-8'); res.end(JSON.stringify(value)); };
        try {
            if (url.pathname.startsWith('/static/')) { const file = path.join(root, 'web', url.pathname); res.setHeader('Content-Type', file.endsWith('.css') ? 'text/css; charset=utf-8' : 'text/javascript; charset=utf-8'); return res.end(fs.readFileSync(file)); }
            if (url.pathname === '/') { res.setHeader('Content-Type', 'text/html; charset=utf-8'); return res.end(html); }
            if (url.pathname === '/favicon.ico') { res.statusCode = 204; return res.end(); }
            let raw = ''; for await (const c of req) raw += c;
            const body = raw ? JSON.parse(raw) : null;
            const request = { method: req.method, path: url.pathname, query: Object.fromEntries(url.searchParams), body, authorization: req.headers.authorization }; state.requests.push(request);
            if (req.headers.authorization !== 'Bearer fixture-token') return send(401, { error: 'fixture requires authentication' });
            if (state.hook) { const intercepted = await state.hook(request); if (intercepted) return send(intercepted.status || 200, intercepted.body); }
            if (url.pathname === '/api/projects') return send(200, { projects: [] });
            if (url.pathname === '/api/batch-tasks') {
                if (req.method === 'POST') { const q = { ...queue('new-queue'), ...body, tasks: body.tasks.map((message, i) => ({ id: 'new-' + i, message, status: 'pending' })) }; state.queues.set(q.id, q); return send(200, { queueId: q.id }); }
                const filtered = [...state.queues.values()].filter(q => (!url.searchParams.get('status') || q.status === url.searchParams.get('status')) && (!url.searchParams.get('keyword') || q.title.includes(url.searchParams.get('keyword'))));
                const limit = Number(url.searchParams.get('limit') || 10), page = Number(url.searchParams.get('page') || 1);
                return send(200, { queues: filtered.slice((page - 1) * limit, page * limit), total: filtered.length, total_pages: Math.max(1, Math.ceil(filtered.length / limit)) });
            }
            const batch = url.pathname.match(/^\/api\/batch-tasks\/([^/]+)(?:\/(.*))?$/);
            if (batch) {
                const q = state.queues.get(batch[1]); if (!q) return send(404, { error: 'queue missing' }); const suffix = batch[2] || '';
                if (suffix === 'start' || suffix === 'rerun') q.status = 'running';
                else if (suffix === 'pause') q.status = 'paused';
                else if (suffix === 'metadata' || suffix === 'schedule' || suffix === 'schedule-enabled') Object.assign(q, body);
                else if (suffix === 'tasks' && req.method === 'POST') q.tasks.push({ id: 'added', message: body.message, status: 'pending' });
                else if (suffix.startsWith('tasks/')) { const parts = suffix.split('/'), item = q.tasks.find(t => t.id === parts[1]); if (!item) return send(404, { error: 'step missing' }); if (parts[2] === 'run') { item.status = 'completed'; q.status = 'paused'; } else if (req.method === 'DELETE') q.tasks = q.tasks.filter(t => t !== item); else Object.assign(item, body); }
                else if (req.method === 'DELETE') state.queues.delete(q.id);
                return send(200, { queue: q });
            }
            return send(404, { error: 'unhandled fixture route' });
        } catch (error) { send(500, { error: error.message }); }
    });
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    const executablePath = process.env.CHROME_PATH || (process.platform === 'darwin' ? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : undefined);
    const browser = await chromium.launch(executablePath ? { executablePath } : {});
    t.after(async () => { await browser.close(); server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); });
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, acceptDownloads: true }); page.setDefaultTimeout(5000);
    page.on('pageerror', error => state.pageErrors.push(error.message));
    page.on('dialog', dialog => (dialog.type() === 'confirm' && !state.confirm) ? dialog.dismiss() : dialog.accept());
    await page.goto(`http://127.0.0.1:${server.address().port}/#tasks`);
    await page.locator('[data-queue-id="q1"]').waitFor();
    await page.locator('#tasks-auto-refresh').uncheck();
    const refresh = () => page.evaluate(() => refreshBatchQueues());
    return { page, state, refresh };
}
module.exports = { fixture, queue };
