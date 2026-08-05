package model

import (
	"fmt"
	"log"
	"log/slog"
	"os"
	"time"

	"gok/internal/config"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var database *gorm.DB

func prepareDb() (*gorm.DB, error) {
	dbLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		logger.Config{
			SlowThreshold:             time.Second,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)

	dsn := fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%d sslmode=%s TimeZone=UTC",
		config.Config.DbHost, config.Config.DbUser, config.Config.DbPassword, config.Config.DbName,
		config.Config.DbPort, config.Config.DbSSLMode)

	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: dbLogger,
	})
	if err != nil {
		slog.Error("couldn't connect to db", "error", err)
		return nil, err
	}

	// Enable pgvector extension
	if err := database.Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		slog.Warn("could not create pgvector extension (may already exist)", "error", err)
	}

	// Enable unaccent for diacritic-insensitive search (e.g. "ozan guven" → "ozan güven")
	if err := database.Exec("CREATE EXTENSION IF NOT EXISTS unaccent").Error; err != nil {
		slog.Warn("could not create unaccent extension", "error", err)
	}
	if err := database.Exec("CREATE EXTENSION IF NOT EXISTS pg_trgm").Error; err != nil {
		slog.Warn("could not create pg_trgm extension", "error", err)
	}

	// AutoMigrate digest tables. Existing scrape tables managed by the scraper binary
	// are left untouched to avoid constraint-name conflicts across GORM versions.
	if err = database.AutoMigrate(&Digest{}, &TopicBrief{}); err != nil {
		slog.Error("couldn't migrate digest tables", "error", err)
		return nil, err
	}

	// Idempotently add embedding columns if this is the first run after the feature was added.
	// These are no-ops once the columns exist, so the overhead is negligible.
	database.Exec(`ALTER TABLE entries ADD COLUMN IF NOT EXISTS embedding vector(384)`)
	database.Exec(`ALTER TABLE entries ADD COLUMN IF NOT EXISTS embedding_at timestamptz`)

	// HNSW index for vector similarity search
	if err := database.Exec(`CREATE INDEX IF NOT EXISTS idx_entries_embedding
		ON entries USING hnsw (embedding vector_cosine_ops)`).Error; err != nil {
		slog.Warn("could not create HNSW index", "error", err)
	}

	// GIN index for BM25-style full-text search using the Turkish text search configuration.
	// Built CONCURRENTLY so it never takes a write lock on the (large) entries table.
	if err := database.Exec(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_entries_fts
		ON entries USING gin (to_tsvector('turkish', coalesce(text,'')))`).Error; err != nil {
		slog.Warn("could not create GIN FTS index", "error", err)
	}

	// Archive search uses keyset pagination and typeahead. These indexes keep
	// timestamp traversal and author/topic substring matching off sequential scans.
	if err := database.Exec(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_entries_timestamp_id
		ON entries (timestamp DESC, id DESC) WHERE deleted_at IS NULL`).Error; err != nil {
		slog.Warn("could not create entry pagination index", "error", err)
	}
	if err := database.Exec(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_entries_author_trgm
		ON entries USING gin (author gin_trgm_ops) WHERE deleted_at IS NULL`).Error; err != nil {
		slog.Warn("could not create author search index", "error", err)
	}
	if err := database.Exec(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_popular_topics_topic_timestamp
		ON popular_topics (topic_id, timestamp) WHERE deleted_at IS NULL`).Error; err != nil {
		slog.Warn("could not create popular topic history index", "error", err)
	}
	if err := database.Exec(`CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_topics_text_trgm
		ON topics USING gin (text gin_trgm_ops) WHERE deleted_at IS NULL`).Error; err != nil {
		slog.Warn("could not create topic search index", "error", err)
	}

	return database, nil
}

func createEntries(database *gorm.DB, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	tx := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&entries)

	if tx.Error != nil {
		slog.Error("couldn't insert entries", "error", tx.Error)
		return tx.Error
	}

	return nil
}

func getLastTopicEntry(database *gorm.DB, topicId uint64) (Entry, error) {
	var entry Entry

	tx := database.Select("entry_id").Order("entry_id desc").Where("topic_id = ?", topicId).Limit(1).Find(&entry)
	if tx.Error != nil {
		//log.Println(fmt.Sprintf("[DB] couldn't query any entry with given topic id(%d) : %s\n", topicId, tx.Error))
		return entry, tx.Error
	}

	return entry, nil
}

func createTopics(database *gorm.DB, topics []Topic) error {
	if len(topics) == 0 {
		return nil
	}
	tx := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&topics)

	if tx.Error != nil {
		slog.Error("couldn't insert topics", "error", tx.Error)
		return tx.Error
	}

	return nil
}

func createPopularTopics(database *gorm.DB, topics []PopularTopic) error {
	if len(topics) == 0 {
		return nil
	}
	tx := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&topics)

	if tx.Error != nil {
		slog.Error("couldn't insert popular topics", "error", tx.Error)
		return tx.Error
	}

	return nil
}

func createEntryAttachments(database *gorm.DB, topics []EntryAttachment) error {
	if len(topics) == 0 {
		return nil
	}
	tx := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&topics)

	if tx.Error != nil {
		slog.Error("couldn't insert entry attachments", "error", tx.Error)
		return tx.Error
	}

	return nil
}

// getTopicsWithEntryTimestampsSince returns every topic that has a non-deleted
// entry since the given unix timestamp, along with its timestamps in ascending
// order. It selects only pulse data; entry text and author data never leave the
// database for this query.
func getTopicsWithEntryTimestampsSince(db *gorm.DB, since int64) (map[uint64][]int64, []Topic, error) {
	type row struct {
		TopicId   uint64
		Timestamp int64
		Text      string
		Url       string
	}

	var rows []row
	tx := db.Model(&Entry{}).
		Select("entries.topic_id, entries.timestamp, topics.text, topics.url").
		Joins("JOIN topics ON topics.topic_id = entries.topic_id").
		Where("entries.timestamp > ? AND entries.deleted_at IS NULL AND topics.deleted_at IS NULL", since).
		Order("entries.timestamp ASC").
		Scan(&rows)
	if tx.Error != nil {
		return nil, nil, tx.Error
	}

	timestamps := make(map[uint64][]int64)
	topicByID := make(map[uint64]Topic)
	for _, r := range rows {
		timestamps[r.TopicId] = append(timestamps[r.TopicId], r.Timestamp)
		if _, ok := topicByID[r.TopicId]; !ok {
			topicByID[r.TopicId] = Topic{TopicId: r.TopicId, Text: r.Text, Url: r.Url}
		}
	}

	topics := make([]Topic, 0, len(topicByID))
	for _, topic := range topicByID {
		topics = append(topics, topic)
	}
	return timestamps, topics, nil
}

// getFirstPopularTimestamps returns the first recorded popular-list appearance
// for each requested topic.
func getFirstPopularTimestamps(db *gorm.DB, topicIDs []uint64) (map[uint64]int64, error) {
	if len(topicIDs) == 0 {
		return map[uint64]int64{}, nil
	}
	type row struct {
		TopicId   uint64
		Timestamp int64
	}
	var rows []row
	tx := db.Model(&PopularTopic{}).
		Select("topic_id, MIN(timestamp) AS timestamp").
		Where("topic_id IN ?", topicIDs).
		Group("topic_id").
		Scan(&rows)
	if tx.Error != nil {
		return nil, tx.Error
	}

	firstSeen := make(map[uint64]int64, len(rows))
	for _, row := range rows {
		firstSeen[row.TopicId] = row.Timestamp
	}
	return firstSeen, nil
}

func createRequests(database *gorm.DB, requests []Request) error {
	if len(requests) == 0 {
		return nil
	}
	tx := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&requests)

	if tx.Error != nil {
		slog.Error("couldn't insert requests", "error", tx.Error)
		return tx.Error
	}

	return nil
}
