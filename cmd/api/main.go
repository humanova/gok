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
	ID        uint64  `json:"id"`
	Title     string  `json:"title"`
	Rank      int     `json:"rank"`       // 1-based; 1 = hottest
	HeatScore float64 `json:"heat_score"` // normalised [0.05, 1.0]
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

const (
	cacheHours      = 48
	liveWindowSecs  = 3600  // default live window: last 60 min
	maxRangeSecs    = 21600 // max range per /range request: 6 h
	refreshInterval = 60 * time.Second
	gridSize        = 25
)

var cache struct {
	mu            sync.RWMutex
	topics        []model.HotTopic       // top-gridSize for live view (fixed cacheHours ranking)
	popularTopics []model.PopularTopic   // raw popular_topics rows in last cacheHours (dynamic range ranking)
	timestamps    map[uint64][]int64     // topicId → timestamps for all topics that appeared in popular_topics
	topicMeta     map[uint64]model.Topic // topicId → Topic metadata
	updatedAt     int64
}

func refreshCache() {
	since := time.Now().UTC().Add(-time.Duration(cacheHours) * time.Hour).Unix()

	// 1. Fixed live ranking: top-N topics over the full cache window.
	topics, err := model.GetHotTopics(cacheHours, gridSize)
	if err != nil {
		slog.Error("pulse: GetHotTopics failed", "error", err)
		return
	}

	// 2. All popular_topics rows for dynamic per-window ranking.
	popTopics, err := model.GetPopularTopicsSince(since)
	if err != nil {
		slog.Error("pulse: GetPopularTopicsSince failed", "error", err)
		return
	}

	// Collect every topic ID that appeared on the popular page.
	idSet := make(map[uint64]struct{})
	for _, p := range popTopics {
		idSet[p.TopicId] = struct{}{}
	}
	allIDs := make([]uint64, 0, len(idSet))
	for id := range idSet {
		allIDs = append(allIDs, id)
	}

	// 3. Entry timestamps for the full extended topic set.
	timestamps, err := model.GetTopicEntryTimestamps(allIDs, since)
	if err != nil {
		slog.Error("pulse: GetTopicEntryTimestamps failed", "error", err)
		return
	}

	// 4. Build a complete topic metadata map (id → Topic).
	topicMeta := make(map[uint64]model.Topic, len(allIDs))
	for _, ht := range topics {
		topicMeta[ht.TopicId] = ht.Topic
	}
	extraIDs := make([]uint64, 0)
	for id := range idSet {
		if _, ok := topicMeta[id]; !ok {
			extraIDs = append(extraIDs, id)
		}
	}
	if len(extraIDs) > 0 {
		extra, err := model.GetTopicsByIDs(extraIDs)
		if err != nil {
			slog.Warn("pulse: GetTopicsByIDs for extended set failed", "error", err)
		} else {
			for _, t := range extra {
				topicMeta[t.TopicId] = t
			}
		}
	}

	cache.mu.Lock()
	cache.topics = topics
	cache.popularTopics = popTopics
	cache.timestamps = timestamps
	cache.topicMeta = topicMeta
	cache.updatedAt = time.Now().Unix()
	cache.mu.Unlock()

	slog.Info("pulse: cache refreshed", "topics", len(topics), "extended", len(allIDs))
}

// computeRankingForWindowLocked computes a dynamic hot-topic ranking from cached
// popular_topics rows filtered to [from, to]. Must be called with cache.mu held for reading.
func computeRankingForWindowLocked(from, to int64) []model.HotTopic {
	type agg struct {
		appearances uint64
		totalRank   uint64
		maxNew      uint64
	}
	byTopic := make(map[uint64]*agg)

	for _, r := range cache.popularTopics {
		if r.Timestamp <= from || r.Timestamp > to {
			continue
		}
		a := byTopic[r.TopicId]
		if a == nil {
			a = &agg{}
			byTopic[r.TopicId] = a
		}
		a.appearances++
		a.totalRank += r.PageNumber + 1
		if r.NewEntries > a.maxNew {
			a.maxNew = r.NewEntries
		}
	}

	type scored struct {
		topicId uint64
		heat    float64
		a       *agg
	}
	scores := make([]scored, 0, len(byTopic))
	for id, a := range byTopic {
		if _, ok := cache.topicMeta[id]; !ok {
			continue
		}
		avgRank := float64(a.totalRank) / float64(a.appearances)
		heat := float64(a.appearances) * float64(a.maxNew+1) / avgRank
		scores = append(scores, scored{id, heat, a})
	}
	sort.Slice(scores, func(i, j int) bool { return scores[i].heat > scores[j].heat })
	if len(scores) > gridSize {
		scores = scores[:gridSize]
	}

	result := make([]model.HotTopic, 0, len(scores))
	for _, s := range scores {
		t := cache.topicMeta[s.topicId]
		avgRank := float64(s.a.totalRank) / float64(s.a.appearances)
		result = append(result, model.HotTopic{
			Topic:       t,
			Appearances: s.a.appearances,
			AvgRank:     avgRank,
			TotalNew:    s.a.maxNew,
		})
	}
	return result
}

// buildSnapshotFromTopicsLocked assembles a PulseSnapshot from a given hot-topic list and
// slices timestamps to [from, to]. Must be called with cache.mu held for reading.
func buildSnapshotFromTopicsLocked(hotTopics []model.HotTopic, from, to int64) PulseSnapshot {
	heats := make([]float64, len(hotTopics))
	maxHeat := 0.0
	for i, ht := range hotTopics {
		h := 0.0
		if ht.AvgRank > 0 {
			h = float64(ht.Appearances) * float64(ht.TotalNew+1) / ht.AvgRank
		}
		heats[i] = h
		if h > maxHeat {
			maxHeat = h
		}
	}

	topics := make([]TopicPulse, 0, len(hotTopics))
	for rank, ht := range hotTopics {
		normalizedHeat := 0.05
		if maxHeat > 0 {
			normalizedHeat = math.Max(0.05, heats[rank]/maxHeat)
		}

		ts := cache.timestamps[ht.TopicId]
		lo := sort.Search(len(ts), func(i int) bool { return ts[i] >= from })
		hi := sort.Search(len(ts), func(i int) bool { return ts[i] > to })
		sliced := ts[lo:hi]
		copied := make([]int64, len(sliced))
		copy(copied, sliced)

		topics = append(topics, TopicPulse{
			TopicMeta: TopicMeta{
				ID:        ht.TopicId,
				Title:     ht.Text,
				Rank:      rank + 1,
				HeatScore: normalizedHeat,
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

// buildSnapshot returns the live view: fixed cached ranking sliced to [from, to].
func buildSnapshot(from, to int64) PulseSnapshot {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	return buildSnapshotFromTopicsLocked(cache.topics, from, to)
}

// buildRangeSnapshot dynamically computes topic ranking for [from, to] from cached data.
func buildRangeSnapshot(from, to int64) PulseSnapshot {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	hotTopics := computeRankingForWindowLocked(from, to)
	return buildSnapshotFromTopicsLocked(hotTopics, from, to)
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

// handlePulseRange returns an arbitrary historical slice from the 48h cache.
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

	if since < minSince {
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
