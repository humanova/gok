package model

import (
	"time"

	"github.com/pgvector/pgvector-go"
)

// TopicStory records one new topic's place in a story thread. Each new topic
// gets one row once it has three entries; ParentTopicID points at the earlier
// topic it follows from (nil when it starts its own thread or no candidate
// reached the link threshold). A story is the
// tree of these links, identified by its root topic.
type TopicStory struct {
	TopicID       uint64 `gorm:"primaryKey;autoIncrement:false"`
	Title         string
	BornAt        int64 `gorm:"index"` // unix time of the topic's first entry
	ReadyAt       int64 `gorm:"index"` // unix time of its third entry, when it was decided
	InputText     string
	FirstEntry    string
	Embedding     pgvector.Vector `gorm:"type:vector(768)"`
	ParentTopicID *uint64         `gorm:"index"`
	RootTopicID   uint64          `gorm:"index"`
	Score         float64         // yes probability of the most likely candidate
	Model         string
	CreatedAt     time.Time
}

// StoryTopicEntries is a topic without a story row, with its first entries.
type StoryTopicEntries struct {
	TopicID uint64
	Title   string
	Entries []Entry // first entries in entry_id order
}

// GetStoryCandidateTopics returns topics opened after since that have no story
// row yet, each with up to its first limit entries. Ekşi topic ids grow with
// creation time, so only ids above the newest topic stored before since can be
// new. This also skips old topics whose stored history starts late.
func GetStoryCandidateTopics(since time.Time, limit int) ([]StoryTopicEntries, error) {
	type row struct {
		TopicID   uint64
		Title     string
		EntryID   uint64
		Timestamp int64
		Text      string
	}
	var rows []row
	err := database.Raw(`
		SELECT t.topic_id, t.text AS title, e.entry_id, e.timestamp, e.text
		FROM topics t
		LEFT JOIN topic_stories s ON s.topic_id = t.topic_id
		CROSS JOIN LATERAL (
			SELECT entry_id, timestamp, text FROM entries
			WHERE topic_id = t.topic_id AND deleted_at IS NULL
			ORDER BY entry_id LIMIT ?
		) e
		WHERE s.topic_id IS NULL AND t.deleted_at IS NULL
			AND t.topic_id > (SELECT coalesce(max(topic_id), 0) FROM topics WHERE created_at < ?)
		ORDER BY t.topic_id, e.entry_id`, limit, since).Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	var out []StoryTopicEntries
	for _, r := range rows {
		if len(out) == 0 || out[len(out)-1].TopicID != r.TopicID {
			out = append(out, StoryTopicEntries{TopicID: r.TopicID, Title: r.Title})
		}
		last := &out[len(out)-1]
		last.Entries = append(last.Entries, Entry{EntryId: r.EntryID, Timestamp: r.Timestamp, Text: r.Text, TopicId: r.TopicID})
	}
	return out, nil
}

// GetTopicStoriesReadySince returns story rows decided at or after readySince,
// oldest first.
func GetTopicStoriesReadySince(readySince int64) ([]TopicStory, error) {
	var rows []TopicStory
	err := database.Where("ready_at >= ?", readySince).Order("ready_at, topic_id").Find(&rows).Error
	return rows, err
}

func AddTopicStory(row *TopicStory) error {
	return database.Create(row).Error
}

// StoryMember is a topic in a story, without its embedding.
type StoryMember struct {
	TopicID       uint64
	Title         string
	ReadyAt       int64
	ParentTopicID *uint64
	RootTopicID   uint64
	URL           string
	EntryCount    int64
}

// GetStoryMembersForTopics returns every member of the stories that contain any
// of the given topics, with each member's stored entry count.
func GetStoryMembersForTopics(topicIDs []uint64) ([]StoryMember, error) {
	if len(topicIDs) == 0 {
		return nil, nil
	}
	var members []StoryMember
	err := database.Raw(`
		SELECT s.topic_id, s.title, s.ready_at, s.parent_topic_id, s.root_topic_id, t.url,
			(SELECT count(*) FROM entries e WHERE e.topic_id = s.topic_id AND e.deleted_at IS NULL) AS entry_count
		FROM topic_stories s
		LEFT JOIN topics t ON t.topic_id = s.topic_id
		WHERE s.root_topic_id IN (SELECT root_topic_id FROM topic_stories WHERE topic_id IN ?)
			AND NOT (s.topic_id IN (SELECT from_topic_id FROM topic_moves)
				AND NOT EXISTS (SELECT 1 FROM entries e WHERE e.topic_id = s.topic_id AND e.deleted_at IS NULL))
		ORDER BY s.root_topic_id, s.ready_at`, topicIDs).Scan(&members).Error
	return members, err
}
