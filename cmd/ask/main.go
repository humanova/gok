package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"gok/internal/config"
	"gok/internal/embedder"
	"gok/internal/llm"
	"gok/internal/model"
	"gok/internal/rag"
)

func main() {
	topicFlag := flag.String("topic", "", "Topic filter (substring match)")
	limitFlag := flag.String("limit", "24h", "Time window: e.g. 1d, 12h, 30m, 1w")
	noEmbedFlag := flag.Bool("no-embed", false, "Skip vector search, use FTS only")
	flag.Parse()

	question := strings.TrimSpace(strings.Join(flag.Args(), " "))

	if err := model.InitDb(); err != nil {
		slog.Error("db init failed", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	var embClient *embedder.Client
	if !*noEmbedFlag && config.Config.EmbedderUrl != "" {
		embClient = embedder.NewClient(config.Config.EmbedderUrl)
		if !embClient.Healthy(ctx) {
			fmt.Fprintln(os.Stderr, "⚠  embedder sidecar unavailable — falling back to FTS-only")
			embClient = nil
		}
	}

	var llmClient *llm.Client
	if config.Config.GeminiApiKey != "" {
		var err error
		llmClient, err = llm.NewClient(ctx, config.Config.GeminiApiKey, config.Config.GeminiModel)
		if err != nil {
			slog.Error("llm init failed", "error", err)
			os.Exit(1)
		}
	} else {
		fmt.Fprintln(os.Stderr, "GeminiApiKey not set — LLM synthesis disabled")
	}

	if question != "" {
		// Single-run mode
		since, err := parseLimit(*limitFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid --limit value %q: %s\n", *limitFlag, err)
			os.Exit(1)
		}
		runQuery(ctx, question, *topicFlag, since, embClient, llmClient)
		return
	}

	// Interactive mode
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("╔══════════════════════════════════╗")
	fmt.Println("║         gok – ask mode           ║")
	fmt.Println("║  Ctrl-C or empty question → exit ║")
	fmt.Println("╚══════════════════════════════════╝")

	for {
		topic := prompt(scanner, "\nTopic filter (blank = all): ")
		limitStr := promptDefault(scanner, "Time limit (1d/12h/1w/…, blank = 24h): ", "24h")
		since, err := parseLimit(limitStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Invalid time limit %q, using 24h\n", limitStr)
			since = 24 * time.Hour
		}
		question := prompt(scanner, "Question: ")
		if question == "" {
			fmt.Println("Exiting.")
			break
		}
		runQuery(ctx, question, topic, since, embClient, llmClient)
		fmt.Println("\n" + strings.Repeat("─", 60))
	}
}

func runQuery(ctx context.Context, question, topicFilter string, window time.Duration, embClient *embedder.Client, llmClient *llm.Client) {
	since := time.Now().UTC().Add(-window)
	filters := rag.SearchFilters{
		TopicName: topicFilter,
		Since:     since,
		MaxResult: 80,
	}

	var queryVec []float32
	if embClient != nil {
		var err error
		queryVec, err = embClient.EmbedQuery(ctx, question)
		if err != nil {
			fmt.Fprintf(os.Stderr, "⚠  embed query failed: %v — FTS only\n", err)
			queryVec = nil
		}
	}

	db := model.DB()
	entries, err := rag.HybridSearch(ctx, question, queryVec, db, filters)
	if err != nil {
		fmt.Fprintf(os.Stderr, "search error: %v\n", err)
		os.Exit(1)
	}

	if len(entries) == 0 {
		fmt.Println("No results found.")
		return
	}

	viewpoints := rag.ExtractViewpoints(entries, 4)

	if llmClient == nil {
		// No LLM: just print raw entries
		fmt.Printf("\n%d entries found:\n\n", len(entries))
		for i, e := range entries {
			ts := time.Unix(e.Timestamp, 0).Format("02.01.2006 15:04")
			fmt.Printf("[%s, %s]\n%s\n\n", e.Author, ts, e.Text)
			if i >= 9 {
				fmt.Printf("...and %d more entries\n", len(entries)-10)
				break
			}
		}
		return
	}

	fmt.Printf("\n🔍 %d entries retrieved\n\n", len(entries))

	tokenCh, errCh := llmClient.StreamAnswer(ctx, question, entries, viewpoints)

	for token := range tokenCh {
		fmt.Print(token)
	}
	fmt.Println()

	if err := <-errCh; err != nil {
		fmt.Fprintf(os.Stderr, "\nstream error: %v\n", err)
	}
}

// parseLimit converts a human duration string to time.Duration.
// Supports: Nd (days), Nw (weeks), Nh (hours), Nm (minutes), Ns (seconds).
func parseLimit(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 24 * time.Hour, nil
	}
	// try standard Go duration first
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	// custom suffixes
	suffixes := map[string]time.Duration{
		"d": 24 * time.Hour,
		"w": 7 * 24 * time.Hour,
	}
	for suffix, unit := range suffixes {
		if strings.HasSuffix(s, suffix) {
			numStr := strings.TrimSuffix(s, suffix)
			var n float64
			if _, err := fmt.Sscanf(numStr, "%f", &n); err != nil {
				return 0, fmt.Errorf("cannot parse %q", s)
			}
			return time.Duration(n * float64(unit)), nil
		}
	}
	return 0, fmt.Errorf("unrecognised duration %q (use e.g. 1d, 12h, 30m, 1w)", s)
}

func prompt(scanner *bufio.Scanner, label string) string {
	fmt.Print(label)
	if !scanner.Scan() {
		fmt.Println()
		os.Exit(0)
	}
	return strings.TrimSpace(scanner.Text())
}

func promptDefault(scanner *bufio.Scanner, label, def string) string {
	v := prompt(scanner, label)
	if v == "" {
		return def
	}
	return v
}
