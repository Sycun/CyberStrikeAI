/* Independent assessment workspace. Batch task routes and controllers are untouched. */
(function () {
    'use strict';
    const S = { id: '', task: null, tab: 'overview', page: 0, more: false, busy: false, epoch: 0, timer: null, mounted: false, drawerEpoch: 0, events: [], receipts: [], receiptOffset: 0, receiptMore: false };
    const labels = { status: '状态', updatedAt: '更新时间', loading: '正在读取测评…', contextUsage:'上下文 / 窗口', toolDefinitions:'工具定义', title: '深度测评', subtitle: '围绕一个目标持续探索，保存证据与进度，跨会话继续执行。', create: '新建测评', refresh: '刷新', search: '搜索测评名称', all: '全部状态', empty: '暂无测评，先创建一个目标。', select: '选择测评，查看探索进度与证据。', previous: '上一页', next: '下一页', name: '测评名称', objective: '目标', scope: '允许范围与排除项', criteria: '验收条件', model: '模型渠道', project: '关联项目', role: '角色', default: '系统默认', none: '不关联', hours: '任务窗口（小时）', tokens: 'Token 预算', rounds: '规划轮数上限', concurrency: '并行步骤', autoResume: '服务重启后自动恢复结果明确的步骤', windowNote: '从首次开始计算，暂停和服务停机也计入窗口。', save: '保存草稿', close: '关闭', start: '开始 / 继续', pause: '暂停', cancel: '停止', delete: '删除', report: '下载报告', overview: '概览', plan: '探索计划', evidence: '证据回执', activity: '运行记录', quality: '上下文与用量', guidance: '补充线索或调整方向', send: '提交方向', answer: '答复并继续', budget: '调整预算', retry: '恢复此步骤', conversation: '查看执行会话', unknown: '请先核对回执，确认上次操作结果及执行资源状态。', reconcile: '我已核对上次调用结果，允许从原会话继续', confirmStop: '停止后将保留已有结果，结束本次测评。', confirmDelete: '删除本条测评？关联执行会话仍保留。', remaining: '剩余窗口', deadline: '截止时间', strategy: '当前路线', summary: '当前结论 / 缺口', question: '待补充信息', dependencies: '前置步骤', segments: '执行分段', evidenceIDs: '证据引用', noSteps: '开始后由规划器逐步生成探索方向。', noRecords: '暂无记录', calls: '工具调用', compactions: '上下文压缩', activeTools: '当前可见工具', session: '会话', tokenNote: '包含 Pi 会话中的压缩用量。预算用于停止后续调度，在途请求可能超出预算。', recoveryNote: '结果不明的调用会暂停等待核对，已完成步骤不会自动重做。', saved: '已保存', updated: '已更新', retryNote: '恢复会保留原 Pi 会话及历史证据。', newHours: '从现在起的剩余窗口（小时）' };
    const states = { draft: '草稿', running: '执行中', pending: '待执行', completed: '已完成', blocked: '受阻', failed: '失败', paused: '已暂停', pausing: '暂停中', cancelling: '停止中', cancelled: '已停止', recovering: '待恢复', waiting_retry: '等待模型恢复', waiting_input: '待答复', needs_review: '待核对', uncertain: '结果不明', budget_exhausted: '预算耗尽', interrupted: '已中断', cleanup_unconfirmed: '待确认资源停止', retired: '已撤下', reconciled: '已核对', yielded: '分段完成' };
    const tr = (key, fallback) => { const v = typeof window.t === 'function' ? window.t('deepAssessments.' + key) : ''; return v && v !== 'deepAssessments.' + key ? v : fallback; };
    const L = key => tr(key, labels[key] || key);
    const status = key => tr('states.' + key, states[key] || key);
    const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
    const $ = id => document.getElementById(id);
    const active = t => ['running', 'pausing', 'cancelling'].includes(t?.status);
    const terminal = t => ['completed', 'cancelled'].includes(t?.status);
    const can = action => typeof window.hasPermission !== 'function' || window.hasPermission('assessments:' + action);
    function notice(message, error = false) {
        ['da-notice', 'da-detail-notice', 'da-form-notice'].forEach(id => {
            const el = $(id);
            if (el) { el.textContent = message; el.classList.toggle('is-error', error); el.hidden = !message; }
        });
    }
    function syncDrawerLock() {
        document.body.classList.toggle('da-drawer-open', !!$('da-inspector')?.open || !!$('da-form-drawer')?.open);
    }
    function openDrawer(id) {
        const drawer = $(id);
        if (!drawer.open) {
            drawer.returnFocus = document.activeElement;
            drawer.showModal();
        }
        syncDrawerLock();
        const target = id === 'da-form-drawer' ? (drawer.querySelector('input:not([type="checkbox"]), textarea') || drawer.querySelector('button')) : drawer.querySelector('[data-da-action="close-detail"]');
        target?.focus({ preventScroll: true });
    }
    function closeDrawer(id) {
        ++S.drawerEpoch;
        const drawer = $(id);
        if (drawer?.open) drawer.close();
        syncDrawerLock();
    }
    function bindDrawer(id) {
        const drawer = $(id);
        let outsideDown = false;
        const outside = event => {
            const rect = drawer.getBoundingClientRect();
            return event.target === drawer && (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom);
        };
        drawer.addEventListener('pointerdown', event => { outsideDown = outside(event); });
        drawer.addEventListener('pointercancel', () => { outsideDown = false; });
        drawer.addEventListener('click', event => {
            const dismiss = outsideDown && outside(event); outsideDown = false;
            if (dismiss && !S.busy) closeDrawer(id);
        });
        drawer.addEventListener('cancel', event => {
            if (S.busy) event.preventDefault();
            else ++S.drawerEpoch;
        });
        drawer.addEventListener('close', () => {
            syncDrawerLock();
            requestAnimationFrame(() => {
                if (!$('page-deep-assessments')?.classList.contains('active')) return;
                const top = $('da-form-drawer').open ? $('da-form-drawer') : $('da-inspector').open ? $('da-inspector') : null;
                const prior = drawer.returnFocus;
                if (prior?.isConnected && !prior.disabled && (!top || top.contains(prior))) prior.focus({ preventScroll: true });
                else if (top) top.querySelector('button')?.focus({ preventScroll: true });
                else ([...document.querySelectorAll('.da-list-item')].find(row => row.dataset.id === S.id) || document.querySelector('[data-da-action="create"]'))?.focus({ preventScroll: true });
            });
        });
    }
    async function api(path, options = {}) {
        const fetcher = typeof window.apiFetch === 'function' ? window.apiFetch : fetch;
        const response = await fetcher('/api/deep-assessments' + path, { ...options, headers: { 'Content-Type': 'application/json', ...(options.headers || {}) } });
        if (!response.ok) { let value; try { value = await response.json(); } catch {} throw new Error(value?.error || 'HTTP ' + response.status); }
        return response;
    }
    async function json(path, options) { return (await api(path, options)).json(); }
    const button = (action, label, extra = '') => `<button type="button" class="btn-secondary" data-da-action="${action}" ${extra}>${esc(label)}</button>`;
    const badge = value => `<span class="da-badge da-${esc(value)}">${esc(status(value))}</span>`;
    function mount() {
        const root = $('deep-assessments-root'); if (!root || S.mounted) return;
        S.mounted = true;
        root.innerHTML = `
            <div class="page-header da-header"><div><h2>${esc(L('title'))}</h2><p>${esc(L('subtitle'))}</p></div><div>${button('refresh', L('refresh'))}${can('write') ? button('create', L('create')) : ''}</div></div>
            <div id="da-notice" class="da-notice" role="status" hidden></div>
            <section class="da-list-panel">
                <div class="da-filters"><input id="da-search" class="form-control" aria-label="${esc(L('search'))}" placeholder="${esc(L('search'))}"><select id="da-filter" class="form-control" aria-label="${esc(L('all'))}"><option value="">${esc(L('all'))}</option>${Object.keys(states).filter(s => !['pending', 'retired', 'reconciled', 'yielded'].includes(s)).map(s => `<option value="${s}">${esc(status(s))}</option>`).join('')}</select></div>
                <div class="da-list-head" aria-hidden="true"><span>${esc(L('name'))}</span><span>${esc(L('status'))}</span><span>${esc(L('rounds'))}</span><span>${esc(L('tokens'))}</span><span>${esc(L('updatedAt'))}</span></div>
                <div id="da-list"></div>
                <div class="da-pagination">${button('previous', L('previous'))}<span id="da-page"></span>${button('next', L('next'))}</div>
            </section>
            <dialog id="da-inspector" class="da-drawer da-inspector" aria-labelledby="da-title">
                <header class="da-drawer-header"><div id="da-title"></div><button type="button" class="da-drawer-close" data-da-action="close-detail" aria-label="${esc(L('close'))}">&times;</button></header>
                <div class="da-detail-scroll"><div id="da-detail-notice" class="da-notice" role="status" hidden></div><div id="da-controls" class="da-controls"></div><div id="da-tabs" class="da-tabs" role="tablist"></div><div id="da-body"></div><form id="da-guidance-form" hidden><label for="da-guidance">${esc(L('guidance'))}</label><textarea id="da-guidance" class="form-control" rows="3" maxlength="8000"></textarea><button class="btn-primary" id="da-guidance-submit" type="submit">${esc(L('send'))}</button></form></div>
            </dialog>
            <dialog id="da-form-drawer" class="da-drawer da-form-drawer" aria-labelledby="da-form-title">
                <header class="da-drawer-header"><h3 id="da-form-title"></h3><button type="button" class="da-drawer-close" data-da-action="close" aria-label="${esc(L('close'))}">&times;</button></header>
                <div id="da-form-notice" class="da-notice" role="alert" hidden></div><div id="da-form-content"></div>
            </dialog>`;
        bindDrawer('da-inspector');
        bindDrawer('da-form-drawer');
        window.addEventListener('hashchange', () => {
            if (window.location.hash.slice(1).split('?')[0] !== 'deep-assessments') { closeDrawer('da-form-drawer'); closeDrawer('da-inspector'); }
        });
        root.addEventListener('click', event => { const target = event.target.closest('[data-da-action]'); if (target) void action(target.dataset.daAction, target.dataset); });
        root.addEventListener('submit', event => { event.preventDefault(); if (event.target.id === 'da-create-form') void saveCreate(event.target); if (event.target.id === 'da-guidance-form') void guidance(); if (event.target.id === 'da-budget-form') void saveBudget(event.target); });
        let searchTimer;
        $('da-search').addEventListener('input', () => { clearTimeout(searchTimer); searchTimer = setTimeout(() => { S.page = 0; void refresh(); }, 250); });
        $('da-filter').addEventListener('change', () => { S.page = 0; void refresh(); });
    }
    function renderTask() {
        const task = S.task;
        if (!task) { $('da-title').textContent = L('title'); $('da-controls').innerHTML = ''; $('da-tabs').innerHTML = ''; $('da-body').textContent = L(S.id ? 'loading' : 'select'); $('da-guidance-form').hidden = true; return; }
        $('da-title').innerHTML = `<h3>${esc(task.name)} ${badge(task.status)}</h3>`;
        $('da-controls').innerHTML = `${can('write') && !terminal(task) ? (!active(task) ? button('start', L('start'), task.question || task.status === 'needs_review' ? 'disabled' : '') + button('budget', L('budget')) : button('pause', L('pause'), task.status !== 'running' ? 'disabled' : '')) + button('cancel', L('cancel')) : ''}${button('report', L('report'))}${can('delete') && !active(task) ? button('delete', L('delete')) : ''}`;
        $('da-tabs').innerHTML = ['overview', 'plan', 'evidence', 'activity', 'quality'].map(tab => `<button type="button" role="tab" aria-selected="${S.tab === tab}" data-da-action="tab" data-tab="${tab}" class="${S.tab === tab ? 'active' : ''}">${esc(L(tab))}</button>`).join('');
        const body = $('da-body'), scroller = $('da-inspector').querySelector('.da-detail-scroll'), scroll = scroller.scrollTop;
        const opened = new Set([...body.querySelectorAll('details[open]')].map(el => el.dataset.step));
        if (S.tab === 'overview') {
            const seconds = task.deadline ? Math.max(0, (Date.parse(task.deadline) - Date.now()) / 1000) : task.remaining_seconds;
            body.innerHTML = `<div class="da-stats"><div><small>${esc(L('remaining'))}</small><strong>${(seconds / 3600).toFixed(1)} h</strong></div><div><small>${esc(L('tokens'))}</small><strong>${Number(task.tokens_used || 0).toLocaleString()} / ${Number(task.token_budget || 0).toLocaleString()}</strong></div><div><small>${esc(L('rounds'))}</small><strong>${task.rounds || 0} / ${task.max_rounds}</strong></div></div>${task.deadline ? `<p>${esc(L('deadline'))}：${esc(new Date(task.deadline).toLocaleString())}</p>` : ''}${['objective', 'scope', 'criteria', 'strategy', 'summary', 'question'].filter(k => task[k]).map(k => `<section class="da-section"><h4>${esc(L(k))}</h4><pre>${esc(task[k])}</pre></section>`).join('')}<p class="da-muted">${esc(L('recoveryNote'))}</p>`;
        } else if (S.tab === 'plan') {
            body.innerHTML = (task.steps || []).map(step => `<details class="da-step" data-step="${esc(step.id)}" ${opened.has(step.id) ? 'open' : ''}><summary>${badge(step.status)} <strong>${esc(step.title)}</strong><small>${esc(step.id)}</small></summary><pre>${esc(step.instruction)}</pre><p>${esc(L('dependencies'))}: ${esc((step.depends_on || []).join(', ') || '—')} · ${esc(L('segments'))}: ${step.attempts || 0}</p><pre>${esc(step.summary)}</pre><p>${esc(L('evidenceIDs'))}: ${esc((step.evidence || []).join(', ') || '—')}</p><div class="da-controls">${step.conversation_id ? button('conversation', L('conversation'), `data-conversation="${esc(step.conversation_id)}"`) : ''}${!active(task) && can('write') && ['failed', 'blocked', 'interrupted', 'uncertain', 'cleanup_unconfirmed'].includes(step.status) ? button('retry', L('retry'), `data-step="${esc(step.id)}"`) : ''}</div></details>`).join('') || `<p>${esc(L('noSteps'))}</p>`;
        } else if (S.tab === 'evidence') {
            body.innerHTML = S.receipts.map(row => `<details class="da-step" data-step="${esc(row.id)}" ${opened.has(row.id) ? 'open' : ''}><summary>${badge(row.status)} ${esc(row.tool)} · ${esc(row.step_id)}</summary><code>${esc(row.execution_id || row.id)}</code><pre>${esc(row.arguments)}</pre><pre>${esc(row.result)}</pre></details>`).join('') || `<p>${esc(L('noRecords'))}</p>`;
            body.innerHTML += `<div class="da-controls">${S.receiptOffset > 0 ? button('previous-receipts', L('previous')) : ''}${S.receiptMore ? button('more-receipts', L('next')) : ''}</div>`;
        } else if (S.tab === 'activity') {
            body.innerHTML = S.events.slice().reverse().map(row => `<div class="da-event"><small>${esc(new Date(row.created_at).toLocaleString())} · ${esc(row.step_id)} · ${esc(row.kind)}</small><pre>${esc(row.message)}</pre></div>`).join('') || `<p>${esc(L('noRecords'))}</p>`;
        } else {
            body.innerHTML = `<p class="da-muted">${esc(L('tokenNote'))}</p><div class="da-table-wrap"><table><thead><tr><th>${esc(L('session'))}</th><th>Token</th><th>${esc(L('compactions'))}</th><th>${esc(L('activeTools'))}</th><th>${esc(L('contextUsage'))}</th><th>${esc(L('toolDefinitions'))}</th></tr></thead><tbody>${Object.entries(task.metrics || {}).map(([id, m]) => `<tr><td>${esc(m.step_id)}<br><small>${esc(id.slice(0, 12))}</small></td><td>${Number(m.tokens || 0).toLocaleString()}</td><td>${m.compactions || 0}</td><td>${m.tool_count || 0}</td><td>${m.context_tokens == null ? '—' : Number(m.context_tokens).toLocaleString()} / ${Number(m.context_window || 0).toLocaleString()}</td><td>${(Number(m.tool_definition_bytes || 0) / 1024).toFixed(1)} KB</td></tr>`).join('')}</tbody></table></div>`;
        }
        scroller.scrollTop = scroll;
        $('da-guidance-form').hidden = !can('write') || terminal(task);
        $('da-guidance-submit').textContent = L(task.question ? 'answer' : 'send');
        if (S.busy) $('da-controls').querySelectorAll('button').forEach(b => b.disabled = true);
    }
    async function refresh() {
        if (S.busy || !S.mounted) return;
        const epoch = ++S.epoch, id = $('da-inspector').open ? S.id : '';
        try {
            const query = new URLSearchParams({ q: $('da-search').value, status: $('da-filter').value, limit: '20', offset: String(S.page * 20) });
            const [list, detail] = await Promise.all([json('?' + query), id ? json('/' + encodeURIComponent(id)) : null]);
            if (epoch !== S.epoch || S.busy) return;
            S.more = list.has_more;
            const listScroll = $('da-list').scrollTop;
            $('da-list').innerHTML = (list.tasks || []).map(task => `<button type="button" class="da-list-item ${task.id === S.id ? 'selected' : ''}" data-da-action="select" data-id="${esc(task.id)}" aria-haspopup="dialog"><strong>${esc(task.name)}</strong><span>${badge(task.status)}</span><span class="da-row-rounds"><small>${esc(L('rounds'))}</small>${task.rounds || 0} / ${task.max_rounds}</span><span class="da-row-tokens"><small>${esc(L('tokens'))}</small>${Number(task.tokens_used || 0).toLocaleString()} / ${Number(task.token_budget || 0).toLocaleString()}</span><time>${esc(new Date(task.updated_at).toLocaleString())}</time></button>`).join('') || `<p class="da-muted da-empty">${esc(L('empty'))}</p>`;
            $('da-list').scrollTop = listScroll;
            $('da-page').textContent = String(S.page + 1);
            $('deep-assessments-root').querySelector('[data-da-action="previous"]').disabled = S.page === 0;
            $('deep-assessments-root').querySelector('[data-da-action="next"]').disabled = !S.more;
            if (detail && (!S.task || detail.task.revision >= S.task.revision)) S.task = detail.task;
            if (id && S.tab === 'activity') { const data = await json('/' + encodeURIComponent(id) + '/events?after=' + (S.events.at(-1)?.id || 0)); if (epoch !== S.epoch) return; S.events.push(...data.events); S.events = S.events.slice(-500); }
            if (id && S.tab === 'evidence') { const data = await json('/' + encodeURIComponent(id) + '/receipts?offset=' + S.receiptOffset); if (epoch !== S.epoch) return; S.receipts = data.receipts; S.receiptMore = data.has_more; }
            renderTask();
        } catch (error) { if (epoch === S.epoch) notice(error.message, true); }
    }
    async function mutate(action, values = {}) {
        if (S.busy || !S.task || !can('write')) return false;
        S.busy = true; ++S.epoch; renderTask();
        try {
            const data = await json('/' + encodeURIComponent(S.id) + '/control/' + action, { method: 'POST', body: JSON.stringify({ revision: S.task.revision, ...values }) });
            S.task = data.task; notice(data.start_error || L('updated'), !!data.start_error); return !data.start_error;
        } catch (error) { notice(error.message, true); return false; }
        finally { S.busy = false; renderTask(); void refresh(); }
    }
    function showForm(title, html) {
        $('da-form-title').textContent = title;
        $('da-form-content').innerHTML = html;
        $('da-form-notice').hidden = true;
        openDrawer('da-form-drawer');
    }
    const field = (name, label, value = '', type = 'text', extra = '') => `<label>${esc(label)}<input class="form-control" name="${name}" type="${type}" value="${esc(value)}" ${extra}></label>`;
    const choices = (name, label, items, empty) => `<label>${esc(label)}<select class="form-control" name="${name}"><option value="">${esc(empty)}</option>${items.map(item => `<option value="${esc(item.id ?? item)}">${esc(item.name ?? item)}</option>`).join('')}</select></label>`;
    async function create() {
        if (!can('write')) return;
        const epoch = ++S.drawerEpoch;
        const opts = await json('/options');
        if (epoch !== S.drawerEpoch || !$('page-deep-assessments')?.classList.contains('active')) return;
        showForm(L('create'), `<form id="da-create-form" class="da-drawer-form"><div class="da-form-body">${field('name', L('name'), '', 'text', 'maxlength="200" required')}<label>${esc(L('objective'))}<textarea class="form-control" name="objective" rows="3" maxlength="16000" required></textarea></label><label>${esc(L('scope'))}<textarea class="form-control" name="scope" rows="3" maxlength="16000" required></textarea></label><label>${esc(L('criteria'))}<textarea class="form-control" name="criteria" rows="2" maxlength="8000"></textarea></label><div class="da-form-grid">${choices('model', L('model'), opts.models || [], L('default'))}${choices('role', L('role'), opts.roles || [], L('default'))}${choices('project_id', L('project'), opts.projects || [], L('none'))}${field('hours', L('hours'), 48, 'number', 'min="0.1" max="168" step="0.1" required')}${field('token_budget', L('tokens'), 2000000, 'number', 'min="1" max="100000000" required')}${field('max_rounds', L('rounds'), 512, 'number', 'min="1" max="4096" required')}${field('concurrency', L('concurrency'), 2, 'number', 'min="1" max="4" required')}</div><label class="da-check"><input type="checkbox" name="auto_resume" checked>${esc(L('autoResume'))}</label><p class="da-muted">${esc(L('windowNote'))} ${esc(L('recoveryNote'))}</p></div><footer class="da-drawer-footer"><button type="submit" class="btn-primary">${esc(L('save'))}</button>${button('close', L('close'))}</footer></form>`);
    }
    async function saveCreate(form) {
        if (S.busy || !can('write')) return;
        const values = Object.fromEntries(new FormData(form)); values.remaining_seconds = Math.round(Number(values.hours) * 3600); delete values.hours;
        for (const key of ['token_budget', 'max_rounds', 'concurrency']) values[key] = Number(values[key]); values.auto_resume = form.elements.auto_resume.checked;
        S.busy = true; ++S.epoch; form.querySelector('button[type="submit"]').disabled = true;
        try { const data = await json('', { method: 'POST', body: JSON.stringify(values) }); S.id = data.task.id; S.task = data.task; S.tab = 'overview'; S.events = []; S.receipts = []; S.receiptOffset = 0; closeDrawer('da-form-drawer'); renderTask(); if ($('page-deep-assessments')?.classList.contains('active')) openDrawer('da-inspector'); notice(L('saved')); }
        catch (error) { notice(error.message, true); }
        finally { S.busy = false; form.querySelector('button[type="submit"]').disabled = false; await refresh(); }
    }
    async function guidance() { const value = $('da-guidance').value.trim(); if (!value) return; if (await mutate(S.task.question ? 'answer' : 'guidance', { message: value })) { if ($('da-guidance').value.trim() === value) $('da-guidance').value = ''; } }
    async function saveBudget(form) { const values = Object.fromEntries(new FormData(form)); if (await mutate('budget', { remaining_seconds: Math.round(Number(values.hours) * 3600), max_rounds: Number(values.max_rounds), token_budget: Number(values.token_budget) })) closeDrawer('da-form-drawer'); }
    async function action(name, data = {}) {
        if (S.busy) return;
        try {
            if (name === 'close') return closeDrawer('da-form-drawer');
            if (name === 'close-detail') return closeDrawer('da-inspector');
            if (name === 'create') return await create();
            if (name === 'refresh') return await refresh();
            if (name === 'previous' || name === 'next') { S.page += name === 'previous' ? -1 : 1; S.page = Math.max(0, S.page); return await refresh(); }
            if (name === 'select') { ++S.drawerEpoch; if (S.id !== data.id) { S.id = data.id; S.task = null; S.events = []; S.receipts = []; S.receiptOffset = 0; $('da-guidance').value = ''; $('da-inspector').querySelector('.da-detail-scroll').scrollTop = 0; } renderTask(); openDrawer('da-inspector'); return await refresh(); }
            if (name === 'tab') { S.tab = data.tab; $('da-inspector').querySelector('.da-detail-scroll').scrollTop = 0; return await refresh(); }
            if (name === 'start' || name === 'pause') return await mutate(name);
            if (name === 'cancel') { if (window.confirm(L('confirmStop'))) return await mutate('cancel'); return; }
            if (name === 'conversation') { closeDrawer('da-form-drawer'); closeDrawer('da-inspector'); if (typeof window.switchPage === 'function') window.switchPage('chat'); if (typeof window.loadConversation === 'function') await window.loadConversation(data.conversation); return; }
            if (name === 'budget') return showForm(L('budget'), `<form id="da-budget-form" class="da-drawer-form"><div class="da-form-body">${field('hours', L('newHours'), 48, 'number', 'min="0.1" max="168" step="0.1" required')}${field('max_rounds', L('rounds'), Math.max(S.task.max_rounds, S.task.rounds + 1), 'number', 'min="1" max="4096" required')}${field('token_budget', L('tokens'), Math.max(S.task.token_budget, S.task.tokens_used + 1), 'number', 'min="1" max="100000000" required')}</div><footer class="da-drawer-footer"><button type="submit" class="btn-primary">${esc(L('save'))}</button>${button('close', L('close'))}</footer></form>`);
            if (name === 'retry') { const step = S.task.steps.find(s => s.id === data.step); const uncertain = ['uncertain', 'cleanup_unconfirmed'].includes(step?.status); if (uncertain && !window.confirm(L('unknown') + '\n\n' + L('reconcile'))) return; return await mutate('retry', { step_id: data.step, reconciled: uncertain, cleanup_confirmed: uncertain }); }
            if (name === 'report') { const response = await api('/' + encodeURIComponent(S.id) + '/report'); const url = URL.createObjectURL(await response.blob()); const a = document.createElement('a'); a.href = url; a.download = 'assessment-' + S.id + '.md'; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); return; }
            if (name === 'delete' && can('delete') && window.confirm(L('confirmDelete'))) { S.busy = true; ++S.epoch; try { await api('/' + encodeURIComponent(S.id), { method: 'DELETE' }); S.id = ''; S.task = null; closeDrawer('da-inspector'); } finally { S.busy = false; } return await refresh(); }
            if (name === 'more-receipts' || name === 'previous-receipts') { S.receiptOffset = Math.max(0, S.receiptOffset + (name === 'more-receipts' ? 50 : -50)); return await refresh(); }
        } catch (error) { notice(error.message, true); }
    }
    window.DeepAssessments = { init() { mount(); void refresh(); if (!S.timer) S.timer = setInterval(() => { if ($('page-deep-assessments')?.classList.contains('active') && !document.hidden) void refresh(); }, 4000); }, refresh };
})();
