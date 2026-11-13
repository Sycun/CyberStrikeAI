const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const { chromium } = require('playwright');
const root = path.join(__dirname, '..');
const read = file => fs.readFileSync(path.join(root, file), 'utf8');
const template = read('web/templates/index.html');
const region = template.match(/<div id="page-deep-assessments"[^>]*><div id="deep-assessments-root"><\/div><\/div>/)?.[0];
assert.ok(region, 'independent page missing');
const sample = id => ({ id, name: '隔离样例 ' + id, status: 'draft', revision: 0, objective: '检查本地夹具', scope: '仅本地', criteria: '引用真实证据', strategy: '', summary: '', question: '', concurrency: 2, remaining_seconds: 172800, token_budget: 2000000, tokens_used: 0, max_rounds: 512, rounds: 0, updated_at: new Date().toISOString(), steps: [], metrics: {}, auto_resume: true });
async function fixture(t) {
 const state = { tasks: new Map(Array.from({ length: 24 }, (_, i) => ['t' + i, sample('t' + i)])), requests: [], errors: [] };
 const locale = JSON.stringify(JSON.parse(read('web/static/i18n/zh-CN.json'))).replaceAll('<', '\\u003c');
 const scripts = ['router', 'auth', 'deep-assessments'].map(x => `<script src="/static/js/${x}.js"></script>`).join('');
 const html = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">${['style', 'mobile', 'deep-assessments'].map(x=>`<link rel="stylesheet" href="/static/css/${x}.css">`).join('')}</head><body><div class="container"><main class="main-layout"><section class="content-area">${region}<div class="page" id="page-chat"></div><div class="page" id="page-tasks"></div></section></main></div><script>window.t=k=>k.split('.').reduce((o,p)=>o&&o[p],${locale})||k;window.loadConversation=async id=>{window.visitedConversation=id};window.initializeChatUI=()=>{};window.refreshAppData=async()=>{};</script>${scripts}<script>document.removeEventListener('DOMContentLoaded',initializeApp);saveAuth('fixture-token',new Date(Date.now()+3600000),{user:{id:'fixture',username:'fixture'},permissions:['assessments:read','assessments:write','assessments:delete','agent:execute','chat:read','chat:write','tasks:read'],scope:'own'});</script></body></html>`;
 const server = http.createServer(async (req,res)=>{
  const url = new URL(req.url,'http://fixture');
  const send=(code,body)=>{res.statusCode=code;res.setHeader('Content-Type','application/json; charset=utf-8');res.end(JSON.stringify(body));};
  if(url.pathname.startsWith('/static/')){res.setHeader('Content-Type',url.pathname.endsWith('.css')?'text/css':'text/javascript');return res.end(read('web'+url.pathname));}
  if(url.pathname==='/'){res.setHeader('Content-Type','text/html; charset=utf-8');return res.end(html);}
  if(url.pathname==='/favicon.ico'){res.statusCode=204;return res.end();}
  let raw='';for await(const chunk of req)raw+=chunk;const body=raw?JSON.parse(raw):null;
  state.requests.push({path:url.pathname,method:req.method,body,authorization:req.headers.authorization});
  if(req.headers.authorization!=='Bearer fixture-token')return send(401,{error:'authentication missing'});
  if(url.pathname==='/api/deep-assessments/options')return send(200,{models:[{id:'fixture',name:'Fixture'}],roles:['review'],projects:[]});
  if(url.pathname==='/api/deep-assessments'){
   if(req.method==='POST'){const task={...sample('new'),...body};state.tasks.set(task.id,task);return send(201,{task});}
   const filtered=[...state.tasks.values()].filter(t=>(!url.searchParams.get('q')||t.name.includes(url.searchParams.get('q')))&&(!url.searchParams.get('status')||t.status===url.searchParams.get('status')));
   const offset=Number(url.searchParams.get('offset')||0),limit=Number(url.searchParams.get('limit')||20);return send(200,{tasks:filtered.slice(offset,offset+limit),has_more:filtered.length>offset+limit});
  }
  const match=url.pathname.match(/^\/api\/deep-assessments\/([^/]+)(?:\/(.*))?$/);
  if(!match)return send(404,{error:'unhandled route'});
  const task=state.tasks.get(match[1]);if(!task)return send(404,{error:'missing task'});
  if(match[2]==='events')return send(200,{events:[]});
  if(match[2]==='receipts')return send(200,{receipts:[],next_offset:0,has_more:false});
  if(match[2]==='report'){res.setHeader('Content-Type','text/markdown');return res.end('# fixture report');}
  if(match[2]?.startsWith('control/')){
   if(body.revision!==task.revision)return send(409,{error:'stale revision'});
   const action=match[2].split('/')[1];if(action==='start')task.status='running';if(action==='pause')task.status='paused';if(action==='guidance')task.summary=body.message;if(action==='budget')Object.assign(task,body);task.revision++;
  } else if(req.method==='DELETE') state.tasks.delete(task.id);
  return send(200,{task});
 });
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const executablePath=process.env.CHROME_PATH||(process.platform==='darwin'?'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome':undefined);
 const browser=await chromium.launch(executablePath?{executablePath}:{});
 t.after(async()=>{await browser.close();server.closeAllConnections();await new Promise(resolve=>server.close(resolve));});
 const page=await browser.newPage({viewport:{width:1440,height:1000},acceptDownloads:true});page.setDefaultTimeout(5000);
 page.on('pageerror',error=>state.errors.push(error.message));page.on('dialog',d=>d.accept());
 await page.goto(`http://127.0.0.1:${server.address().port}/#deep-assessments`);
 await page.locator('[data-da-action="select"]').first().waitFor();
 return {page,state};
}

test('deep assessment is independent, paginates and only creates drafts',async t=>{
 const {page,state}=await fixture(t);
 assert.equal(await page.locator('.da-list-item').count(),20);
 await page.locator('[data-da-action="next"]').click();await page.waitForFunction(()=>document.querySelectorAll('.da-list-item').length===4);
 await page.locator('[data-da-action="create"]').click();
 await page.locator('[name="name"]').fill('Two day fixture');await page.locator('[name="objective"]').fill('Read fixture evidence');await page.locator('[name="scope"]').fill('Isolated fixture only');
 assert.equal(await page.locator('[name="hours"]').inputValue(),'48');
 await page.locator('#da-create-form button[type="submit"]').click();await page.waitForFunction(()=>document.querySelector('#da-title').textContent.includes('Two day fixture'));
 const create=state.requests.find(r=>r.method==='POST'&&r.path==='/api/deep-assessments');assert.equal(create.body.remaining_seconds,172800);assert.equal(create.body.auto_resume,true);
 assert.equal(state.requests.filter(r=>r.path.endsWith('/control/start')).length,0);
 await page.locator('[data-da-action="start"]').click();await page.locator('[data-da-action="pause"]').waitFor();
 await page.locator('#da-guidance').fill('Keep this unsent text');await page.evaluate(()=>DeepAssessments.refresh());assert.equal(await page.locator('#da-guidance').inputValue(),'Keep this unsent text');
 await page.locator('[data-da-action="pause"]').click();await page.locator('[data-da-action="start"]').waitFor();
 const download=page.waitForEvent('download');await page.locator('[data-da-action="report"]').click();assert.match((await download).suggestedFilename(),/^assessment-/);
 assert.ok(state.requests.every(r=>!r.path.startsWith('/api/batch-tasks')&&r.path!=='/api/tasks'));
 assert.deepEqual(state.errors,[]);
});

test('assessment escapes evidence and fits mobile and dark layouts',async t=>{
 const {page,state}=await fixture(t);const task=state.tasks.get('t0');task.name='<img src=x onerror="window.injected=1">';task.steps=[{id:'s1',title:'Evidence review',instruction:'Read fixture',summary:'<script>window.injected=1</script>',status:'completed',depends_on:[],attempts:3,conversation_id:'fixture-conversation',evidence:['receipt']}];
 await page.evaluate(()=>DeepAssessments.refresh());await page.locator('[data-da-action="select"][data-id="t0"]').click();
 await page.locator('[data-da-action="tab"][data-tab="plan"]').click();await page.locator('.da-step summary').click();
 assert.equal(await page.evaluate(()=>window.injected),undefined);assert.equal(await page.locator('#da-title img').count(),0);
 await page.locator('#da-guidance').fill('Unsent direction');await page.evaluate(()=>DeepAssessments.refresh());assert.equal(await page.locator('.da-step[open]').count(),1);
 const screenshotRoot=process.env.CSAI_SCREENSHOT_DIR;
 if(screenshotRoot){fs.mkdirSync(screenshotRoot,{recursive:true});await page.screenshot({path:path.join(screenshotRoot,'deep-assessments-desktop.png'),fullPage:true});}
 await page.setViewportSize({width:390,height:844});await page.evaluate(()=>document.documentElement.setAttribute('data-theme','dark'));
 assert.ok(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));
 await page.locator('[data-da-action="create"]').click();
 await page.locator('#da-dialog').waitFor({state:'visible'});
 const box=await page.locator('#da-dialog').boundingBox();assert.ok(box.x>=0&&box.x+box.width<=391);
 if(screenshotRoot)await page.screenshot({path:path.join(screenshotRoot,'deep-assessments-mobile.png'),fullPage:true});
 assert.deepEqual(state.errors,[]);
});
