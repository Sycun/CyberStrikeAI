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
function goal(id, status = 'draft') {
    return { id, name: `目标 ${id}`, status, revision: 0, objective: 'Local fixture report', scope: 'Fixtures only', criteria: 'Verify evidence', role: '', model: '', project_id: '', strategy: 'Read then verify', summary: 'Saved progress', steps: [], guidance: [], question: '', rounds: 2, max_rounds: 40, remaining_seconds: 3600, updated_at: new Date().toISOString() };
}
function queue(id = 'q1') {
    return { id, title: '批量样例', status: 'pending', role: '', agentMode: 'single', hitlPolicy: '', scheduleMode: 'manual', scheduleEnabled: true, concurrency: 1, createdAt: new Date().toISOString(), currentIndex: 0, tasks: [{ id: 'b1', message: 'first local task', status: 'pending', conversationId: 'batch-conversation' }, { id: 'b2', message: 'second local task', status: 'pending' }] };
}
async function fixture(t) {
    const a = goal('a', 'paused'); a.steps = [{ id: 's1', title: 'Local step', instruction: 'Read evidence', depends_on: [], status: 'failed', summary: 'Previous failure', conversation_id: 'conversation-a', attempts: 1 }];
    const state = { tasks: new Map([['a', a], ['b', goal('b')]]), queues: new Map([['q1', queue()]]), requests: [], hook: null, confirm: true, failResume: '', pageErrors: [] };
    const scripts = [...template.matchAll(/<script\b[^>]*\bsrc=["']([^"']+)["'][^>]*><\/script>/g)].filter(m => /^\/static\/js\/(router|auth|modal|task-list-view|task-checklist-view|tasks|long-tasks|rbac-guards)\.js(?:\?|$)/.test(m[1])).map(m => m[0]).join('');
    const styles = [...template.matchAll(/<link\b[^>]*\bhref=["']([^"']+)["'][^>]*>/g)].filter(m => /^\/static\/css\/(style|mobile|long-tasks)\.css(?:\?|$)/.test(m[1])).map(m => m[0]).join('');
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
            if (url.pathname === '/api/long-tasks/options') return send(200, { projects: [], roles: [], models: [] });
            if (url.pathname === '/api/tasks') {
                const rows = [...state.tasks.values()].map(task => ({ ...task, mode: 'goal', completed_steps: task.steps.filter(step => step.status === 'completed').length, total_steps: task.steps.length }));
                for (const q of state.queues.values()) rows.push({ id:q.id, mode:'list', name:q.title, status:q.status, summary:q.lastRunError || '', updated_at:q.createdAt, schedule_mode:q.scheduleMode, next_run_at:q.nextRunAt, completed_steps:q.tasks.filter(step => step.status==='completed').length, total_steps:q.tasks.length });
                const filtered=rows.filter(task => (!url.searchParams.get('mode') || task.mode===url.searchParams.get('mode')) && (!url.searchParams.get('status') || task.status===url.searchParams.get('status')) && (!url.searchParams.get('q') || task.name.includes(url.searchParams.get('q'))));
                const offset=Number(url.searchParams.get('offset')||0),limit=Number(url.searchParams.get('limit')||30);
                return send(200,{tasks:filtered.slice(offset,offset+limit),has_more:filtered.length>offset+limit,total:filtered.length,offset});
            }
            if (url.pathname === '/api/long-tasks') {
                if (req.method === 'POST') { const task = { ...goal('new-' + state.tasks.size), ...body }; state.tasks.set(task.id, task); return send(201, { task }); }
                const tasks = [...state.tasks.values()].filter(task => (!url.searchParams.get('status') || task.status === url.searchParams.get('status')) && (!url.searchParams.get('q') || task.name.includes(url.searchParams.get('q'))));
                const offset = Number(url.searchParams.get('offset') || 0), limit = Number(url.searchParams.get('limit') || 30);
                return send(200, { tasks: tasks.slice(offset, offset + limit), has_more: tasks.length > offset + limit });
            }
            const match = url.pathname.match(/^\/api\/long-tasks\/([^/]+)(?:\/(.*))?$/);
            if (match) {
                const task = state.tasks.get(decodeURIComponent(match[1])); if (!task) return send(404, { error: 'task missing' });
                const suffix = match[2] || '';
                if (suffix === 'report') { res.setHeader('Content-Type', 'text/markdown'); return res.end('# ' + task.id); }
                if (suffix === 'events') return send(200, { events: [] });
                if (req.method === 'DELETE') { state.tasks.delete(task.id); return send(200, { deleted: true }); }
                if (suffix.startsWith('control/')) {
                    if (body.revision !== task.revision) return send(409, { error: 'revision conflict' });
                    const action = suffix.slice(8);
                    if (['completed', 'cancelled'].includes(task.status)) return send(409, { error: 'terminal task' });
                    if (action === 'start') task.status = 'running';
                    else if (action === 'pause') task.status = 'paused';
                    else if (action === 'cancel') task.status = 'cancelled';
                    else if (action === 'retry') { const step = task.steps.find(s => s.id === body.step_id); if (!step) return send(400, { error: 'missing step' }); step.status = 'pending'; task.status = 'paused'; }
                    else if (action === 'budget') { task.max_rounds = body.max_rounds; task.remaining_seconds = body.remaining_seconds; task.status = 'paused'; }
                    else if (action === 'guidance' || action === 'answer') { task.guidance.push(body.message); if (action === 'answer') { task.question = ''; task.status = state.failResume ? 'paused' : 'running'; } }
                    else return send(400, { error: 'unknown action' });
                    task.revision++; return send(200, { task, ...(action === 'answer' && state.failResume ? { start_error: state.failResume } : {}) });
                }
                return send(200, { task });
            }
            if (url.pathname === '/api/batch-tasks') {
                if (req.method === 'POST') { const q = { ...queue('new-queue'), ...body, tasks: body.tasks.map((message, i) => ({ id: 'new-' + i, message, status: 'pending' })) }; state.queues.set(q.id, q); return send(200, { queueId: q.id }); }
                const queues = [...state.queues.values()]; return send(200, { queues, total: queues.length, total_pages: 2 });
            }
            const batch = url.pathname.match(/^\/api\/batch-tasks\/([^/]+)(?:\/(.*))?$/);
            if (batch) {
                const q = state.queues.get(batch[1]); if (!q) return send(404, { error: 'queue missing' }); const suffix = batch[2] || '';
                if(suffix.startsWith('tasks/') && req.method==='GET') {
                    const item=q.tasks.find(item=>item.id===suffix.split('/')[1]);if(!item)return send(404,{error:'step missing'});
                    const counts={total:q.tasks.length};for(const item of q.tasks)counts[item.status]=(counts[item.status]||0)+1;
                    return send(200,{task:item,queue:{...q,tasks:[]},task_stats:counts});
                }
                if(!suffix && req.method==='GET' && url.searchParams.has('task_page')) {
                    const size=Number(url.searchParams.get('task_page_size')||20),total=q.tasks.length,pages=Math.max(1,Math.ceil(total/size)),page=Math.min(pages,Math.max(1,Number(url.searchParams.get('task_page')||1))),offset=(page-1)*size;
                    const counts={total,pending:0,running:0,completed:0,failed:0,cancelled:0};for(const item of q.tasks)counts[item.status]=(counts[item.status]||0)+1;
                    return send(200,{queue:{...q,tasks:q.tasks.slice(offset,offset+size).map(item=>({...item,message:item.message.slice(0,600),result:(item.result||'').slice(0,400)}))},task_stats:counts,task_pagination:{page,page_size:size,total,total_pages:pages,offset,preview:true}});
                }
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
    await page.locator('[data-record-id="goal:a"] [data-record-open]').waitFor();
    await page.locator('#tasks-auto-refresh').uncheck();
    const select = async id => { await page.locator(`[data-record-id="goal:${id}"] [data-record-open]`).click(); await page.waitForFunction(id => document.getElementById('lt-name').textContent === '目标 ' + id, id); };
    const refresh = () => page.evaluate(() => refreshTaskWorkspace());
    return { page, state, select, refresh };
}
module.exports = { fixture, goal };
