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

// SynthesizeDigest generates a structured daily digest from per-topic entry bundles.
func (c *Client) SynthesizeDigest(ctx context.Context, bundles []model.TopicBundle) (*model.DigestPayload, error) {
	prompt := buildDigestPrompt(bundles)
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
	return `Sen Ekşi Sözlük'teki Türkçe girdileri analiz eden bir gazetecilik asistansın.
Görevin sadece özetlemek değil; okuyucuya "bugün gerçekte ne oluyor ve neden önemli?" sorusuna cevap vermek.
Analizin keskin, bağlamsal ve nüanslı olmalı. Yüzeysel özetlerden kaçın; tartışmanın özündeki gerilimleri, farklı bakış açılarını ve dikkat çekici momentleri öne çıkar.
Cevaplarını her zaman Türkçe yaz.

JSON çıktısı istediğinde şu şemayı kullan:
{
  "headline": "Günün ruhunu ve ana temayı yakalayan tek, çarpıcı cümle",
  "overview": "2-3 cümle: Gündemin özü, neden şu an bu konular konuşuluyor, genel atmosfer",
  "top_stories": [
    {
      "title": "Konu başlığı",
      "summary": "Ne tartışılıyor (2-3 cümle)",
      "why_it_matters": "Bu neden önemli, ne anlama geliyor, hangi derin meseleye işaret ediyor (1-2 cümle)",
      "sentiment": "pozitif | negatif | karışık | nötr"
    }
  ],
  "debates": [
    {
      "topic": "Tartışma konusu",
      "side_a": {"label": "Bu tarafı tanımlayan etiket", "argument": "Bu tarafın temel argümanı", "quote": "Temsili alıntı"},
      "side_b": {"label": "Diğer tarafı tanımlayan etiket", "argument": "Bu tarafın temel argümanı", "quote": "Temsili alıntı"},
      "tension": "Anlaşmazlığın özündeki gerilimi tek cümleyle açıkla"
    }
  ],
  "mood_snapshot": "Günün kolektif ruh halinin nüanslı tasviri (hayal kırıklığı mı, öfke mi, umut mu, ironi mi, vb.)",
  "notable_quotes": [
    {"text": "Alıntı metni", "author": "Yazar", "context": "Bu alıntı neden dikkat çekici?"}
  ],
  "under_the_radar": "Gürültünün gölgesinde kalan ama aslında önemli olan bir konu veya gözlem"
}`
}

func buildDigestPrompt(bundles []model.TopicBundle) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Aşağıda %d adet güncel popüler konu var. ", len(bundles)))
	sb.WriteString("Her konu için birbirinden farklı bakış açılarını temsil eden girdi grupları (perspektif kümeleri) verilmiştir.\n\n")

	for i, bundle := range bundles {
		sb.WriteString(fmt.Sprintf("### Konu %d: %s\n", i+1, bundle.TopicTitle))
		for ci, cluster := range bundle.Clusters {
			sb.WriteString(fmt.Sprintf("  [Perspektif %d]\n", ci+1))
			for _, e := range cluster.Entries {
				sb.WriteString(fmt.Sprintf("  - [%s]: %s\n", e.Author, truncate(e.Text, 300)))
			}
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Yukarıdaki yapılandırılmış veriyi bir gazeteci gözüyle analiz et ve belirtilen JSON şemasına göre içgörü dolu bir özet oluştur.\n")
	sb.WriteString("Önemli: Sadece özetleme. Tartışmaların özündeki gerilimleri, beklenmedik boyutları ve 'neden önemli' sorularını yanıtla.")
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

// truncate clips s to at most max Unicode code points, appending an ellipsis if needed.
func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "\u2026"
}
