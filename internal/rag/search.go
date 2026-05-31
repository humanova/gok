package rag

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gok/internal/model"

	"github.com/pgvector/pgvector-go"
	"gorm.io/gorm"
)

// SearchFilters controls the hybrid search scope.
type SearchFilters struct {
	TopicName string    // substring match against topics.text; empty = all topics
	Since     time.Time // zero value -> defaults to 24 h ago
	MaxResult int       // 0 -> defaults to 40
}

type scoredEntry struct {
	entry    model.Entry
	rrfScore float64
}

const rrfK = 60

// HybridSearch performs a two-stage vector + BM25 retrieval with Reciprocal Rank Fusion.
// If queryVec is nil the vector leg is skipped and only full-text search is used.
func HybridSearch(ctx context.Context, query string, queryVec []float32, db *gorm.DB, f SearchFilters) ([]model.Entry, error) {
	if f.MaxResult <= 0 {
		f.MaxResult = 80
	}
	if f.Since.IsZero() {
		f.Since = time.Now().UTC().Add(-24 * time.Hour)
	}

	// Resolve optional topic filter -> list of topic IDs.
	var topicIDs []uint64
	if f.TopicName != "" {
		var topics []model.Topic
		if err := db.Where("unaccent(text) ILIKE unaccent(?)", fmt.Sprintf("%%%s%%", f.TopicName)).Find(&topics).Error; err != nil {
			return nil, fmt.Errorf("topic lookup: %w", err)
		}
		for _, t := range topics {
			topicIDs = append(topicIDs, t.TopicId)
		}
		if len(topicIDs) == 0 {
			// No matching topic found; return empty rather than crash.
			return nil, nil
		}
	}

	sinceUnix := f.Since.Unix()
	fetch := f.MaxResult * 3 // over-fetch for fusion headroom

	// --- Leg A: vector similarity ---
	vecRanks := make(map[uint]int) // entry primary-key -> 1-based rank
	vecEntries := make(map[uint]model.Entry)
	if queryVec != nil {
		rows, err := vectorSearch(ctx, db, queryVec, topicIDs, sinceUnix, fetch)
		if err != nil {
			// Non-fatal: log and continue with FTS only.
			_ = err
		} else {
			for i, e := range rows {
				vecRanks[e.ID] = i + 1
				vecEntries[e.ID] = e
			}
		}
	}

	// --- Leg B: full-text BM25 ---
	ftsRanks := make(map[uint]int)
	ftsEntries := make(map[uint]model.Entry)
	if query != "" {
		rows, err := ftsSearch(ctx, db, query, topicIDs, sinceUnix, fetch)
		if err != nil {
			_ = err
		} else {
			for i, e := range rows {
				ftsRanks[e.ID] = i + 1
				ftsEntries[e.ID] = e
			}
		}
	}

	// Fallback: if both legs are empty and query is set, try ILIKE broad search.
	if len(vecRanks) == 0 && len(ftsRanks) == 0 && query != "" {
		rows, err := ilikeSearch(ctx, db, query, topicIDs, sinceUnix, fetch)
		if err != nil {
			return nil, fmt.Errorf("fallback search: %w", err)
		}
		return rows, nil
	}

	// --- RRF fusion ---
	allIDs := make(map[uint]struct{})
	for id := range vecRanks {
		allIDs[id] = struct{}{}
	}
	for id := range ftsRanks {
		allIDs[id] = struct{}{}
	}

	scored := make([]scoredEntry, 0, len(allIDs))
	for id := range allIDs {
		s := 0.0
		if r, ok := vecRanks[id]; ok {
			s += 1.0 / float64(rrfK+r)
		}
		if r, ok := ftsRanks[id]; ok {
			s += 1.0 / float64(rrfK+r)
		}
		e := vecEntries[id]
		if e.ID == 0 {
			e = ftsEntries[id]
		}
		scored = append(scored, scoredEntry{entry: e, rrfScore: s})
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].rrfScore > scored[j].rrfScore
	})

	out := make([]model.Entry, 0, f.MaxResult)
	for _, s := range scored {
		if len(out) >= f.MaxResult {
			break
		}
		out = append(out, s.entry)
	}
	return out, nil
}

// vectorSearch runs an HNSW cosine-similarity query via pgvector.
func vectorSearch(ctx context.Context, db *gorm.DB, vec []float32, topicIDs []uint64, sinceUnix int64, limit int) ([]model.Entry, error) {
	pgVec := pgvector.NewVector(vec)
	q := db.WithContext(ctx).
		Where("embedding IS NOT NULL AND timestamp > ? AND deleted_at IS NULL", sinceUnix).
		Order(fmt.Sprintf("embedding <=> '%s'", pgVec.String())).
		Limit(limit)
	if len(topicIDs) > 0 {
		q = q.Where("topic_id IN ?", topicIDs)
	}
	var entries []model.Entry
	if err := q.Find(&entries).Error; err != nil {
		return nil, err
	}
	return entries, nil
}

// ftsSearch runs a Turkish full-text search with ts_rank_cd scoring.
func ftsSearch(ctx context.Context, db *gorm.DB, query string, topicIDs []uint64, sinceUnix int64, limit int) ([]model.Entry, error) {
	rankExpr := fmt.Sprintf("ts_rank_cd(to_tsvector('turkish', coalesce(text,'')), plainto_tsquery('turkish', '%s')) DESC",
		strings.ReplaceAll(query, "'", "''"))
	q := db.WithContext(ctx).
		Where("to_tsvector('turkish', coalesce(text,'')) @@ plainto_tsquery('turkish', ?) AND timestamp > ? AND deleted_at IS NULL", query, sinceUnix).
		Order(rankExpr).
		Limit(limit)
	if len(topicIDs) > 0 {
		q = q.Where("topic_id IN ?", topicIDs)
	}
	var entries []model.Entry
	if err := q.Find(&entries).Error; err != nil {
		return nil, err
	}
	return entries, nil
}

// ilikeSearch is a broad fallback when vector + FTS both return nothing.
func ilikeSearch(ctx context.Context, db *gorm.DB, query string, topicIDs []uint64, sinceUnix int64, limit int) ([]model.Entry, error) {
	q := db.WithContext(ctx).
		Where("text ILIKE ? AND timestamp > ? AND deleted_at IS NULL", fmt.Sprintf("%%%s%%", query), sinceUnix).
		Order("timestamp DESC").
		Limit(limit)
	if len(topicIDs) > 0 {
		q = q.Where("topic_id IN ?", topicIDs)
	}
	var entries []model.Entry
	if err := q.Find(&entries).Error; err != nil {
		return nil, err
	}
	return entries, nil
}
