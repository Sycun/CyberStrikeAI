package security

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"cyberstrike-ai/internal/authctx"
	"cyberstrike-ai/internal/mcp"

	"go.uber.org/zap"
)

const (
	queryResultDefaultPage     = 1
	queryResultDefaultPageRows = 100
	queryResultMaxPageRows     = 2000
	queryResultMaxPage         = 1000000
	queryResultMaxScannedRows  = 1000000
	queryResultSpillMarker     = "Full output saved to: "
	queryResultJSONShellBytes  = 1024
)

// queryExecutionResult backs the internal:query_execution_result recipe against the MCP
// execution store, i.e. the same access path get_tool_execution uses: in-memory records
// first, then persisted tool_executions rows.
//
// A persisted result is already normalized by ConfigureToolResultMaxBytes, so an oversized
// run surfaces as a <persisted-output> notice naming its spill file. When that file is still
// present its full text becomes the query source; otherwise the stored payload is used as is.
// filter/search narrow the source line by line and page/limit walk what remains, so the model
// can read a large result without ever receiving more than the platform tool-result ceiling.
func (e *Executor) queryExecutionResult(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
	if e.mcpServer == nil {
		return internalToolFailure("错误: 未连接 MCP 执行存储，无法查询工具执行结果"), nil
	}
	executionID := queryResultStringArg(args, "execution_id")
	if executionID == "" {
		return internalToolFailure("错误: 缺少必需的参数 execution_id"), nil
	}
	exec, ok := e.mcpServer.GetExecution(executionID)
	if !ok || exec == nil {
		return internalToolFailure(fmt.Sprintf("错误: 未找到该 execution_id: %s", executionID)), nil
	}
	if !executionVisibleToPrincipal(ctx, exec) {
		e.logger.Warn("拒绝查询非本人可用的执行结果",
			zap.String("toolName", "query_execution_result"),
			zap.String("executionId", executionID),
		)
		return internalToolFailure(fmt.Sprintf("错误: 无权访问该 execution_id: %s", executionID)), nil
	}

	page := int64(queryResultIntArg(args, "page", queryResultDefaultPage, queryResultMaxPage))
	limit := int64(queryResultIntArg(args, "limit", queryResultDefaultPageRows, queryResultMaxPageRows))
	filter := queryResultStringArg(args, "filter")
	search := queryResultStringArg(args, "search")

	storedText := mcp.ToolResultPlainText(exec.Result)
	reader := io.Reader(strings.NewReader(storedText))
	source := "stored_result"
	if path := e.spilledToolOutputPath(exec, storedText); path != "" {
		if file, err := os.Open(path); err == nil {
			defer func() { _ = file.Close() }()
			reader = file
			source = "spilled_output_file"
		}
	}

	window := queryResultWindow{
		start:    (page - 1) * limit,
		end:      page * limit,
		filter:   filter,
		search:   search,
		maxBytes: e.queryResultBudgetBytes(),
	}
	queried := scanExecutionResult(reader, window)

	payload := map[string]interface{}{
		"execution_id":  exec.ID,
		"tool":          exec.ToolName,
		"status":        exec.Status,
		"result_source": source,
		"page":          page,
		"limit":         limit,
		"total_rows":    queried.scannedRows,
		"matched_rows":  queried.matchedRows,
		"returned_rows": queried.returnedRows,
		"has_more":      queried.matchedRows > window.start+int64(queried.returnedRows),
		"output_bytes":  len(queried.body),
		"content":       queried.body,
	}
	if exec.Error != "" {
		payload["error"] = exec.Error
	}
	if exec.Result != nil {
		payload["is_error"] = exec.Result.IsError
		if exec.Result.Blocked {
			payload["blocked"] = true
		}
	}
	if filter != "" {
		payload["filter"] = filter
	}
	if search != "" {
		payload["search"] = search
	}
	if queried.truncatedByBytes {
		payload["content_truncated"] = true
		payload["content_max_bytes"] = window.maxBytes
	}
	if queried.scanCapped {
		payload["scan_capped_at_rows"] = queryResultMaxScannedRows
	}
	if queried.scanErr != "" {
		payload["scan_error"] = queried.scanErr
	}
	if exec.Result == nil {
		switch exec.Status {
		case mcp.ToolExecutionStatusQueued, mcp.ToolExecutionStatusRunning:
			payload["note"] = "execution 仍在进行，尚无结果正文；可调用 wait_tool_execution 继续等待。"
		default:
			payload["note"] = "该 execution 没有保存结果正文，可参考 error 字段了解原因。"
		}
	}

	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return internalToolFailure(fmt.Sprintf("错误: 序列化执行结果失败: %v", err)), nil
	}
	return &mcp.ToolResult{
		Content: []mcp.Content{{Type: "text", Text: string(body)}},
		IsError: false,
	}, nil
}

type queryResultWindow struct {
	start    int64
	end      int64
	filter   string
	search   string
	maxBytes int
}

type queryResultScan struct {
	body             string
	scannedRows      int64
	matchedRows      int64
	returnedRows     int64
	truncatedByBytes bool
	scanCapped       bool
	scanErr          string
}

func scanExecutionResult(reader io.Reader, window queryResultWindow) queryResultScan {
	var out queryResultScan
	var builder strings.Builder
	buffered := 0
	bufReader := bufio.NewReaderSize(reader, 256*1024)
	for {
		if out.scannedRows >= queryResultMaxScannedRows {
			out.scanCapped = true
			break
		}
		line, err := bufReader.ReadString('\n')
		if line != "" {
			out.scannedRows++
			row := strings.TrimRight(line, "\r\n")
			if (window.filter == "" || strings.Contains(row, window.filter)) &&
				(window.search == "" || strings.Contains(row, window.search)) {
				out.matchedRows++
				if out.matchedRows > window.start && out.matchedRows <= window.end {
					if buffered+len(row)+1 <= window.maxBytes {
						if buffered > 0 {
							builder.WriteByte('\n')
							buffered++
						}
						builder.WriteString(row)
						buffered += len(row)
						out.returnedRows++
					} else {
						out.truncatedByBytes = true
					}
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				out.scanErr = err.Error()
			}
			break
		}
	}
	out.body = builder.String()
	return out
}

// spilledToolOutputPath resolves the spill file named inside a normalized tool result, but
// only when it is provably this execution's own file: it must sit under the configured spill
// root and be named after the execution id. Result text is untrusted output, so a crafted
// "Full output saved to:" line never turns this tool into an arbitrary file read.
func (e *Executor) spilledToolOutputPath(exec *mcp.ToolExecution, storedText string) string {
	idx := strings.Index(storedText, queryResultSpillMarker)
	if idx < 0 {
		return ""
	}
	rest := storedText[idx+len(queryResultSpillMarker):]
	candidate := strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0])
	if tag := strings.Index(candidate, "</persisted-output>"); tag >= 0 {
		candidate = strings.TrimSpace(candidate[:tag])
	}
	if candidate == "" || !filepath.IsAbs(candidate) {
		return ""
	}
	executionID := strings.TrimSpace(exec.ID)
	if executionID == "" || strings.ContainsAny(executionID, `/\`) || filepath.Base(candidate) != executionID {
		return ""
	}
	root := strings.TrimSpace(e.spillRootDir)
	if root == "" {
		root = filepath.Join("tmp", "reduction")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return ""
	}
	clean := filepath.Clean(candidate)
	rel, err := filepath.Rel(absRoot, clean)
	if err != nil || rel == ".." || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	info, err := os.Stat(clean)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return clean
}

func (e *Executor) queryResultBudgetBytes() int {
	if e.toolOutputMaxBytes > 0 {
		if budget := e.toolOutputMaxBytes - queryResultJSONShellBytes; budget > 0 {
			return budget
		}
		return e.toolOutputMaxBytes
	}
	if budget := mcp.DefaultToolResultMaxBytes - queryResultJSONShellBytes; budget > 0 {
		return budget
	}
	return mcp.DefaultToolResultMaxBytes
}

// executionVisibleToPrincipal mirrors the ownership rule the capability policy applies to this
// tool (UserCanAccessToolExecution): owner match, or the same conversation for records that
// predate ownership, and global-scope principals only. The policy is the decision point for
// user-attributed calls; this keeps a direct executor call from reading another user's result.
func executionVisibleToPrincipal(ctx context.Context, exec *mcp.ToolExecution) bool {
	principal, ok := authctx.PrincipalFromContext(ctx)
	if !ok {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(principal.ScopeFor("monitor:read")), "all") {
		return true
	}
	owner := strings.TrimSpace(exec.OwnerUserID)
	if owner != "" {
		return owner == strings.TrimSpace(principal.UserID)
	}
	conversation := strings.TrimSpace(exec.ConversationID)
	return conversation != "" && conversation == mcp.MCPConversationIDFromContext(ctx)
}

func internalToolFailure(text string) *mcp.ToolResult {
	return &mcp.ToolResult{
		Content: []mcp.Content{{Type: "text", Text: text}},
		IsError: true,
	}
}

func queryResultStringArg(args map[string]interface{}, key string) string {
	if args == nil {
		return ""
	}
	raw, ok := args[key]
	if !ok || raw == nil {
		return ""
	}
	if s, ok := raw.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(raw))
}

func queryResultIntArg(args map[string]interface{}, key string, def, max int) int {
	if args == nil {
		return def
	}
	var n int
	switch v := args[key].(type) {
	case int:
		n = v
	case int64:
		n = int(v)
	case float64:
		n = int(v)
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return def
		}
		n = int(parsed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return def
		}
		n = parsed
	default:
		return def
	}
	if n <= 0 {
		return def
	}
	if max > 0 && n > max {
		return max
	}
	return n
}
