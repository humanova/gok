package model

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

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

// DebateSide represents one position in a debate
type DebateSide struct {
	Stance   string   `json:"stance"`   // Descriptive label (not "görüş 1")
	Argument string   `json:"argument"` // 20-word synthesis
	Quotes   []string `json:"quotes"`   // 1-2 representative quotes
	Support  string   `json:"support"`  // majority | minority | balanced
}

// TopicBundle is a structured per-topic package fed to the LLM for a topic brief.
// Each bundle contains the topic title and its entries (no pre-clustering).
type TopicBundle struct {
	TopicTitle string  `json:"topic_title"`
	Entries    []Entry `json:"entries"`
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
