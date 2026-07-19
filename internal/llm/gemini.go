package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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

// SynthesizeTopicBrief produces one concise stored explanation for a radar topic.
func (c *Client) SynthesizeTopicBrief(ctx context.Context, bundle model.TopicBundle) (*model.TopicBriefPayload, error) {
	prompt := buildTopicBriefPrompt(bundle)
	fullPrompt := topicBriefSystemPrompt() + "\n\n" + prompt

	contents := []*genai.Content{genai.NewContentFromText(fullPrompt, genai.RoleUser)}
	resp, err := c.client.Models.GenerateContent(ctx, c.modelName, contents,
		&genai.GenerateContentConfig{ResponseMIMEType: "application/json"})
	if err != nil {
		return nil, fmt.Errorf("topic brief gemini call failed: %w", err)
	}

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return nil, fmt.Errorf("empty topic brief response")
	}
	var raw strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		raw.WriteString(part.Text)
	}

	var payload model.TopicBriefPayload
	if err := json.Unmarshal([]byte(raw.String()), &payload); err != nil {
		return nil, fmt.Errorf("parsing topic brief: %w", err)
	}
	if err := validateTopicBriefPayload(payload); err != nil {
		return nil, fmt.Errorf("invalid topic brief response: %w", err)
	}
	return &payload, nil
}

// SynthesizeQuery answers a free-form user question given retrieved context.
func (c *Client) SynthesizeQuery(ctx context.Context, query string, entries []model.Entry, viewpoints []model.Viewpoint) (*model.DigestPayload, error) {
	prompt := buildQueryPrompt(query, entries, viewpoints)
	return c.callStructured(ctx, prompt)
}

// StreamAnswer streams a focused Q&A answer (non-digest, no JSON, cites authors).
func (c *Client) StreamAnswer(ctx context.Context, question string, entries []model.Entry, viewpoints []model.Viewpoint) (<-chan string, <-chan error) {
	tokenCh := make(chan string, 64)
	errCh := make(chan error, 1)

	go func() {
		defer close(tokenCh)
		defer close(errCh)

		prompt := buildAnswerPrompt(question, entries, viewpoints)
		fullPrompt := qaSystemPrompt() + "\n\n" + prompt

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
	return `Sen bir haber özet botusun. Kullanıcılar sabahları 90 saniyede günü öğrenmek istiyor.

ÇIKTI KURALLARI:
- Compact view: Her hikaye 1 satır (25 kelime max) + opsiyonel kanca (15 kelime max)
- Expansion view: 50 kelime bağlam + tartışma/olay detayları
- Yasak kelimeler: "önemli", "dikkat çekici", "ilginç", "nüanslı" — bunlar boş
- Kural: Göster, anlatma. Veri ver, yorum yapma. Somut ol: "Fiyatlar arttı" değil "Ekmeğe %40 zam"

TARTIŞMA TESPİTİ:
- Eğer bir konuda kümelenmeler varsa ve dengeli dağılım (en az 2 küme):
  → type: "debate", her tarafa gerçek stance etiketi ver (örn: "Zam Yeterli" vs "Zam Yetersiz")
- Eğer belirli bir olay/gelişme/açıklama gündemin tetikçisiyse (kaza, karar, konuşma, duyuru):
  → type: "event"; timeline ile anlat
- Eğer tetikleyici tek bir olay değil, organik birikim/kültürel moment/gündem kaymasıysa (meme, tartışma trendi, sosyal gözlem):
  → type: "trend"; reactions ile anlat
- Stance etiketleri açıklayıcı olmalı, "görüş 1" gibi genel etiketler kullanma

EXPANSION BAĞLAMI:
- context alanında line ve hook'ta geçmeyen ek bilgi ver: isimler, rakamlar, arka plan, önceki gelişmeler
- line/hook'u tekrar etme; context onları tamamlamalı, özetlememeli
- Timeline varsa zamanları ekle (örn: "12:00 X oldu", "14:30 Y açıklama yaptı")
- Alıntılar kısa ve vurucu (15 kelime max)
- Debate için: Her tarafın argümanını 20 kelimeyle özetle + 1-2 alıntı

KELİME SINIRLARI (kesinlikle aşma):
- headline: 10 kelime
- story.line: 25 kelime
- story.hook: 15 kelime (opsiyonel)
- mood: 5 kelime
- expansion.context: 50 kelime (line/hook'ı tekrar etme)
- expansion debate argument: 20 kelime
- expansion quotes: 15 kelime

Her zaman Türkçe yaz.

JSON çıktı şeması:
{
  "headline": "10-kelime günün ruhu",
  "stories": [
    {
      "id": "story_1",
      "topic": "Konu başlığı (5-8 kelime)",
      "line": "Ne oldu (25 kelime)",
      "hook": "Opsiyonel quote/data/twist (15 kelime)",
      "type": "debate|event|trend",
      "expandable": true
    }
  ],
  "quick_hits": ["Kısa konu: 3-5 kelime"],
  "mood": "5-kelime duygusal snapshot",
  "expansions": {
    "story_1": {
      "type": "debate",
      "context": "50-kelime — line/hook'ta olmayan isimler, rakamlar, arka plan",
      "sides": [
        {
          "stance": "Açıklayıcı etiket",
          "argument": "20-kelime argüman",
          "quotes": ["alıntı 1", "alıntı 2"],
          "support": "majority|minority|balanced"
        }
      ]
    },
    "story_2": {
      "type": "event",
      "context": "50-kelime — line/hook'ta olmayan isimler, rakamlar, arka plan",
      "timeline": ["12:00 X", "14:30 Y"],
      "reactions": ["alıntı 1", "alıntı 2"]
    }
  }
}`
}

func buildDigestPrompt(bundles []model.TopicBundle) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Aşağıda %d adet güncel popüler konu var. ", len(bundles)))
	sb.WriteString("Her konu için girdiler verilmiştir. LLM tartışma yapısını organik olarak belirleyecek.\n")
	sb.WriteString("Compact + expansion formatında özet oluştur. Kelime sınırlarını kesinlikle aşma.\n\n")

	for i, bundle := range bundles {
		sb.WriteString(fmt.Sprintf("### Konu %d: %s\n", i+1, bundle.TopicTitle))
		sb.WriteString(fmt.Sprintf("(%d girdi)\n\n", len(bundle.Entries)))

		for ei, e := range bundle.Entries {
			if ei >= 30 { // max 30 entries per topic shown in prompt
				sb.WriteString(fmt.Sprintf("...ve %d girdi daha\n", len(bundle.Entries)-30))
				break
			}
			timestamp := time.Unix(e.Timestamp, 0).Format("15:04")
			sb.WriteString(fmt.Sprintf("  - [%s, %s]: %s\n", e.Author, timestamp, truncate(e.Text, 300)))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Her story için:\n")
	sb.WriteString("1. Compact: id, topic, line (25 kelime), hook (opsiyonel, 15 kelime), type (debate/event/trend)\n")
	sb.WriteString("2. Expansion: context (50 kelime), sides/timeline/reactions type'a göre\n")
	sb.WriteString("3. Debate detection: Eğer girdilerde karşıt görüşler varsa → debate, değilse → event/trend\n")
	sb.WriteString("4. Quick hits: Önemli ama ana hikaye olmayan 3-5 konu için kısa etiketler\n")
	return sb.String()
}

func topicBriefSystemPrompt() string {
	return `Sen Ekşi Sözlük girdilerinden radar için kısa konu özeti çıkaran bir asistansın.

YALNIZCA verilen girdilere dayan. Dış dünyadan bilgi ekleme, zaman çizelgesi uydurma, olayın bütün resmini biliyormuş gibi yazma.

RAPORLAMA DİLİ:
- Göster, anlatma. Veri ver, yorum yapma. Somut ol: “tepkiler yükseldi” yerine hangi iddia veya itirazın konuşulduğunu yaz.
- “önemli”, “dikkat çekici”, “ilginç”, “nüanslı” kelimelerini kullanma; bunlar bilgi taşımaz.
- Kesinlik iddiası, genelleme veya dış bilgi ekleme. Girdilerin desteklemediği neden-sonuç ilişkisi kurma.
- Özet haber diliyle, sade ve doğrudan yaz; yazarları veya grupları küçümseme, taraf tutma.

ÇIKTI:
- Her zaman Türkçe JSON üret.
- summary: Konuda ne konuşulduğunu 50 kelimeyi aşmadan, somut ve tarafsız anlat.
- debate: Yalnızca girdilerde belirgin ve karşıt iki veya daha fazla görüş varsa üret. Yoksa null yap.
- debate.sides: Yalnızca 2 veya 3 taraf. stance en fazla 8 kelimeyle açıklayıcı, argument en fazla 20 kelime, support majority|minority|balanced, quotes en fazla 2 adet ve her biri en fazla 15 kelime.
- Tartışma yoksa “trend”, “event”, “timeline”, “reactions” gibi alanlar üretme.
- Alıntılar girdilerden türetilmeli, yeni iddia eklememeli.

JSON şeması:
{
  "summary": "Kısa konu özeti",
  "debate": null veya {
    "sides": [
      {
        "stance": "Açıklayıcı taraf etiketi",
        "argument": "Kısa argüman",
        "support": "majority|minority|balanced",
        "quotes": ["kısa alıntı"]
      }
    ]
  }
}`
}

const (
	briefSummaryMaxWords  = 50
	briefStanceMaxWords   = 8
	briefArgumentMaxWords = 20
	briefQuoteMaxWords    = 15
	briefMaxSides         = 3
	briefMaxQuotes        = 2
)

func validateTopicBriefPayload(payload model.TopicBriefPayload) error {
	if err := requireWords("summary", payload.Summary, 1, briefSummaryMaxWords); err != nil {
		return err
	}
	if payload.Debate == nil {
		return nil
	}
	if len(payload.Debate.Sides) < 2 || len(payload.Debate.Sides) > briefMaxSides {
		return fmt.Errorf("debate must contain 2-%d sides", briefMaxSides)
	}

	for i, side := range payload.Debate.Sides {
		if err := requireWords(fmt.Sprintf("debate side %d stance", i+1), side.Stance, 1, briefStanceMaxWords); err != nil {
			return err
		}
		if err := requireWords(fmt.Sprintf("debate side %d argument", i+1), side.Argument, 1, briefArgumentMaxWords); err != nil {
			return err
		}
		if len(side.Quotes) > briefMaxQuotes {
			return fmt.Errorf("debate side %d has more than %d quotes", i+1, briefMaxQuotes)
		}
		if side.Support != "majority" && side.Support != "minority" && side.Support != "balanced" {
			return fmt.Errorf("debate side %d has invalid support %q", i+1, side.Support)
		}
		for quoteIndex, quote := range side.Quotes {
			if err := requireWords(fmt.Sprintf("debate side %d quote %d", i+1, quoteIndex+1), quote, 1, briefQuoteMaxWords); err != nil {
				return err
			}
		}
	}
	return nil
}

func requireWords(field, value string, minWords, maxWords int) error {
	count := len(strings.Fields(value))
	if count < minWords || count > maxWords {
		return fmt.Errorf("%s must contain %d-%d words, got %d", field, minWords, maxWords, count)
	}
	return nil
}

func buildTopicBriefPrompt(bundle model.TopicBundle) string {
	var sb strings.Builder
	sb.WriteString("## Konu\n")
	sb.WriteString(bundle.TopicTitle)
	sb.WriteString("\n\n## Son girdiler\n")
	for i, entry := range bundle.Entries {
		if i >= 40 {
			break
		}
		timestamp := time.Unix(entry.Timestamp, 0).Format("02.01 15:04")
		sb.WriteString(fmt.Sprintf("[%s, %s] %s\n---\n", entry.Author, timestamp, truncate(entry.Text, 500)))
	}
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

func qaSystemPrompt() string {
	return `Sen Ekşi Sözlük girdilerine dayalı çalışan bir soru-cevap asistanısın.

KURALLAR:
- Soruyu doğrudan ve net yanıtla; günlük Türkçe kullan
- Cevabını girdi yazarlarından alıntılarla destekle; alıntıyı "yazar_adı: '...'" formatında göster
- Farklı görüşler varsa hepsini dengeli biçimde aktar, kendi yorumunu katma
- Yeterli bilgi yoksa bunu açıkça söyle
- Uzun cevaplar için paragraf kullan; liste/başlık yalnızca gerçekten gerekiyorsa
- JSON çıktısı üretme; düz metin yaz`
}

func buildAnswerPrompt(question string, entries []model.Entry, viewpoints []model.Viewpoint) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Soru\n%s\n\n", question))
	sb.WriteString(fmt.Sprintf("## Girdiler (%d adet)\n", len(entries)))
	for i, e := range entries {
		if i >= 60 {
			sb.WriteString(fmt.Sprintf("...ve %d girdi daha\n", len(entries)-60))
			break
		}
		ts := time.Unix(e.Timestamp, 0).Format("02.01.2006 15:04")
		sb.WriteString(fmt.Sprintf("[%s, %s]: %s\n---\n", e.Author, ts, truncate(e.Text, 500)))
	}
	if len(viewpoints) > 0 {
		sb.WriteString("\n## Öne Çıkan Görüşler\n")
		for _, v := range viewpoints {
			sb.WriteString(fmt.Sprintf("- %s: \"%s\" — %s\n", v.Stance, v.RepresentativeQuote, v.Author))
		}
	}
	sb.WriteString("\nYukarıdaki girdilere dayanarak soruyu yanıtla.")
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
