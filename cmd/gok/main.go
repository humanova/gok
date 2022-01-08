package main

import (
	"github.com/go-co-op/gocron"
	"gok/internal/config"
	"gok/internal/model"
	"gok/internal/scraper"
	"log"
	"time"
)

func main() {
	err := model.InitDb()
	if err != nil {
		log.Panicf("couldn't connect to the database : %v", err)
	}

	log.Println("Starting scraper cron job...")
	scraperCron := gocron.NewScheduler(time.UTC)

	_, err = scraperCron.Every(config.Config.ScrapeInterval).Minutes().Do(scraper.ScrapeAll)
	if err != nil {
		log.Fatalf("couldn't create cron job : %s", err)
	}
	scraperCron.StartBlocking()
}
