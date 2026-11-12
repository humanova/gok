package main

import (
	"log/slog"
	"net/http"
	"sync"

	"gok/internal/model"
	"gok/internal/rag"
)

var agendaCache struct {
	mu      sync.RWMutex
	byTopic map[uint64]bool
}

// refreshAgenda loads which topics in the pulse cache belong to the gündem view.
func refreshAgenda() {
	cache.mu.RLock()
	topicIDs := make([]uint64, 0, len(cache.topicMeta))
	for topicID := range cache.topicMeta {
		topicIDs = append(topicIDs, topicID)
	}
	cache.mu.RUnlock()

	rows, err := model.GetTopicAgendas(topicIDs)
	if err != nil {
		slog.Error("agenda: load failed", "error", err)
		return
	}
	byTopic := make(map[uint64]bool, len(rows))
	for topicID, row := range rows {
		if row.Score >= rag.AgendaThreshold {
			byTopic[topicID] = true
		}
	}

	agendaCache.mu.Lock()
	agendaCache.byTopic = byTopic
	agendaCache.mu.Unlock()
	slog.Info("agenda: cache refreshed", "topics", len(byTopic))
}

// agendaFilter returns the gündem topics when the request asks for them, else nil (all topics).
func agendaFilter(r *http.Request) map[uint64]bool {
	if r.URL.Query().Get("gundem") != "1" {
		return nil
	}
	agendaCache.mu.RLock()
	defer agendaCache.mu.RUnlock()
	if agendaCache.byTopic == nil {
		return map[uint64]bool{}
	}
	return agendaCache.byTopic
}
