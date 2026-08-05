package model

import (
	"sort"
	"time"

	"gorm.io/gorm"
)

// HotTopic holds a topic with its heat signal derived from recent popular_topics rows.
type HotTopic struct {
	Topic
	Appearances uint64  // how many times it appeared in popular_topics within the window
	AvgRank     float64 // lower is hotter (rank 1 = top of the list)
	TotalNew    uint64  // peak new_entries seen in a single scrape within the window
}

// GetHotTopics returns the most active topics that appeared in popular_topics
// within the last hoursBack hours, ranked by a heat score (appearances × totalNew / avgRank).
// At most topK topics are returned.
func GetHotTopics(hoursBack int, topK int) ([]HotTopic, error) {
	cutoff := time.Now().UTC().Add(-time.Duration(hoursBack) * time.Hour).Unix()

	var rows []PopularTopic
	tx := database.Where("timestamp > ?", cutoff).Find(&rows)
	if tx.Error != nil {
		return nil, tx.Error
	}

	type agg struct {
		appearances uint64
		totalRank   uint64
		maxNew      uint64
	}
	byTopic := make(map[uint64]*agg)
	for _, r := range rows {
		a := byTopic[r.TopicId]
		if a == nil {
			a = &agg{}
			byTopic[r.TopicId] = a
		}
		a.appearances++
		a.totalRank += r.PageNumber + 1 // PageNumber is 0-based; treat as rank proxy
		if r.NewEntries > a.maxNew {
			a.maxNew = r.NewEntries
		}
	}

	// Fetch topic metadata
	ids := make([]uint64, 0, len(byTopic))
	for id := range byTopic {
		ids = append(ids, id)
	}
	var topics []Topic
	if err := database.Where("topic_id IN ?", ids).Find(&topics).Error; err != nil {
		return nil, err
	}
	topicByID := make(map[uint64]Topic, len(topics))
	for _, t := range topics {
		topicByID[t.TopicId] = t
	}

	hot := make([]HotTopic, 0, len(byTopic))
	for topicID, a := range byTopic {
		t, ok := topicByID[topicID]
		if !ok {
			continue
		}
		avgRank := float64(a.totalRank) / float64(a.appearances)
		heat := float64(a.appearances) * float64(a.maxNew+1) / avgRank
		hot = append(hot, HotTopic{
			Topic:       t,
			Appearances: a.appearances,
			AvgRank:     heat, // will be restored after sort
			TotalNew:    a.maxNew,
		})
	}

	// Sort descending by heat (stored in AvgRank after the loop above)
	sort.Slice(hot, func(i, j int) bool {
		return hot[i].AvgRank > hot[j].AvgRank
	})

	if topK > 0 && len(hot) > topK {
		hot = hot[:topK]
	}

	// Restore real avgRank for callers (recompute cleanly)
	for i, h := range hot {
		a := byTopic[h.TopicId]
		hot[i].AvgRank = float64(a.totalRank) / float64(a.appearances)
	}

	return hot, nil
}

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
