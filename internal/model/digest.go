package model

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type Digest struct {
	ID          uint64 `gorm:"primarykey;autoIncrement"`
	CreatedAt   time.Time
	WindowStart time.Time `gorm:"index:idx_digests_window_start"`
	WindowEnd   time.Time
	Payload     datatypes.JSON
}

// DigestPayload is the structured output of the daily digest generation.
type DigestPayload struct {
	Headline      string         `json:"headline"`        // One punchy sentence capturing the day's theme
	Overview      string         `json:"overview"`        // 2-3 sentences: what's dominating discourse and why now
	TopStories    []StoryDigest  `json:"top_stories"`     // 3-5 key stories with context and analysis
	Debates       []DebateDigest `json:"debates"`         // Active debates with real, named opposing sides
	MoodSnapshot  string         `json:"mood_snapshot"`   // Nuanced description of the collective mood/tone
	NotableQuotes []NotableQuote `json:"notable_quotes"`  // 2-3 striking quotes that capture the moment
	UnderTheRadar string         `json:"under_the_radar"` // Something noteworthy that might be flying under the radar
}

type StoryDigest struct {
	Title        string `json:"title"`
	Summary      string `json:"summary"`        // What is being discussed
	WhyItMatters string `json:"why_it_matters"` // Significance, context, what it reveals
	Sentiment    string `json:"sentiment"`      // pozitif | negatif | karışık | nötr
}

type DebateDigest struct {
	Topic   string     `json:"topic"`
	SideA   DebateSide `json:"side_a"`
	SideB   DebateSide `json:"side_b"`
	Tension string     `json:"tension"` // The core of the disagreement in one sentence
}

type DebateSide struct {
	Label    string `json:"label"`    // Descriptive label for this camp (e.g. "hükümet eleştirmenleri")
	Argument string `json:"argument"` // Their main argument
	Quote    string `json:"quote"`    // A representative quote
}

type NotableQuote struct {
	Text    string `json:"text"`
	Author  string `json:"author"`
	Context string `json:"context"` // Why this quote stands out
}

// TopicBundle is a structured per-topic package fed to the LLM for digest synthesis.
// Each bundle contains the topic title and its entries pre-grouped into perspective clusters.
type TopicBundle struct {
	TopicTitle  string              `json:"topic_title"`
	Clusters    []PerspectiveCluster `json:"clusters"`
}

// PerspectiveCluster is one group of thematically similar entries within a topic.
type PerspectiveCluster struct {
	Entries []Entry `json:"-"` // raw entries; only representative fields sent to LLM
}

// Viewpoint is used by the query/chat path for embedding-based clustering.
type Viewpoint struct {
	Stance              string `json:"stance"`
	RepresentativeQuote string `json:"representative_quote"`
	Author              string `json:"author"`
	EntryCount          int    `json:"entry_count"`
}

func AddDigest(windowStart, windowEnd time.Time, payload DigestPayload) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	d := Digest{
		WindowStart: windowStart,
		WindowEnd:   windowEnd,
		Payload:     datatypes.JSON(b),
	}
	tx := database.Create(&d)
	if tx.Error != nil {
		slog.Error("couldn't insert digest", "error", tx.Error)
		return tx.Error
	}
	return nil
}

func GetLatestDigest() (*Digest, error) {
	var d Digest
	tx := database.Order("created_at desc").Limit(1).Find(&d)
	if tx.Error != nil {
		return nil, tx.Error
	}
	if d.ID == 0 {
		return nil, nil
	}
	return &d, nil
}

func updateEntryEmbedding(database *gorm.DB, id uint, embedding []float32, embeddedAt time.Time) error {
	tx := database.Exec(
		"UPDATE entries SET embedding = ?, embedding_at = ? WHERE id = ?",
		formatVector(embedding), embeddedAt, id,
	)
	if tx.Error != nil {
		slog.Error("couldn't update entry embedding", "id", id, "error", tx.Error)
		return tx.Error
	}
	return nil
}

// formatVector converts float32 slice to Postgres vector literal e.g. "[0.1,0.2,...]"
func formatVector(v []float32) string {
	b := make([]byte, 0, len(v)*8+2)
	b = append(b, '[')
	for i, f := range v {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, []byte(formatFloat32(f))...)
	}
	b = append(b, ']')
	return string(b)
}

func formatFloat32(f float32) string {
	return strconv.FormatFloat(float64(f), 'f', -1, 32)
}

func UpdateEntryEmbeddings(entries []Entry, embeddings [][]float32) error {
	now := time.Now().UTC()
	for i, entry := range entries {
		if i >= len(embeddings) {
			break
		}
		if err := updateEntryEmbedding(database, entry.ID, embeddings[i], now); err != nil {
			return err
		}
	}
	return nil
}
