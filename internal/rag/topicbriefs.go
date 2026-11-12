package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"gok/internal/model"
	"gok/internal/stories"
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

// BriefGate asks yes/no questions; it decides whether new entries are worth a
// brief refresh.
type BriefGate interface {
	YesNo(ctx context.Context, input string, questions []string) ([]float64, error)
}

const (
	gateMinNewEntries = 2
	gateMaxEntries    = 15
	gateEntryChars    = 600
	gateThreshold     = 0.10
)

const gateQuestion = "Do the new entries add a development or angle that the summary lacks? Answer yes only for something a reader " +
	"of the summary would need: a new event or fact (a result, a statement, an arrest, a decision, new numbers), or a substantial " +
	"new claim or position that several entries raise. Answer no when the new entries repeat, joke about, or give personal examples " +
	"of what the summary already covers, or when the topic is a list/game where new items are just more of the same."

// hotTopics returns the Radar's hottest topics by entry heat over the last hour.
func hotTopics(now time.Time, limit int) ([]model.Topic, error) {
	timestamps, topics, err := model.GetTopicsWithEntryTimestampsSince(now.Add(-briefHotWindow).Unix())
	if err != nil {
		return nil, err
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
	if len(scored) > limit {
		scored = scored[:limit]
	}
	out := make([]model.Topic, len(scored))
	for i, candidate := range scored {
		out[i] = candidate.topic
	}
	return out, nil
}

// GenerateTopicBriefs creates a persisted brief for every topic currently
// eligible for the Radar. It never relies on popular_topics, so selection
// matches the Radar's activity signal. With a gate, a topic that already has a
// brief is only rewritten when its new entries add something.
func GenerateTopicBriefs(ctx context.Context, synthesizer TopicBriefSynthesizer, gate BriefGate) (int, error) {
	now := time.Now().UTC()
	topics, err := hotTopics(now, briefTopTopics)
	if err != nil {
		return 0, err
	}

	windowStart := now.Add(-briefEntryWindow)
	created, skipped := 0, 0
	for _, topic := range topics {
		entries, err := model.GetRecentTopicEntries(topic.TopicId, windowStart.Unix(), briefMaxPromptEntries*2)
		if err != nil {
			slog.Warn("topic brief: entries unavailable", "topic_id", topic.TopicId, "error", err)
			continue
		}

		entries = substantialRecentEntries(entries, briefMaxPromptEntries)
		if len(entries) < briefMinEntries {
			slog.Info("topic brief: insufficient substantive entries", "topic_id", topic.TopicId, "entries", len(entries))
			continue
		}
		if gate != nil && !briefNeedsRefresh(ctx, gate, topic, entries) {
			skipped++
			continue
		}

		payload, err := synthesizer.SynthesizeTopicBrief(ctx, model.TopicBundle{
			TopicTitle: topic.Text,
			Entries:    entries,
		})
		if err != nil {
			slog.Warn("topic brief: synthesis failed", "topic_id", topic.TopicId, "error", err)
			continue
		}
		if err := model.AddTopicBrief(topic.TopicId, windowStart, now, len(entries), *payload); err != nil {
			slog.Warn("topic brief: persistence failed", "topic_id", topic.TopicId, "error", err)
			continue
		}
		created++
	}

	slog.Info("topic briefs generated", "count", created, "unchanged", skipped, "candidates", len(topics))
	return created, nil
}

// briefNeedsRefresh reports whether the entries posted since the topic's last
// brief add a development it lacks. It fails open.
func briefNeedsRefresh(ctx context.Context, gate BriefGate, topic model.Topic, entries []model.Entry) bool {
	brief, err := model.GetLatestTopicBrief(topic.TopicId)
	if err != nil || brief == nil {
		return true
	}
	var payload model.TopicBriefPayload
	if err := json.Unmarshal(brief.Payload, &payload); err != nil || payload.Summary == "" {
		return true
	}

	var fresh []model.Entry
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Timestamp > brief.GeneratedAt.Unix() {
			fresh = append(fresh, entries[i])
		}
	}
	if len(fresh) < gateMinNewEntries {
		return false
	}

	probs, err := gate.YesNo(ctx, gateInput(topic.Text, payload, spreadEntries(fresh, gateMaxEntries)), []string{gateQuestion})
	if err != nil {
		slog.Warn("topic brief: refresh gate failed", "topic_id", topic.TopicId, "error", err)
		return true
	}
	return probs[0] >= gateThreshold
}

func gateInput(title string, payload model.TopicBriefPayload, entries []model.Entry) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Ekşi Sözlük topic: %s\nCURRENT BRIEF SUMMARY (written at an earlier time):\n%s\n", title, payload.Summary)
	if payload.Debate != nil && len(payload.Debate.Sides) > 0 {
		stances := make([]string, len(payload.Debate.Sides))
		for i, side := range payload.Debate.Sides {
			stances[i] = side.Stance
		}
		fmt.Fprintf(&sb, "Debate sides in the brief: %s\n", strings.Join(stances, " | "))
	}
	sb.WriteString("\nNEW ENTRIES posted after the summary was written:")
	for _, text := range stories.CleanEntries(entries, gateEntryChars) {
		sb.WriteString("\n- ")
		sb.WriteString(text)
	}
	return sb.String()
}

// spreadEntries keeps at most k entries, evenly spaced.
func spreadEntries(entries []model.Entry, k int) []model.Entry {
	if len(entries) <= k {
		return entries
	}
	out := make([]model.Entry, k)
	for i := range out {
		out[i] = entries[i*(len(entries)-1)/(k-1)]
	}
	return out
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
