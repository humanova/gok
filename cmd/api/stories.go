package main

import (
	"log/slog"
	"sync"
	"time"

	"gok/internal/model"
)

const storyRefreshInterval = 5 * time.Minute

// StoryRef names the story a Radar topic belongs to.
type StoryRef struct {
	ID    uint64 `json:"id"` // root topic id
	Title string `json:"title"`
	Size  int    `json:"size"`
}

type StoryTopic struct {
	ID         uint64  `json:"id"`
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	ReadyAt    int64   `json:"ready_at"`
	ParentID   *uint64 `json:"parent_id,omitempty"`
	EntryCount int64   `json:"entry_count"`
}

// StoryDetail is a story with its topics, oldest first.
type StoryDetail struct {
	StoryRef
	Topics []StoryTopic `json:"topics"`
}

var storyCache struct {
	mu      sync.RWMutex
	byTopic map[uint64]*StoryDetail
}

// refreshStories loads the stories of every topic in the pulse cache. Only
// stories with at least two topics are kept.
func refreshStories() {
	cache.mu.RLock()
	topicIDs := make([]uint64, 0, len(cache.topicMeta))
	for topicID := range cache.topicMeta {
		topicIDs = append(topicIDs, topicID)
	}
	cache.mu.RUnlock()

	members, err := model.GetStoryMembersForTopics(topicIDs)
	if err != nil {
		slog.Error("stories: load members failed", "error", err)
		return
	}
	byTopic := buildStories(members)

	storyCache.mu.Lock()
	storyCache.byTopic = byTopic
	storyCache.mu.Unlock()
	slog.Info("stories: cache refreshed", "topics", len(byTopic))
}

// buildStories groups members by root and names each story after its topic
// with the most entries.
func buildStories(members []model.StoryMember) map[uint64]*StoryDetail {
	stories := map[uint64]*StoryDetail{}
	var order []uint64
	busiest := map[uint64]int64{}
	for _, member := range members {
		story := stories[member.RootTopicID]
		if story == nil {
			story = &StoryDetail{StoryRef: StoryRef{ID: member.RootTopicID}}
			stories[member.RootTopicID] = story
			order = append(order, member.RootTopicID)
			busiest[member.RootTopicID] = -1
		}
		story.Topics = append(story.Topics, StoryTopic{
			ID:         member.TopicID,
			Title:      member.Title,
			URL:        member.URL,
			ReadyAt:    member.ReadyAt,
			ParentID:   member.ParentTopicID,
			EntryCount: member.EntryCount,
		})
		if member.EntryCount > busiest[member.RootTopicID] {
			busiest[member.RootTopicID] = member.EntryCount
			story.Title = member.Title
		}
	}

	byTopic := map[uint64]*StoryDetail{}
	for _, root := range order {
		story := stories[root]
		story.Size = len(story.Topics)
		if story.Size < 2 {
			continue
		}
		for _, topic := range story.Topics {
			byTopic[topic.ID] = story
		}
	}
	return byTopic
}

func storyForTopic(topicID uint64) *StoryDetail {
	storyCache.mu.RLock()
	defer storyCache.mu.RUnlock()
	return storyCache.byTopic[topicID]
}

func storyRefForTopic(topicID uint64) *StoryRef {
	if story := storyForTopic(topicID); story != nil {
		ref := story.StoryRef
		return &ref
	}
	return nil
}
