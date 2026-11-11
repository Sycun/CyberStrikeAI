# Native Agent finalization and task acceptance

Pi `agent_settled` ends automatic model work for the current run. Go still decides whether the business result can be finalized.

`internal/agentfinalizer` keeps runtime failure, cancellation and iteration-limit outcomes separate from candidate assistant text. Pending tool executions, empty responses and missing required execution evidence prevent completion. Specialist output must be checked against execution receipts; `exit.final_result` does not bypass finalization.

A completed model run is distinct from a completed long-running objective. Assessments own goal, dependencies, budgets, evidence and independent Review, and retain partial/blocked work. A milestone report or a question for the owner must not masquerade as final acceptance.

Implementation: `internal/agentfinalizer/decision.go`, `internal/multiagent/pi_runner.go`, and `internal/assessment/review.go`. See [Pi SDK lifecycle](https://github.com/earendil-works/pi/blob/v1.5.2/packages/coding-agent/docs/sdk.md) and the proposed [task-delegation design](../zh-CN/task-delegation-design.md).
