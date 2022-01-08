package scraper

import (
	"log"
	"gok/internal/model"
)

func ScrapeAll() {
	var entries []model.Entry
	var topics []model.Topic
	var popularTopics []model.PopularTopic
	var pTopics []model.PTopic // scraped
	var requests map[string]uint16

	topicsChan := make(chan []model.PTopic)
	entriesChan := make(chan []model.Entry)
	requestsChan := make(chan map[string]uint16)
	go scrapeEksiTopicsAndEntries(entriesChan, topicsChan, requestsChan)

	pTopics = <- topicsChan

	for _, pTopic := range pTopics {
		t := model.Topic{TopicId: pTopic.TopicId,
							 Text: pTopic.Text,
							 Url: pTopic.Url}
		pT := model.PopularTopic{TopicId: pTopic.TopicId,
										   NewEntries: pTopic.NewEntries,
										   Timestamp: pTopic.Timestamp,
										   PageNumber: pTopic.PageNumber}
		popularTopics = append(popularTopics, pT)
		topics = append(topics, t)
	}

	err := model.AddTopics(topics)
	if err != nil {
		log.Printf("[Scraper:main] could not insert topics : %s\n", err)
	}

	err = model.AddPopularTopics(popularTopics)
	if err != nil {
		log.Printf("[Scraper:main] could not insert pTopics : %s\n", err)
	}

	entries = <- entriesChan
	requests = <- requestsChan

	// insert entries in batches of 250
	batch := 250
	for i:=0; i < len(entries); i+= batch {
		j := i + batch
		if j > len(entries) {
			j = len(entries)
		}
		err := model.AddEntries(entries[i:j])
		if err != nil {
			log.Printf("[Scraper:main] could not insert entries to db : %s\n", err)
		}
	}

	var unsuccessfulRequests []model.Request
	for url, code := range requests {
		if code != 200 {
			unsuccessfulRequests = append(unsuccessfulRequests, model.Request{Url: url, StatusCode: code})
		}
	}

	err = model.AddRequests(unsuccessfulRequests)

	log.Printf("[Scraper:main] Scraped %d entries, %d topics, %d pTopics from eksisozluk.\n" +
		       "\t%d total requests (%d ok | %d failed).",
		       len(entries), len(topics), len(popularTopics), len(requests), len(requests)-len(unsuccessfulRequests),
		       len(unsuccessfulRequests))
}
