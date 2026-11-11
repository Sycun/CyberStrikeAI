# Delegated work and owner questions

In the assessment workbench, choose **Delegate work**, describe the desired outcome, select the available tools and budget, and submit. A tool-free Pi request interprets the goal, scope, deliverables and acceptance criteria. Missing essential information becomes a persisted question; informational requests are answered without starting execution steps.

Once clarified, the existing durable engine plans and executes work. `ask_owner` suspends an affected step while independent steps continue. A planner-level question stops new step dispatch. When no independent work remains, the engine releases execution resources and stops charging running time while awaiting input. Answers restore the original step context and retain completed operations.

The workbench displays factual stage reports with completed work, remaining work, blockers, next actions and execution references. Reports are generated from persisted state, with the latest 50 snapshots in task details and durable events/Markdown exports. They do not invent completion percentages.

Create using `POST /api/assessments` with `request`, `tools`, and optional `project_id`, `model`, budgets and `auto_start: true`. Creation requires `assessment:write`; starting also requires `agent:execute`. A 201 response with `start_error` means the task was saved but not started; do not create a duplicate.

Answer with `POST /api/assessments/{id}/questions/{questionId}/answer` and `{"revision":2,"answer":"Your choice"}`. Task write and execution permissions are checked. Identical retries are idempotent; stale or conflicting answers return 409. A `resume_error` preserves the accepted answer and explains why execution could not start. Pending resumptions are durable and retried at startup and when a run slot is released.

Owner clarification never grants HITL approval or expands selected tools, scope, project or budgets. Interrupted external operations remain uncertain and are not automatically replayed. Updated owner guidance advances the task revision and supersedes unanswered questions.

Tests cover intake, non-executing informational requests, independent branches, planning/question races, restart, idempotency, authorization and rendering. A synthetic live-model smoke run clarified an ambiguous request, invoked the sole echo tool once, generated progress/evidence and completed. This does not establish general intent accuracy or long-running provider coverage. Production was not deployed.

The current entry points are the workbench and API. Automatic chat/robot routing, cross-project name resolution, external notifications and ARTEX adapters remain future work.
