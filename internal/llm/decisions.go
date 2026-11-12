package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"gok/internal/stories"
)

const decisionsURL = "https://generativelanguage.googleapis.com/v1beta/decisions"

// Decider asks the Gemini Decisions API yes/no questions about one shared
// input and returns the probability of yes for each.
type Decider struct {
	apiKey string
	model  string
	http   *http.Client
}

func NewDecider(apiKey, model string) *Decider {
	return &Decider{apiKey: apiKey, model: model, http: &http.Client{Timeout: 30 * time.Second}}
}

func (d *Decider) ModelName() string { return d.model }

type decisionQuestion struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

func (d *Decider) YesNo(ctx context.Context, input string, questions []string) ([]float64, error) {
	body := struct {
		Model     string                      `json:"model"`
		Input     string                      `json:"input"`
		Questions map[string]decisionQuestion `json:"questions"`
	}{Model: d.model, Input: input, Questions: map[string]decisionQuestion{}}
	for i, question := range questions {
		body.Questions[fmt.Sprintf("q%d", i+1)] = decisionQuestion{Type: "noul", Instructions: question}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, decisionsURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", d.apiKey)
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("decisions call failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var answer struct {
		Answers map[string]struct {
			Noul float64 `json:"noul"`
		} `json:"answers"`
		Error struct {
			Code    any    `json:"code"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, fmt.Errorf("decisions: %s: %.200s", resp.Status, raw)
	}
	if resp.StatusCode != http.StatusOK {
		if answer.Error.Code == "prohibited_content" {
			return nil, stories.ErrBlocked
		}
		return nil, fmt.Errorf("decisions: %s: %s", resp.Status, answer.Error.Message)
	}
	out := make([]float64, len(questions))
	for i := range questions {
		a, ok := answer.Answers[fmt.Sprintf("q%d", i+1)]
		if !ok {
			return nil, fmt.Errorf("decisions: no answer for question %d", i+1)
		}
		out[i] = a.Noul
	}
	return out, nil
}
