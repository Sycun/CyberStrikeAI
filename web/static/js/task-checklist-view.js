/* Paged checklist rows use the same list and inspector as autonomous steps. */
(function () {
    'use strict';
    const pages=new Map(), editorResets=new Set();
    const V=window.TaskListView;
    const allowed=permission=>typeof window.hasPermission!=='function'||window.hasPermission(permission);
    async function request(url, options={}) {
        const response=await window.apiFetch(url,options);
        const data=await response.json().catch(()=>({}));
        if(!response.ok)throw new Error(data.error||String(response.status));
        return data;
    }
    function render(queue,pagination) {
        const root=document.getElementById('batch-queue-detail-content');
        const restore=V.captureScroll(root);
        const size=V.pageSize;
        const page=pagination?.page||1,total=pagination?.total??queue.tasks.length,offset=(page-1)*size;
        const samePage=root.dataset.checklistID===queue.id && Number(root.dataset.renderedPage)===page;
        pages.set(queue.id,page);
        if(root.dataset.checklistID!==queue.id || !root.querySelector('#bq-task-rows')) {
            root.innerHTML=`<div class="batch-queue-detail-layout" data-bq-detail-for="${V.esc(queue.id)}"><div class="batch-queue-detail-hero" id="bq-inline-summary"></div><details class="lt-context" id="bq-settings"><summary>${V.esc(V.tr('taskSettings'))}</summary><div class="lt-context-body" id="bq-metadata" data-task-scroll></div></details></div><section class="task-items-panel"><div class="task-items-heading"><h4>${V.esc(V.tr('stepList'))}</h4></div><div id="bq-task-rows" class="task-record-list" data-task-scroll></div><div id="bq-task-pagination" class="task-pagination"></div></section>`;
            root.dataset.checklistID=queue.id;
        }
        const presentation=getBatchQueueStatusPresentation(queue);
        let displayStatus=queue.status;
        if(queue.status==='completed' && queue.taskStats?.failed>0) displayStatus=queue.taskStats.completed>0?'partial':'failed';
        else if(queue.status==='completed' && queue.scheduleMode==='cron' && queue.scheduleEnabled!==false) displayStatus='scheduled';
        V.setHTML(root.querySelector('#bq-inline-summary'),`<span class="lt-status lt-status-${V.esc(displayStatus)}">${V.esc(V.tr('status.'+displayStatus))}</span><p>${V.esc(V.tr('listProgress',{done:queue.taskStats?.completed||0,total}))}${presentation.sublabel?' · '+V.esc(presentation.sublabel):''}</p>`);
        const canEdit=batchQueueAllowsSubtaskMutation(queue)&&allowed('tasks:write');
        const row=(label,value,id,edit)=>`<div class="bq-kv"><span class="bq-kv__k">${V.esc(label)}</span><span class="bq-kv__v" id="${id}">${canEdit&&edit?`<button type="button" class="bq-inline-editable task-meta-edit" onclick="${edit}()">${V.esc(value)}</button>`:V.esc(value)}</span></div>`;
        const metadata=root.querySelector('#bq-metadata');
        if(editorResets.has(queue.id)||!metadata.querySelector('.bq-inline-edit-controls')) {
            const schedule=queue.scheduleMode==='cron'?queue.cronExpr:_t('batchImportModal.scheduleModeManual');
            const html=`<section class="batch-queue-detail-kv">${row(_t('batchQueueDetailModal.queueTitle'),queue.title||V.tr('untitled'),'bq-title-val','startInlineEditTitle')}${row(_t('batchQueueDetailModal.role'),queue.role||_t('batchQueueDetailModal.defaultRole'),'bq-role-val','startInlineEditRole')}${row(_t('batchImportModal.agentMode'),batchQueueAgentModeLabel(queue.agentMode),'bq-agentmode-val','startInlineEditAgentMode')}${row(_t('batchImportModal.hitlPolicy'),batchHITLPolicyLabel(queue.hitlPolicy),'bq-hitl-val','startInlineEditHITLPolicy')}${row(_t('batchImportModal.scheduleMode'),schedule,'bq-schedule-val','startInlineEditSchedule')}${row(_t('batchQueueDetailModal.concurrency'),queue.concurrency||1,'bq-concurrency-val','startInlineEditConcurrency')}</section>
                ${queue.scheduleMode==='cron'?`<label class="bq-cron-toggle"><input type="checkbox" ${queue.scheduleEnabled!==false?'checked':''} ${!allowed('tasks:write')?'disabled':''} onchange="updateBatchQueueScheduleEnabled(this.checked)">${V.esc(_t('batchQueueDetailModal.scheduleCronAuto'))}</label>`:''}
                <details class="batch-queue-detail-tech"><summary>${V.esc(_t('batchQueueDetailModal.technicalDetails'))}</summary><p>${V.esc(queue.id)}\n${V.esc(_t('batchQueueDetailModal.createdAt'))}: ${V.esc(new Date(queue.createdAt).toLocaleString())}${queue.nextRunAt?'\n'+V.esc(_t('batchQueueDetailModal.nextRunAt'))+': '+V.esc(new Date(queue.nextRunAt).toLocaleString()):''}</p></details>
                ${queue.lastRunError?`<p class="lt-form-error">${V.esc(queue.lastRunError)}</p>`:''}${queue.lastScheduleError?`<p class="lt-form-error">${V.esc(queue.lastScheduleError)}</p>`:''}`;
            V.setHTML(metadata,html,editorResets.has(queue.id));editorResets.delete(queue.id);
        }
        V.renderRows(root.querySelector('#bq-task-rows'),queue.tasks.map((task,index)=>({id:task.id,index:offset+index+1,status:task.status,title:task.message,summary:task.error||task.result||V.tr('noResult')})),{key:queue.id+':'+page,open:id=>open(queue.id,id)});
        V.renderPager(root.querySelector('#bq-task-pagination'),{page,total,size,change:next=>{
            pages.set(queue.id,next);showBatchQueueDetail(queue.id,{inline:true,taskPage:next});
        }});
        restore(); if(!samePage)root.querySelector('#bq-task-rows').scrollTop=0;root.dataset.renderedPage=String(page);
    }
    async function open(queueID,taskID) {
        const dialog=V.openDrawer(V.tr('stepDetail'),`<p>${V.esc(V.tr('loading'))}</p>`);
        const current=()=>dialog.isConnected&&dialog.open&&document.getElementById('task-step-inspector')===dialog;
        let task,queue,busy=false;
        const showError=error=>{if(current()){const box=dialog.querySelector('[data-inspector-error]');if(box){box.textContent=error.message||String(error);box.hidden=false;}}};
        async function load() {
            try {
                const data=await request(`/api/batch-tasks/${encodeURIComponent(queueID)}/tasks/${encodeURIComponent(taskID)}`);
                if(!current())return;
                task=data.task;queue=data.queue;queue.taskStats=data.task_stats;
                const editable=batchQueueAllowsSubtaskMutation(queue)&&task.status!=='running';
                dialog.querySelector('.task-inspector-body').innerHTML=`<div data-task-overview><span class="lt-status lt-status-${V.esc(task.status)}">${V.esc(V.tr('status.'+task.status))}</span><h4>${V.esc(V.tr('taskContent'))}</h4><p>${V.esc(task.message)}</p>${task.startedAt?`<h4>${V.esc(_t('batchQueueDetailModal.startLabel'))}</h4><p>${V.esc(new Date(task.startedAt).toLocaleString())}</p>`:''}${task.completedAt?`<h4>${V.esc(_t('batchQueueDetailModal.completeLabel'))}</h4><p>${V.esc(new Date(task.completedAt).toLocaleString())}</p>`:''}<h4>${V.esc(V.tr('result'))}</h4><pre>${V.esc(task.result||V.tr('noResult'))}</pre>${task.error?`<h4>${V.esc(V.tr('error'))}</h4><pre>${V.esc(task.error)}</pre>`:''}</div><form data-task-editor hidden><label>${V.esc(V.tr('taskContent'))}<textarea name="message" required>${V.esc(task.message)}</textarea></label><div class="lt-actions"><button class="btn-primary" type="submit">${V.esc(V.tr('save'))}</button><button type="button" class="btn-secondary" data-checklist-action="cancel-edit">${V.esc(V.tr('cancelEdit'))}</button></div></form><p class="lt-form-error" data-inspector-error role="alert" hidden></p>`;
                dialog.querySelector('.task-inspector-actions').hidden=false;
                dialog.querySelector('.task-inspector-actions').innerHTML=`${task.conversationId&&allowed('chat:read')?`<button type="button" class="btn-secondary" data-checklist-action="conversation">${V.esc(V.tr('trace'))}</button>`:''}${allowed('tasks:write')&&batchQueueCanRunSingleTask(queue,task)?`<button type="button" class="btn-primary" data-checklist-action="run">${V.esc(_t('tasks.runSingleTask'))}</button>`:''}${editable&&allowed('tasks:write')?`<button type="button" class="btn-secondary" data-checklist-action="edit">${V.esc(_t('common.edit'))}</button>`:''}${editable&&allowed('tasks:delete')?`<button type="button" class="btn-secondary btn-delete" data-checklist-action="delete">${V.esc(V.tr('delete'))}</button>`:''}`;
            }catch(error){if(current())dialog.querySelector('.task-inspector-body').innerHTML=`<p role="alert" class="lt-form-error">${V.esc(error.message)}</p>`;}
        }
        dialog.addEventListener('click',async event=>{
            const button=event.target.closest('[data-checklist-action]');if(!button||busy||!task)return;
            const action=button.dataset.checklistAction;
            if(action==='edit'||action==='cancel-edit'){
                dialog.querySelector('[data-task-overview]').hidden=action==='edit';dialog.querySelector('[data-task-editor]').hidden=action!=='edit';
                dialog.querySelector('.task-inspector-actions').hidden=action==='edit';
                if(action==='edit')dialog.querySelector('textarea').focus();else dialog.querySelector('textarea').value=task.message;return;
            }
            if(action==='conversation'){V.closeDrawer();viewBatchTaskConversation(task.conversationId);return;}
            if(action!=='run'&&action!=='delete')return;
            if(action==='run'&&!confirm(_t('tasks.confirmRunSingleTask')))return;
            if(action==='delete'&&!confirm(_t('tasks.confirmDeleteTask',{message:V.text(task.message).slice(0,50)})))return;
            busy=true;button.disabled=true;
            try{
                const base=`/api/batch-tasks/${encodeURIComponent(queueID)}/tasks/${encodeURIComponent(taskID)}`;
                const result=await request(action==='run'?base+'/run':base,{method:action==='run'?'POST':'DELETE'});
                if(action==='delete'&&current())V.closeDrawer();
                await refreshBatchQueues();
                if(action==='run'&&current()){await load();if(result.autoStarted===false&&result.message)showError(new Error(result.message));}
            }catch(error){showError(error);}finally{busy=false;if(button.isConnected)button.disabled=false;}
        });
        dialog.addEventListener('submit',async event=>{
            if(!event.target.matches('[data-task-editor]'))return;event.preventDefault();if(busy)return;
            const message=event.target.querySelector('textarea').value.trim();if(!message)return;
            busy=true;const save=event.target.querySelector('[type=submit]');save.disabled=true;
            try{await request(`/api/batch-tasks/${encodeURIComponent(queueID)}/tasks/${encodeURIComponent(taskID)}`,{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({message})});await refreshBatchQueues();if(current())await load();}
            catch(error){showError(error);}finally{busy=false;if(save.isConnected)save.disabled=false;}
        });
        await load();
    }
    window.TaskChecklistView={render,open,page:id=>pages.get(id)||1,resetEditor:id=>editorResets.add(id)};
})();
