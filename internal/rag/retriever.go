package rag

import (
"context"
"fmt"
"log/slog"
"sort"
"time"

"gok/internal/embedder"
"gok/internal/model"

"gorm.io/gorm"
)

// RetrievalOpts controls the hybrid search behaviour.
type RetrievalOpts struct {
TopK         int     // total results to return after fusion
HoursBack    int     // restrict to entries within this window (0 = no limit)
VectorWeight float64 // weight for vector score in RRF (0-1)
RRFConstant  int     // k in RRF formula (default 60)
}

func DefaultOpts() RetrievalOpts {
return RetrievalOpts{
TopK:         20,
HoursBack:    24,
VectorWeight: 0.6,
RRFConstant:  60,
}
}

type scoredEntry struct {
entry model.Entry
score float64
}

// HybridSearch performs RRF fusion of pgvector cosine search and Postgres full-text search.
func HybridSearch(ctx context.Context, db *gorm.DB, embedClient *embedder.Client, queryText string, opts RetrievalOpts) ([]model.Entry, error) {
if opts.TopK == 0 {
opts = DefaultOpts()
}
if opts.RRFConstant == 0 {
opts.RRFConstant = 60
}

queryVec, err := embedClient.EmbedQuery(ctx, queryText)
if err != nil {
slog.Warn("could not embed query, falling back to BM25 only", "error", err)
}

var timeFilter string
if opts.HoursBack > 0 {
cutoff := time.Now().UTC().Add(-time.Duration(opts.HoursBack) * time.Hour).Unix()
timeFilter = fmt.Sprintf("AND timestamp > %d", cutoff)
}

// -- Vector search --
vectorRanks := make(map[uint]int)
if queryVec != nil {
vecSQL := fmt.Sprintf(`
SELECT id FROM entries
WHERE embedding IS NOT NULL AND deleted_at IS NULL %s
ORDER BY embedding <=> ?
LIMIT ?`, timeFilter)

var vectorIDs []uint
if err := db.Raw(vecSQL, formatVectorLiteral(queryVec), opts.TopK*2).Scan(&vectorIDs).Error; err != nil {
slog.Warn("vector search failed", "error", err)
}
for rank, id := range vectorIDs {
vectorRanks[id] = rank + 1
}
}

// -- BM25 full-text search --
bm25Ranks := make(map[uint]int)
bm25SQL := fmt.Sprintf(`
SELECT id FROM entries
WHERE to_tsvector('simple', coalesce(text,'')) @@ plainto_tsquery('simple', ?)
  AND deleted_at IS NULL %s
ORDER BY ts_rank(to_tsvector('simple', coalesce(text,'')), plainto_tsquery('simple', ?)) DESC
LIMIT ?`, timeFilter)

var bm25IDs []uint
if err := db.Raw(bm25SQL, queryText, queryText, opts.TopK*2).Scan(&bm25IDs).Error; err != nil {
slog.Warn("bm25 search failed", "error", err)
}
for rank, id := range bm25IDs {
bm25Ranks[id] = rank + 1
}

// -- Reciprocal Rank Fusion --
k := float64(opts.RRFConstant)
allIDs := make(map[uint]struct{})
for id := range vectorRanks {
allIDs[id] = struct{}{}
}
for id := range bm25Ranks {
allIDs[id] = struct{}{}
}

scored := make([]scoredEntry, 0, len(allIDs))
for id := range allIDs {
var rrfScore float64
if r, ok := vectorRanks[id]; ok {
rrfScore += opts.VectorWeight * (1.0 / (k + float64(r)))
}
if r, ok := bm25Ranks[id]; ok {
rrfScore += (1 - opts.VectorWeight) * (1.0 / (k + float64(r)))
}
e := model.Entry{}
e.ID = id
scored = append(scored, scoredEntry{entry: e, score: rrfScore})
}

sort.Slice(scored, func(i, j int) bool {
return scored[i].score > scored[j].score
})

topN := opts.TopK
if topN > len(scored) {
topN = len(scored)
}
topIDs := make([]uint, topN)
for i := 0; i < topN; i++ {
topIDs[i] = scored[i].entry.ID
}

// Fetch full entry rows
var results []model.Entry
if err := db.Where("id IN ?", topIDs).Find(&results).Error; err != nil {
return nil, fmt.Errorf("fetching entries by id: %w", err)
}

// Reorder by RRF score
idToEntry := make(map[uint]model.Entry, len(results))
for _, e := range results {
idToEntry[e.ID] = e
}
ordered := make([]model.Entry, 0, len(topIDs))
for _, id := range topIDs {
if e, ok := idToEntry[id]; ok {
ordered = append(ordered, e)
}
}

return ordered, nil
}

// formatVectorLiteral converts []float32 to a Postgres vector string literal e.g. "[0.1,0.2,...]"
func formatVectorLiteral(v []float32) string {
if len(v) == 0 {
return "[]"
}
b := make([]byte, 0, len(v)*10+2)
b = append(b, '[')
for i, f := range v {
if i > 0 {
b = append(b, ',')
}
b = fmt.Appendf(b, "%g", f)
}
b = append(b, ']')
return string(b)
}
