package rag

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"time"

	"gok/internal/model"
)

// TopicBriefSynthesizer is the narrow LLM dependency for stored radar briefs.
type TopicBriefSynthesizer interface {
	SynthesizeTopicBrief(ctx context.Context, bundle model.TopicBundle) (*model.TopicBriefPayload, error)
}

const (
	// Keep these values aligned with the Radar ranking in cmd/api/main.go.
	briefHotWindow        = time.Hour
	briefEntryWindow      = 12 * time.Hour
	briefHeatHalfLife     = 15 * time.Minute
	briefTopTopics        = 25
	briefMinEntries       = 8
	briefMinEntryLength   = 100
	briefMaxPromptEntries = 40
)

type scoredBriefTopic struct {
	topic model.Topic
	heat  float64
}

// GenerateTopicBriefs creates a persisted brief for every topic currently
// eligible for the Radar. It never relies on popular_topics, so selection
// matches the Radar's activity signal.
func GenerateTopicBriefs(ctx context.Context, synthesizer TopicBriefSynthesizer) (int, error) {
	now := time.Now().UTC()
	hotSince := now.Add(-briefHotWindow).Unix()
	timestamps, topics, err := model.GetTopicsWithEntryTimestampsSince(hotSince)
	if err != nil {
		return 0, err
	}

	scored := make([]scoredBriefTopic, 0, len(topics))
	for _, topic := range topics {
		heat := 0.0
		for _, timestamp := range timestamps[topic.TopicId] {
			ageSeconds := float64(now.Unix() - timestamp)
			heat += math.Exp(-math.Ln2 * ageSeconds / briefHeatHalfLife.Seconds())
		}
		if heat > 0 {
			scored = append(scored, scoredBriefTopic{topic: topic, heat: heat})
		}
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].heat == scored[j].heat {
			return scored[i].topic.TopicId < scored[j].topic.TopicId
		}
		return scored[i].heat > scored[j].heat
	})
	if len(scored) > briefTopTopics {
		scored = scored[:briefTopTopics]
	}

	windowStart := now.Add(-briefEntryWindow)
	created := 0
	for _, candidate := range scored {
		entries, err := model.GetRecentTopicEntries(candidate.topic.TopicId, windowStart.Unix(), briefMaxPromptEntries*2)
		if err != nil {
			slog.Warn("topic brief: entries unavailable", "topic_id", candidate.topic.TopicId, "error", err)
			continue
		}

		entries = substantialRecentEntries(entries, briefMaxPromptEntries)
		if len(entries) < briefMinEntries {
			slog.Info("topic brief: insufficient substantive entries", "topic_id", candidate.topic.TopicId, "entries", len(entries))
			continue
		}

		payload, err := synthesizer.SynthesizeTopicBrief(ctx, model.TopicBundle{
			TopicTitle: candidate.topic.Text,
			Entries:    entries,
		})
		if err != nil {
			slog.Warn("topic brief: synthesis failed", "topic_id", candidate.topic.TopicId, "error", err)
			continue
		}
		if err := model.AddTopicBrief(candidate.topic.TopicId, windowStart, now, len(entries), *payload); err != nil {
			slog.Warn("topic brief: persistence failed", "topic_id", candidate.topic.TopicId, "error", err)
			continue
		}
		created++
	}

	slog.Info("topic briefs generated", "count", created, "candidates", len(scored))
	return created, nil
}

func substantialRecentEntries(entries []model.Entry, limit int) []model.Entry {
	filtered := make([]model.Entry, 0, min(limit, len(entries)))
	for _, entry := range entries {
		if len([]rune(entry.Text)) < briefMinEntryLength {
			continue
		}
		filtered = append(filtered, entry)
		if len(filtered) == limit {
			break
		}
	}
	return filtered
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
