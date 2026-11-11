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
	"gok/internal/stories"

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

	storyModel, storyEmbeddingModel := config.Config.StoryModels()
	storyJudge := llmClient.StoryJudge(storyModel)
	storyEmbedder := llmClient.StoryEmbedder(storyEmbeddingModel)
	runStories := func() {
		release, ok, err := stories.TryLock()
		if err != nil || !ok {
			slog.Warn("story links skipped: another stories run holds the lock", "error", err)
			return
		}
		defer release()
		decided, err := stories.Run(ctx, stories.DBStore{}, storyEmbedder, storyJudge, stories.Options{
			Since: time.Now().UTC().Add(-stories.PoolWindow),
			Save:  true,
		})
		if err != nil {
			slog.Error("story links failed", "error", err, "decided", decided)
			return
		}
		slog.Info("story links decided", "count", decided)
	}

	if *once {
		slog.Info("running digest, topic briefs and story links once")
		runDigest()
		runTopicBriefs()
		runStories()
		return
	}

	briefInterval := config.Config.TopicBriefIntervalMinutes
	if briefInterval <= 0 {
		briefInterval = 720
	}

	digestCron := gocron.NewScheduler(time.UTC)
	digestInterval := config.Config.DigestIntervalMinutes
	slog.Info("starting digest cron jobs", "digest_interval_minutes", digestInterval, "topic_brief_interval_minutes", briefInterval)
	if digestInterval > 0 {
		_, err = digestCron.Every(digestInterval).Minutes().Do(runDigest)
		if err != nil {
			slog.Error("couldn't create digest cron job", "error", err)
			os.Exit(1)
		}
	} else {
		slog.Info("digest generation disabled")
	}
	_, err = digestCron.Every(briefInterval).Minutes().Do(runTopicBriefs)
	if err != nil {
		slog.Error("couldn't create topic brief cron job", "error", err)
		os.Exit(1)
	}
	if storyInterval := config.Config.StoryIntervalMinutes; storyInterval > 0 {
		slog.Info("starting story link job", "interval_minutes", storyInterval, "model", storyModel, "embedding_model", storyEmbeddingModel)
		_, err = digestCron.Every(storyInterval).Minutes().Do(runStories)
		if err != nil {
			slog.Error("couldn't create story link cron job", "error", err)
			os.Exit(1)
		}
	} else {
		slog.Info("story links disabled")
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
