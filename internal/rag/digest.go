package rag

import (
"context"
"log/slog"
"time"

"gok/internal/embedder"
"gok/internal/model"
)

// DigestSynthesizer is the interface rag.GenerateDigest depends on from the llm package.
// This breaks the import cycle: rag does not import llm.
type DigestSynthesizer interface {
SynthesizeDigest(ctx context.Context, entries []model.Entry, viewpoints []model.Viewpoint, topicTitles []string) (*model.DigestPayload, error)
}

// GenerateDigest produces a pre-computed digest for the last 24 hours,
// stores it in the digests table, and returns the payload.
func GenerateDigest(ctx context.Context, embedClient *embedder.Client, synthesizer DigestSynthesizer) (*model.DigestPayload, error) {
windowEnd := time.Now().UTC()
windowStart := windowEnd.Add(-24 * time.Hour)

queries := []string{"gündem", "bugün", "siyaset", "ekonomi", "spor", "magazin"}
opts := RetrievalOpts{
TopK:         40,
HoursBack:    24,
VectorWeight: 0.6,
RRFConstant:  60,
}

seenIDs := make(map[uint]struct{})
var allEntries []model.Entry

db := model.DB()
for _, q := range queries {
results, err := HybridSearch(ctx, db, embedClient, q, opts)
if err != nil {
slog.Warn("hybrid search failed during digest generation", "query", q, "error", err)
continue
}
for _, e := range results {
if _, seen := seenIDs[e.ID]; !seen {
seenIDs[e.ID] = struct{}{}
allEntries = append(allEntries, e)
}
}
if len(allEntries) >= 80 {
break
}
}

if len(allEntries) == 0 {
slog.Warn("no entries found for digest generation")
return nil, nil
}

// Collect unique topic IDs for context titles
topicIDs := make([]uint64, 0, 20)
seenTopics := make(map[uint64]struct{})
for _, e := range allEntries {
if _, ok := seenTopics[e.TopicId]; !ok {
seenTopics[e.TopicId] = struct{}{}
topicIDs = append(topicIDs, e.TopicId)
if len(topicIDs) >= 20 {
break
}
}
}
var topics []model.Topic
db.Where("topic_id IN ?", topicIDs).Find(&topics)
topicTitles := make([]string, 0, len(topics))
for _, t := range topics {
topicTitles = append(topicTitles, t.Text)
}

viewpoints := ExtractViewpoints(allEntries, 3)

payload, err := synthesizer.SynthesizeDigest(ctx, allEntries, viewpoints, topicTitles)
if err != nil {
return nil, err
}

if err := model.AddDigest(windowStart, windowEnd, *payload); err != nil {
slog.Error("could not persist digest", "error", err)
}

slog.Info("digest generated",
"entries_used", len(allEntries),
"viewpoints", len(viewpoints),
"trending_topics", len(payload.TrendingTopics),
)
return payload, nil
}
