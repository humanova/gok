package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"gok/internal/config"
	"gok/internal/model"
)

//go:embed static
var staticFiles embed.FS

type TopicMeta struct {
	ID             uint64  `json:"id"`
	Title          string  `json:"title"`
	URL            string  `json:"url"`
	Rank           int     `json:"rank"`
	RankDelta      int     `json:"rank_delta"`
	HeatScore      float64 `json:"heat_score"`
	FirstPopularAt int64   `json:"first_popular_at"`
}

type TopicPulse struct {
	TopicMeta
	Timestamps []int64 `json:"timestamps"`
}

type PulseSnapshot struct {
	SnapshotAt int64        `json:"snapshot_at"`
	WindowFrom int64        `json:"window_from"`
	WindowTo   int64        `json:"window_to"`
	Topics     []TopicPulse `json:"topics"`
}

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
	liveWindowSecs  = 3600
	maxRangeSecs    = 21600
	refreshInterval = 60 * time.Second
	gridSize        = 25
	rankingWindow   = 60 * time.Minute
	heatHalfLife    = 15 * time.Minute
	rankComparison  = 15 * time.Minute
)

var cache struct {
	mu             sync.RWMutex
	timestamps     map[uint64][]int64
	topicMeta      map[uint64]model.Topic
	firstPopularAt map[uint64]int64
	updatedAt      int64
}

var mapCache struct {
	mu       sync.RWMutex
	snapshot *MapSnapshot
}

type rankedTopic struct {
	topic model.Topic
	heat  float64
}

func refreshCache() {
	since := time.Now().UTC().Add(-time.Duration(cacheHours) * time.Hour).Unix()
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
		lo := sort.Search(len(timestamps), func(index int) bool { return timestamps[index] >= from })
		hi := sort.Search(len(timestamps), func(index int) bool { return timestamps[index] > to })
		if lo == hi {
			continue
		}
		heat := 0.0
		for _, timestamp := range timestamps[lo:hi] {
			if timestamp < weightFrom {
				continue
			}
			ageSecs := float64(to - timestamp)
			heat += math.Exp(-math.Ln2 * ageSecs / halfLifeSecs)
		}
		ranked = append(ranked, rankedTopic{topic: topic, heat: heat})
	}
	sort.Slice(ranked, func(left, right int) bool {
		if ranked[left].heat == ranked[right].heat {
			return ranked[left].topic.TopicId < ranked[right].topic.TopicId
		}
		return ranked[left].heat > ranked[right].heat
	})
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked
}

func buildSnapshotFromTopicsLocked(hotTopics []rankedTopic, from, to int64) PulseSnapshot {
	previousTopics := rankTopicsForWindowLocked(from-int64(rankComparison/time.Second), to-int64(rankComparison/time.Second), 0)
	previousRanks := make(map[uint64]int, len(previousTopics))
	for rank, topic := range previousTopics {
		previousRanks[topic.topic.TopicId] = rank + 1
	}
	maxHeat := 0.0
	for _, topic := range hotTopics {
		maxHeat = math.Max(maxHeat, topic.heat)
	}
	topics := make([]TopicPulse, 0, len(hotTopics))
	for rank, hotTopic := range hotTopics {
		normalizedHeat := 0.05
		if maxHeat > 0 {
			normalizedHeat = math.Max(0.05, hotTopic.heat/maxHeat)
		}
		timestamps := cache.timestamps[hotTopic.topic.TopicId]
		lo := sort.Search(len(timestamps), func(index int) bool { return timestamps[index] >= from })
		hi := sort.Search(len(timestamps), func(index int) bool { return timestamps[index] > to })
		copied := append([]int64(nil), timestamps[lo:hi]...)
		topics = append(topics, TopicPulse{TopicMeta: TopicMeta{
			ID:             hotTopic.topic.TopicId,
			Title:          hotTopic.topic.Text,
			URL:            hotTopic.topic.Url,
			Rank:           rank + 1,
			RankDelta:      previousRanks[hotTopic.topic.TopicId] - (rank + 1),
			HeatScore:      normalizedHeat,
			FirstPopularAt: cache.firstPopularAt[hotTopic.topic.TopicId],
		}, Timestamps: copied})
	}
	return PulseSnapshot{SnapshotAt: cache.updatedAt, WindowFrom: from, WindowTo: to, Topics: topics}
}

func buildSnapshot(from, to int64) PulseSnapshot {
	cache.mu.RLock()
	defer cache.mu.RUnlock()
	return buildSnapshotFromTopicsLocked(rankTopicsForWindowLocked(from, to, gridSize), from, to)
}

func handlePulse(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC().Unix()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	if err := json.NewEncoder(w).Encode(buildSnapshot(now-liveWindowSecs, now)); err != nil {
		slog.Error("pulse: encode /api/pulse failed", "error", err)
	}
}

func handlePulseRange(w http.ResponseWriter, r *http.Request) {
	since, err1 := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	until, err2 := strconv.ParseInt(r.URL.Query().Get("until"), 10, 64)
	if err1 != nil || err2 != nil {
		http.Error(w, "invalid since or until parameter", http.StatusBadRequest)
		return
	}
	now := time.Now().UTC().Unix()
	if since < now-int64(cacheHours)*3600-120 {
		http.Error(w, fmt.Sprintf("since must be within last %dh", cacheHours), http.StatusBadRequest)
		return
	}
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
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-cache")
	if err := json.NewEncoder(w).Encode(buildSnapshot(since, until)); err != nil {
		slog.Error("pulse: encode /api/pulse/range failed", "error", err)
	}
}

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
	w.Header().Set("Cache-Control", "public, max-age=60, s-maxage=300, stale-while-revalidate=3600")
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
	response := TopicBriefResponse{Available: true, GeneratedAt: &brief.GeneratedAt, WindowStart: &brief.WindowStart, WindowEnd: &brief.WindowEnd, EntryCount: brief.EntryCount, Payload: &payload}
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("pulse: encode topic brief failed", "topic_id", topicID, "error", err)
	}
}

func handleMap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=300, s-maxage=3600, stale-while-revalidate=86400")
	mapCache.mu.RLock()
	snapshot := mapCache.snapshot
	mapCache.mu.RUnlock()
	if snapshot == nil {
		_ = json.NewEncoder(w).Encode(MapSnapshot{Available: false})
		return
	}
	if err := json.NewEncoder(w).Encode(snapshot); err != nil {
		slog.Error("map: encode snapshot failed", "error", err)
	}
}

func handleMapPage(w http.ResponseWriter, r *http.Request) {
	data, err := staticFiles.ReadFile("static/map.html")
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func main() {
	port := flag.Int("port", config.Config.ApiPort, "HTTP server port")
	flag.Parse()
	if err := model.InitDb(); err != nil {
		slog.Error("pulse: couldn't connect to database", "error", err)
		panic(err)
	}

	slog.Info("pulse: initial cache refresh...")
	refreshCache()
	layoutDir := os.Getenv("GOK_MAP_LAYOUT_DIR")
	if layoutDir == "" {
		layoutDir = filepath.Join("reports", "maps", "current", "layout")
	}
	graphDir := os.Getenv("GOK_MAP_GRAPH_DIR")
	if graphDir == "" {
		graphDir = filepath.Join("reports", "maps", "current", "graph")
	}
	snapshot, err := loadMapSnapshot(layoutDir, graphDir)
	if err != nil {
		slog.Warn("map: generated reports unavailable", "layout_dir", layoutDir, "graph_dir", graphDir, "error", err)
	} else {
		mapCache.snapshot = snapshot
		slog.Info("map: snapshot loaded", "nodes", len(snapshot.Nodes), "edges", len(snapshot.Edges), "communities", len(snapshot.Clusters))
	}

	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for range ticker.C {
			refreshCache()
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/map", handleMap)
	mux.HandleFunc("GET /api/topics/{topicID}/brief", handleTopicBrief)
	mux.HandleFunc("GET /api/pulse/range", handlePulseRange)
	mux.HandleFunc("GET /api/pulse", handlePulse)
	mux.Handle("GET /static/", http.FileServer(http.FS(staticFiles)))
	mux.HandleFunc("GET /map", handleMapPage)
	mux.HandleFunc("GET /atlas", handleMapPage)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		data, err := staticFiles.ReadFile("static/index.html")
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})

	addr := fmt.Sprintf(":%d", *port)
	slog.Info("pulse: listening", "addr", addr)
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		slog.Error("pulse: server error", "error", err)
	}
}
