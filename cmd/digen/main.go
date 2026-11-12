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

	llmClient, err := llm.NewClient(ctx, config.Config.GeminiApiKey, config.Config.GeminiModel)
	if err != nil {
		slog.Error("couldn't create LLM client", "error", err)
		os.Exit(1)
	}

	storyModel, storyEmbeddingModel := config.Config.StoryModels()
	storyJudge := llm.NewDecider(config.Config.GeminiApiKey, storyModel)
	runTopicBriefs := func() {
		if _, err := rag.GenerateTopicBriefs(ctx, llmClient, storyJudge); err != nil {
			slog.Error("topic brief generation failed", "error", err)
		}
	}
	runAgenda := func() {
		if _, err := rag.ScoreAgenda(ctx, storyJudge); err != nil {
			slog.Error("agenda scoring failed", "error", err)
		}
	}
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
		slog.Info("running topic briefs, agenda scores and story links once")
		runTopicBriefs()
		runAgenda()
		runStories()
		return
	}

	briefInterval := config.Config.TopicBriefIntervalMinutes
	if briefInterval <= 0 {
		briefInterval = 720
	}

	scheduler := gocron.NewScheduler(time.UTC)
	slog.Info("starting topic brief job", "interval_minutes", briefInterval)
	_, err = scheduler.Every(briefInterval).Minutes().Do(runTopicBriefs)
	if err != nil {
		slog.Error("couldn't create topic brief cron job", "error", err)
		os.Exit(1)
	}
	if storyInterval := config.Config.StoryIntervalMinutes; storyInterval > 0 {
		slog.Info("starting story link job", "interval_minutes", storyInterval, "model", storyModel, "embedding_model", storyEmbeddingModel)
		_, err = scheduler.Every(storyInterval).Minutes().Do(runStories)
		if err != nil {
			slog.Error("couldn't create story link cron job", "error", err)
			os.Exit(1)
		}
	} else {
		slog.Info("story links disabled")
	}

	if agendaInterval := config.Config.AgendaIntervalMinutes; agendaInterval > 0 {
		slog.Info("starting agenda job", "interval_minutes", agendaInterval)
		_, err = scheduler.Every(agendaInterval).Minutes().Do(runAgenda)
		if err != nil {
			slog.Error("couldn't create agenda cron job", "error", err)
			os.Exit(1)
		}
	} else {
		slog.Info("agenda scores disabled")
	}

	scheduler.StartBlocking()
}

// acquireProcessLock ensures only one digen scheduler can use Gemini at a
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
