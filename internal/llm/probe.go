package llm

import (
	"context"

	"cyberstrike-ai/internal/config"

	"github.com/cloudwego/eino/schema"
)

// PingAgentic runs the smallest possible agentic turn - one "Hi" - to prove a Claude
// endpoint answers. It lives in the model layer because the message it builds is an Eino
// type; the caller that only wants to know "does this config work" should not have to
// import the SDK.
func PingAgentic(ctx context.Context, cfg config.OpenAIConfig) error {
	model, err := NewClaudeAgenticModel(ctx, cfg, nil, 5, nil)
	if err != nil {
		return err
	}
	_, err = model.Generate(ctx, []*schema.AgenticMessage{
		schema.UserAgenticMessage("Hi"),
	})
	return err
}
