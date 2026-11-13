const {test}=require('node:test');
const assert=require('node:assert/strict');
const {fixture,goal}=require('./helpers/task-ui-fixture.cjs');

const queueRow=page=>page.locator('#lt-list [data-record-id="list:q1"] [data-record-open]');

test('365 tasks use stable 20-row pages; automatic refresh keeps both scroll positions and the selected task',async t=>{
 const {page,state,refresh}=await fixture(t);
 for(let i=0;i<36;i++)state.tasks.set('extra'+i,goal('extra'+i));
 const q=state.queues.get('q1');q.status='paused';q.tasks=Array.from({length:365},(_,i)=>({id:'step-'+i,message:`https://asset-${i}.example.test/`+'long-path/'.repeat(25),status:i<28?'completed':'pending',result:'Report preview '+i+' '+ 'evidence '.repeat(500)+'TAIL_EVIDENCE_'+i,conversationId:'conv-'+i}));
 await refresh();assert.equal(await page.locator('#lt-list .task-record-row').count(),20);
 await page.locator('#lt-pagination [data-page-nav=next]').click();await queueRow(page).click();
 await page.locator('#bq-task-rows [data-record-id=step-0]').waitFor();
 assert.equal(await page.locator('#bq-task-rows .task-record-row').count(),20);
 assert.match(await page.locator('#bq-task-pagination').textContent(),/365/);
 await page.locator('#bq-task-pagination [data-page-nav=next]').click();await page.locator('#bq-task-rows [data-record-id=step-20]').waitFor();
 await page.evaluate(()=>{document.getElementById('bq-task-rows').scrollTop=500;document.getElementById('lt-list').scrollTop=400;});
 const before=await page.evaluate(()=>({right:document.getElementById('bq-task-rows').scrollTop,left:document.getElementById('lt-list').scrollTop}));
 assert.ok(before.right>100&&before.left>100,JSON.stringify(before));
 q.tasks[21].status='completed';await refresh();
 const after=await page.evaluate(()=>({right:document.getElementById('bq-task-rows').scrollTop,left:document.getElementById('lt-list').scrollTop}));
 assert.deepEqual(after,before);
 let release;const gate=new Promise(resolve=>{release=resolve;});state.hook=async req=>{if(req.path==='/api/batch-tasks/q1'&&req.method==='GET')await gate;};
 await page.evaluate(()=>{window.pendingTaskRefresh=refreshTaskWorkspace();});await page.waitForTimeout(50);
 await page.evaluate(()=>{document.getElementById('bq-task-rows').scrollTop=600;});release();await page.evaluate(()=>window.pendingTaskRefresh);state.hook=null;
 assert.equal(await page.locator('#bq-task-rows').evaluate(node=>node.scrollTop),600,'scrolling while a refresh is in flight must keep the latest position');
 await page.evaluate(top=>{document.getElementById('bq-task-rows').scrollTop=top;},before.right);
 assert.match(await page.locator('#bq-task-pagination .task-page-controls span').textContent(),/^2 \/ 19/);
 assert.equal(await page.locator('#lt-list [data-record-id="list:q1"]').getAttribute('aria-current'),'true');
 await page.locator('#tasks-auto-refresh').check();await page.waitForTimeout(4300);await page.locator('#tasks-auto-refresh').uncheck();
 assert.equal(await page.locator('#bq-task-rows').evaluate(node=>node.scrollTop),before.right);
 assert.equal(await page.locator('#lt-list').evaluate(node=>node.scrollTop),before.left);
 // Exact same rows are retained, rather than replaced wholesale on an unchanged poll.
 await page.evaluate(()=>{window.savedTaskRow=document.querySelector('#bq-task-rows [data-record-id="step-20"]');});await refresh();
 assert.equal(await page.evaluate(()=>window.savedTaskRow===document.querySelector('#bq-task-rows [data-record-id="step-20"]')),true);
 await page.locator('#bq-task-pagination [data-page-nav=lastPage]').click();await page.locator('#bq-task-rows [data-record-id=step-360]').waitFor();
 assert.equal(await page.locator('#bq-task-rows .task-record-row').count(),5);
 assert.match(await page.locator('#bq-task-pagination').textContent(),/361.*365/);
 await page.screenshot({path:'/tmp/csai-task-rows-desktop.png'});
 await page.locator('#bq-task-rows [data-record-id=step-364] [data-record-open]').first().click();
 await page.waitForFunction(()=>document.getElementById('task-step-inspector').textContent.includes('TAIL_EVIDENCE_364'));
 assert.ok(state.requests.some(r=>r.path.endsWith('/tasks/step-364')&&r.method==='GET'));
 await page.locator('[data-inspector-close]').click();assert.match(await page.locator('#bq-task-pagination .task-page-controls span').textContent(),/^19 \/ 19/);
 assert.deepEqual(state.pageErrors,[]);
});

test('autonomous steps share row pagination and keep drafts and scroll on refresh',async t=>{
 const {page,state,select,refresh}=await fixture(t);
 const task=state.tasks.get('a');task.steps=Array.from({length:45},(_,i)=>({id:'s'+i,title:'Local step '+i,instruction:'Inspect fixture '+i,status:'completed',summary:'Verified '+i,depends_on:[],conversation_id:'c'+i,attempts:1}));
 await select('a');assert.equal(await page.locator('#lt-steps .task-record-row').count(),20);
 await page.locator('#lt-step-pagination [data-page-nav=next]').click();await page.locator('#lt-steps [data-record-id=s20]').waitFor();
 await page.evaluate(()=>{document.getElementById('lt-steps').scrollTop=400;});const top=await page.locator('#lt-steps').evaluate(n=>n.scrollTop);assert.ok(top>0);
 task.steps[23].summary='New observation';await refresh();assert.equal(await page.locator('#lt-steps').evaluate(n=>n.scrollTop),top);
 await page.locator('#lt-support > summary').click();await page.locator('#lt-guidance-input').fill('Keep this unsent guidance');await refresh();assert.equal(await page.locator('#lt-guidance-input').inputValue(),'Keep this unsent guidance');
 await page.locator('#lt-support > summary').click();await page.locator('#lt-step-pagination [data-page-nav=lastPage]').click();assert.equal(await page.locator('#lt-steps .task-record-row').count(),5);
 await page.locator('#lt-steps [data-record-open=s44]').first().click();assert.match(await page.locator('.task-inspector-body').textContent(),/Verified 44/);
 await refresh();assert.equal(await page.locator('#task-step-inspector').isVisible(),true);await page.locator('[data-inspector-close]').click();
 assert.deepEqual(state.pageErrors,[]);
});


test('compact rows and fixed pagination remain usable in dark mode and narrow screens',async t=>{
 const {page,state,refresh}=await fixture(t);const q=state.queues.get('q1');q.status='paused';q.tasks=Array.from({length:35},(_,i)=>({id:'s'+i,message:'https://local.example.test/'+('long-path/'.repeat(30))+i,status:'completed',result:'<img src=x onerror="window.injected=1"> Full evidence '+i,conversationId:'c'+i}));
 await refresh();await queueRow(page).click();await page.locator('#bq-task-rows [data-record-id=s0]').waitFor();
 for(const width of [1440,390]){
  await page.setViewportSize({width,height:900});await page.evaluate(()=>document.documentElement.setAttribute('data-theme','dark'));
  assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));
  await page.locator('#bq-task-pagination').scrollIntoViewIfNeeded();
  const rect=await page.locator('#bq-task-pagination').boundingBox();assert.ok(rect.y+rect.height<=902);
  if(width===390){const controls=await page.locator('#bq-task-pagination .task-page-controls').boundingBox();assert.ok(controls.height<=50,'mobile paging controls must stay on one row');}
  await page.screenshot({path:`/tmp/csai-task-rows-dark-${width}.png`});
 }
 await page.setViewportSize({width:1440,height:900});await page.locator('#bq-task-rows [data-record-open=s0]').first().click();
 await page.locator('[data-checklist-action=edit]').waitFor();assert.equal(await page.evaluate(()=>window.injected),undefined);assert.equal(await page.locator('.task-inspector-body img').count(),0);
 await page.locator('[data-checklist-action=edit]').click();await page.locator('[data-task-editor] textarea').fill('Unsaved edit');await refresh();assert.equal(await page.locator('[data-task-editor] textarea').inputValue(),'Unsaved edit');
 await page.locator('[data-checklist-action=cancel-edit]').click();await page.locator('[data-checklist-action=edit]').click();assert.equal(await page.locator('[data-task-editor] textarea').inputValue(),q.tasks[0].message);
 assert.deepEqual(state.pageErrors,[]);
});


test('new parent rows above the viewport preserve the visible reading anchor',async t=>{
 const {page,state,refresh}=await fixture(t);for(let i=0;i<19;i++)state.tasks.set('p'+i,goal('p'+i));await refresh();
 await page.locator('#lt-list').evaluate(node=>{node.scrollTop=400;});
 const anchor=await page.locator('#lt-list').evaluate(root=>{const bounds=root.getBoundingClientRect();const row=[...root.children].find(row=>row.getBoundingClientRect().bottom>bounds.top);return {id:row.dataset.recordId,y:row.getBoundingClientRect().top};});
 state.tasks=new Map([['new-parent',goal('new-parent')],...state.tasks.entries()]);await refresh();
 const y=await page.locator('#lt-list').evaluate((root,id)=>[...root.children].find(row=>row.dataset.recordId===id).getBoundingClientRect().top,anchor.id);
 assert.ok(Math.abs(y-anchor.y)<2,`reading anchor moved from ${anchor.y} to ${y}`);
});


test('a stale step snapshot can be refreshed explicitly before retry',async t=>{
 const {page,state,select,refresh}=await fixture(t);await select('a');await page.locator('#lt-steps [data-record-open=s1]').first().click();
 state.tasks.get('a').revision++;state.tasks.get('a').steps[0].summary='New checked result';await refresh();
 await page.locator('[data-step-retry]').click();await page.waitForFunction(()=>document.querySelector('[data-step-error]').textContent.includes('revision conflict'));
 await page.locator('[data-step-refresh]').click();await page.waitForFunction(()=>document.querySelector('.task-inspector-body').textContent.includes('New checked result'));
 await page.locator('[data-step-retry]').click();await page.locator('#task-step-inspector').waitFor({state:'detached'});assert.equal(state.tasks.get('a').steps[0].status,'pending');
});


test('a task waiting for input before its first plan still renders and accepts an answer',async t=>{
 const {page,state,select}=await fixture(t);const task=state.tasks.get('a');task.steps=null;task.status='waiting_input';task.question='Which local fixture?';
 await select('a');await page.locator('#lt-question').waitFor();assert.equal(await page.locator('#lt-steps .task-record-row').count(),0);
 await page.locator('#lt-guidance-input').fill('Use fixture A');await page.locator('#lt-guidance-send').click();await page.locator('#lt-state .lt-status-running').waitFor();
 assert.deepEqual(task.guidance,['Use fixture A']);assert.deepEqual(state.pageErrors,[]);
});
