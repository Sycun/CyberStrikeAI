package llm

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"cyberstrike-ai/internal/config"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"
)

// VisionRequest is one single-shot image description. It carries no vendor types on purpose:
// the Eino-facing model construction lives in the model layer, and the feature layer that
// needs an image described stays free of the SDK. That is the direction the P6 convergence
// target sets (one package speaking Eino, not eleven), and this is one of the leaks being
// closed rather than a new place to grow one.
type VisionRequest struct {
	OpenAI config.OpenAIConfig
	// HTTPClient is the plain client the native Claude agentic channel uses.
	HTTPClient *http.Client
	// CompatibleHTTPClient is the same client with the OpenAI-compatible request fixes
	// applied. The caller builds it because that transport helper lives in
	// internal/openai, which imports this package - passing it in keeps the cycle closed.
	CompatibleHTTPClient *http.Client
	Prompt               string
	ImageBase64          string
	MIMEType             string
	// Detail is "low", "high" or "auto"; anything else means low, which is what the
	// vision client defaulted to before this call moved here.
	Detail string
	// MaxCompletionTokens is the effective cap for the compatible channel, resolved by the
	// caller so this function does not need the whole config.
	MaxCompletionTokens int
}

// DescribeImage sends the image once and returns the model's text, choosing the native
// agentic channel for Claude and the OpenAI-compatible chat model for everything else.
func DescribeImage(ctx context.Context, req VisionRequest) (string, error) {
	detail := imageURLDetail(req.Detail)

	if IsClaudeProvider(req.OpenAI.Provider) {
		nativeModel, err := NewClaudeAgenticModel(ctx, req.OpenAI, req.HTTPClient, req.MaxCompletionTokens, nil)
		if err != nil {
			return "", fmt.Errorf("vision native Claude model: %w", err)
		}
		resp, err := nativeModel.Generate(ctx, []*schema.AgenticMessage{{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.UserInputText{Text: req.Prompt}),
				schema.NewContentBlock(&schema.UserInputImage{
					Base64Data: req.ImageBase64,
					MIMEType:   req.MIMEType,
					Detail:     detail,
				}),
			},
		}})
		if err != nil {
			return "", fmt.Errorf("vision native Claude generate: %w", err)
		}
		content, _ := AgenticText(resp)
		if strings.TrimSpace(content) == "" {
			return "", fmt.Errorf("vision model returned empty content")
		}
		return strings.TrimSpace(content), nil
	}

	maxCompletionTokens := req.MaxCompletionTokens
	chatModel, err := einoopenai.NewChatModel(ctx, &einoopenai.ChatModelConfig{
		APIKey:              req.OpenAI.APIKey,
		BaseURL:             strings.TrimSuffix(req.OpenAI.BaseURL, "/"),
		Model:               req.OpenAI.Model,
		HTTPClient:          req.CompatibleHTTPClient,
		MaxCompletionTokens: &maxCompletionTokens,
	})
	if err != nil {
		return "", fmt.Errorf("vision chat model: %w", err)
	}
	b64 := req.ImageBase64
	userMsg := &schema.Message{
		Role: schema.User,
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: req.Prompt},
			{
				Type: schema.ChatMessagePartTypeImageURL,
				Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{
						Base64Data: &b64,
						MIMEType:   req.MIMEType,
					},
					Detail: detail,
				},
			},
		},
	}
	resp, err := chatModel.Generate(ctx, []*schema.Message{userMsg})
	if err != nil {
		return "", fmt.Errorf("vision generate: %w", err)
	}
	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		return "", fmt.Errorf("vision model returned empty content")
	}
	return strings.TrimSpace(resp.Content), nil
}

func imageURLDetail(name string) schema.ImageURLDetail {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "high":
		return schema.ImageURLDetailHigh
	case "auto":
		return schema.ImageURLDetailAuto
	default:
		return schema.ImageURLDetailLow
	}
}
