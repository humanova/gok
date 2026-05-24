package model

import (
	"time"

	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
)

type Entry struct {
	gorm.Model
	EntryId     uint64 `gorm:"unique;index:idx_entries_topic_entry,priority:2,sort:desc"`
	Timestamp   int64  `gorm:"index:idx_entries_timestamp"` // unix time UTC
	Author      string
	Text        string
	Url         string `gorm:"unique"`
	Score       int64
	TopicId     uint64           `gorm:"index:idx_entries_topic_entry,priority:1"`
	Embedding   *pgvector.Vector `gorm:"type:vector(384)"`
	EmbeddingAt *time.Time
}

func InitDb() error {
	db, err := prepareDb()
	if err != nil {
		return err
	}
	database = db
	return nil
}

// DB returns the underlying *gorm.DB for use by packages that need raw queries (e.g. rag).
func DB() *gorm.DB {
	return database
}

// GetRecentTopicEntries returns the most recent entries for a topic published after the given unix timestamp.
// Results are ordered newest-first and capped at limit.
func GetRecentTopicEntries(topicID uint64, since int64, limit int) ([]Entry, error) {
	var entries []Entry
	tx := database.
		Where("topic_id = ? AND timestamp > ? AND deleted_at IS NULL", topicID, since).
		Order("entry_id DESC").
		Limit(limit).
		Find(&entries)
	if tx.Error != nil {
		return nil, tx.Error
	}
	return entries, nil
}

func AddEntry(newEntry Entry) error {
	// insert to db
	err := createEntry(database, newEntry)
	if err != nil {
		return err
	}

	return nil
}

func AddEntries(newEntries []Entry) error {
	// insert to db
	err := createEntries(database, newEntries)
	if err != nil {
		return err
	}
	return nil
}

func GetLastTopicEntry(topicId uint64) (Entry, error) {
	var entry, err = getLastTopicEntry(database, topicId)
	if err != nil {
		return entry, err
	}
	return entry, nil
}

func GetTopicEntries(topicId uint64) ([]Entry, error) {
	var entries, err = getTopicEntries(database, topicId)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func GetEntriesSince(timestamp int64) ([]Entry, error) {
	var entries, err = getEntriesPublishedAfter(database, timestamp)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func GetEntriesFiltered(filters Filters) ([]Entry, error) {
	var entries, err = getEntriesFiltered(database, filters)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func UpdateScore(entry Entry, score int64) error {
	err := updateEntryScore(database, entry, score)
	if err != nil {
		return err
	}
	return nil
}
