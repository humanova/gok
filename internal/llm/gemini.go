package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gok/internal/model"

	"google.golang.org/genai"
)

// Client wraps the Gemini generative model.
type Client struct {
	client    *genai.Client
	modelName string
}

func NewClient(ctx context.Context, apiKey, modelName string) (*Client, error) {
	c, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey: apiKey,
	})
	if err != nil {
		return nil, fmt.Errorf("creating gemini client: %w", err)
	}
	return &Client{client: c, modelName: modelName}, nil
}

// SynthesizeDigest generates a structured daily digest from retrieved entries and viewpoints.
func (c *Client) SynthesizeDigest(ctx context.Context, entries []model.Entry, viewpoints []model.Viewpoint, topicTitles []string) (*model.DigestPayload, error) {
	prompt := buildDigestPrompt(entries, viewpoints, topicTitles)
	return c.callStructured(ctx, prompt)
}

// SynthesizeQuery answers a free-form user question given retrieved context.
func (c *Client) SynthesizeQuery(ctx context.Context, query string, entries []model.Entry, viewpoints []model.Viewpoint) (*model.DigestPayload, error) {
	prompt := buildQueryPrompt(query, entries, viewpoints)
	return c.callStructured(ctx, prompt)
}

// StreamQuery streams a conversational answer as text tokens via the returned channel.
// The channel is closed when the stream ends or errors.
func (c *Client) StreamQuery(ctx context.Context, query string, entries []model.Entry, viewpoints []model.Viewpoint) (<-chan string, <-chan error) {
	tokenCh := make(chan string, 64)
	errCh := make(chan error, 1)

	go func() {
		defer close(tokenCh)
		defer close(errCh)

		prompt := buildQueryPrompt(query, entries, viewpoints)
		fullPrompt := systemPrompt() + "\n\n" + prompt

		contents := []*genai.Content{genai.NewContentFromText(fullPrompt, genai.RoleUser)}
		iter := c.client.Models.GenerateContentStream(ctx, c.modelName, contents, nil)

		for resp, err := range iter {
			if err != nil {
				errCh <- err
				return
			}
			for _, cand := range resp.Candidates {
				if cand.Content == nil {
					continue
				}
				for _, part := range cand.Content.Parts {
					if part.Text != "" {
						tokenCh <- part.Text
					}
				}
			}
		}
	}()

	return tokenCh, errCh
}

func (c *Client) callStructured(ctx context.Context, prompt string) (*model.DigestPayload, error) {
	fullPrompt := systemPrompt() + "\n\n" + prompt

	contents := []*genai.Content{genai.NewContentFromText(fullPrompt, genai.RoleUser)}
	resp, err := c.client.Models.GenerateContent(ctx, c.modelName, contents,
		&genai.GenerateContentConfig{
			ResponseMIMEType: "application/json",
		})
	if err != nil {
		return nil, fmt.Errorf("gemini call failed: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return nil, fmt.Errorf("empty gemini response")
	}

	raw := ""
	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			raw += part.Text
		}
	}

	var payload model.DigestPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("parsing structured response: %w\nraw: %s", err, raw)
	}
	return &payload, nil
}

func systemPrompt() string {
	return `Sen Ekşi Sözlük'teki Türkçe girdileri analiz eden bir asistansın.
Kullanıcıların "bugün ne oluyor?" sorusuna net, tarafsız ve bilgilendirici cevaplar veriyorsun.
Cevaplarını her zaman Türkçe yaz.
JSON çıktısı istediğinde şu şemayı kullan:
{
  "summary": "Genel özet (2-4 cümle)",
  "dominant_sentiment": "pozitif | negatif | karışık | nötr",
  "key_viewpoints": [
    {"stance": "görüş etiketi", "representative_quote": "alıntı", "author": "yazar", "entry_count": 0}
  ],
  "trending_topics": ["konu1", "konu2"]
}`
}

func buildDigestPrompt(entries []model.Entry, viewpoints []model.Viewpoint, topicTitles []string) string {
	var sb strings.Builder
	sb.WriteString("## Bugünün Popüler Konuları\n")
	for _, t := range topicTitles {
		sb.WriteString("- ")
		sb.WriteString(t)
		sb.WriteString("\n")
	}
	sb.WriteString("\n## Seçilmiş Girdiler\n")
	for i, e := range entries {
		if i >= 30 {
			break
		}
		sb.WriteString(fmt.Sprintf("[%s]: %s\n---\n", e.Author, truncate(e.Text, 400)))
	}
	sb.WriteString("\n## Tespit Edilen Görüşler\n")
	for _, v := range viewpoints {
		sb.WriteString(fmt.Sprintf("- %s (%d girdi): \"%s\" — %s\n", v.Stance, v.EntryCount, v.RepresentativeQuote, v.Author))
	}
	sb.WriteString("\nYukarıdaki verilere dayanarak JSON özet oluştur.")
	return sb.String()
}

func buildQueryPrompt(query string, entries []model.Entry, viewpoints []model.Viewpoint) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Kullanıcı Sorusu\n%s\n\n", query))
	sb.WriteString("## İlgili Girdiler\n")
	for i, e := range entries {
		if i >= 20 {
			break
		}
		sb.WriteString(fmt.Sprintf("[%s]: %s\n---\n", e.Author, truncate(e.Text, 400)))
	}
	if len(viewpoints) > 0 {
		sb.WriteString("\n## Görüşler\n")
		for _, v := range viewpoints {
			sb.WriteString(fmt.Sprintf("- %s: \"%s\" — %s\n", v.Stance, v.RepresentativeQuote, v.Author))
		}
	}
	sb.WriteString("\nBu soruyu JSON formatında yanıtla.")
	return sb.String()
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "\u2026"
}
