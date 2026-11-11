// Command stories links new topics into story threads over a past period, the
// same way the digen job does live. Topics are decided in the order they got
// their third entry, so a historical run sees what the live job would have seen.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"gok/internal/config"
	"gok/internal/llm"
	"gok/internal/model"
	"gok/internal/stories"
)

func main() {
	since := flag.String("since", "", "first day to cover, YYYY-MM-DD (UTC); topics born before it are skipped")
	until := flag.String("until", "", "last day to cover, YYYY-MM-DD (UTC): topics that got their third entry later are left out; default now")
	limit := flag.Int("limit", 0, "stop after this many decisions (0 = no limit)")
	dryRun := flag.Bool("dry-run", false, "print decisions without saving them")
	flag.Parse()

	if *since == "" {
		fmt.Fprintln(os.Stderr, "usage: stories -since YYYY-MM-DD [-until YYYY-MM-DD] [-limit N] [-dry-run]")
		os.Exit(2)
	}
	opts := stories.Options{Limit: *limit, Save: !*dryRun}
	var err error
	if opts.Since, err = time.Parse("2006-01-02", *since); err != nil {
		slog.Error("invalid -since", "error", err)
		os.Exit(2)
	}
	if *until != "" {
		day, err := time.Parse("2006-01-02", *until)
		if err != nil {
			slog.Error("invalid -until", "error", err)
			os.Exit(2)
		}
		opts.Until = day.Add(24*time.Hour - time.Second)
	}
	if config.Config.GeminiApiKey == "" {
		slog.Error("GeminiApiKey is not set")
		os.Exit(1)
	}

	release, ok, err := stories.TryLock()
	if err != nil || !ok {
		slog.Error("another stories run holds the lock", "error", err)
		os.Exit(3)
	}
	defer release()

	if err := model.InitStoryDb(); err != nil {
		slog.Error("couldn't connect to the database", "error", err)
		os.Exit(1)
	}
	ctx := context.Background()
	client, err := llm.NewClient(ctx, config.Config.GeminiApiKey, config.Config.GeminiModel)
	if err != nil {
		slog.Error("couldn't create LLM client", "error", err)
		os.Exit(1)
	}
	judgeModel, embeddingModel := config.Config.StoryModels()

	opts.OnDecision = func(node model.TopicStory, parentTitle string) {
		ready := time.Unix(node.ReadyAt, 0).UTC().Format("2006-01-02 15:04")
		if node.ParentTopicID == nil {
			fmt.Printf("%s  %d %s\n", ready, node.TopicID, node.Title)
			return
		}
		fmt.Printf("%s  %d %s  <-  %d %s  | %s\n", ready, node.TopicID, node.Title, *node.ParentTopicID, parentTitle, node.Reason)
	}
	decided, err := stories.Run(ctx, stories.DBStore{}, client.StoryEmbedder(embeddingModel), client.StoryJudge(judgeModel), opts)
	if err != nil {
		slog.Error("stories run stopped", "error", err, "decided", decided)
		os.Exit(1)
	}
	slog.Info("stories run done", "decided", decided, "saved", opts.Save, "model", judgeModel)
}
