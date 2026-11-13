/* Durable objectives in the existing task-management page. */
(function () {
    'use strict';
    const state = { active: false, tab: 'goals', tasks: [], selected: null, task: null, events: [], cursor: 0, offset: 0, more: false, epoch: 0, loading: false, timer: null, options: null, controller: null, optionsRequest: null, viewVersion: 0, searchTimer: null, mutating: new Set(), errorKind: null };
    const el = id => document.getElementById(id);
    const tr = (key, args) => typeof window.t === 'function' ? window.t('longTasks.' + key, args) : key;
    const esc = value => String(value == null ? '' : value).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
    const active = task => task && ['running', 'pausing', 'cancelling'].includes(task.status);
    const badge = status => `<span class="lt-status lt-status-${esc(status)}">${esc(tr('status.' + status))}</span>`;
    const mutationActions = new Set(['start', 'pause', 'cancel', 'retry', 'budget', 'delete']);
    const button = (action, label, extra = '', disabled = false) => `<button type="button" class="btn-secondary" data-lt-action="${action}" ${extra} ${disabled || (mutationActions.has(action) && state.task && state.mutating.has(state.task.id)) ? 'disabled' : ''}>${esc(tr(label))}</button>`;
    function writable(permission = 'tasks:write') { return typeof window.hasPermission !== 'function' || window.hasPermission(permission); }
    function canExecute() { return writable() && ['agent:execute', 'chat:read', 'chat:write'].every(permission => writable(permission)); }
    function current(task) { return state.active && state.selected === task.id; }
    async function api(path, options = {}) {
        const response = await window.apiFetch('/api/long-tasks' + path, { ...options, headers: { 'Content-Type': 'application/json', ...options.headers } });
        if (!response.ok) { const body = await response.json().catch(() => ({})); throw new Error(body.error || String(response.status)); }
        return response.json();
    }
    function error(message, kind = 'action') { state.errorKind = message ? kind : null; const box = el('lt-error'); if (box) { box.textContent = message || ''; box.hidden = !message; } }
    function switchTab(tab) {
        state.viewVersion++; cancelOptions();
        state.tab = tab;
        el('long-task-workspace').hidden = tab !== 'goals';
        el('task-batch-workspace').hidden = tab !== 'batch';
        // Page mode and RBAC own separate visibility rules. Permission refreshes
        // must not reveal the other mode's button, or tab switches reveal denied controls.
        el('page-tasks').dataset.taskMode = tab;
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
        if (!t) { el('lt-steps').replaceChildren(); el('lt-steps').dataset.taskId = ''; return; }
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
            if (canExecute() && !active(t) && !terminal && !t.question) {
                const needsBudget = t.rounds >= t.max_rounds || t.remaining_seconds <= 0;
                actions.push(button('start', t.status === 'draft' ? 'start' : 'resume', needsBudget ? `title="${esc(tr('extendFirst'))}"` : '', needsBudget));
            }
            if (t.status === 'running') actions.push(button('pause', 'pause'));
            if (!terminal) actions.push(button('cancel', 'cancel', '', ['pausing', 'cancelling'].includes(t.status)));
            if (!active(t) && !terminal) actions.push(button('budget', 'budget', t.rounds >= 200 ? `title="${esc(tr('roundLimit'))}"` : '', t.rounds >= 200));
        }
        actions.push(button('report', 'report'));
        if (writable('tasks:delete') && !active(t)) actions.push(button('delete', 'delete'));
        el('lt-actions').innerHTML = actions.join('');
        el('lt-guidance-form').hidden = terminal || !writable();
        el('lt-guidance-send').textContent = tr(t.question ? 'answerResume' : 'sendGuidance');
        el('lt-guidance-send').disabled = state.mutating.has(t.id) || ['pausing', 'cancelling'].includes(t.status) || (!!t.question && (active(t) || !canExecute()));
        const stepsRoot = el('lt-steps');
        const expanded = new Set(stepsRoot.dataset.taskId === t.id ? [...stepsRoot.querySelectorAll('[data-step-id] details[open]')].map(detail => detail.closest('[data-step-id]').dataset.stepId) : []);
        stepsRoot.dataset.taskId = t.id;
        el('lt-steps').innerHTML = t.steps.length ? t.steps.map(s => `<article class="lt-step" data-step-id="${esc(s.id)}"><header><strong>${esc(s.id)} · ${esc(s.title)}</strong>${badge(s.status)}</header><p class="lt-dependencies">${esc(tr('depends'))}: ${(s.depends_on || []).length ? s.depends_on.map(esc).join(' → ') : esc(tr('independent'))}</p><details><summary>${esc(tr('instruction'))}</summary><p>${esc(s.instruction)}</p></details><p class="lt-step-result">${esc(s.summary || tr('noResult'))}</p><footer>${s.conversation_id ? `<a class="btn-secondary" href="#chat?conversation=${encodeURIComponent(s.conversation_id)}">${esc(tr('trace'))}</a>` : ''}${writable() && !terminal && !active(t) && ['failed', 'blocked', 'interrupted', 'cleanup_unconfirmed'].includes(s.status) ? button('retry', 'retry', `data-step="${esc(s.id)}"`) : ''}<small>${esc(tr('attempts', { count: s.attempts || 0 }))}</small></footer></article>`).join('') : `<div class="lt-empty">${esc(tr('noPlan'))}</div>`;
        stepsRoot.querySelectorAll('[data-step-id] details').forEach(detail => { detail.open = expanded.has(detail.closest('[data-step-id]').dataset.stepId); });
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
            if (state.errorKind === 'load') error('');
        } catch (e) { if (e.name !== 'AbortError' && epoch === state.epoch) error(e.message, 'load'); }
        finally { if (state.controller === controller) { state.controller = null; state.loading = false; } }
    }
    function invalidate() { state.epoch++; if (state.controller) state.controller.abort(); state.controller = null; state.loading = false; }
    function select(id) {
        error(''); invalidate(); state.selected = id; state.task = null; state.events = []; state.cursor = 0;
        el('lt-guidance-input').value = ''; renderTask(); renderEvents(); renderList(); refresh();
    }
    async function control(action, extra = {}, task = state.task) {
        if (!task || state.mutating.has(task.id)) return null;
        state.mutating.add(task.id); if (current(task)) { error(''); renderTask(); }
        try {
            const data = await api('/' + encodeURIComponent(task.id) + '/control/' + action, { method: 'POST', body: JSON.stringify({ revision: task.revision, ...extra }) });
            if (current(task)) { state.task = data.task; renderTask(); invalidate(); await refresh(); }
            return data;
        } catch (e) {
            if (current(task)) { invalidate(); await refresh(); }
            throw e;
        } finally { state.mutating.delete(task.id); if (current(task)) renderTask(); }
    }
    async function act(action, target) {
        const task = state.task;
        try {
            if (action === 'select') return select(target.dataset.id);
            if (action === 'refresh') { invalidate(); return refresh(); }
            if (action === 'new') return openCreate();
            if (action === 'previous' || action === 'next') { state.offset = Math.max(0, state.offset + (action === 'next' ? 30 : -30)); invalidate(); return refresh(); }
            if (!task || (mutationActions.has(action) && state.mutating.has(task.id))) return;
            if (action === 'delete') {
                if (!window.confirm(tr('deleteConfirm'))) return;
                error(''); state.mutating.add(task.id); renderTask();
                try {
                    await api('/' + encodeURIComponent(task.id), { method: 'DELETE' });
                    if (current(task)) { state.selected = null; state.task = null; state.events = []; state.cursor = 0; renderTask(); renderEvents(); }
                    if (state.active) { invalidate(); await refresh(); }
                } finally { state.mutating.delete(task.id); if (current(task)) renderTask(); }
                return;
            }
            if (action === 'cancel' && !window.confirm(tr('cancelConfirm'))) return;
            if (action === 'retry') {
                if (!window.confirm(tr('retryConfirm'))) return;
                return await control('retry', { step_id: target.dataset.step, cleanup_confirmed: true });
            }
            if (action === 'budget') return openBudget();
            if (action === 'report') {
                const response = await window.apiFetch('/api/long-tasks/' + encodeURIComponent(task.id) + '/report');
                if (!response.ok) throw new Error(String(response.status));
                const url = URL.createObjectURL(await response.blob()); const a = document.createElement('a'); a.href = url; a.download = 'task-' + task.id + '.md'; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); return;
            }
            await control(action);
        } catch (e) { if (!task || current(task)) error(e.message); }
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
    function syncOptionsButton() {
        const button = el('task-new-goal'); if (!button) return;
        button.dataset.stateDisabled = String(!!state.optionsRequest);
        button.disabled = !!state.optionsRequest || !writable();
        button.setAttribute('aria-busy', String(!!state.optionsRequest));
    }
    function cancelOptions() {
        if (state.optionsRequest) state.optionsRequest.abort();
        state.optionsRequest = null; syncOptionsButton();
    }
    async function openCreate() {
        if (!state.active || state.tab !== 'goals' || state.optionsRequest || !writable()) return;
        const viewVersion = state.viewVersion;
        const request = new AbortController(); state.optionsRequest = request; syncOptionsButton(); error('');
        try {
            const options = await api('/options', { signal: request.signal });
            if (!state.active || state.tab !== 'goals' || state.optionsRequest !== request || state.viewVersion !== viewVersion) return;
            state.options = options;
            const selectOptions = (items, key = 'id', label = 'name') => `<option value="">${esc(tr('default'))}</option>` + items.map(item => `<option value="${esc(typeof item === 'string' ? item : item[key])}">${esc(typeof item === 'string' ? item : item[label] || item[key])}</option>`).join('');
            modal(tr('new'), `<p class="lt-muted">${esc(tr('createHint'))}</p>${field('name', '<input name="name" maxlength="200" required>')}${field('objective', '<textarea name="objective" rows="3" maxlength="16000" required></textarea>')}${field('scope', '<textarea name="scope" rows="2" maxlength="16000" required></textarea>')}${field('criteria', '<textarea name="criteria" rows="2" maxlength="8000" required></textarea>')}<div class="lt-form-grid">${field('project', `<select name="project_id">${selectOptions(options.projects)}</select>`)}${field('role', `<select name="role">${selectOptions(options.roles)}</select>`)}${field('model', `<select name="model">${selectOptions(options.models)}</select>`)}${field('hitl', `<select name="hitl_policy"><option value="">${esc(tr('default'))}</option><option value="human">${esc(tr('human'))}</option><option value="audit_agent">${esc(tr('audit'))}</option></select>`)}${field('concurrency', '<input name="concurrency" type="number" min="1" max="4" value="2" required>')}${field('rounds', '<input name="max_rounds" type="number" min="1" max="200" value="40" required>')}${field('minutes', '<input name="minutes" type="number" min="1" max="10080" value="360" required>')}</div>`, async form => {
                const body = Object.fromEntries(form); body.concurrency = Number(body.concurrency); body.max_rounds = Number(body.max_rounds); body.remaining_seconds = Number(body.minutes) * 60; delete body.minutes;
                const created = await api('', { method: 'POST', body: JSON.stringify(body) }); if (state.active && state.tab === 'goals' && viewVersion === state.viewVersion) select(created.task.id);
            });
        } catch (e) { if (e.name !== 'AbortError' && state.active && viewVersion === state.viewVersion) error(e.message); }
        finally { if (state.optionsRequest === request) { state.optionsRequest = null; syncOptionsButton(); } }
    }
    function openBudget() {
        const task = state.task;
        modal(tr('budget'), `<div class="lt-form-grid">${field('rounds', `<input type="number" name="max_rounds" min="${task.rounds + 1}" max="200" value="${Math.min(200, Math.max(task.max_rounds, task.rounds + 10))}" required>`)}${field('minutes', `<input type="number" name="minutes" min="1" max="10080" value="${Math.max(60, Math.ceil(task.remaining_seconds / 60))}" required>`)}</div>`, form => control('budget', { max_rounds: Number(form.get('max_rounds')), remaining_seconds: Number(form.get('minutes')) * 60 }, task));
    }
    function init() {
        state.active = true;
        const root = el('long-task-workspace');
        if (!root.dataset.initialized) {
            root.dataset.initialized = 'true';
            document.querySelectorAll('[data-task-tab]').forEach(b => b.addEventListener('click', () => switchTab(b.dataset.taskTab)));
            el('task-new-goal').addEventListener('click', openCreate);
            root.addEventListener('click', event => { const target = event.target.closest('[data-lt-action]'); if (target) act(target.dataset.ltAction, target); });
            const filter = () => { clearTimeout(state.searchTimer); state.offset = 0; invalidate(); refresh(); };
            el('lt-filter').addEventListener('change', filter); el('lt-search').addEventListener('change', filter);
            el('lt-search').addEventListener('search', filter);
            el('lt-search').addEventListener('input', () => { clearTimeout(state.searchTimer); state.searchTimer = setTimeout(filter, 250); });
            el('lt-guidance-form').addEventListener('submit', async event => {
                event.preventDefault(); const input = el('lt-guidance-input'), value = input.value, task = state.task;
                if (!task || state.mutating.has(task.id) || el('lt-guidance-send').disabled) return;
                try {
                    const data = await control(task.question ? 'answer' : 'guidance', { message: value }, task);
                    if (data && current(task)) {
                        if (input.value === value) input.value = '';
                        if (data.start_error) error(tr('answerSaved') + ' ' + data.start_error);
                    }
                } catch (e) { if (current(task)) error(e.message); }
            });
        }
        switchTab(state.tab);
        if (!state.timer) state.timer = setInterval(() => { if (el('tasks-auto-refresh')?.checked) refresh(); }, 4000);
    }
    function cleanup() { state.active = false; state.viewVersion++; cancelOptions(); clearTimeout(state.searchTimer); invalidate(); clearInterval(state.timer); state.timer = null; el('lt-modal')?.close(); }
    window.initLongTasks = init; window.cleanupLongTasks = cleanup;
    window.refreshTaskWorkspace = () => state.tab === 'goals' ? (invalidate(), refresh()) : window.refreshBatchQueues();
})();
