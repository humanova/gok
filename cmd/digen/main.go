package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"syscall"
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

	lock, err := acquireProcessLock()
	if err != nil {
		slog.Error("couldn't acquire digen process lock", "error", err)
		os.Exit(1)
	}
	if lock == nil {
		slog.Warn("another digen process is already running; exiting")
		return
	}
	defer releaseProcessLock(lock)

	err = model.InitDb()
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
	runTopicBriefs := func() {
		if _, err := rag.GenerateTopicBriefs(ctx, llmClient); err != nil {
			slog.Error("topic brief generation failed", "error", err)
		}
	}

	if *once {
		slog.Info("running digest and topic briefs once")
		runDigest()
		runTopicBriefs()
		return
	}

	digestInterval := config.Config.DigestIntervalMinutes
	if digestInterval <= 0 {
		digestInterval = 240
	}
	briefInterval := config.Config.TopicBriefIntervalMinutes
	if briefInterval <= 0 {
		briefInterval = 720
	}

	// A restart must not leave the radar without topic explanations until the
	// next 12-hour tick. Existing fresh briefs are reused without an LLM call.
	latestBrief, err := model.LatestTopicBriefGeneratedAt()
	if err != nil {
		slog.Warn("couldn't check topic brief freshness", "error", err)
	} else if latestBrief == nil || time.Since(*latestBrief) >= time.Duration(briefInterval)*time.Minute {
		slog.Info("topic briefs are missing or stale; generating on startup")
		runTopicBriefs()
	}

	slog.Info("starting digest cron jobs", "digest_interval_minutes", digestInterval, "topic_brief_interval_minutes", briefInterval)
	digestCron := gocron.NewScheduler(time.UTC)
	_, err = digestCron.Every(digestInterval).Minutes().Do(runDigest)
	if err != nil {
		slog.Error("couldn't create digest cron job", "error", err)
		os.Exit(1)
	}
	_, err = digestCron.Every(briefInterval).Minutes().Do(runTopicBriefs)
	if err != nil {
		slog.Error("couldn't create topic brief cron job", "error", err)
		os.Exit(1)
	}

	digestCron.StartBlocking()
}

// acquireProcessLock ensures only one digest scheduler can use Gemini at a
// time, even when digen is launched manually as well as through run.sh.
func acquireProcessLock() (*os.File, error) {
	lock, err := os.OpenFile("/tmp/gok-digen.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			_ = lock.Close()
			return nil, nil
		}
		_ = lock.Close()
		return nil, err
	}
	return lock, nil
}

func releaseProcessLock(lock *os.File) {
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		slog.Warn("couldn't release digen process lock", "error", err)
	}
	if err := lock.Close(); err != nil {
		slog.Warn("couldn't close digen process lock", "error", err)
	}
}
