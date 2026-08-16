package mapcuration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// Sends a JSON-only prompt to Gemini and decodes the complete candidate response.
func GenerateJSON(ctx context.Context, client *genai.Client, model, prompt string, destination any) error {
	contents := []*genai.Content{genai.NewContentFromText(prompt, genai.RoleUser)}
	response, err := client.Models.GenerateContent(ctx, model, contents, &genai.GenerateContentConfig{ResponseMIMEType: "application/json", Temperature: genai.Ptr(float32(0))})
	if err != nil {
		return err
	}
	if len(response.Candidates) == 0 || response.Candidates[0].Content == nil {
		return fmt.Errorf("empty Gemini response")
	}

	var raw strings.Builder
	for _, part := range response.Candidates[0].Content.Parts {
		raw.WriteString(part.Text)
	}
	if err := json.Unmarshal([]byte(raw.String()), destination); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// Repeats an operation and returns the final error after all attempts fail.
func Retry[T any](attempts int, operation func(attempt int) (T, error)) (T, error) {
	var zero T
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		value, err := operation(attempt)
		if err == nil {
			return value, nil
		}
		lastErr = err
	}
	return zero, lastErr
}
