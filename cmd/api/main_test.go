package main

import (
	"testing"

	"gok/internal/model"
)

func TestSnapshotMarksTopicsMissingFromPreviousWindowAsNew(t *testing.T) {
	const now = int64(1_000_000)
	cache.mu.Lock()
	cache.timestamps = map[uint64][]int64{
		1: {now - 1800, now - 60}, // already had an entry in the previous window
		2: {now - 60},             // first entry is inside the last 15 minutes
	}
	cache.topicMeta = map[uint64]model.Topic{1: {TopicId: 1}, 2: {TopicId: 2}}
	cache.firstPopularAt = map[uint64]int64{}
	cache.mu.Unlock()

	snapshot := buildSnapshot(now-liveWindowSecs, now, nil)
	byID := map[uint64]TopicMeta{}
	for _, topic := range snapshot.Topics {
		byID[topic.ID] = topic.TopicMeta
	}
	if byID[1].RankNew {
		t.Errorf("topic 1 had entries in the previous window but is marked new")
	}
	if !byID[2].RankNew || byID[2].RankDelta != 0 {
		t.Errorf("topic 2 = %+v, want rank_new with zero delta", byID[2])
	}
}

func TestBuildStoriesNamesStoriesAfterTheirBusiestTopic(t *testing.T) {
	root := uint64(10)
	members := []model.StoryMember{
		{TopicID: 10, Title: "fon tasfiyeleri", RootTopicID: 10, EntryCount: 40},
		{TopicID: 11, Title: "fon açıklaması", RootTopicID: 10, ParentTopicID: &root, EntryCount: 90},
		{TopicID: 20, Title: "tek başlık", RootTopicID: 20, EntryCount: 500},
	}
	byTopic := buildStories(members)
	if byTopic[20] != nil {
		t.Errorf("a single-topic story should be left out, got %+v", byTopic[20])
	}
	story := byTopic[10]
	if story == nil || byTopic[11] != story {
		t.Fatalf("topics 10 and 11 should share one story, got %+v and %+v", story, byTopic[11])
	}
	if story.ID != 10 || story.Size != 2 || story.Title != "fon açıklaması" {
		t.Errorf("story = %+v, want id 10, size 2, titled after the busiest topic", story.StoryRef)
	}
	if story.Topics[1].ParentID == nil || *story.Topics[1].ParentID != 10 {
		t.Errorf("second topic should keep its parent, got %+v", story.Topics[1])
	}
}
