package rag

import (
	"context"
	"log/slog"
	"time"

	"gok/internal/embedder"
	"gok/internal/model"
)

// DigestSynthesizer is the interface rag.GenerateDigest depends on from the llm package.
type DigestSynthesizer interface {
	SynthesizeDigest(ctx context.Context, bundles []model.TopicBundle) (*model.DigestPayload, error)
}

const (
	digestHoursBack        = 3   // how far back to look in popular_topics for hotness signal
	digestTopTopics        = 15  // max hot topics to consider
	digestEntryBudget      = 250 // total entries across all topics sent to the LLM
	digestMinPerTopic      = 5   // floor: every qualifying topic gets at least this many
	digestMaxPerTopic      = 60  // ceiling: no single topic can monopolise the budget
	digestClustersPerTopic = 3   // perspective clusters per topic
	digestMinEntries       = 3   // skip topic bundles with fewer entries than this
)

// GenerateDigest produces a pre-computed digest anchored to the hottest topics
// from the last few hours, stores it in the digests table, and returns the payload.
func GenerateDigest(ctx context.Context, _ *embedder.Client, synthesizer DigestSynthesizer) (*model.DigestPayload, error) {
	windowEnd := time.Now().UTC()
	windowStart := windowEnd.Add(-24 * time.Hour)

	// 1. Fetch the hottest topics from popular_topics in the last N hours.
	hotTopics, err := model.GetHotTopics(digestHoursBack, digestTopTopics)
	if err != nil || len(hotTopics) == 0 {
		slog.Warn("no hot topics found for digest", "error", err)
		return nil, nil
	}
	slog.Info("digest: hot topics fetched", "count", len(hotTopics))

	// 2. Compute per-topic entry quotas proportional to heat score,
	//    bounded by [digestMinPerTopic, digestMaxPerTopic] and a global budget.
	totalHeat := 0.0
	for _, ht := range hotTopics {
		totalHeat += ht.AvgRank // AvgRank holds the heat score after GetHotTopics
	}
	quotas := make([]int, len(hotTopics))
	budgetUsed := 0
	for i, ht := range hotTopics {
		share := int(float64(digestEntryBudget) * ht.AvgRank / totalHeat)
		if share < digestMinPerTopic {
			share = digestMinPerTopic
		}
		if share > digestMaxPerTopic {
			share = digestMaxPerTopic
		}
		quotas[i] = share
		budgetUsed += share
	}
	// if proportional allocation overshoots, trim from the coldest topics first.
	for budgetUsed > digestEntryBudget {
		for i := len(quotas) - 1; i >= 0 && budgetUsed > digestEntryBudget; i-- {
			if quotas[i] > digestMinPerTopic {
				quotas[i]--
				budgetUsed--
			}
		}
	}

	slog.Info("digest: entry budget allocated", "total", budgetUsed, "topics", len(hotTopics))

	// 3. For each hot topic, fetch entries up to its quota and cluster into perspectives.
	since := windowEnd.Add(-time.Duration(digestHoursBack+1) * time.Hour).Unix()
	bundles := make([]model.TopicBundle, 0, len(hotTopics))

	for i, ht := range hotTopics {
		entries, err := model.GetRecentTopicEntries(ht.TopicId, since, quotas[i])
		if err != nil {
			slog.Warn("could not fetch entries for topic", "topic_id", ht.TopicId, "error", err)
			continue
		}
		if len(entries) < digestMinEntries {
			continue
		}

		// Cluster entries into perspective groups using embeddings (falls back to positional).
		viewpoints := ExtractViewpoints(entries, digestClustersPerTopic)
		clusters := make([]model.PerspectiveCluster, 0, len(viewpoints))

		// Re-map viewpoints back to entry slices for the bundle.
		// ExtractViewpoints already selected representative entries; we pass those.
		for _, vp := range viewpoints {
			// Find the entry matching the representative quote to anchor the cluster,
			// then include all entries that are semantically close (we approximate by
			// using the viewpoint's author/quote as the sole representative).
			var clusterEntries []model.Entry
			for _, e := range entries {
				if e.Author == vp.Author && truncate(e.Text, 300) == vp.RepresentativeQuote {
					clusterEntries = append(clusterEntries, e)
					break
				}
			}
			// Fill remaining slots from entries not yet claimed, evenly.
			if len(clusterEntries) == 0 {
				clusterEntries = append(clusterEntries, entries[0])
			}
			clusters = append(clusters, model.PerspectiveCluster{Entries: clusterEntries})
		}

		// Also keep a few un-clustered top entries for raw context.
		// Append entries not already in clusters as a "general" cluster.
		usedAuthors := make(map[string]struct{})
		for _, c := range clusters {
			for _, e := range c.Entries {
				usedAuthors[e.Author] = struct{}{}
			}
		}
		var general []model.Entry
		for _, e := range entries {
			if _, used := usedAuthors[e.Author]; !used {
				general = append(general, e)
				if len(general) >= 5 {
					break
				}
			}
		}
		if len(general) > 0 {
			clusters = append(clusters, model.PerspectiveCluster{Entries: general})
		}

		bundles = append(bundles, model.TopicBundle{
			TopicTitle: ht.Text,
			Clusters:   clusters,
		})
	}

	if len(bundles) == 0 {
		slog.Warn("no valid topic bundles for digest")
		return nil, nil
	}

	// 3. Synthesize.
	payload, err := synthesizer.SynthesizeDigest(ctx, bundles)
	if err != nil {
		return nil, err
	}

	if err := model.AddDigest(windowStart, windowEnd, *payload); err != nil {
		slog.Error("could not persist digest", "error", err)
	}

	slog.Info("digest generated",
		"topics", len(bundles),
		"top_stories", len(payload.TopStories),
		"debates", len(payload.Debates),
	)
	return payload, nil
}
