package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"gok/internal/config"
	"gok/internal/embedder"
	"gok/internal/llm"
	"gok/internal/model"
	"gok/internal/rag"
	"gok/internal/scraper"

	"github.com/go-co-op/gocron"
)

func main() {
	err := model.InitDb()
	if err != nil {
		slog.Error("couldn't connect to the database", "error", err)
		panic(err)
	}

	ctx := context.Background()

	// Build shared clients (embedder + LLM) — used by the digest cron job.
	embedClient := embedder.NewClient(config.Config.EmbedderUrl)
	llmClient, err := llm.NewClient(ctx, config.Config.GeminiApiKey, config.Config.GeminiModel)
	if err != nil {
		slog.Error("couldn't create LLM client", "error", err)
		os.Exit(1)
	}

	slog.Info("starting scraper cron job")
	scraperCron := gocron.NewScheduler(time.UTC)

	_, err = scraperCron.Every(config.Config.ScrapeInterval).Minutes().Do(scraper.ScrapeAll)
	if err != nil {
		slog.Error("couldn't create scraper cron job", "error", err)
		os.Exit(1)
	}

	digestInterval := config.Config.DigestIntervalMinutes
	if digestInterval <= 0 {
		digestInterval = 30
	}
	_, err = scraperCron.Every(digestInterval).Minutes().Do(func() {
		if _, err := rag.GenerateDigest(ctx, embedClient, llmClient); err != nil {
			slog.Error("digest generation failed", "error", err)
		}
	})
	if err != nil {
		slog.Error("couldn't create digest cron job", "error", err)
		os.Exit(1)
	}

	scraperCron.StartBlocking()
}
