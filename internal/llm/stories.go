package llm

import (
	"context"
	"fmt"
	"math"

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
