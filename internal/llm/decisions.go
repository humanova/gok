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

// YesNo retries rate limits, server errors and network failures twice.
func (d *Decider) YesNo(ctx context.Context, input string, questions []string) ([]float64, error) {
	var probs []float64
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(time.Duration(attempt*attempt) * 2 * time.Second):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		var retry bool
		probs, retry, err = d.yesNo(ctx, input, questions)
		if !retry {
			break
		}
	}
	return probs, err
}

func (d *Decider) yesNo(ctx context.Context, input string, questions []string) ([]float64, bool, error) {
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
		return nil, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, decisionsURL, bytes.NewReader(payload))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", d.apiKey)
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, true, fmt.Errorf("decisions call failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
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
	retry := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
	if err := json.Unmarshal(raw, &answer); err != nil {
		return nil, retry, fmt.Errorf("decisions: %s: %.200s", resp.Status, raw)
	}
	if resp.StatusCode != http.StatusOK {
		if answer.Error.Code == "prohibited_content" {
			return nil, false, stories.ErrBlocked
		}
		return nil, retry, fmt.Errorf("decisions: %s: %s", resp.Status, answer.Error.Message)
	}
	out := make([]float64, len(questions))
	for i := range questions {
		a, ok := answer.Answers[fmt.Sprintf("q%d", i+1)]
		if !ok {
			return nil, false, fmt.Errorf("decisions: no answer for question %d", i+1)
		}
		out[i] = a.Noul
	}
	return out, false, nil
}
