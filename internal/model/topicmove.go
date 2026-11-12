package model

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TopicMove records that Ekşi moved entries from one topic to another, which
// moderators do when they fix a title. The entries follow the move; an old
// topic left without entries is hidden from stories.
type TopicMove struct {
	FromTopicID uint64 `gorm:"primaryKey;autoIncrement:false"`
	ToTopicID   uint64 `gorm:"primaryKey;autoIncrement:false"`
	Entries     int
	DetectedAt  time.Time
}

// recordTopicMoves finds entries already stored under another topic than the
// one they were just scraped from and records each such move.
func recordTopicMoves(database *gorm.DB, entries []Entry) error {
	ids := make([]uint64, len(entries))
	topicOf := make(map[uint64]uint64, len(entries))
	for i, entry := range entries {
		ids[i] = entry.EntryId
		topicOf[entry.EntryId] = entry.TopicId
	}
	var stored []Entry
	if err := database.Select("entry_id", "topic_id").Where("entry_id IN ?", ids).Find(&stored).Error; err != nil {
		return err
	}
	moves := map[[2]uint64]int{}
	for _, entry := range stored {
		if to := topicOf[entry.EntryId]; to != 0 && entry.TopicId != to {
			moves[[2]uint64{entry.TopicId, to}]++
		}
	}
	for pair, count := range moves {
		row := TopicMove{FromTopicID: pair[0], ToTopicID: pair[1], Entries: count, DetectedAt: time.Now()}
		err := database.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "from_topic_id"}, {Name: "to_topic_id"}},
			DoUpdates: clause.Assignments(map[string]any{"entries": gorm.Expr("topic_moves.entries + excluded.entries")}),
		}).Create(&row).Error
		if err != nil {
			return err
		}
	}
	return nil
}
