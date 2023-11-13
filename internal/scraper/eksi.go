package scraper

import (
	"github.com/gocolly/colly"
	"gok/internal/config"
	model "gok/internal/model"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
)

type popularTopics []model.PTopic

func containsString(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

func containsUint64(s []uint64, e uint64) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

func (topics *popularTopics) appendTopics(id int, element *colly.HTMLElement) {
	// get topic url
	urlSlice := []string{"https://eksisozluk1923.com", strings.Split(element.Attr("href"), "?")[0]}
	topicUrl := strings.Join(urlSlice, "")
	// get topic_id by using '--' seperator. format : /<topic>--<topic_id>.
	topicId, err := strconv.ParseUint(strings.Split(topicUrl, "--")[1], 10, 64)
	if err != nil {
		topicId = 0
		log.Println("[Scraper:eksi] Couldn't scrape topic id (topic scraper)")
	}

	urlQuery := element.Request.URL.RawQuery
	pageNumber, err := strconv.ParseUint(urlQuery[len(urlQuery)-1:], 10, 64) // last character of rawQuery
	if err != nil {
		pageNumber = 0
		log.Println("[Scraper:eksi] Couldn't scrape page number (topic scraper)")
	}

	newEntriesRaw := element.DOM.Find("small").Text()
	newEntriesString := strings.Replace(newEntriesRaw, "b", "00", 1)
	newEntriesString = strings.Replace(newEntriesString, ",", "", 1)

	newEntryCount, err := strconv.ParseUint(newEntriesString, 10, 64)
	if err != nil {
		log.Println("[Scraper:eksi] Couldn't scrape new entry count")
		newEntryCount = 0
	}

	// "topic (new entry count)" -> "topic"
	topicText := element.DOM.Text()[0 : len(element.DOM.Text())-len(newEntriesRaw)-1]

	*topics = append(*topics, model.PTopic{Text: topicText,
		Url:        topicUrl,
		NewEntries: newEntryCount,
		Timestamp:  time.Now().Unix(),
		TopicId:    topicId,
		PageNumber: pageNumber})
}

func scrapeEksiTopicsAndEntries(entriesChan chan []model.Entry,
	topicsChan chan []model.PTopic,
	attachmentsChan chan []model.EntryAttachment,
	requestsChan chan map[string]uint16) {
	const baseUrl = "https://eksisozluk1923.com"
	const popularTopicsPath = "/basliklar/gundem"
	topicPages := []string{"1", "2", "3", "4", "5"}
	const timeLayout = "02.01.2006 15:04"

	var topics popularTopics
	var entries []model.Entry
	var attachments []model.EntryAttachment
	requests := make(map[string]uint16)
	var scrapedIds []uint64

	entriesMutex := &sync.Mutex{}
	attachmentsMutex := &sync.Mutex{}
	requestsMutex := &sync.RWMutex{}
	scrapedIdsMutex := &sync.Mutex{}

	topicCollector := colly.NewCollector(
		colly.AllowedDomains("eksisozluk1923.com"),
	)

	entryCollector := colly.NewCollector(
		colly.AllowedDomains("eksisozluk1923.com"),
		colly.MaxDepth(1),
		colly.Async(true),
	)

	entryCollector.Limit(&colly.LimitRule{
		DomainGlob:  "*",
		Parallelism: 6,
		Delay:       time.Duration(config.Config.EntryCollectorDelay) * time.Millisecond,
		RandomDelay: time.Duration(config.Config.EntryCollectorRandomDelay) * time.Millisecond,
	})

	entryCollector.OnError(func(r *colly.Response, e error) {
		requestsMutex.Lock()
		requests[r.Request.URL.String()] = uint16(r.StatusCode)
		requestsMutex.Unlock()
	})

	entryCollector.OnRequest(func(r *colly.Request) {
		requestsMutex.Lock()
		requests[r.URL.String()] = 0
		requestsMutex.Unlock()
	})

	entryCollector.OnResponse(func(r *colly.Response) {
		requestsMutex.Lock()
		requests[r.Request.URL.String()] = uint16(r.StatusCode)
		requestsMutex.Unlock()
	})

	// scrape popular topics
	topicCollector.OnHTML("ul[class]", func(e *colly.HTMLElement) {
		if e.Attr("class") != "topic-list partial" {
			return
		}
		e.ForEach("a", topics.appendTopics)

		if len(topics) == 0 {
			log.Println("[Scraper:eksi] Couldn't scrape any topics")
			return
		}
	})

	// scrape entries
	entryCollector.OnHTML("div[id]", func(e *colly.HTMLElement) {
		if e.Attr("id") != "topic" {
			return
		}
		topicUrl := e.ChildAttr("a[itemprop]", "href")
		// topicTitle := e.ChildAttr("h1[data-title]", "data-title")
		topicId, err := strconv.ParseUint(e.ChildAttr("h1[data-title]", "data-id"), 10, 64)
		if err != nil {
			topicId = 0
			log.Println("[Scraper:eksi] Couldn't parse topic id (entry scraper)")
		}
		var lastEntryId uint64 = 0

		e.ForEach("li[data-id]", func(_ int, tEntry *colly.HTMLElement) {
			entryId, err := strconv.ParseUint(tEntry.Attr("data-id"), 10, 64)
			// check if it's not already scraped
			if containsUint64(scrapedIds, entryId) {
				return
			}

			author := tEntry.Attr("data-author")
			if err != nil {
				log.Println("[Scraper:eksi] Couldn't scrape entry id")
				entryId = 0
			}

			text := tEntry.ChildText("div .content")
			tEntry.DOM.Find("div .content")
			text = strings.TrimSuffix(strings.TrimPrefix(text, "\n    "), "\n")

			favString := tEntry.Attr("data-favorite-count")

			score, err := strconv.ParseInt(favString, 10, 64)
			if err != nil {
				log.Println("[Scraper:eksi] Couldn't parse entry score")
				score = 0
			}

			dateStr := tEntry.ChildText("a[class='entry-date permalink']")
			if strings.Contains(dateStr, "~") {
				dateStr = strings.Split(dateStr, " ~")[0]
			}

			urlPath := tEntry.ChildAttr("a[class='entry-date permalink']", "href")
			url := baseUrl + urlPath

			entryTime, _ := time.Parse(timeLayout, dateStr)
			entryTime = entryTime.Add(time.Duration(-3) * time.Hour)

			// scrape entry links (attachments)
			tEntry.ForEach("a[class='url']", func(_ int, tLink *colly.HTMLElement) {
				attachmentsMutex.Lock()
				attachments = append(attachments, model.EntryAttachment{EntryId: entryId, Url: tLink.Attr("href")})
				attachmentsMutex.Unlock()
			})

			p := model.Entry{
				EntryId:   entryId,
				Author:    author,
				Text:      text,
				Url:       url,
				Timestamp: entryTime.Unix(),
				Score:     score,
				TopicId:   topicId,
			}

			entriesMutex.Lock()
			entries = append(entries, p)
			entriesMutex.Unlock()

			scrapedIdsMutex.Lock()
			scrapedIds = append(scrapedIds, entryId)
			scrapedIdsMutex.Unlock()

			lastEntryId = entryId
		})

		slc := []string{baseUrl, topicUrl, "?focusto=", strconv.FormatUint(lastEntryId+1, 10)}
		nextPageUrl := strings.Join(slc, "")
		if _, exists := requests[nextPageUrl]; !exists && lastEntryId != 0 {
			entryCollector.Visit(nextPageUrl)
		}
	})

	// -----------------
	log.Println("[Scraper:eksi] Scraping popular topics from eksisozluk...")
	for _, pageNum := range topicPages {
		url := strings.Join([]string{baseUrl, popularTopicsPath, "?p=", pageNum}, "")
		topicCollector.Visit(url)
	}
	topicsChan <- topics

	for _, topic := range topics {
		var url string
		// use the last entry's id as a starting point, if scraped
		entry, err := model.GetLastTopicEntry(topic.TopicId)
		if err != nil {
			url = strings.Join([]string{topic.Url, "?a=popular&p=1"}, "")
		} else {
			url = strings.Join([]string{topic.Url, "?focusto=", strconv.FormatUint(entry.EntryId+1, 10)}, "")
		}
		entryCollector.Visit(url)
	}
	entryCollector.Wait()
	entriesChan <- entries
	attachmentsChan <- attachments
	requestsChan <- requests
}
