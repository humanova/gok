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

type Filters struct {
	CreatedAfter  string
	CreatedBefore string
	QueryText     string
	Author        string
}

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

	err = database.AutoMigrate(&Entry{}, &Topic{}, &PopularTopic{}, &EntryAttachment{}, &Request{})

	if err != nil {
		slog.Error("couldn't create new table", "error", err)
		return nil, err
	}

	return database, nil
}

func createEntry(database *gorm.DB, entry Entry) error {
	tx := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&entry)

	if tx.Error != nil {
		slog.Error("couldn't insert entry", "error", tx.Error)
		return tx.Error
	}

	return nil
}

func createEntries(database *gorm.DB, entries []Entry) error {
	tx := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&entries)

	if tx.Error != nil {
		slog.Error("couldn't insert entries", "error", tx.Error)
		return tx.Error
	}

	return nil
}

func updateEntryScore(database *gorm.DB, entry Entry, score int64) error {
	tx := database.Model(&entry).Where("url = ?", entry.Url).Update("Score", score)
	if tx.Error != nil {
		slog.Error("couldn't update entry score", "error", tx.Error)
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

func getTopicEntries(database *gorm.DB, topicId uint64) ([]Entry, error) {
	var entries []Entry

	tx := database.Where("topic_id = ?", topicId).Find(&entries)
	if tx.Error != nil {
		slog.Error("couldn't query entries by topic", "topic_id", topicId, "error", tx.Error)
		return nil, tx.Error
	}

	return entries, nil
}

func getEntriesPublishedAfter(database *gorm.DB, timestamp int64) ([]Entry, error) {
	var entries []Entry

	tx := database.Where("timestamp > ?", timestamp).Find(&entries)
	if tx.Error != nil {
		slog.Error("couldn't query entries by timestamp", "timestamp", timestamp, "error", tx.Error)
		return nil, tx.Error
	}

	return entries, nil
}

func getEntriesUpdatedAfter(database *gorm.DB, qTime time.Time) ([]Entry, error) {
	var entries []Entry

	tx := database.Where("updated_at > ?", qTime).Find(&entries)
	if tx.Error != nil {
		slog.Error("couldn't query entries updated after", "time", qTime, "error", tx.Error)
		return nil, tx.Error
	}

	return entries, nil
}

func getEntriesFiltered(database *gorm.DB, filters Filters) ([]Entry, error) {
	var entries []Entry

	tx := database.Where("")
	if filters.CreatedAfter != "" {
		tx = tx.Where("timestamp > ?", filters.CreatedAfter)
	}
	if filters.CreatedBefore != "" {
		tx = tx.Where("timestamp < ?", filters.CreatedBefore)
	}
	if filters.Author != "" {
		tx = tx.Where("author ILIKE ?", fmt.Sprintf("%%%s%%", filters.Author))
	}
	if filters.QueryText != "" {
		tx = tx.Where("text ILIKE ? OR title ILIKE ?",
			fmt.Sprintf("%%%s%%", filters.QueryText),
			fmt.Sprintf("%%%s%%", filters.QueryText))
	}

	// if nothing is passed as a filter, return entries from last 12 hours
	if filters.CreatedAfter == "" && filters.CreatedBefore == "" &&
		filters.Author == "" && filters.QueryText == "" {
		tx = tx.Where("timestamp > ?", (time.Now().Add(-12 * time.Hour)).UTC().Unix())
	}

	tx = tx.Find(&entries)

	if tx.Error != nil {
		slog.Error("couldn't query entries with filters", "filters", filters, "error", tx.Error)
		return nil, tx.Error
	}

	return entries, nil
}

// -- Topic--
func createTopic(database *gorm.DB, topic Topic) error {
	tx := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&topic)

	if tx.Error != nil {
		slog.Error("couldn't insert topic", "error", tx.Error)
		return tx.Error
	}

	return nil
}

func createTopics(database *gorm.DB, topics []Topic) error {
	tx := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&topics)

	if tx.Error != nil {
		slog.Error("couldn't insert topics", "error", tx.Error)
		return tx.Error
	}

	return nil
}

// -- PopularTopic --
func createPopularTopic(database *gorm.DB, topic PopularTopic) error {
	tx := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&topic)

	if tx.Error != nil {
		slog.Error("couldn't insert popular topic", "error", tx.Error)
		return tx.Error
	}

	return nil
}

func createPopularTopics(database *gorm.DB, topics []PopularTopic) error {
	tx := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&topics)

	if tx.Error != nil {
		slog.Error("couldn't insert popular topics", "error", tx.Error)
		return tx.Error
	}

	return nil
}

func getPopularTopics(database *gorm.DB, topicId uint64) ([]PopularTopic, error) {
	var topics []PopularTopic

	tx := database.Where("topic_id = ?", topicId).Find(&topics)
	if tx.Error != nil {
		slog.Error("couldn't query popular topics", "topic_id", topicId, "error", tx.Error)
		return nil, tx.Error
	}

	return topics, nil
}

func getPopularTopicsAfter(database *gorm.DB, timestamp int64) ([]PopularTopic, error) {
	var topics []PopularTopic

	tx := database.Where("timestamp > ?", timestamp).Find(&topics)
	if tx.Error != nil {
		slog.Error("couldn't query popular topics after timestamp", "timestamp", timestamp, "error", tx.Error)
		return nil, tx.Error
	}

	return topics, nil
}

// -- EntryAttachment --
func createEntryAttachment(database *gorm.DB, topic EntryAttachment) error {
	tx := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&topic)

	if tx.Error != nil {
		slog.Error("couldn't insert entry attachment", "error", tx.Error)
		return tx.Error
	}

	return nil
}

func createEntryAttachments(database *gorm.DB, topics []EntryAttachment) error {
	tx := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&topics)

	if tx.Error != nil {
		slog.Error("couldn't insert entry attachments", "error", tx.Error)
		return tx.Error
	}

	return nil
}

// Request
func createRequest(database *gorm.DB, request Request) error {
	tx := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&request)

	if tx.Error != nil {
		slog.Error("couldn't insert request", "error", tx.Error)
		return tx.Error
	}

	return nil
}

func createRequests(database *gorm.DB, requests []Request) error {
	tx := database.Clauses(clause.OnConflict{DoNothing: true}).Create(&requests)

	if tx.Error != nil {
		slog.Error("couldn't insert requests", "error", tx.Error)
		return tx.Error
	}

	return nil
}
