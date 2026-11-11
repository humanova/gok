package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"gok/internal/stories"

	"google.golang.org/genai"
)

const storyEmbeddingDims = 768

// StoryEmbedder embeds story texts with a Gemini embedding model.
type StoryEmbedder struct {
	client *Client
	model  string
}

func (c *Client) StoryEmbedder(model string) *StoryEmbedder {
	return &StoryEmbedder{client: c, model: model}
}

func (e *StoryEmbedder) EmbedStoryTexts(ctx context.Context, texts []string) ([][]float32, error) {
	contents := make([]*genai.Content, len(texts))
	for i, text := range texts {
		contents[i] = genai.NewContentFromText(text, genai.RoleUser)
	}
	dims := int32(storyEmbeddingDims)
	resp, err := e.client.client.Models.EmbedContent(ctx, e.model, contents, &genai.EmbedContentConfig{
		TaskType:             "SEMANTIC_SIMILARITY",
		OutputDimensionality: &dims,
	})
	if err != nil {
		return nil, fmt.Errorf("story embedding call failed: %w", err)
	}
	if len(resp.Embeddings) != len(texts) {
		return nil, fmt.Errorf("story embedding returned %d vectors for %d texts", len(resp.Embeddings), len(texts))
	}
	out := make([][]float32, len(texts))
	for i, embedding := range resp.Embeddings {
		out[i] = normalize(embedding.Values)
	}
	return out, nil
}

func normalize(values []float32) []float32 {
	sum := 0.0
	for _, v := range values {
		sum += float64(v) * float64(v)
	}
	norm := math.Sqrt(sum)
	out := make([]float32, len(values))
	if norm == 0 {
		return out
	}
	for i, v := range values {
		out[i] = float32(float64(v) / norm)
	}
	return out
}

// StoryJudge asks a Gemini model which candidate a new topic follows from.
type StoryJudge struct {
	client *Client
	model  string
}

func (c *Client) StoryJudge(model string) *StoryJudge {
	return &StoryJudge{client: c, model: model}
}

func (j *StoryJudge) ModelName() string { return j.model }

var storyParentSchema = &genai.Schema{
	Type: genai.TypeObject,
	Properties: map[string]*genai.Schema{
		"reason": {Type: genai.TypeString},
		"parent": {Type: genai.TypeInteger},
	},
	Required:         []string{"reason", "parent"},
	PropertyOrdering: []string{"reason", "parent"},
}

func (j *StoryJudge) PickStoryParent(ctx context.Context, prompt string) (int, string, error) {
	contents := []*genai.Content{genai.NewContentFromText(prompt, genai.RoleUser)}
	resp, err := j.client.client.Models.GenerateContent(ctx, j.model, contents, &genai.GenerateContentConfig{
		Temperature:      genai.Ptr(float32(0)),
		ResponseMIMEType: "application/json",
		ResponseSchema:   storyParentSchema,
	})
	if err != nil {
		return 0, "", fmt.Errorf("story parent gemini call failed: %w", err)
	}
	if resp.PromptFeedback != nil && resp.PromptFeedback.BlockReason != "" {
		return 0, "", stories.ErrBlocked
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return 0, "", stories.ErrBlocked
	}
	var raw strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		if !part.Thought {
			raw.WriteString(part.Text)
		}
	}
	var answer struct {
		Reason string `json:"reason"`
		Parent int    `json:"parent"`
	}
	if err := json.Unmarshal([]byte(raw.String()), &answer); err != nil {
		return 0, "", fmt.Errorf("parsing story parent answer: %w", err)
	}
	return answer.Parent, answer.Reason, nil
}
