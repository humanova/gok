package model

import (
	"time"

	"gorm.io/gorm/clause"
)

// TopicAgenda scores whether a topic is about something happening now (news,
// politics, economy, incidents) rather than sports or everyday chat. Score is
// the yes probability; topics are decided again once the score is a day old.
type TopicAgenda struct {
	TopicID   uint64 `gorm:"primaryKey;autoIncrement:false"`
	Score     float64
	Model     string
	DecidedAt time.Time
}

// GetTopicAgendas returns the stored scores of the given topics.
func GetTopicAgendas(topicIDs []uint64) (map[uint64]TopicAgenda, error) {
	out := make(map[uint64]TopicAgenda, len(topicIDs))
	if len(topicIDs) == 0 {
		return out, nil
	}
	var rows []TopicAgenda
	if err := database.Where("topic_id IN ?", topicIDs).Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.TopicID] = row
	}
	return out, nil
}

func SaveTopicAgenda(row *TopicAgenda) error {
	return database.Clauses(clause.OnConflict{UpdateAll: true}).Create(row).Error
}

// GetFirstTopicEntries returns a topic's first entries, oldest first.
func GetFirstTopicEntries(topicID uint64, limit int) ([]Entry, error) {
	var entries []Entry
	err := database.Where("topic_id = ? AND deleted_at IS NULL", topicID).Order("entry_id").Limit(limit).Find(&entries).Error
	return entries, err
}
