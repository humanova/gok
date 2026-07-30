package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"gok/internal/config"
	"gok/internal/model"
)

//go:embed static
var staticFiles embed.FS

// TopicMeta holds rank and heat metadata for a topic.
type TopicMeta struct {
	ID             uint64  `json:"id"`
	Title          string  `json:"title"`
	URL            string  `json:"url"`
	Rank           int     `json:"rank"`             // 1-based; 1 = hottest
	RankDelta      int     `json:"rank_delta"`       // positive means higher than 15 minutes ago
	HeatScore      float64 `json:"heat_score"`       // normalised [0.05, 1.0]
	FirstPopularAt int64   `json:"first_popular_at"` // earliest recorded popular-list appearance
}

// TopicPulse is a topic's metadata with its entry timestamps sliced to the response window.
type TopicPulse struct {
	TopicMeta
	Timestamps []int64 `json:"timestamps"` // unix UTC, ascending
}

// PulseSnapshot is the API response envelope.
type PulseSnapshot struct {
	SnapshotAt int64        `json:"snapshot_at"` // unix UTC of last cache refresh
	WindowFrom int64        `json:"window_from"`
	WindowTo   int64        `json:"window_to"`
	Topics     []TopicPulse `json:"topics"`
}

// TopicBriefResponse is deliberately a cache/database read only. A browser
// request never invokes Gemini; unavailable means no pre-generated brief exists.
type TopicBriefResponse struct {
	Available   bool                     `json:"available"`
	GeneratedAt *time.Time               `json:"generated_at,omitempty"`
	WindowStart *time.Time               `json:"window_start,omitempty"`
	WindowEnd   *time.Time               `json:"window_end,omitempty"`
	EntryCount  int                      `json:"entry_count,omitempty"`
	Payload     *model.TopicBriefPayload `json:"payload,omitempty"`
}

const (
	cacheHours      = 24
	liveWindowSecs  = 3600  // default live window: last 60 min
	maxRangeSecs    = 21600 // max range per /range request: 6 h
	refreshInterval = 60 * time.Second
	gridSize        = 25
	rankingWindow   = 60 * time.Minute
	heatHalfLife    = 15 * time.Minute
	rankComparison  = 15 * time.Minute
)

var cache struct {
	mu             sync.RWMutex
	timestamps     map[uint64][]int64     // topicId → entry timestamps for every active topic
	topicMeta      map[uint64]model.Topic // topicId → Topic metadata
	firstPopularAt map[uint64]int64       // topicId → earliest popular-list appearance
	updatedAt      int64
}

type rankedTopic struct {
	topic model.Topic
	heat  float64
}

func refreshCache() {
	since := time.Now().UTC().Add(-time.Duration(cacheHours) * time.Hour).Unix()

	// Refresh a single compact snapshot after scraping. Browser requests only
	// read this cache; no entry text, author, or per-user database work is sent.
	timestamps, topics, err := model.GetTopicsWithEntryTimestampsSince(since)
	if err != nil {
		slog.Error("pulse: GetTopicsWithEntryTimestampsSince failed", "error", err)
		return
	}

	topicMeta := make(map[uint64]model.Topic, len(topics))
	topicIDs := make([]uint64, 0, len(topics))
	for _, topic := range topics {
		topicMeta[topic.TopicId] = topic
		topicIDs = append(topicIDs, topic.TopicId)
	}
	firstPopularAt, err := model.GetFirstPopularTimestamps(topicIDs)
	if err != nil {
		slog.Error("pulse: GetFirstPopularTimestamps failed", "error", err)
		return
	}

	cache.mu.Lock()
	cache.timestamps = timestamps
	cache.topicMeta = topicMeta
	cache.firstPopularAt = firstPopularAt
	cache.updatedAt = time.Now().Unix()
	cache.mu.Unlock()

	slog.Info("pulse: cache refreshed", "topics", len(topics))
}

// rankTopicsForWindowLocked ranks topics by entry velocity. Each event decays
// by half every heatHalfLife, so the board reflects what is accelerating now
// instead of which topic stayed on the popular page longest.
func rankTopicsForWindowLocked(from, to int64, limit int) []rankedTopic {
	if to <= from {
		return nil
	}

	weightFrom := to - int64(rankingWindow/time.Second)
	if weightFrom < from {
		weightFrom = from
	}
	halfLifeSecs := heatHalfLife.Seconds()
	ranked := make([]rankedTopic, 0, len(cache.timestamps))

	for topicID, timestamps := range cache.timestamps {
		topic, ok := cache.topicMeta[topicID]
		if !ok {
			continue
		}
		lo := sort.Search(len(timestamps), func(i int) bool { return timestamps[i] >= from })
		hi := sort.Search(len(timestamps), func(i int) bool { return timestamps[i] > to })
		if lo == hi {
			continue
		}

		heat := 0.0
		for _, ts := range timestamps[lo:hi] {
			if ts < weightFrom {
				continue
			}
			ageSecs := float64(to - ts)
			heat += math.Exp(-math.Ln2 * ageSecs / halfLifeSecs)
		}
		ranked = append(ranked, rankedTopic{topic: topic, heat: heat})
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].heat == ranked[j].heat {
			return ranked[i].topic.TopicId < ranked[j].topic.TopicId
		}
		return ranked[i].heat > ranked[j].heat
	})
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked
}

// buildSnapshotFromTopicsLocked assembles a PulseSnapshot from ranked topics and
// slices timestamps to [from, to]. Must be called with cache.mu held for reading.
func buildSnapshotFromTopicsLocked(hotTopics []rankedTopic, from, to int64) PulseSnapshot {
	previousTopics := rankTopicsForWindowLocked(
		from-int64(rankComparison/time.Second),
		to-int64(rankComparison/time.Second),
		0,
	)
	previousRanks := make(map[uint64]int, len(previousTopics))
	for rank, topic := range previousTopics {
		previousRanks[topic.topic.TopicId] = rank + 1
	}

	heats := make([]float64, len(hotTopics))
	maxHeat := 0.0
	for i, ht := range hotTopics {
		heats[i] = ht.heat
		if ht.heat > maxHeat {
			maxHeat = ht.heat
		}
	}

	topics := make([]TopicPulse, 0, len(hotTopics))
	for rank, ht := range hotTopics {
		normalizedHeat := 0.05
		if maxHeat > 0 {
			normalizedHeat = math.Max(0.05, heats[rank]/maxHeat)
		}

		ts := cache.timestamps[ht.topic.TopicId]
		lo := sort.Search(len(ts), func(i int) bool { return ts[i] >= from })
		hi := sort.Search(len(ts), func(i int) bool { return ts[i] > to })
		sliced := ts[lo:hi]
		copied := make([]int64, len(sliced))
		copy(copied, sliced)

		previousRank := previousRanks[ht.topic.TopicId]
		topics = append(topics, TopicPulse{
			TopicMeta: TopicMeta{
				ID:             ht.topic.TopicId,
				Title:          ht.topic.Text,
				URL:            ht.topic.Url,
				Rank:           rank + 1,
				RankDelta:      previousRank - (rank + 1),
				HeatScore:      normalizedHeat,
				FirstPopularAt: cache.firstPopularAt[ht.topic.TopicId],
			},
			Timestamps: copied,
		})
	}

	return PulseSnapshot{
		SnapshotAt: cache.updatedAt,
		WindowFrom: from,
		WindowTo:   to,
		Topics:     topics,
	}
}

// buildSnapshot ranks and slices the cache for the requested event window.
func buildSnapshot(from, to int64) PulseSnapshot {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	return buildSnapshotFromTopicsLocked(rankTopicsForWindowLocked(from, to, gridSize), from, to)
}

// buildRangeSnapshot ranks historical entries from the same cache.
func buildRangeSnapshot(from, to int64) PulseSnapshot {
	return buildSnapshot(from, to)
}

// handlePulse returns the live view: last 60 min of the cached data.
func handlePulse(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC().Unix()
	snap := buildSnapshot(now-liveWindowSecs, now)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	if err := json.NewEncoder(w).Encode(snap); err != nil {
		slog.Error("pulse: encode /api/pulse failed", "error", err)
	}
}

// handlePulseRange returns an arbitrary historical slice from the 24h cache.
// Query params: since (unix), until (unix). Max range: 6 h.
func handlePulseRange(w http.ResponseWriter, r *http.Request) {
	sinceStr := r.URL.Query().Get("since")
	untilStr := r.URL.Query().Get("until")

	since, err1 := strconv.ParseInt(sinceStr, 10, 64)
	until, err2 := strconv.ParseInt(untilStr, 10, 64)
	if err1 != nil || err2 != nil {
		http.Error(w, "invalid since or until parameter", http.StatusBadRequest)
		return
	}

	now := time.Now().UTC().Unix()
	minSince := now - int64(cacheHours)*3600

	// Allow a small clock/request tolerance at the left edge of the rolling
	// client timeline. The cache simply has no events before its true boundary.
	if since < minSince-120 {
		http.Error(w, fmt.Sprintf("since must be within last %dh", cacheHours), http.StatusBadRequest)
		return
	}
	// Silently clamp until to now; requesting future data is always empty
	// and produces nonsensical window_to values in the JSON response.
	if until > now {
		until = now
	}
	if until <= since {
		http.Error(w, "until must be greater than since", http.StatusBadRequest)
		return
	}
	if until-since > maxRangeSecs {
		http.Error(w, fmt.Sprintf("range exceeds maximum of %dh", maxRangeSecs/3600), http.StatusBadRequest)
		return
	}

	snap := buildRangeSnapshot(since, until)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	if err := json.NewEncoder(w).Encode(snap); err != nil {
		slog.Error("pulse: encode /api/pulse/range failed", "error", err)
	}
}

// handleTopicBrief returns a stored, pre-generated explanation for one topic.
func handleTopicBrief(w http.ResponseWriter, r *http.Request) {
	topicID, err := strconv.ParseUint(r.PathValue("topicID"), 10, 64)
	if err != nil || topicID == 0 {
		http.Error(w, "invalid topic id", http.StatusBadRequest)
		return
	}

	brief, err := model.GetLatestTopicBrief(topicID)
	if err != nil {
		slog.Error("pulse: load topic brief failed", "topic_id", topicID, "error", err)
		http.Error(w, "could not load topic brief", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, max-age=60")
	if brief == nil {
		_ = json.NewEncoder(w).Encode(TopicBriefResponse{Available: false})
		return
	}

	var payload model.TopicBriefPayload
	if err := json.Unmarshal(brief.Payload, &payload); err != nil {
		slog.Error("pulse: decode topic brief failed", "topic_id", topicID, "error", err)
		http.Error(w, "stored topic brief is invalid", http.StatusInternalServerError)
		return
	}
	response := TopicBriefResponse{
		Available:   true,
		GeneratedAt: &brief.GeneratedAt,
		WindowStart: &brief.WindowStart,
		WindowEnd:   &brief.WindowEnd,
		EntryCount:  brief.EntryCount,
		Payload:     &payload,
	}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("pulse: encode topic brief failed", "topic_id", topicID, "error", err)
	}
}

func main() {
	if err := model.InitDb(); err != nil {
		slog.Error("pulse: couldn't connect to database", "error", err)
		panic(err)
	}

	slog.Info("pulse: initial cache refresh...")
	refreshCache()

	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for range ticker.C {
			refreshCache()
		}
	}()

	mux := http.NewServeMux()
	// More specific route must be registered first.
	mux.HandleFunc("GET /api/topics/{topicID}/brief", handleTopicBrief)
	mux.HandleFunc("GET /api/pulse/range", handlePulseRange)
	mux.HandleFunc("GET /api/pulse", handlePulse)
	mux.Handle("GET /static/", http.FileServer(http.FS(staticFiles)))
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		data, err := staticFiles.ReadFile("static/index.html")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})

	addr := fmt.Sprintf(":%d", config.Config.ApiPort)
	slog.Info("pulse: listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("pulse: server error", "error", err)
	}
}
