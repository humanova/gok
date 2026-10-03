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

	snapshot := buildSnapshot(now-liveWindowSecs, now)
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
