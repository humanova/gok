package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"gok/internal/config"
	"gok/internal/embedder"
	"gok/internal/llm"
	"gok/internal/model"
	"gok/internal/rag"

	"github.com/go-co-op/gocron"
)

func main() {
	once := flag.Bool("once", false, "run digest generation once and exit")
	flag.Parse()

	err := model.InitDb()
	if err != nil {
		slog.Error("couldn't connect to the database", "error", err)
		panic(err)
	}

	if config.Config.GeminiApiKey == "" {
		slog.Error("GeminiApiKey is not set, cannot run digen")
		os.Exit(1)
	}

	ctx := context.Background()

	embedClient := embedder.NewClient(config.Config.EmbedderUrl)
	llmClient, err := llm.NewClient(ctx, config.Config.GeminiApiKey, config.Config.GeminiModel)
	if err != nil {
		slog.Error("couldn't create LLM client", "error", err)
		os.Exit(1)
	}

	runDigest := func() {
		if _, err := rag.GenerateDigest(ctx, embedClient, llmClient); err != nil {
			slog.Error("digest generation failed", "error", err)
		}
	}

	if *once {
		slog.Info("running digest generation once")
		runDigest()
		return
	}

	digestInterval := config.Config.DigestIntervalMinutes
	if digestInterval <= 0 {
		digestInterval = 240
	}

	slog.Info("starting digest cron job", "interval_minutes", digestInterval)
	digestCron := gocron.NewScheduler(time.UTC)
	_, err = digestCron.Every(digestInterval).Minutes().Do(runDigest)
	if err != nil {
		slog.Error("couldn't create digest cron job", "error", err)
		os.Exit(1)
	}

	digestCron.StartBlocking()
}
