package main

import (
	"log/slog"
	"os"
	"time"

	"gok/internal/config"
	"gok/internal/model"
	"gok/internal/scraper"

	"github.com/go-co-op/gocron"
)

func main() {
	err := model.InitDb()
	if err != nil {
		slog.Error("couldn't connect to the database", "error", err)
		panic(err)
	}

	slog.Info("starting scraper cron job")
	scraperCron := gocron.NewScheduler(time.UTC)

	_, err = scraperCron.Every(config.Config.ScrapeInterval).Minutes().Do(scraper.ScrapeAll)
	if err != nil {
		slog.Error("couldn't create scraper cron job", "error", err)
		os.Exit(1)
	}

	scraperCron.StartBlocking()
}
