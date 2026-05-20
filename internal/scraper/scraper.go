package scraper

import (
	"log/slog"

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
