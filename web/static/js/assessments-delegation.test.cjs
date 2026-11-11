const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
function load() {
 const context={window:{hasPermission:()=>true,t:k=>k},document:{getElementById:()=>null}};
 vm.createContext(context);
 let source=fs.readFileSync('web/static/js/assessments.js','utf8');
 const end=source.lastIndexOf('})();');
 source=source.slice(0,end)+'window.testDelegation={delegationPanel,onOwnerInput,setTask(t){task=t;},drafts:answerDrafts};'+source.slice(end);
 vm.runInContext(source,context);return context.window.testDelegation;
}
test('questions and reports render escaped facts and retain typed answers across refreshes',()=>{
 const ui=load(),task={id:'t',questions:[{id:'q',status:'pending',revision:7,question:'<img onerror=alert(1)>',reason:'Need your choice',options:['<script>bad</script>']}],reports:[{created_at:'2026-11-11T00:00:00Z',completed:[{title:'Observed fixture',status:'completed',execution_ids:['exec-1']}],remaining:[{title:'Waiting branch',status:'awaiting_input'}],blockers:['<unsafe>'],next_action:'answer_owner_question'}]};
 ui.setTask(task);ui.onOwnerInput({target:{dataset:{ownerQuestion:'q'},value:'Preserve my <answer>'}});
 const html=ui.delegationPanel(task);
 assert.ok(html.includes('&lt;img onerror=alert(1)&gt;'));assert.ok(!html.includes('<script>'));
 assert.ok(html.includes('Preserve my &lt;answer&gt;'));assert.ok(html.includes('data-revision="7"'));
 assert.ok(html.includes('exec-1'));assert.ok(html.includes('Waiting branch'));assert.ok(html.includes('answerAndContinue'));
 const answered=ui.delegationPanel({...task,questions:[{...task.questions[0],status:'answered',answer:'chosen'}]});
 assert.ok(!answered.includes('data-assessment-action="answer-owner"'));assert.ok(answered.includes('chosen'));
});
test('informational replies are visible without claiming completed execution',()=>{
 const ui=load();const html=ui.delegationPanel({id:'t',intake:{message:'What does this do?',intent:'question',state:'answered',reply:'Explanation only'},questions:[],reports:[]});
 assert.ok(html.includes('Explanation only'));assert.ok(!html.includes('exec-'));assert.ok(!html.includes('data-assessment-action="answer-owner"'));
});
test('stage-report translations preserve pagination labels',()=>{
 for (const language of ['zh-CN','en-US']) {
  const data=JSON.parse(fs.readFileSync('web/static/i18n/'+language+'.json','utf8')).assessments;
  assert.equal(typeof data.next,'string');
  assert.equal(typeof data.nextActionValues.answer_owner_question,'string');
 }
});
