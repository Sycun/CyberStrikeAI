# Provider 方言目录（生成物）

由 `make generate` 从 `internal/provider` 的方言表生成，**不要手改**。

这张表是「厂商差异」的唯一落点：能力位、重试姿态、溢出标记、鉴权头形态都在这里，
调用方只允许问 `internal/provider` 的问题，不许再比较厂商字符串。
`api` 决定线格式（`anthropic-messages` 与 `openai-chat` 是两套），`→ alias` 只做渠道归一化。

| vendor | api | endpoint | 默认 base_url | context | max_tokens | 能力位 | 重试 | 溢出标记 |
|---|---|---|---|---|---|---|---|---|
| `anthropic` | `anthropic-messages` | `https://api.anthropic.com/v1/messages` | `https://api.anthropic.com` | 200000 | 8192 | agentic_backend, rejects_dangling_tool_calls, stream_errors_in_body, thinking_blocks, vision_input | 3 次 / 起 500ms / 5xx true | prompt is too long, input is too long, context_length |
| `claude` | `anthropic-messages` | `https://api.anthropic.com/v1/messages` | `https://api.anthropic.com` | 200000 | 8192 | agentic_backend, rejects_dangling_tool_calls, stream_errors_in_body, thinking_blocks, vision_input | 3 次 / 起 500ms / 5xx true | prompt is too long, input is too long, context_length |
| `dashscope` | `openai-chat` | `https://api.openai.com/v1/chat/completions` | `—` | 128000 | 8192 | rejects_dangling_tool_calls, vision_input | 3 次 / 起 500ms / 5xx true | context_length, range of input length, input is too long |
| `deepseek` | `openai-chat` | `https://api.openai.com/v1/chat/completions` | `—` | 128000 | 8192 | reasoning_effort, rejects_dangling_tool_calls | 3 次 / 起 500ms / 5xx true | context_length, input is too long |
| `openai` | `openai-chat` | `https://api.openai.com/v1/chat/completions` | `https://api.openai.com/v1` | 400000 | 16384 | agentic_backend, reasoning_effort, rejects_dangling_tool_calls, strict_json_schema, vision_input | 3 次 / 起 500ms / 5xx true | context_length, input is too long, maximum context length |
| `openai_compatible` → openai | `openai-chat` | `https://api.openai.com/v1/chat/completions` | `https://api.openai.com/v1` | 128000 | 8192 | agentic_backend, forced_tool_choice_unsafe, rejects_dangling_tool_calls, vision_input | 3 次 / 起 500ms / 5xx true | context_length, input is too long, prompt is too long, too many tokens |

## 派生答案（改动即行为变更）

| vendor | provider 名（归一化后） | agentic 后端 | anthropic 线格式 | 支持列模型 |
|---|---|---|---|---|
| `anthropic` | `anthropic` | true | true | false |
| `claude` | `claude` | true | true | false |
| `dashscope` | `dashscope` | false | false | true |
| `deepseek` | `deepseek` | false | false | true |
| `openai` | `openai` | true | false | true |
| `openai_compatible` | `openai` | true | false | true |

## 成本（USD / 百万 token）

| vendor | input | output | cache read | cache write |
|---|---|---|---|---|
| `anthropic` | 3.00 | 15.00 | 0.30 | 3.75 |
| `claude` | 3.00 | 15.00 | 0.30 | 3.75 |
| `dashscope` | 0.00 | 0.00 | 0.00 | 0.00 |
| `deepseek` | 0.14 | 0.28 | 0.00 | 0.00 |
| `openai` | 2.50 | 10.00 | 0.00 | 0.00 |
| `openai_compatible` | 0.00 | 0.00 | 0.00 | 0.00 |
