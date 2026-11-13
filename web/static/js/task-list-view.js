/* Shared row, pagination and scroll behavior for both sides of task management. */
(function () {
    'use strict';
    const states = new WeakMap();
    const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({ '&':'&amp;', '<':'&lt;', '>':'&gt;', '"':'&quot;', "'":'&#39;' }[c]));
    const text = value => String(value || '').replace(/\s+/g, ' ').trim();
    const tr = (key, args) => typeof window.t === 'function' ? window.t('longTasks.' + key, { ...args, interpolation: { escapeValue:false } }) : key;
    function captureScroll(root) {
        const nodes = new Set(root ? [root, ...root.querySelectorAll('[data-task-scroll]')] : []);
        for (let node = root?.parentElement; node; node = node.parentElement) nodes.add(node);
        const saved = [...nodes].map(node => [node, node.scrollTop, node.scrollLeft]);
        return () => saved.forEach(([node, top, left]) => { if (node.isConnected) { node.scrollTop = top; node.scrollLeft = left; } });
    }
    function setHTML(node, html, force = false) {
        const state = states.get(node) || {};
        if (!force && state.html === html) return false;
        node.innerHTML = html; state.html = html; states.set(node, state); return true;
    }
    function renderRows(root, records, options = {}) {
        let state = states.get(root);
        if (!state) {
            state = {}; states.set(root, state);
            root.addEventListener('click', event => {
                const button = event.target.closest('[data-record-open]');
                if (button && root.contains(button)) state.open?.(button.dataset.recordOpen);
            });
        }
        state.open = options.open;
        const samePage = state.key === options.key;
        const top = root.scrollTop, bounds = root.getBoundingClientRect();
        const oldRows = new Map([...root.children].map(node => [node.dataset.recordId, node]));
        const anchor = samePage ? [...root.children].find(node => node.getBoundingClientRect().bottom > bounds.top) : null;
        const anchorOffset = anchor ? anchor.getBoundingClientRect().top - bounds.top : 0;
        const keep = new Set();
        records.forEach((record, index) => {
            const id = String(record.id); keep.add(id);
            const html = `<span class="task-row-index" ${record.index == null ? 'hidden' : ''}>${esc(record.index == null ? '' : '#' + record.index)}</span>
                <span class="lt-status lt-status-${esc(record.status)}" title="${esc(record.statusLabel || tr('status.' + record.status))}">${esc(record.statusLabel || tr('status.' + record.status))}</span>
                <button type="button" class="task-record-main" data-record-open="${esc(id)}" title="${esc(text(record.title).slice(0,1200))}">
                    <span class="task-record-title">${esc(text(record.title))}</span>
                    <span class="task-record-summary">${esc(text(record.summary).slice(0,240))}</span>
                </button>${options.navigation ? '' : `<button type="button" class="task-record-view" data-record-open="${esc(id)}">${esc(tr('viewDetail'))}</button>`}`;
            let row = oldRows.get(id);
            if (!row) { row = document.createElement('article'); row.dataset.recordId = id; }
            row.className = 'task-record-row' + (options.navigation ? ' task-record-row--nav' : '') + (record.selected ? ' is-selected' : '');
            row.setAttribute('aria-current', record.selected ? 'true' : 'false');
            if (states.get(row)?.html !== html) {
                const focused = row.contains(document.activeElement);
                setHTML(row, html);
                if (focused) row.querySelector('button')?.focus({preventScroll:true});
            }
            const at = root.children[index];
            if (at !== row) root.insertBefore(row, at || null);
        });
        oldRows.forEach((node, id) => { if (!keep.has(id)) node.remove(); });
        if (!records.length && !root.children.length) {
            const empty = document.createElement('div'); empty.className='lt-empty'; empty.textContent=options.empty || tr('noMatches'); root.append(empty);
        }
        if (samePage) {
            const retained = anchor && [...root.children].find(node => node.dataset.recordId === anchor.dataset.recordId);
            root.scrollTop = retained ? root.scrollTop + retained.getBoundingClientRect().top - root.getBoundingClientRect().top - anchorOffset : top;
        } else root.scrollTop = 0;
        state.key = options.key;
    }
    function renderPager(root, {page=1, total=0, size=20, change}) {
        const pages = Math.max(1, Math.ceil(total / size));
        let state = states.get(root);
        if (!state) {
            state = {}; states.set(root,state);
            root.addEventListener('click', event => { const button=event.target.closest('[data-task-page]'); if (button && !button.disabled) state.change?.(Number(button.dataset.taskPage)); });
        }
        state.change = change;
        const nav = (target,label,disabled) => `<button type="button" class="btn-secondary" data-task-page="${target}" data-page-nav="${label}" aria-label="${esc(tr(label))}" title="${esc(tr(label))}" ${disabled?'disabled':''}>${esc(tr(label))}</button>`;
        setHTML(root, `<span class="task-page-summary">${esc(tr('pageSummary',{start:total?(page-1)*size+1:0,end:Math.min(page*size,total),total}))} · ${esc(tr('perPage',{count:size}))}</span><div class="task-page-controls">
            ${nav(1,'firstPage',page<=1)}${nav(page-1,'previous',page<=1)}<span>${esc(tr('pageNumber',{page,pages}))}</span>${nav(page+1,'next',page>=pages)}${nav(pages,'lastPage',page>=pages)}</div>`);
    }
    function closeDrawer() {
        const dialog=document.getElementById('task-step-inspector');
        if (!dialog) return;
        const restore=captureScroll(document.getElementById('long-task-workspace'));
        dialog.close(); restore();
    }
    function openDrawer(title, body, actions = '') {
        closeDrawer(); document.getElementById('task-step-inspector')?.remove();
        const dialog=document.createElement('dialog'); dialog.id='task-step-inspector'; dialog.className='task-inspector';
        dialog.setAttribute('aria-labelledby','task-inspector-title');
        dialog.innerHTML=`<div class="task-inspector-header"><h3 id="task-inspector-title">${esc(title)}</h3><button type="button" class="lt-dialog-close" data-inspector-close aria-label="${esc(tr('close'))}">×</button></div><div class="task-inspector-body" data-task-scroll>${body}</div><div class="task-inspector-actions">${actions}</div>`;
        document.body.append(dialog); dialog.querySelector('[data-inspector-close]').onclick=closeDrawer;
        dialog.addEventListener('close',()=>dialog.remove());
        dialog.addEventListener('click',event=>{if(event.target===dialog){const r=dialog.getBoundingClientRect();if(event.clientX<r.left||event.clientX>r.right||event.clientY<r.top||event.clientY>r.bottom)closeDrawer();}});
        dialog.showModal();return dialog;
    }
    window.TaskListView={pageSize:20,esc,text,tr,captureScroll,setHTML,renderRows,renderPager,openDrawer,closeDrawer};
})();
