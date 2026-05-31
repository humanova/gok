package rag

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"gok/internal/embedder"
	"gok/internal/model"
)

// DigestSynthesizer is the interface rag.GenerateDigest depends on from the llm package.
type DigestSynthesizer interface {
	SynthesizeDigest(ctx context.Context, bundles []model.TopicBundle) (*model.DigestPayload, error)
}

const (
	digestBurstHours       = 3   // burst window: recent hotness signal
	digestMomentumHours    = 12  // momentum window: sustained interest
	digestTopTopics        = 12  // max hot topics to consider (down from 15 for quality)
	digestMinEntriesFloor  = 10  // skip topics with fewer entries (up from 3)
	digestMinEntryLength   = 100 // skip one-liner entries
	digestEntrySampleHours = 6   // entry fetch window (up from 4h)
	digestClustersPerTopic = 3   // perspective clusters per topic
)

// GenerateDigest produces a pre-computed digest anchored to the hottest topics
// from the last few hours, stores it in the digests table, and returns the payload.
func GenerateDigest(ctx context.Context, _ *embedder.Client, synthesizer DigestSynthesizer) (*model.DigestPayload, error) {
	windowEnd := time.Now().UTC()
	windowStart := windowEnd.Add(-24 * time.Hour)

	// 1. Fetch topics with dual-window scoring: burst (3h) + momentum (12h) + diversity
	burstTopics, err := model.GetHotTopics(digestBurstHours, digestTopTopics*2)
	if err != nil || len(burstTopics) == 0 {
		slog.Warn("no hot topics found for digest", "error", err)
		return nil, nil
	}

	momentumTopics, err := model.GetHotTopics(digestMomentumHours, digestTopTopics*2)
	if err != nil {
		slog.Warn("momentum fetch failed, using burst only", "error", err)
		momentumTopics = nil
	}

	// Build momentum map
	momentumMap := make(map[uint64]float64)
	for _, mt := range momentumTopics {
		// Momentum score: appearances per hour
		momentumMap[mt.TopicId] = float64(mt.Appearances) / float64(digestMomentumHours)
	}

	// Compute final heat = 0.7 × burst + 0.3 × momentum
	type scoredTopic struct {
		topic     model.HotTopic
		finalHeat float64
	}
	scored := make([]scoredTopic, 0, len(burstTopics))
	for _, bt := range burstTopics {
		burstScore := bt.AvgRank // AvgRank holds heat after GetHotTopics
		momentumScore := momentumMap[bt.TopicId]
		finalHeat := 0.7*burstScore + 0.3*momentumScore

		scored = append(scored, scoredTopic{topic: bt, finalHeat: finalHeat})
	}

	// Sort by final heat descending
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].finalHeat > scored[j].finalHeat
	})

	if len(scored) > digestTopTopics {
		scored = scored[:digestTopTopics]
	}

	hotTopics := make([]model.HotTopic, len(scored))
	for i, st := range scored {
		hotTopics[i] = st.topic
		hotTopics[i].AvgRank = st.finalHeat // store final heat in AvgRank for quota calc
	}

	slog.Info("digest: hot topics scored", "count", len(hotTopics))

	// 2. Dynamic quota allocation based on topic rank (no fixed budget)
	//    Top 3: 60-80 entries, Mid 4-8: 30-50 entries, Cool 9-12: 15-30 entries
	quotas := make([]int, len(hotTopics))
	for i := range hotTopics {
		if i < 3 {
			quotas[i] = 70 // hot topics
		} else if i < 8 {
			quotas[i] = 40 // mid topics
		} else {
			quotas[i] = 20 // cool topics
		}
	}

	slog.Info("digest: entry quotas allocated", "topics", len(hotTopics))

	// 3. For each hot topic, fetch and filter entries with smart sampling
	since := windowEnd.Add(-time.Duration(digestEntrySampleHours) * time.Hour).Unix()
	bundles := make([]model.TopicBundle, 0, len(hotTopics))

	for i, ht := range hotTopics {
		// Fetch more entries than quota for filtering
		allEntries, err := model.GetRecentTopicEntries(ht.TopicId, since, quotas[i]*2)
		if err != nil {
			slog.Warn("could not fetch entries for topic", "topic_id", ht.TopicId, "error", err)
			continue
		}

		// Filter: length > 100 chars, then score by recency + embedding presence
		type scoredEntry struct {
			entry model.Entry
			score float64
		}
		var scored []scoredEntry
		now := time.Now().Unix()
		for _, e := range allEntries {
			if len(e.Text) < digestMinEntryLength {
				continue
			}
			ageHours := float64(now-e.Timestamp) / 3600.0
			recencyScore := 1.0 - (ageHours / float64(digestEntrySampleHours))
			if recencyScore < 0 {
				recencyScore = 0
			}
			engagementBoost := 1.0
			if e.EmbeddingAt != nil {
				engagementBoost = 1.2 // embedded = scraped when popular
			}
			scored = append(scored, scoredEntry{
				entry: e,
				score: recencyScore * engagementBoost,
			})
		}

		// Sort by score descending, take quota
		sort.Slice(scored, func(i, j int) bool {
			return scored[i].score > scored[j].score
		})

		if len(scored) < digestMinEntriesFloor {
			continue
		}

		if len(scored) > quotas[i] {
			scored = scored[:quotas[i]]
		}

		entries := make([]model.Entry, len(scored))
		for j, se := range scored {
			entries[j] = se.entry
		}

		bundles = append(bundles, model.TopicBundle{
			TopicTitle: ht.Text,
			Entries:    entries,
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
		"stories", len(payload.Stories),
	)
	return payload, nil
}
