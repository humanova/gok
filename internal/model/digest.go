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
	ID          uint `gorm:"primarykey"`
	CreatedAt   time.Time
	WindowStart time.Time `gorm:"index"`
	WindowEnd   time.Time
	Payload     datatypes.JSON
}

type DigestPayload struct {
	Summary           string      `json:"summary"`
	DominantSentiment string      `json:"dominant_sentiment"`
	KeyViewpoints     []Viewpoint `json:"key_viewpoints"`
	TrendingTopics    []string    `json:"trending_topics"`
}

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
	// Store as raw float array in the vector column using raw SQL to avoid driver type issues
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
