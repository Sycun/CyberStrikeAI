/* Durable objectives in the existing task-management page. */
(function () {
    'use strict';
    const state = { active: false, tab: 'goals', tasks: [], selected: null, task: null, events: [], cursor: 0, offset: 0, more: false, epoch: 0, loading: false, timer: null, options: null, controller: null };
    const el = id => document.getElementById(id);
    const tr = (key, args) => typeof window.t === 'function' ? window.t('longTasks.' + key, args) : key;
    const esc = value => String(value == null ? '' : value).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
    const active = task => task && ['running', 'pausing', 'cancelling'].includes(task.status);
    const badge = status => `<span class="lt-status lt-status-${esc(status)}">${esc(tr('status.' + status))}</span>`;
    const button = (action, label, extra = '', disabled = false) => `<button type="button" class="btn-secondary" data-lt-action="${action}" ${extra} ${disabled ? 'disabled' : ''}>${esc(tr(label))}</button>`;
    function writable(permission = 'tasks:write') { return typeof window.hasPermission !== 'function' || window.hasPermission(permission); }
    async function api(path, options = {}) {
        const response = await window.apiFetch('/api/long-tasks' + path, { ...options, headers: { 'Content-Type': 'application/json', ...options.headers } });
        if (!response.ok) { const body = await response.json().catch(() => ({})); throw new Error(body.error || String(response.status)); }
        return response.json();
    }
    function error(message) { const box = el('lt-error'); if (box) { box.textContent = message || ''; box.hidden = !message; } }
    function switchTab(tab) {
        state.tab = tab;
        el('long-task-workspace').hidden = tab !== 'goals';
        el('task-batch-workspace').hidden = tab !== 'batch';
        el('task-new-goal').hidden = tab !== 'goals';
        el('task-new-batch').hidden = tab !== 'batch';
        document.querySelectorAll('[data-task-tab]').forEach(b => b.setAttribute('aria-selected', String(b.dataset.taskTab === tab)));
        if (tab === 'goals') refresh();
    }
    function renderList() {
        el('lt-list').innerHTML = state.tasks.length ? state.tasks.map(t => `<button class="lt-card ${state.selected === t.id ? 'is-selected' : ''}" data-lt-action="select" data-id="${esc(t.id)}" aria-pressed="${state.selected === t.id}"><span class="lt-card-title">${esc(t.name)}</span>${badge(t.status)}<span class="lt-card-summary">${esc(t.summary || tr('draftHint'))}</span><time>${esc(new Date(t.updated_at).toLocaleString())}</time></button>`).join('') : `<div class="lt-empty">${esc(tr('empty'))}</div>`;
        el('lt-page').textContent = String(state.offset / 30 + 1);
        el('lt-prev').disabled = state.offset === 0; el('lt-next').disabled = !state.more;
    }
    function renderTask() {
        const t = state.task;
        el('lt-empty-detail').hidden = !!t; el('lt-detail').hidden = !t;
        if (!t) return;
        el('lt-name').textContent = t.name;
        el('lt-state').innerHTML = badge(t.status);
        const done = t.steps.filter(s => s.status === 'completed').length;
        const total = t.steps.filter(s => s.status !== 'retired').length;
        el('lt-progress').textContent = tr('progress', { done, total, rounds: t.rounds, max: t.max_rounds, minutes: Math.ceil(t.remaining_seconds / 60) });
        el('lt-summary').textContent = t.summary || tr('draftHint');
        el('lt-strategy').textContent = t.strategy || '—';
        el('lt-objective').textContent = t.objective; el('lt-scope').textContent = t.scope; el('lt-criteria').textContent = t.criteria;
        el('lt-question').hidden = !t.question; el('lt-question-text').textContent = t.question || '';
        el('lt-guidance-label').textContent = tr(t.question ? 'answer' : 'guidance');
        const terminal = ['completed', 'cancelled'].includes(t.status);
        const actions = [];
        if (writable()) {
            if (!active(t) && !terminal && !t.question) actions.push(button('start', t.status === 'draft' ? 'start' : 'resume'));
            if (t.status === 'running') actions.push(button('pause', 'pause'));
            if (!terminal) actions.push(button('cancel', 'cancel', '', ['pausing', 'cancelling'].includes(t.status)));
            if (!active(t) && !terminal) actions.push(button('budget', 'budget'));
        }
        actions.push(button('report', 'report'));
        if (writable('tasks:delete') && !active(t)) actions.push(button('delete', 'delete'));
        el('lt-actions').innerHTML = actions.join('');
        el('lt-guidance-form').hidden = terminal || !writable();
        el('lt-guidance-send').textContent = tr(t.question ? 'answerResume' : 'sendGuidance');
        el('lt-guidance-send').disabled = ['pausing', 'cancelling'].includes(t.status);
        el('lt-steps').innerHTML = t.steps.length ? t.steps.map(s => `<article class="lt-step"><header><strong>${esc(s.id)} · ${esc(s.title)}</strong>${badge(s.status)}</header><p class="lt-dependencies">${esc(tr('depends'))}: ${(s.depends_on || []).length ? s.depends_on.map(esc).join(' → ') : esc(tr('independent'))}</p><details><summary>${esc(tr('instruction'))}</summary><p>${esc(s.instruction)}</p></details><p class="lt-step-result">${esc(s.summary || tr('noResult'))}</p><footer>${s.conversation_id ? `<a class="btn-secondary" href="#chat?conversation=${encodeURIComponent(s.conversation_id)}">${esc(tr('trace'))}</a>` : ''}${writable() && !active(t) && ['failed', 'blocked', 'interrupted', 'cleanup_unconfirmed'].includes(s.status) ? button('retry', 'retry', `data-step="${esc(s.id)}"`) : ''}<small>${esc(tr('attempts', { count: s.attempts || 0 }))}</small></footer></article>`).join('') : `<div class="lt-empty">${esc(tr('noPlan'))}</div>`;
        el('lt-guidance-history').textContent = (t.guidance || []).join('\n\n');
    }
    function renderEvents() {
        el('lt-events').innerHTML = state.events.slice(-200).reverse().map(e => `<li><time>${esc(new Date(e.created_at).toLocaleTimeString())}</time><span>${esc(e.step_id ? '[' + e.step_id + '] ' : '')}${esc(e.message || e.kind)}</span></li>`).join('') || `<li>${esc(tr('noEvents'))}</li>`;
    }
    async function refresh() {
        if (!state.active || state.tab !== 'goals' || state.loading) return;
        state.loading = true;
        const epoch = state.epoch, selected = state.selected;
        const controller = new AbortController(); state.controller = controller;
        try {
            const params = new URLSearchParams({ limit: 30, offset: state.offset, status: el('lt-filter').value, q: el('lt-search').value });
            const list = await api('?' + params, { signal: controller.signal });
            if (epoch !== state.epoch || !state.active) return;
            state.tasks = list.tasks; state.more = list.has_more; renderList();
            if (selected) {
                const [detail, log] = await Promise.all([api('/' + encodeURIComponent(selected), { signal: controller.signal }), api('/' + encodeURIComponent(selected) + '/events?after=' + state.cursor, { signal: controller.signal })]);
                if (epoch !== state.epoch || selected !== state.selected || !state.active) return;
                state.task = detail.task;
                state.events.push(...log.events); state.events = state.events.slice(-200);
                if (log.events.length) state.cursor = log.events[log.events.length - 1].id;
                renderTask(); renderEvents();
            }
            error('');
        } catch (e) { if (e.name !== 'AbortError' && epoch === state.epoch) error(e.message); }
        finally { if (state.controller === controller) { state.controller = null; state.loading = false; } }
    }
    function invalidate() { state.epoch++; if (state.controller) state.controller.abort(); state.controller = null; state.loading = false; }
    function select(id) {
        invalidate(); state.selected = id; state.task = null; state.events = []; state.cursor = 0;
        el('lt-guidance-input').value = ''; renderTask(); renderEvents(); renderList(); refresh();
    }
    async function control(action, extra = {}) {
        const task = state.task; if (!task) return;
        const data = await api('/' + encodeURIComponent(task.id) + '/control/' + action, { method: 'POST', body: JSON.stringify({ revision: task.revision, ...extra }) });
        if (state.selected === task.id) { state.task = data.task; renderTask(); }
        if (data.start_error) throw new Error(data.start_error);
        invalidate(); await refresh();
    }
    async function act(action, target) {
        try {
            if (action === 'select') return select(target.dataset.id);
            if (action === 'refresh') { invalidate(); return refresh(); }
            if (action === 'new') return openCreate();
            if (action === 'previous' || action === 'next') { state.offset = Math.max(0, state.offset + (action === 'next' ? 30 : -30)); invalidate(); return refresh(); }
            if (!state.task) return;
            if (action === 'delete') {
                if (!window.confirm(tr('deleteConfirm'))) return;
                await api('/' + encodeURIComponent(state.task.id), { method: 'DELETE' }); state.selected = null; state.task = null; renderTask(); invalidate(); return refresh();
            }
            if (action === 'cancel' && !window.confirm(tr('cancelConfirm'))) return;
            if (action === 'retry') {
                if (!window.confirm(tr('retryConfirm'))) return;
                return await control('retry', { step_id: target.dataset.step, cleanup_confirmed: true });
            }
            if (action === 'budget') return openBudget();
            if (action === 'report') {
                const response = await window.apiFetch('/api/long-tasks/' + encodeURIComponent(state.task.id) + '/report');
                if (!response.ok) throw new Error(String(response.status));
                const url = URL.createObjectURL(await response.blob()); const a = document.createElement('a'); a.href = url; a.download = 'task-' + state.task.id + '.md'; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); return;
            }
            await control(action);
        } catch (e) { error(e.message); if (state.active) { invalidate(); await refresh(); error(e.message); } }
    }
    function modal(title, body, submit) {
        el('lt-modal')?.remove();
        const dialog = document.createElement('dialog'); dialog.id = 'lt-modal'; dialog.className = 'lt-dialog';
        dialog.innerHTML = `<form><header><h3>${esc(title)}</h3><button type="button" data-close aria-label="${esc(tr('close'))}">×</button></header>${body}<p class="lt-form-error" role="alert"></p><footer><button type="button" class="btn-secondary" data-close>${esc(tr('close'))}</button><button type="submit" class="btn-primary">${esc(tr('save'))}</button></footer></form>`;
        document.body.append(dialog); dialog.querySelectorAll('[data-close]').forEach(b => b.onclick = () => dialog.close());
        dialog.addEventListener('close', () => dialog.remove());
        dialog.querySelector('form').onsubmit = async event => { event.preventDefault(); const button = dialog.querySelector('[type=submit]'); button.disabled = true; try { await submit(new FormData(event.target)); dialog.close(); } catch (e) { dialog.querySelector('.lt-form-error').textContent = e.message; } finally { button.disabled = false; } };
        dialog.showModal();
    }
    const field = (label, html) => `<label>${esc(tr(label))}${html}</label>`;
    async function openCreate() {
        try {
            const options = await api('/options'); state.options = options;
            const selectOptions = (items, key = 'id', label = 'name') => `<option value="">${esc(tr('default'))}</option>` + items.map(item => `<option value="${esc(typeof item === 'string' ? item : item[key])}">${esc(typeof item === 'string' ? item : item[label] || item[key])}</option>`).join('');
            modal(tr('new'), `<p class="lt-muted">${esc(tr('createHint'))}</p>${field('name', '<input name="name" maxlength="200" required>')}${field('objective', '<textarea name="objective" rows="3" maxlength="16000" required></textarea>')}${field('scope', '<textarea name="scope" rows="2" maxlength="16000" required></textarea>')}${field('criteria', '<textarea name="criteria" rows="2" maxlength="8000" required></textarea>')}<div class="lt-form-grid">${field('project', `<select name="project_id">${selectOptions(options.projects)}</select>`)}${field('role', `<select name="role">${selectOptions(options.roles)}</select>`)}${field('model', `<select name="model">${selectOptions(options.models)}</select>`)}${field('hitl', `<select name="hitl_policy"><option value="">${esc(tr('default'))}</option><option value="human">${esc(tr('human'))}</option><option value="audit_agent">${esc(tr('audit'))}</option></select>`)}${field('concurrency', '<input name="concurrency" type="number" min="1" max="4" value="2" required>')}${field('rounds', '<input name="max_rounds" type="number" min="1" max="200" value="40" required>')}${field('minutes', '<input name="minutes" type="number" min="1" max="10080" value="360" required>')}</div>`, async form => {
                const body = Object.fromEntries(form); body.concurrency = Number(body.concurrency); body.max_rounds = Number(body.max_rounds); body.remaining_seconds = Number(body.minutes) * 60; delete body.minutes;
                const created = await api('', { method: 'POST', body: JSON.stringify(body) }); select(created.task.id);
            });
        } catch (e) { error(e.message); }
    }
    function openBudget() {
        const task = state.task;
        modal(tr('budget'), `<div class="lt-form-grid">${field('rounds', `<input type="number" name="max_rounds" min="${task.rounds + 1}" max="200" value="${Math.min(200, Math.max(task.max_rounds, task.rounds + 10))}" required>`)}${field('minutes', `<input type="number" name="minutes" min="1" max="10080" value="${Math.max(60, Math.ceil(task.remaining_seconds / 60))}" required>`)}</div>`, form => control('budget', { max_rounds: Number(form.get('max_rounds')), remaining_seconds: Number(form.get('minutes')) * 60 }));
    }
    function init() {
        state.active = true;
        const root = el('long-task-workspace');
        if (!root.dataset.initialized) {
            root.dataset.initialized = 'true';
            document.querySelectorAll('[data-task-tab]').forEach(b => b.addEventListener('click', () => switchTab(b.dataset.taskTab)));
            el('task-new-goal').addEventListener('click', openCreate);
            root.addEventListener('click', event => { const target = event.target.closest('[data-lt-action]'); if (target) act(target.dataset.ltAction, target); });
            const filter = () => { state.offset = 0; invalidate(); refresh(); };
            el('lt-filter').addEventListener('change', filter); el('lt-search').addEventListener('change', filter);
            el('lt-guidance-form').addEventListener('submit', async event => {
                event.preventDefault(); const input = el('lt-guidance-input'), value = input.value, selected = state.selected;
                el('lt-guidance-send').disabled = true;
                try { await control(state.task.question ? 'answer' : 'guidance', { message: value }); if (selected === state.selected && input.value === value) input.value = ''; }
                catch (e) { error(e.message); }
                finally { renderTask(); }
            });
        }
        switchTab(state.tab);
        if (!state.timer) state.timer = setInterval(() => { if (el('tasks-auto-refresh')?.checked) refresh(); }, 4000);
    }
    function cleanup() { state.active = false; invalidate(); clearInterval(state.timer); state.timer = null; el('lt-modal')?.close(); }
    window.initLongTasks = init; window.cleanupLongTasks = cleanup;
    window.refreshTaskWorkspace = () => state.tab === 'goals' ? (invalidate(), refresh()) : window.refreshBatchQueues();
})();
