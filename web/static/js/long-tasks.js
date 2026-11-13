/* Durable objectives in the existing task-management page. */
(function () {
    'use strict';
    const state = { active: false, tasks: [], selected: null, selectedMode: 'goal', task: null, events: [], cursor: 0, offset: 0, total: 0, stepPages: new Map(), more: false, epoch: 0, loading: false, timer: null, options: null, controller: null, optionsRequest: null, viewVersion: 0, searchTimer: null, mutating: new Set(), errorKind: null };
    const V = window.TaskListView;
    const el = id => document.getElementById(id);
    const tr = (key, args) => typeof window.t === 'function' ? window.t('longTasks.' + key, args) : key;
    const esc = value => String(value == null ? '' : value).replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
    const active = task => task && ['running', 'pausing', 'cancelling'].includes(task.status);
    const badge = status => `<span class="lt-status lt-status-${esc(status)}">${esc(tr('status.' + status))}</span>`;
    const mutationActions = new Set(['start', 'pause', 'cancel', 'retry', 'budget', 'delete']);
    const button = (action, label, extra = '', disabled = false) => `<button type="button" class="btn-secondary" data-lt-action="${action}" ${extra} ${disabled || (mutationActions.has(action) && state.task && state.mutating.has(state.task.id)) ? 'disabled' : ''}>${esc(tr(label))}</button>`;
    function writable(permission = 'tasks:write') { return typeof window.hasPermission !== 'function' || window.hasPermission(permission); }
    function canExecute() { return writable() && ['agent:execute', 'chat:read', 'chat:write'].every(permission => writable(permission)); }
    function current(task) { return state.active && state.selectedMode === 'goal' && state.selected === task.id; }
    async function api(path, options = {}) {
        const response = await window.apiFetch(path.startsWith('/api/') ? path : '/api/long-tasks' + path, { ...options, headers: { 'Content-Type': 'application/json', ...options.headers } });
        if (!response.ok) { const body = await response.json().catch(() => ({})); throw new Error(body.error || String(response.status)); }
        return response.json();
    }
    function error(message, kind = 'action') { state.errorKind = message ? kind : null; const box = el('lt-error'); if (box) { box.textContent = message || ''; box.hidden = !message; } }
    function renderList() {
        V.renderRows(el('lt-list'), state.tasks.map(task => {
            const mode = task.mode || 'goal';
            const progress = Number.isInteger(task.total_steps) ? tr('listProgress', { done: task.completed_steps, total: task.total_steps }) : '';
            const schedule = task.schedule_mode === 'cron' ? ' · ' + tr('scheduledLabel') : '';
            return {id:mode+':'+task.id,title:task.name,status:task.status,summary:tr(mode==='list'?'modeList':'modeGoal')+schedule+(progress?' · '+progress:''),selected:state.selected===task.id&&state.selectedMode===mode};
        }), {navigation:true,key:'parents:'+state.offset,open:key=>{const i=key.indexOf(':');select(key.slice(i+1),key.slice(0,i));}});
        V.renderPager(el('lt-pagination'),{page:state.offset/V.pageSize+1,total:state.total,size:V.pageSize,change:page=>{state.offset=(page-1)*V.pageSize;invalidate();refresh();}});
        const empty = !state.tasks.length && !state.selected;
        el('lt-board').hidden = empty; el('lt-empty-state').hidden = !empty;
        const filtered = el('lt-search').value || el('lt-filter').value || el('lt-mode-filter').value;
        el('lt-empty-title').textContent = tr(filtered ? 'noMatches' : 'emptyTitle');
        el('lt-empty-description').textContent = tr(filtered ? 'changeFilters' : 'empty');
        if (typeof window.applyRBACToUI === 'function') window.applyRBACToUI(el('lt-empty-state'));
    }
    function renderTask(resetSteps = false) {
        const restore = V.captureScroll(el('lt-detail'));
        const t = state.selectedMode === 'goal' ? state.task : null;
        el('lt-empty-detail').hidden = !!state.selected; el('lt-detail').hidden = !t;
        el('lt-batch-detail').hidden = state.selectedMode !== 'list' || !state.selected;
        if (!t) { el('lt-steps').replaceChildren(); el('lt-steps').dataset.taskId = ''; return; }
        if (!Array.isArray(t.steps)) t.steps = [];
        el('lt-name').textContent = t.name; el('lt-name').title = t.name;
        V.setHTML(el('lt-state'), badge(t.status));
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
        V.setHTML(el('lt-actions'), actions.join(''));
        el('lt-guidance-form').hidden = terminal || !writable();
        el('lt-guidance-send').textContent = tr(t.question ? 'answerResume' : 'sendGuidance');
        el('lt-guidance-send').disabled = state.mutating.has(t.id) || ['pausing', 'cancelling'].includes(t.status) || (!!t.question && (active(t) || !canExecute()));
        if (t.question && el('lt-support').dataset.question !== t.question) el('lt-support').open = true;
        el('lt-support').dataset.question = t.question || '';
        const page=Math.min(state.stepPages.get(t.id)||1,Math.max(1,Math.ceil(t.steps.length/V.pageSize)));
        state.stepPages.set(t.id,page);
        const offset=(page-1)*V.pageSize;
        V.renderRows(el('lt-steps'),t.steps.slice(offset,offset+V.pageSize).map((step,index)=>({id:step.id,index:offset+index+1,title:step.title,status:step.status,summary:step.summary||step.instruction})),{key:t.id+':'+page,empty:tr('noPlan'),open:id=>openStep(t,id)});
        V.renderPager(el('lt-step-pagination'),{page,total:t.steps.length,size:V.pageSize,change:next=>{state.stepPages.set(t.id,next);renderTask(true);}});
        el('lt-guidance-history').textContent = (t.guidance || []).join('\n\n');
        restore(); if(resetSteps)el('lt-steps').scrollTop=0;
    }
    function openStep(task,id) {
        const step=task.steps.find(item=>item.id===id);if(!step)return;
        const terminal=['completed','cancelled'].includes(task.status);
        const retry=writable()&&!terminal&&!active(task)&&['failed','blocked','interrupted','cleanup_unconfirmed'].includes(step.status);
        const dialog=V.openDrawer(tr('stepDetail'),`${badge(step.status)}<h4>${esc(tr('taskContent'))}</h4><p>${esc(step.title)}</p><h4>${esc(tr('instruction'))}</h4><p>${esc(step.instruction)}</p><h4>${esc(tr('depends'))}</h4><p>${esc((step.depends_on||[]).join(', ')||tr('independent'))}</p><h4>${esc(tr('result'))}</h4><pre>${esc(step.summary||tr('noResult'))}</pre><p class="lt-form-error" data-step-error hidden></p>`,`${step.conversation_id?`<a class="btn-secondary" href="#chat?conversation=${encodeURIComponent(step.conversation_id)}">${esc(tr('trace'))}</a>`:''}<button type="button" class="btn-secondary" data-step-refresh>${esc(tr('refreshDetail'))}</button>${retry?`<button type="button" class="btn-primary" data-step-retry>${esc(tr('retry'))}</button>`:''}`);
        dialog.querySelector('[data-step-refresh]').addEventListener('click',async event=>{
            event.target.disabled=true;
            try {
                const data=await api('/'+encodeURIComponent(task.id));
                if(!dialog.isConnected||!dialog.open||!current(task))return;
                const scroll=dialog.querySelector('.task-inspector-body').scrollTop;
                state.task=data.task;renderTask();openStep(data.task,id);
                document.querySelector('#task-step-inspector .task-inspector-body')?.scrollTo(0,scroll);
            }catch(error){if(dialog.isConnected){const box=dialog.querySelector('[data-step-error]');box.hidden=false;box.textContent=error.message;}}
            finally{if(event.target.isConnected)event.target.disabled=false;}
        });
        dialog.querySelector('[data-step-retry]')?.addEventListener('click',async event=>{
            if(!confirm(tr('retryConfirm')))return;event.target.disabled=true;
            try { const data=await control('retry',{step_id:id,cleanup_confirmed:true},task);if(data&&dialog.isConnected)V.closeDrawer(); }
            catch(error){if(dialog.isConnected){const box=dialog.querySelector('[data-step-error]');box.hidden=false;box.textContent=error.message;}}
            finally{if(event.target.isConnected)event.target.disabled=false;}
        });
    }
    function renderEvents() {
        V.setHTML(el('lt-events'), state.events.slice(-200).reverse().map(e => `<li><time>${esc(new Date(e.created_at).toLocaleTimeString())}</time><span>${esc(e.step_id ? '[' + e.step_id + '] ' : '')}${esc(e.message || e.kind)}</span></li>`).join('') || `<li>${esc(tr('noEvents'))}</li>`);
    }
    async function refresh() {
        if (!state.active || state.loading) return;
        state.loading = true;
        const epoch = state.epoch, selected = state.selected, mode = state.selectedMode;
        const controller = new AbortController(); state.controller = controller;
        try {
            const params = new URLSearchParams({ limit: V.pageSize, offset: state.offset, mode: el('lt-mode-filter').value, status: el('lt-filter').value, q: el('lt-search').value });
            const list = await api('/api/tasks?' + params, { signal: controller.signal });
            if (epoch !== state.epoch || !state.active) return;
            state.tasks = list.tasks; state.more = list.has_more; state.total = list.total ?? list.tasks.length; state.offset = list.offset ?? state.offset; renderList();
            if (selected && mode === 'list') {
                if (typeof window.showBatchQueueDetail === 'function') await window.showBatchQueueDetail(selected, { inline: true });
            } else if (selected) {
                const [detail, log] = await Promise.all([api('/' + encodeURIComponent(selected), { signal: controller.signal }), api('/' + encodeURIComponent(selected) + '/events?after=' + state.cursor, { signal: controller.signal })]);
                if (epoch !== state.epoch || selected !== state.selected || mode !== state.selectedMode || !state.active) return;
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
    function select(id, mode = 'goal') {
        V.closeDrawer(); error(''); invalidate(); if (typeof window.closeBatchQueueDetailModal === 'function') window.closeBatchQueueDetailModal();
        state.selectedMode = mode; state.selected = id; state.task = null; state.events = []; state.cursor = 0;
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
            if (action === 'select') return select(target.dataset.id, target.dataset.mode || 'goal');
            if (action === 'refresh') { invalidate(); return refresh(); }
            if (action === 'new') return openCreate();
            if (action === 'guidance-panel') { el('lt-support').open=true;el('lt-guidance-input').focus({preventScroll:true});return; }
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
    function modal(title, body, submit, options = {}) {
        el('lt-modal')?.remove();
        const dialog = document.createElement('dialog');
        dialog.id = 'lt-modal';
        dialog.className = 'lt-dialog' + (options.wide ? ' lt-dialog--wide' : '');
        dialog.setAttribute('aria-labelledby', 'lt-dialog-title');
        dialog.setAttribute('aria-describedby', 'lt-dialog-description');
        dialog.innerHTML = `<form class="lt-dialog-form">
            <div class="lt-dialog-header">
                <div class="lt-dialog-heading">
                    <span class="lt-dialog-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"><rect x="4" y="4" width="16" height="16" rx="4"/><path d="M8 9h8M8 13h5M8 17h3"/></svg></span>
                    <div><h3 id="lt-dialog-title">${esc(title)}</h3><p id="lt-dialog-description">${esc(options.description || tr('budgetHint'))}</p></div>
                </div>
                <button type="button" class="lt-dialog-close" data-close aria-label="${esc(tr('close'))}"><svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="m6 6 12 12M18 6 6 18"/></svg></button>
            </div>
            <div class="lt-dialog-body">${body}</div>
            <div class="lt-dialog-footer">
                <p class="lt-form-error" role="alert" hidden></p>
                <div class="lt-dialog-footer-row"><p class="lt-dialog-note">${esc(options.note || tr('budgetNote'))}</p><div class="lt-dialog-buttons">
                    <button type="button" class="btn-secondary" data-close>${esc(tr('cancelEdit'))}</button>
                    <button type="submit" class="btn-primary">${esc(options.submitLabel || tr('save'))}</button>
                </div></div>
            </div>
        </form>`;
        document.body.append(dialog);
        dialog.querySelectorAll('[data-close]').forEach(button => button.onclick = () => dialog.close());
        dialog.addEventListener('close', () => dialog.remove());
        dialog.querySelector('form').onsubmit = async event => {
            event.preventDefault();
            const button = dialog.querySelector('[type=submit]');
            const message = dialog.querySelector('.lt-form-error');
            button.disabled = true; message.hidden = true;
            try { await submit(new FormData(event.target)); dialog.close(); }
            catch (error) { message.textContent = error.message; message.hidden = false; }
            finally { button.disabled = false; }
        };
        dialog.showModal();
        return dialog;
    }
    const field = (label, html, required = false) => `<label class="lt-form-field"><span class="lt-field-label">${esc(tr(label))}${required ? '<span class="lt-required" aria-hidden="true">*</span>' : ''}</span>${html}</label>`;
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
        if (!state.active || state.optionsRequest || !writable()) return;
        const viewVersion = state.viewVersion;
        const request = new AbortController(); state.optionsRequest = request; syncOptionsButton(); error('');
        try {
            const options = await api('/options', { signal: request.signal });
            if (!state.active || state.optionsRequest !== request || state.viewVersion !== viewVersion) return;
            state.options = options;
            const selectOptions = (items, key = 'id', label = 'name', placeholder = 'default') => `<option value="">${esc(tr(placeholder))}</option>` + items.map(item => `<option value="${esc(typeof item === 'string' ? item : item[key])}">${esc(typeof item === 'string' ? item : item[label] || item[key])}</option>`).join('');
            const content = `<fieldset class="lt-mode-picker"><legend>${esc(tr('executionMode'))}</legend>
                <label class="lt-mode-option"><input type="radio" name="execution_mode" value="goal" checked><span><strong>${esc(tr('modeGoal'))}</strong><small>${esc(tr('modeGoalHint'))}</small></span></label>
                <label class="lt-mode-option"><input type="radio" name="execution_mode" value="list"><span><strong>${esc(tr('modeList'))}</strong><small>${esc(tr('modeListHint'))}</small></span></label>
            </fieldset><div class="lt-create-layout">
                <section class="lt-create-main" aria-labelledby="lt-content-title">
                    <h4 class="lt-section-title" id="lt-content-title">${esc(tr('taskContent'))}</h4>
                    ${field('name', `<input name="name" maxlength="200" placeholder="${esc(tr('namePlaceholder'))}" required autofocus>`, true)}
                    <div data-create-mode="goal">
                        ${field('objective', `<textarea name="objective" rows="5" maxlength="16000" placeholder="${esc(tr('objectivePlaceholder'))}" required></textarea>`, true)}
                        <div class="lt-content-pair">
                            ${field('scope', `<textarea name="scope" rows="4" maxlength="16000" placeholder="${esc(tr('scopePlaceholder'))}" required></textarea>`, true)}
                            ${field('criteria', `<textarea name="criteria" rows="4" maxlength="8000" placeholder="${esc(tr('criteriaPlaceholder'))}" required></textarea>`, true)}
                        </div>
                    </div>
                    <div data-create-mode="list" hidden>
                        ${field('checklist', `<textarea name="tasks_text" rows="13" maxlength="1000000" placeholder="${esc(tr('checklistPlaceholder'))}" required disabled></textarea>`, true)}
                        <p class="lt-input-hint">${esc(tr('checklistHint'))}</p>
                    </div>
                </section>
                <aside class="lt-create-settings" aria-labelledby="lt-execution-title">
                    <h4 class="lt-section-title" id="lt-execution-title">${esc(tr('executionSettings'))}</h4>
                    <div class="lt-settings-fields">
                        ${field('project', `<select name="project_id">${selectOptions(options.projects, 'id', 'name', 'noProject')}</select>`)}
                        ${field('role', `<select name="role">${selectOptions(options.roles, 'id', 'name', 'defaultRole')}</select>`)}
                        <div data-create-mode="goal">${field('model', `<select name="model">${selectOptions(options.models, 'id', 'name', 'defaultModel')}</select>`)}</div>
                        <div data-create-mode="list" hidden>${field('agentMode', `<select name="agent_mode" disabled><option value="single">Pi</option><option value="deep">Deep</option><option value="plan_execute">Plan-Execute</option><option value="supervisor">Supervisor</option></select>`)}</div>
                        ${field('hitl', `<select name="hitl_policy"><option value="">${esc(tr('default'))}</option><option value="human">${esc(tr('human'))}</option><option value="audit_agent">${esc(tr('audit'))}</option><option value="review_edit">${esc(tr('reviewEdit'))}</option></select>`)}
                    </div>
                    <div class="lt-budget-section">
                        <h4 class="lt-section-title">${esc(tr('executionOptions'))}</h4>
                        ${field('concurrency', '<input name="concurrency" type="number" min="1" max="4" value="2" required>')}
                        <div class="lt-form-grid" data-create-mode="goal">
                            ${field('rounds', '<input name="max_rounds" type="number" min="1" max="200" value="40" required>')}
                            ${field('minutes', '<input name="minutes" type="number" min="1" max="10080" value="360" required>')}
                        </div>
                        <div data-create-mode="list" hidden>
                            ${field('schedule', `<select name="schedule_mode" disabled><option value="manual">${esc(tr('manualSchedule'))}</option><option value="cron">${esc(tr('cronSchedule'))}</option></select>`)}
                            <div class="lt-cron-field" hidden>${field('cron', '<input name="cron_expr" placeholder="0 2 * * *" disabled>')}<p class="lt-input-hint">${esc(tr('cronHint'))}</p></div>
                        </div>
                    </div>
                </aside>
            </div>`;
            const dialog = modal(tr('new'), content, async form => {
                const body = Object.fromEntries(form);
                let id, mode = body.execution_mode;
                if (mode === 'list') {
                    const created = await api('/api/batch-tasks', { method: 'POST', body: JSON.stringify({
                        title: body.name, tasks: body.tasks_text.split('\n').map(line => line.trim()).filter(Boolean),
                        projectId: body.project_id, role: body.role, agentMode: body.agent_mode,
                        hitlPolicy: body.hitl_policy, concurrency: Number(body.concurrency),
                        scheduleMode: body.schedule_mode, cronExpr: body.cron_expr || '', executeNow: false
                    }) }); id = created.queueId;
                } else {
                    const created = await api('', { method: 'POST', body: JSON.stringify({
                        name: body.name, objective: body.objective, scope: body.scope, criteria: body.criteria,
                        project_id: body.project_id, role: body.role, model: body.model, hitl_policy: body.hitl_policy,
                        concurrency: Number(body.concurrency), max_rounds: Number(body.max_rounds), remaining_seconds: Number(body.minutes) * 60
                    }) }); id = created.task.id;
                }
                if (state.active && viewVersion === state.viewVersion) {
                    state.offset = 0; el('lt-filter').value = ''; el('lt-mode-filter').value = ''; el('lt-search').value = '';
                    select(id, mode);
                }
            }, { wide: true, description: tr('createDescription'), note: tr('draftNote'), submitLabel: tr('saveDraft') });
            const syncMode = () => {
                const mode = dialog.querySelector('[name=execution_mode]:checked').value;
                dialog.querySelectorAll('[data-create-mode]').forEach(group => {
                    const enabled = group.dataset.createMode === mode;
                    group.hidden = !enabled; group.querySelectorAll('input,select,textarea').forEach(input => { input.disabled = !enabled; });
                });
                const concurrency = dialog.querySelector('[name=concurrency]'); concurrency.max = mode === 'list' ? '8' : '4';
                if (Number(concurrency.value) > Number(concurrency.max)) concurrency.value = concurrency.max;
                const scheduled = mode === 'list' && dialog.querySelector('[name=schedule_mode]').value === 'cron';
                dialog.querySelector('.lt-cron-field').hidden = !scheduled;
                const cron = dialog.querySelector('[name=cron_expr]'); cron.disabled = !scheduled; cron.required = scheduled;
                dialog.querySelector('.lt-dialog-note').textContent = tr(mode === 'goal' ? 'draftNote' : scheduled ? 'scheduleNote' : 'checklistNote');
                dialog.querySelector('[type=submit]').textContent = tr(mode === 'goal' ? 'saveDraft' : 'createTask');
            };
            dialog.querySelectorAll('[name=execution_mode],[name=schedule_mode]').forEach(input => input.addEventListener('change', syncMode));
            syncMode();
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
            el('task-new-goal').addEventListener('click', openCreate);
            root.addEventListener('click', event => { const target = event.target.closest('[data-lt-action]'); if (target) act(target.dataset.ltAction, target); });
            const filter = () => { clearTimeout(state.searchTimer); state.offset = 0; invalidate(); refresh(); };
            el('lt-mode-filter').addEventListener('change', filter); el('lt-filter').addEventListener('change', filter); el('lt-search').addEventListener('change', filter);
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
        state.viewVersion++; cancelOptions(); renderTask(); refresh();
        if (!state.timer) state.timer = setInterval(() => { if (el('tasks-auto-refresh')?.checked) refresh(); }, 4000);
    }
    function cleanup() { state.active = false; state.viewVersion++; cancelOptions(); clearTimeout(state.searchTimer); invalidate(); clearInterval(state.timer); state.timer = null; el('lt-modal')?.close(); }
    window.initLongTasks = init; window.cleanupLongTasks = cleanup;
    window.refreshTaskWorkspace = () => { invalidate(); return refresh(); };
    window.openTaskFromQueue = id => {
        if (!el('page-tasks').classList.contains('active') && typeof window.switchPage === 'function') window.switchPage('tasks');
        select(id, 'list');
    };
    window.onTaskQueueDeleted = id => {
        if (state.selectedMode === 'list' && state.selected === id) { state.selected = null; state.task = null; renderTask(); }
    };
})();
