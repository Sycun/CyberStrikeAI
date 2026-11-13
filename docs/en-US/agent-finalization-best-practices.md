# Native Agent finalization and task acceptance

Pi `agent_settled` ends automatic model work for the current run. Go still decides whether the business result can be finalized.

`internal/agentfinalizer` keeps runtime failure, cancellation and iteration-limit outcomes separate from candidate assistant text. Pending tool executions, empty responses and missing required execution evidence prevent completion. Specialist output must be checked against execution receipts; `exit.final_result` does not bypass finalization.

A completed model run is distinct from a completed long-running objective. Goal tasks in task management schedule complete Pi Agent steps and persist their plan and results. Completion requires a separate acceptance check. See [goal tasks](../zh-CN/long-running-tasks.md).

Implementation: `internal/agentfinalizer/decision.go`, `internal/multiagent/pi_runner.go`, and `internal/taskflow/engine.go`.
