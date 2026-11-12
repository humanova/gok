package model

import "gorm.io/gorm"

type Topic struct {
	gorm.Model
	TopicId uint64 `gorm:"uniqueIndex:,sort:desc"`
	Text    string
	Url     string `gorm:"unique"`
}

type PopularTopic struct {
	gorm.Model
	TopicId    uint64
	Timestamp  int64 `gorm:"index:idx_popular_topics_timestamp"` // unix time UTC
	NewEntries uint64
	PageNumber uint64
}

type PTopic struct {
	TopicId    uint64
	Url        string `gorm:"unique"`
	Timestamp  int64  // unix time UTC
	Text       string
	NewEntries uint64
	PageNumber uint64
}

func AddTopics(newTopics []Topic) error {
	// insert to db
	err := createTopics(database, newTopics)
	if err != nil {
		return err
	}
	return nil
}

func AddPopularTopics(newTopics []PopularTopic) error {
	// insert to db
	err := createPopularTopics(database, newTopics)
	if err != nil {
		return err
	}
	return nil
}

// GetFirstPopularTimestamps returns the first recorded popular-list appearance
// for each requested topic.
func GetFirstPopularTimestamps(topicIDs []uint64) (map[uint64]int64, error) {
	return getFirstPopularTimestamps(database, topicIDs)
}
