package scraper

import (
	"context"
	"log/slog"

	"gok/internal/config"
	"gok/internal/embedder"
	"gok/internal/model"
)

func ScrapeAll() {
	var entries []model.Entry
	var topics []model.Topic
	var popularTopics []model.PopularTopic
	var entryAttachments []model.EntryAttachment
	var pTopics []model.PTopic // scraped
	var requests map[string]uint16

	topicsChan := make(chan []model.PTopic)
	entriesChan := make(chan []model.Entry)
	attachmentsChan := make(chan []model.EntryAttachment)
	requestsChan := make(chan map[string]uint16)
	go scrapeEksiTopicsAndEntries(entriesChan, topicsChan, attachmentsChan, requestsChan)

	pTopics = <-topicsChan

	for _, pTopic := range pTopics {
		t := model.Topic{TopicId: pTopic.TopicId,
			Text: pTopic.Text,
			Url:  pTopic.Url}
		pT := model.PopularTopic{TopicId: pTopic.TopicId,
			NewEntries: pTopic.NewEntries,
			Timestamp:  pTopic.Timestamp,
			PageNumber: pTopic.PageNumber}
		popularTopics = append(popularTopics, pT)
		topics = append(topics, t)
	}

	err := model.AddTopics(topics)
	if err != nil {
		slog.Error("could not insert topics", "error", err)
	}

	err = model.AddPopularTopics(popularTopics)
	if err != nil {
		slog.Error("could not insert popular topics", "error", err)
	}

	entries = <-entriesChan
	entryAttachments = <-attachmentsChan
	requests = <-requestsChan

	// insert entries in batches of 250
	batch := 250
	for i := 0; i < len(entries); i += batch {
		j := min(i+batch, len(entries))
		err := model.AddEntries(entries[i:j])
		if err != nil {
			slog.Error("could not insert entries", "error", err)
		}
	}

	// embed entries asynchronously so scrape latency is unaffected
	go embedEntries(entries)

	err = model.AddEntryAttachments(entryAttachments)
	if err != nil {
		slog.Error("could not insert entry attachments", "error", err)
	}

	var unsuccessfulRequests []model.Request
	for url, code := range requests {
		if code != 200 {
			unsuccessfulRequests = append(unsuccessfulRequests, model.Request{Url: url, StatusCode: code})
		}
	}

	err = model.AddRequests(unsuccessfulRequests)
	if err != nil {
		slog.Error("could not insert failed requests", "error", err)
	}

	slog.Info("scrape complete",
		"entries", len(entries),
		"topics", len(topics),
		"links", len(entryAttachments),
		"total_requests", len(requests),
		"ok_requests", len(requests)-len(unsuccessfulRequests),
		"failed_requests", len(unsuccessfulRequests),
	)
}

// embedEntries calls the embedder sidecar and persists embeddings for a batch of entries.
// Runs asynchronously — errors are logged but do not fail the scrape.
func embedEntries(entries []model.Entry) {
	if config.Config.EmbedderUrl == "" {
		return
	}
	client := embedder.NewClient(config.Config.EmbedderUrl)
	ctx := context.Background()

	if !client.Healthy(ctx) {
		slog.Warn("embedder sidecar not reachable, skipping embedding")
		return
	}

	batchSize := 64
	for i := 0; i < len(entries); i += batchSize {
		j := min(i+batchSize, len(entries))
		batch := entries[i:j]

		texts := make([]string, len(batch))
		for k, e := range batch {
			texts[k] = e.Text
		}

		vecs, err := client.EmbedPassages(ctx, texts)
		if err != nil {
			slog.Error("embedding batch failed", "error", err, "offset", i)
			continue
		}

		if err := model.UpdateEntryEmbeddings(batch, vecs); err != nil {
			slog.Error("persisting embeddings failed", "error", err, "offset", i)
		}
	}

	slog.Info("embedding complete", "entries", len(entries))
}
