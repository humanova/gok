package model

import (
	"gorm.io/gorm"
)

type Entry struct {
	gorm.Model
	EntryId   uint64 `gorm:"unique"`
	Timestamp int64  // unix time UTC
	Author    string
	Text      string
	Url       string `gorm:"unique"`
	Score     int64
	TopicId   uint64
}

func InitDb() error {
	db, err := prepareDb()
	if err != nil {
		return err
	}
	database = db
	return nil
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
