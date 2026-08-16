package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gok/internal/mapcuration"
	"gok/internal/model"
)

const daySeconds = 24 * 60 * 60

type topicProfile struct {
	TopicID          uint64  `json:"topic_id"`
	Title            string  `json:"title"`
	URL              string  `json:"url"`
	FirstEntryAt     int64   `json:"first_entry_at"`
	LastEntryAt      int64   `json:"last_entry_at"`
	LifetimeDays     int     `json:"lifetime_days"`
	Entries          int64   `json:"entries"`
	DistinctAuthors  int64   `json:"distinct_authors"`
	ReturningAuthors int64   `json:"returning_authors"`
	ActiveDays       int64   `json:"active_days"`
	ActiveWeeks      int64   `json:"active_weeks"`
	ActiveMonths     int64   `json:"active_months"`
	PeakDayEntries   int64   `json:"peak_day_entries"`
	PeakWeekEntries  int64   `json:"peak_week_entries"`
	PeakDayShare     float64 `json:"peak_day_share"`
	PeakWeekShare    float64 `json:"peak_week_share"`
	PopularSnapshots int64   `json:"popular_snapshots"`
	FirstPopularAt   *int64  `json:"first_popular_at,omitempty"`
	LastPopularAt    *int64  `json:"last_popular_at,omitempty"`
}

type authorProfile struct {
	Author  string `json:"author"`
	Entries int64  `json:"entries"`
	Topics  int64  `json:"topics"`
}

type profileSummary struct {
	GeneratedAt       time.Time `json:"generated_at"`
	WindowStart       time.Time `json:"window_start"`
	WindowEnd         time.Time `json:"window_end"`
	TopicCount        int       `json:"topic_count"`
	AuthorCount       int       `json:"author_count"`
	EligibleTopics    int       `json:"eligible_topics"`
	LikelyOneOff      int       `json:"likely_one_off_topics"`
	TopicQuantiles    quantiles `json:"topic_quantiles"`
	AuthorTopicCounts quantiles `json:"author_topic_counts"`
}

type quantiles struct {
	P50 float64 `json:"p50"`
	P90 float64 `json:"p90"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

func main() {
	days := flag.Int("days", 365, "Profile entries from the last N days; 0 means all history")
	outDir := flag.String("out", "", "Output directory; default: reports/map-profile-YYYYMMDD-HHMMSS")
	minAuthors := flag.Int64("min-authors", 30, "Candidate durable-topic minimum distinct authors")
	minWeeks := flag.Int64("min-weeks", 6, "Candidate durable-topic minimum active weeks")
	minReturningAuthors := flag.Int64("min-returning-authors", 10, "Candidate durable-topic minimum writers active in multiple months")
	maxPeakWeekShare := flag.Float64("max-peak-week-share", 0.50, "Candidate durable-topic maximum one-week entry share")
	flag.Parse()

	if *days < 0 || *minAuthors < 1 || *minWeeks < 1 || *minReturningAuthors < 1 || *maxPeakWeekShare <= 0 || *maxPeakWeekShare > 1 {
		fmt.Fprintln(os.Stderr, "invalid profile thresholds")
		os.Exit(2)
	}
	if err := model.InitDb(); err != nil {
		slog.Error("couldn't connect to database", "error", err)
		os.Exit(1)
	}

	now := time.Now().UTC()
	since := int64(0)
	if *days > 0 {
		since = now.AddDate(0, 0, -*days).Unix()
	}
	if *outDir == "" {
		*outDir = filepath.Join("reports", "map-profile-"+now.Format("20060102-150405"))
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		slog.Error("couldn't create output directory", "error", err)
		os.Exit(1)
	}

	slog.Info("profiling map candidates", "since", since, "out", *outDir)
	topics, err := loadTopicProfiles(since)
	if err != nil {
		slog.Error("topic profile query failed", "error", err)
		os.Exit(1)
	}
	authors, err := loadAuthorProfiles(since)
	if err != nil {
		slog.Error("author profile query failed", "error", err)
		os.Exit(1)
	}
	if err := writeTopicCSV(filepath.Join(*outDir, "topics.csv"), topics); err != nil {
		slog.Error("couldn't write topic report", "error", err)
		os.Exit(1)
	}
	if err := writeAuthorCSV(filepath.Join(*outDir, "authors.csv"), authors); err != nil {
		slog.Error("couldn't write author report", "error", err)
		os.Exit(1)
	}

	summary := buildSummary(now, since, topics, authors, *minAuthors, *minWeeks, *minReturningAuthors, *maxPeakWeekShare)
	if err := mapcuration.WriteJSON(filepath.Join(*outDir, "summary.json"), summary); err != nil {
		slog.Error("couldn't write summary", "error", err)
		os.Exit(1)
	}
	slog.Info("map profile complete", "topics", len(topics), "authors", len(authors), "eligible_topics", summary.EligibleTopics, "likely_one_off", summary.LikelyOneOff, "out", *outDir)
}

func loadTopicProfiles(since int64) ([]topicProfile, error) {
	// This single all-history aggregation feeds durable-topic selection later in
	// the pipeline; the graph stage reads its CSV instead of rerunning this query.
	const query = `WITH scoped_entries AS (
		SELECT topic_id, author, timestamp
		FROM entries
		WHERE deleted_at IS NULL AND timestamp >= ? AND author <> ''
	), topic_base AS (
		SELECT topic_id, MIN(timestamp) AS first_entry_at, MAX(timestamp) AS last_entry_at,
			COUNT(*) AS entries, COUNT(DISTINCT author) AS distinct_authors,
			COUNT(DISTINCT date_trunc('day', to_timestamp(timestamp))) AS active_days,
			COUNT(DISTINCT date_trunc('week', to_timestamp(timestamp))) AS active_weeks,
			COUNT(DISTINCT date_trunc('month', to_timestamp(timestamp))) AS active_months
		FROM scoped_entries GROUP BY topic_id
	), returning_authors AS (
		-- A returning author must appear in at least two separate calendar months,
		-- preventing a busy single-day thread from looking durable.
		SELECT topic_id, COUNT(*) AS returning_authors
		FROM (
			SELECT topic_id, author FROM scoped_entries
			GROUP BY topic_id, author HAVING COUNT(DISTINCT date_trunc('month', to_timestamp(timestamp))) >= 2
		) recurring GROUP BY topic_id
	), daily_counts AS (
		SELECT topic_id, date_trunc('day', to_timestamp(timestamp)) AS bucket, COUNT(*) AS entries
		FROM scoped_entries GROUP BY topic_id, bucket
	), weekly_counts AS (
		SELECT topic_id, date_trunc('week', to_timestamp(timestamp)) AS bucket, COUNT(*) AS entries
		FROM scoped_entries GROUP BY topic_id, bucket
	), peak_days AS (SELECT topic_id, MAX(entries) AS peak_day_entries FROM daily_counts GROUP BY topic_id),
	peak_weeks AS (SELECT topic_id, MAX(entries) AS peak_week_entries FROM weekly_counts GROUP BY topic_id),
	popular AS (
		SELECT topic_id, COUNT(*) AS popular_snapshots, MIN(timestamp) AS first_popular_at, MAX(timestamp) AS last_popular_at
		FROM popular_topics WHERE deleted_at IS NULL AND timestamp >= ? GROUP BY topic_id
	)
	SELECT t.topic_id, t.text AS title, t.url, b.first_entry_at, b.last_entry_at,
		b.entries, b.distinct_authors, COALESCE(r.returning_authors, 0) AS returning_authors, b.active_days, b.active_weeks, b.active_months,
		pday.peak_day_entries, pweek.peak_week_entries, COALESCE(p.popular_snapshots, 0) AS popular_snapshots, p.first_popular_at, p.last_popular_at
	FROM topic_base b
	JOIN topics t ON t.topic_id = b.topic_id AND t.deleted_at IS NULL
	JOIN peak_days pday ON pday.topic_id = b.topic_id
	JOIN peak_weeks pweek ON pweek.topic_id = b.topic_id
	LEFT JOIN returning_authors r ON r.topic_id = b.topic_id
	LEFT JOIN popular p ON p.topic_id = b.topic_id
	ORDER BY b.entries DESC`

	type row struct {
		TopicID, FirstEntryAt, LastEntryAt, Entries, DistinctAuthors, ReturningAuthors, ActiveDays, ActiveWeeks, ActiveMonths, PeakDayEntries, PeakWeekEntries, PopularSnapshots int64
		Title, URL                                                                                                                                                               string
		FirstPopularAt, LastPopularAt                                                                                                                                            *int64
	}
	var rows []row
	if err := model.DB().Raw(query, since, since).Scan(&rows).Error; err != nil {
		return nil, err
	}
	profiles := make([]topicProfile, 0, len(rows))
	for _, row := range rows {
		profiles = append(profiles, topicProfile{
			TopicID: uint64(row.TopicID), Title: row.Title, URL: row.URL, FirstEntryAt: row.FirstEntryAt, LastEntryAt: row.LastEntryAt,
			LifetimeDays: int((row.LastEntryAt-row.FirstEntryAt)/daySeconds) + 1, Entries: row.Entries, DistinctAuthors: row.DistinctAuthors,
			ReturningAuthors: row.ReturningAuthors, ActiveDays: row.ActiveDays, ActiveWeeks: row.ActiveWeeks, ActiveMonths: row.ActiveMonths,
			PeakDayEntries: row.PeakDayEntries, PeakWeekEntries: row.PeakWeekEntries, PeakDayShare: float64(row.PeakDayEntries) / float64(row.Entries),
			PeakWeekShare: float64(row.PeakWeekEntries) / float64(row.Entries), PopularSnapshots: row.PopularSnapshots,
			FirstPopularAt: row.FirstPopularAt, LastPopularAt: row.LastPopularAt,
		})
	}
	return profiles, nil
}

func loadAuthorProfiles(since int64) ([]authorProfile, error) {
	const query = `SELECT author, COUNT(*) AS entries, COUNT(DISTINCT topic_id) AS topics
		FROM entries WHERE deleted_at IS NULL AND timestamp >= ? AND author <> ''
		GROUP BY author ORDER BY topics DESC, entries DESC`
	var profiles []authorProfile
	if err := model.DB().Raw(query, since).Scan(&profiles).Error; err != nil {
		return nil, err
	}
	return profiles, nil
}

func buildSummary(now time.Time, since int64, topics []topicProfile, authors []authorProfile, minAuthors, minWeeks, minReturningAuthors int64, maxPeakWeekShare float64) profileSummary {
	summary := profileSummary{GeneratedAt: now, WindowEnd: now, TopicCount: len(topics), AuthorCount: len(authors)}
	if since > 0 {
		summary.WindowStart = time.Unix(since, 0).UTC()
	}
	topicValues := make([]float64, 0, len(topics))
	for _, topic := range topics {
		topicValues = append(topicValues, float64(topic.DistinctAuthors))
		if topic.DistinctAuthors >= minAuthors && topic.ActiveWeeks >= minWeeks && topic.ReturningAuthors >= minReturningAuthors && topic.PeakWeekShare <= maxPeakWeekShare {
			summary.EligibleTopics++
		}
		if topic.ActiveWeeks <= 2 && topic.PeakWeekShare >= 0.75 {
			summary.LikelyOneOff++
		}
	}
	authorValues := make([]float64, 0, len(authors))
	for _, author := range authors {
		authorValues = append(authorValues, float64(author.Topics))
	}
	summary.TopicQuantiles = calculateQuantiles(topicValues)
	summary.AuthorTopicCounts = calculateQuantiles(authorValues)
	return summary
}

func calculateQuantiles(values []float64) quantiles {
	if len(values) == 0 {
		return quantiles{}
	}
	sort.Float64s(values)
	at := func(p float64) float64 { return values[int(math.Ceil(p*float64(len(values))))-1] }
	return quantiles{P50: at(0.50), P90: at(0.90), P99: at(0.99), Max: values[len(values)-1]}
}

func writeTopicCSV(path string, profiles []topicProfile) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	if err := writer.Write([]string{"topic_id", "title", "url", "first_entry_at", "last_entry_at", "lifetime_days", "entries", "distinct_authors", "returning_authors", "active_days", "active_weeks", "active_months", "peak_day_entries", "peak_week_entries", "peak_day_share", "peak_week_share", "popular_snapshots", "first_popular_at", "last_popular_at"}); err != nil {
		return err
	}
	for _, p := range profiles {
		firstPopular, lastPopular := "", ""
		if p.FirstPopularAt != nil {
			firstPopular = fmt.Sprint(*p.FirstPopularAt)
		}
		if p.LastPopularAt != nil {
			lastPopular = fmt.Sprint(*p.LastPopularAt)
		}
		if err := writer.Write([]string{fmt.Sprint(p.TopicID), p.Title, p.URL, fmt.Sprint(p.FirstEntryAt), fmt.Sprint(p.LastEntryAt), fmt.Sprint(p.LifetimeDays), fmt.Sprint(p.Entries), fmt.Sprint(p.DistinctAuthors), fmt.Sprint(p.ReturningAuthors), fmt.Sprint(p.ActiveDays), fmt.Sprint(p.ActiveWeeks), fmt.Sprint(p.ActiveMonths), fmt.Sprint(p.PeakDayEntries), fmt.Sprint(p.PeakWeekEntries), fmt.Sprintf("%.6f", p.PeakDayShare), fmt.Sprintf("%.6f", p.PeakWeekShare), fmt.Sprint(p.PopularSnapshots), firstPopular, lastPopular}); err != nil {
			return err
		}
	}
	return writer.Error()
}

func writeAuthorCSV(path string, profiles []authorProfile) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	writer := csv.NewWriter(file)
	defer writer.Flush()
	if err := writer.Write([]string{"author", "entries", "distinct_topics"}); err != nil {
		return err
	}
	for _, p := range profiles {
		if err := writer.Write([]string{p.Author, fmt.Sprint(p.Entries), fmt.Sprint(p.Topics)}); err != nil {
			return err
		}
	}
	return writer.Error()
}
