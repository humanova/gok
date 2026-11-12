package stories

import (
	"context"
	"time"

	"gok/internal/model"
)

// DBStore keeps story rows in the topic_stories table.
type DBStore struct{}

func (DBStore) PendingTopics(_ context.Context, since time.Time) ([]model.StoryTopicEntries, error) {
	return model.GetStoryCandidateTopics(since, ReadyEntries)
}

func (DBStore) Nodes(_ context.Context, readySince int64) ([]model.TopicStory, error) {
	return model.GetTopicStoriesReadySince(readySince)
}

func (DBStore) Save(_ context.Context, node *model.TopicStory) error {
	return model.AddTopicStory(node)
}
