# 原生 Agent 运行收尾与任务验收

Pi 的 `agent_settled` 表示本次模型运行不再自动继续。业务任务是否完成，仍由 Go 的运行状态、工具记录和验收条件决定。

## 单次运行

`internal/agentfinalizer` 统一处理单代理、多代理、机器人和相关任务入口。助手文本只是候选交付：

- 迭代上限、失败、取消和等待状态不得被普通文本覆盖为完成。
- 未完成的工具 execution ID 必须保留并处理。
- 需要实际执行的请求必须具有已完成的工具证据。
- 空文本和占位回复不能当成成功交付。
- 主代理汇总子代理结果；子代理一句“完成”不能替代原始执行记录。

过程事件、思考、工具输出与最终交付分开记录。退出工具的 final_result 仍经过宿主验收，不能绕过这些规则。

## 长期任务

目标任务在「任务管理」中调度现有 Pi Agent 的完整步骤，持久保存计划与结果，独立验收后才标记完成。参见[目标任务](long-running-tasks.md)。

阶段汇报和待用户决定的问题不应被伪装成最终目标完成。计划中的自然语言接单与主动请示机制见 [任务委派设计](long-running-tasks.md)，实现与验证边界见文档。

实现：`internal/agentfinalizer/decision.go`、`internal/multiagent/pi_runner.go`、`internal/taskflow/engine.go`。
