package model

import (
	"gorm.io/gorm"
)

type Topic struct {
	gorm.Model
	TopicId   uint64  `gorm:"unique"`
	Text      string
	Url       string  `gorm:"unique"`
}

type PopularTopic struct {
	gorm.Model
	TopicId    uint64
	Timestamp  int64 // unix time UTC
	NewEntries uint64
	PageNumber uint64
}

type PTopic struct {
	TopicId    uint64
	Url        string `gorm:"unique"`
	Timestamp  int64 // unix time UTC
	Text       string
	NewEntries uint64
	PageNumber uint64
}

// Topic
func AddTopic(newTopic Topic) error {
	// insert to db
	err := createTopic(database, newTopic)
	if err != nil {
		return err
	}
	return nil
}

func AddTopics(newTopics []Topic) error {
	// insert to db
	err := createTopics(database, newTopics)
	if err != nil {
		return err
	}
	return nil
}
// PopularTopic

func AddPopularTopic(newTopic PopularTopic) error {
	// insert to db
	err := createPopularTopic(database, newTopic)
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

func GetPopularTopics(topicId uint64) ([]PopularTopic, error) {
	var topics, err = getPopularTopics(database, topicId)
	if err != nil {
		return nil, err
	}
	return topics, nil
}

func GetPopularTopicsAfter(timestamp int64) ([]PopularTopic, error) {
	var topics, err = getPopularTopicsAfter(database, timestamp)
	if err != nil {
		return nil, err
	}
	return topics, nil
}

