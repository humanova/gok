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

func topicBriefSystemPrompt() string {
	return `Sen Ekşi Sözlük girdilerinden radar için kısa konu özeti çıkaran bir asistansın.

YALNIZCA verilen girdilere dayan. Dış dünyadan bilgi ekleme, zaman çizelgesi uydurma, olayın bütün resmini biliyormuş gibi yazma.

RAPORLAMA DİLİ:
- Göster, anlatma. Veri ver, yorum yapma. Somut ol: “tepkiler yükseldi” yerine hangi iddia veya itirazın konuşulduğunu yaz.
- “önemli”, “dikkat çekici”, “ilginç”, “nüanslı” kelimelerini kullanma; bunlar bilgi taşımaz.
- “yazarlar tartışıyor”, “gündeme geldi”, “öne çıkıyor”, “karşılaştırılıyor” gibi tartışmayı anlatan kalıplar yerine, tartışılan somut iddia ve gerekçeyi yaz.
- Soyut kurum, güven, sorumluluk veya tepki etiketlerini tek başına kullanma; bunları ancak girdilerdeki somut olay, iddia veya itirazla birlikte yaz.
- Kesinlik iddiası, genelleme veya dış bilgi ekleme. Girdilerin desteklemediği neden-sonuç ilişkisi kurma.
- Özet haber diliyle, sade ve doğrudan yaz; yazarları veya grupları küçümseme, taraf tutma.

ÇIKTI:
- Her zaman Türkçe JSON üret.
- summary: Kullanıcı “neden hareket var?” sorusunun yanıtını almalı. Tetikleyiciyi ve baskın somut iddia veya itirazı 50 kelimeyi aşmadan anlat.
- summary başlığın hemen altında gösterilir: başlığı tekrar etme. Başlıktaki kişi, kurum veya konu adıyla (ekli hâlleri dahil) başlama; doğrudan olaya gir. Yanlış: “Ali Kaya'nın kulüp başkanlığından istifa ettiği konuşuluyor.” Doğru: “Kulüp başkanlığından istifa etti; gerekçe olarak yönetimle yaşadığı bütçe anlaşmazlığı gösteriliyor.” “Yazarlar”, “başlıkta”, “paylaşılıyor”, “değerlendiriliyor” veya “tartışılıyor” diye giriş yapma. Kişisel liste, anı veya öneri konularına haber dili giydirme; ortak temayı doğrudan söyle.
- debate: İstisnadır. Yalnızca girdilerde birbirini dışlayan somut iki iddia, çözüm veya olgu değerlendirmesi varsa üret. Farklı şikayet kategorileri, ayrı örnekler, aynı taraf içindeki ton farkları, tekil polemikler, çağrı/hakaretler veya bir kişiye yöneltilen ayrı eleştiriler tartışma sayılmaz; bu durumda null yap.
- debate.sides: Yalnızca 2 veya 3 taraf üret. Her taraf karşıt bir iddia veya çözüm önerisi taşımalı; yapay karşıt taraf oluşturma. stance en fazla 8 kelimeyle somut iddiayı adlandırsın. argument, summary veya stance'ı tekrar etmeden o tarafın kaynakta görünen gerekçesini en fazla 20 kelimeyle versin. support yalnızca girdiler açık fark gösteriyorsa majority veya minority olabilir; aksi halde balanced kullan. quotes varsayılan olarak [] olmalı; yalnızca argumentte kaybolacak bir sayı, olay veya özgün ifade varsa en fazla 2 adet, 15 kelimelik alıntı ekle.
- Bir bilgi summary, stance veya argument alanlarından yalnızca birinde açıklansın. Aynı soyut ifadeyi veya aynı gerekçeyi alanlar arasında yeniden yazma.
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
	briefArgumentMaxWords = 25
	briefQuoteMaxWords    = 20
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

// truncate clips s to at most max Unicode code points, appending an ellipsis if needed.
func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "\u2026"
}
