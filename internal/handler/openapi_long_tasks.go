package handler

func addLongTaskSpec(spec map[string]interface{}) {
	paths := spec["paths"].(map[string]interface{})
	stringSchema := map[string]interface{}{"type": "string"}
	integer := map[string]interface{}{"type": "integer"}
	props := map[string]interface{}{}
	for _, name := range []string{"name", "objective", "scope", "criteria", "project_id", "role", "model", "hitl_policy"} {
		props[name] = stringSchema
	}
	for _, name := range []string{"concurrency", "max_rounds", "remaining_seconds"} {
		props[name] = integer
	}
	create := map[string]interface{}{"type": "object", "required": []string{"objective", "scope", "criteria"}, "properties": props}
	control := map[string]interface{}{"type": "object", "required": []string{"revision"}, "properties": map[string]interface{}{"revision": integer, "message": stringSchema, "step_id": stringSchema, "max_rounds": integer, "remaining_seconds": integer, "cleanup_confirmed": map[string]interface{}{"type": "boolean"}}}
	operation := func(summary string, body interface{}, params []interface{}) map[string]interface{} {
		op := map[string]interface{}{"tags": []string{"任务管理"}, "summary": summary, "responses": map[string]interface{}{"200": map[string]interface{}{"description": "成功"}, "400": map[string]interface{}{"description": "参数或状态不允许"}, "403": map[string]interface{}{"description": "无权限或资源不可访问"}, "409": map[string]interface{}{"description": "revision 已变化，请刷新后重试"}}}
		if body != nil {
			op["requestBody"] = map[string]interface{}{"required": true, "content": map[string]interface{}{"application/json": map[string]interface{}{"schema": body}}}
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		return op
	}
	id := map[string]interface{}{"name": "id", "in": "path", "required": true, "schema": stringSchema}
	queueID := map[string]interface{}{"name": "queueId", "in": "path", "required": true, "schema": stringSchema}
	taskID := map[string]interface{}{"name": "taskId", "in": "path", "required": true, "schema": stringSchema}
	taskPath := paths["/api/batch-tasks/{queueId}/tasks/{taskId}"]
	if taskPath == nil {
		taskPath = map[string]interface{}{}
		paths["/api/batch-tasks/{queueId}/tasks/{taskId}"] = taskPath
	}
	taskPath.(map[string]interface{})["get"] = operation("读取清单内单条任务的完整内容及结果", nil, []interface{}{queueID, taskID})

	if path, ok := paths["/api/batch-tasks/{queueId}"].(map[string]interface{}); ok {
		if get, ok := path["get"].(map[string]interface{}); ok {
			params, _ := get["parameters"].([]map[string]interface{})
			get["parameters"] = append(params, map[string]interface{}{"name": "task_page", "in": "query", "schema": map[string]interface{}{"type": "integer", "minimum": 1}}, map[string]interface{}{"name": "task_page_size", "in": "query", "schema": map[string]interface{}{"type": "integer", "default": 20, "minimum": 1, "maximum": 100}})
			get["description"] = "不带分页参数时保留完整队列响应；带 task_page 时返回当前页预览、task_stats 全队列统计和 task_pagination。完整结果通过单条任务接口读取。"
		}
	}
	paths["/api/tasks"] = map[string]interface{}{"get": operation("统一任务列表：按 mode、status、q 筛选后统一分页", nil, nil)}
	paths["/api/long-tasks"] = map[string]interface{}{"get": operation("分页查询目标任务；支持 status、q、limit、offset", nil, nil), "post": operation("创建目标任务草稿，不自动执行", create, nil)}
	paths["/api/long-tasks"].(map[string]interface{})["post"].(map[string]interface{})["responses"].(map[string]interface{})["201"] = map[string]interface{}{"description": "草稿已保存"}
	paths["/api/long-tasks/options"] = map[string]interface{}{"get": operation("查询可用项目、角色与模型渠道", nil, nil)}
	paths["/api/long-tasks/{id}"] = map[string]interface{}{"get": operation("读取计划、结果和当前 revision", nil, []interface{}{id}), "delete": operation("删除非活动任务及事件，保留执行会话", nil, []interface{}{id})}
	paths["/api/long-tasks/{id}/events"] = map[string]interface{}{"get": operation("读取持久事件（after 游标，每页最多 100 条）", nil, []interface{}{id, map[string]interface{}{"name": "after", "in": "query", "schema": integer}})}
	paths["/api/long-tasks/{id}/report"] = map[string]interface{}{"get": operation("下载 Markdown 阶段报告，保留真实状态与缺口", nil, []interface{}{id})}
	actions := map[string]interface{}{"name": "action", "in": "path", "required": true, "schema": map[string]interface{}{"type": "string", "enum": []string{"start", "pause", "cancel", "guidance", "answer", "retry", "budget"}}}
	paths["/api/long-tasks/{id}/control/{action}"] = map[string]interface{}{"post": operation("控制目标任务；answer 保存后尝试续跑，失败返回 start_error", control, []interface{}{id, actions})}
}
