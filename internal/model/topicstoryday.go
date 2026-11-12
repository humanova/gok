package model

import "time"

// TopicStoryDay records which story an old topic was mostly about on one day.
// Story threads only link new topics; old pages (people, clubs, places) join a
// story for the days their entries are about it. Every decided topic-day gets
// a row: RootTopicID is the most likely story, Score its yes probability.
type TopicStoryDay struct {
	TopicID     uint64 `gorm:"primaryKey;autoIncrement:false"`
	Day         string `gorm:"primaryKey;type:date"` // Istanbul calendar day, YYYY-MM-DD
	RootTopicID *uint64
	Score       float64
	Model       string
	CreatedAt   time.Time
}

// FirstEntryIDAt returns the id of the first entry posted at or after ts. Entry
// ids follow posting time, so two of these bound a day's entries.
func FirstEntryIDAt(ts int64) (uint64, error) {
	var id uint64
	err := database.Raw(`SELECT coalesce((SELECT entry_id FROM entries WHERE timestamp >= ? ORDER BY timestamp LIMIT 1), 0)`, ts).Scan(&id).Error
	return id, err
}

// TopicEntryCounts counts entries per topic with ids in [fromID, toID).
func TopicEntryCounts(fromID, toID uint64) (map[uint64]int, error) {
	var rows []struct {
		TopicID uint64
		Count   int
	}
	err := database.Raw(`SELECT topic_id, count(*) AS count FROM entries
		WHERE entry_id >= ? AND entry_id < ? AND deleted_at IS NULL GROUP BY topic_id`, fromID, toID).Scan(&rows).Error
	out := make(map[uint64]int, len(rows))
	for _, r := range rows {
		out[r.TopicID] = r.Count
	}
	return out, err
}

// PopularTopicIDs returns the topics on the popular list between two unix times.
func PopularTopicIDs(from, to int64) ([]uint64, error) {
	var ids []uint64
	err := database.Raw(`SELECT DISTINCT topic_id FROM popular_topics
		WHERE timestamp >= ? AND timestamp < ? AND deleted_at IS NULL`, from, to).Scan(&ids).Error
	return ids, err
}

// TopicEntriesBetween returns a topic's entries with ids in [fromID, toID) and
// at least minChars characters, oldest first.
func TopicEntriesBetween(topicID, fromID, toID uint64, minChars int) ([]Entry, error) {
	var rows []Entry
	err := database.Raw(`SELECT entry_id, timestamp, text, topic_id FROM entries
		WHERE topic_id = ? AND entry_id >= ? AND entry_id < ? AND deleted_at IS NULL AND length(text) >= ?
		ORDER BY entry_id`, topicID, fromID, toID, minChars).Scan(&rows).Error
	return rows, err
}

// FirstTopicEntryTime returns the unix time of a topic's first stored entry.
func FirstTopicEntryTime(topicID uint64) (int64, error) {
	var ts int64
	err := database.Raw(`SELECT coalesce((SELECT timestamp FROM entries WHERE topic_id = ? AND deleted_at IS NULL
		ORDER BY entry_id LIMIT 1), 0)`, topicID).Scan(&ts).Error
	return ts, err
}

// TopicTitles returns the titles of the given topics.
func TopicTitles(topicIDs []uint64) (map[uint64]string, error) {
	var rows []struct {
		TopicID uint64
		Text    string
	}
	err := database.Raw(`SELECT topic_id, text FROM topics WHERE topic_id IN ?`, topicIDs).Scan(&rows).Error
	out := make(map[uint64]string, len(rows))
	for _, r := range rows {
		out[r.TopicID] = r.Text
	}
	return out, err
}

// DecidedStoryDays returns the topics already decided for a day.
func DecidedStoryDays(day string) (map[uint64]bool, error) {
	var ids []uint64
	err := database.Raw(`SELECT topic_id FROM topic_story_days WHERE day = ?`, day).Scan(&ids).Error
	out := make(map[uint64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, err
}

func AddTopicStoryDay(row *TopicStoryDay) error {
	return database.Create(row).Error
}
