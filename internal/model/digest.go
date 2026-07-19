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

// TopicBrief is a pre-generated, topic-specific explanation for the radar UI.
// It deliberately stores only generated JSON and source-window metadata; the
// browser never needs the underlying entry text to display it.
type TopicBrief struct {
	ID          uint64 `gorm:"primarykey;autoIncrement"`
	CreatedAt   time.Time
	TopicID     uint64    `gorm:"index:idx_topic_briefs_topic_generated,priority:1"`
	GeneratedAt time.Time `gorm:"index:idx_topic_briefs_topic_generated,priority:2,sort:desc"`
	WindowStart time.Time
	WindowEnd   time.Time
	EntryCount  int
	Payload     datatypes.JSON
}

// TopicBriefPayload contains a concise explanation and, only when supported
// by the sampled entries, an optional debate breakdown.
type TopicBriefPayload struct {
	Summary string       `json:"summary"`
	Debate  *TopicDebate `json:"debate,omitempty"`
}

type TopicDebate struct {
	Sides []DebateSide `json:"sides"`
}

// DigestPayload is the structured output of the daily digest generation.
type DigestPayload struct {
	Headline   string               `json:"headline"`   // 10-word max mood/theme line
	Stories    []Story              `json:"stories"`    // Compact stories with expandable detail
	QuickHits  []string             `json:"quick_hits"` // Brief mentions of other topics
	Mood       string               `json:"mood"`       // 5-word emotional snapshot
	Expansions map[string]Expansion `json:"expansions"` // Full context for each story
}

// Story is the compact view of a digest entry
type Story struct {
	ID         string `json:"id"`             // story_1, story_2, etc.
	Topic      string `json:"topic"`          // Topic title (5-8 words)
	Line       string `json:"line"`           // What happened in one sentence (25 words max)
	Hook       string `json:"hook,omitempty"` // Optional quote/data/twist (15 words max)
	Type       string `json:"type"`           // debate | event | trend
	Expandable bool   `json:"expandable"`     // Has expansion data
}

// Expansion provides full context for a story
type Expansion struct {
	Type      string       `json:"type"`                // debate | event | trend
	Context   string       `json:"context"`             // 50-word background
	Sides     []DebateSide `json:"sides,omitempty"`     // For debates
	Timeline  []string     `json:"timeline,omitempty"`  // For events
	Reactions []string     `json:"reactions,omitempty"` // Key quotes
}

// DebateSide represents one position in a debate
type DebateSide struct {
	Stance   string   `json:"stance"`   // Descriptive label (not "görüş 1")
	Argument string   `json:"argument"` // 20-word synthesis
	Quotes   []string `json:"quotes"`   // 1-2 representative quotes
	Support  string   `json:"support"`  // majority | minority | balanced
}

// TopicBundle is a structured per-topic package fed to the LLM for digest synthesis.
// Each bundle contains the topic title and its entries (no pre-clustering).
type TopicBundle struct {
	TopicTitle string  `json:"topic_title"`
	Entries    []Entry `json:"entries"`
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

func AddTopicBrief(topicID uint64, windowStart, windowEnd time.Time, entryCount int, payload TopicBriefPayload) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	brief := TopicBrief{
		TopicID:     topicID,
		GeneratedAt: time.Now().UTC(),
		WindowStart: windowStart,
		WindowEnd:   windowEnd,
		EntryCount:  entryCount,
		Payload:     datatypes.JSON(b),
	}
	if err := database.Create(&brief).Error; err != nil {
		slog.Error("couldn't persist topic brief", "topic_id", topicID, "error", err)
		return err
	}
	return nil
}

// GetLatestTopicBrief returns the most recently generated stored brief for a topic.
func GetLatestTopicBrief(topicID uint64) (*TopicBrief, error) {
	var brief TopicBrief
	tx := database.Where("topic_id = ?", topicID).Order("generated_at desc").Limit(1).Find(&brief)
	if tx.Error != nil {
		return nil, tx.Error
	}
	if brief.ID == 0 {
		return nil, nil
	}
	return &brief, nil
}

// LatestTopicBriefGeneratedAt returns the most recent topic-brief generation time.
func LatestTopicBriefGeneratedAt() (*time.Time, error) {
	var brief TopicBrief
	tx := database.Order("generated_at desc").Limit(1).Find(&brief)
	if tx.Error != nil {
		return nil, tx.Error
	}
	if brief.ID == 0 {
		return nil, nil
	}
	return &brief.GeneratedAt, nil
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
