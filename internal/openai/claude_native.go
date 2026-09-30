package openai

import (
	"context"

	"cyberstrike-ai/internal/llm"
)

// The native Anthropic channel needs the agent framework's message type, so the request
// construction and response marshalling live in the model layer. What stays here is only
// the Client-shaped glue: this package carries no SDK types.

func (c *Client) isClaude() bool {
	return c != nil && llm.IsClaudeNative(c.config)
}

func (c *Client) claudeNativeChatCompletion(ctx context.Context, payload, out any) error {
	return llm.ClaudeNativeChatCompletion(ctx, c.config, c.httpClient, payload, out)
}

func (c *Client) claudeNativeChatCompletionStream(
	ctx context.Context,
	payload any,
	onDelta func(delta string) error,
) (string, error) {
	return llm.ClaudeNativeChatCompletionStream(ctx, c.config, c.httpClient, payload, onDelta)
}
