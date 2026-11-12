package stories

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"gok/internal/model"
)

const (
	oldDayMinEntries = 5   // entries a topic needs on a day to be considered
	oldDaySample     = 6   // entries of the day shown to the model
	oldDayEntryChars = 350 // characters per sampled entry
	OldDayThreshold  = 0.8 // yes probability at which an old topic counts as part of a story
)

const oldDayQuestion = "Are today's entries in this topic mainly about the story below: the same specific event or unfolding affair " +
	"(a development, reaction, statement, joke or spin-off of it)? Answer no if they only share a broad theme, a person, " +
	"a club or a party, or if only one or two entries mention it.\nSTORY: "

// OldDayOptions bound one old-topic run over Istanbul calendar days.
type OldDayOptions struct {
	Since, Until time.Time // first and last day, by their calendar dates
	Workers      int
	OnDecision   func(row model.TopicStoryDay, title, storyTitle string)
}

// RunOldDays asks, for every old topic on the popular list with enough entries
// on a day, which of that day's active stories its entries are about. Each
// decided topic-day is saved once, so the run can be resumed.
func RunOldDays(ctx context.Context, judge Judge, opts OldDayOptions) (int, error) {
	ist, err := time.LoadLocation("Europe/Istanbul")
	if err != nil {
		return 0, err
	}
	nodes, err := model.GetTopicStoriesReadySince(0)
	if err != nil {
		return 0, fmt.Errorf("loading story nodes: %w", err)
	}
	if opts.Workers < 1 {
		opts.Workers = 1
	}

	decided := 0
	start := time.Date(opts.Since.Year(), opts.Since.Month(), opts.Since.Day(), 0, 0, 0, 0, ist)
	last := time.Date(opts.Until.Year(), opts.Until.Month(), opts.Until.Day(), 0, 0, 0, 0, ist)
	for day := start; !day.After(last); day = day.AddDate(0, 0, 1) {
		n, err := runOldDay(ctx, judge, nodes, day, opts)
		decided += n
		if err != nil {
			return decided, fmt.Errorf("%s: %w", day.Format("2006-01-02"), err)
		}
	}
	return decided, nil
}

type oldDayJob struct {
	topicID uint64
	title   string
}

func runOldDay(ctx context.Context, judge Judge, nodes []model.TopicStory, day time.Time, opts OldDayOptions) (int, error) {
	dayName := day.Format("2006-01-02")
	from, to := day.Unix(), day.AddDate(0, 0, 1).Unix()
	fromID, err := model.FirstEntryIDAt(from)
	if err != nil {
		return 0, err
	}
	toID, err := model.FirstEntryIDAt(to)
	if err != nil {
		return 0, err
	}
	if toID == 0 {
		toID = math.MaxInt64
	}
	counts, err := model.TopicEntryCounts(fromID, toID)
	if err != nil {
		return 0, err
	}

	stories := activeStories(nodes, counts, to)
	if len(stories) == 0 {
		return 0, nil
	}
	popular, err := model.PopularTopicIDs(from, to)
	if err != nil {
		return 0, err
	}
	done, err := model.DecidedStoryDays(dayName)
	if err != nil {
		return 0, err
	}
	newFrom := oldestNewTopic(nodes, from-int64(PoolWindow.Seconds()))
	inStory := make(map[uint64]bool, len(nodes))
	for _, node := range nodes {
		inStory[node.TopicID] = true
	}
	var candidates []uint64
	for _, id := range popular {
		if id < newFrom && !inStory[id] && !done[id] && counts[id] >= oldDayMinEntries {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) == 0 {
		return 0, nil
	}
	titles, err := model.TopicTitles(candidates)
	if err != nil {
		return 0, err
	}

	questions := make([]string, len(stories))
	for i, story := range stories {
		questions[i] = oldDayQuestion + story.text
	}
	jobs := make(chan oldDayJob)
	var (
		mu       sync.Mutex
		decided  int
		firstErr error
		wg       sync.WaitGroup
	)
	for w := 0; w < opts.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				row, storyTitle, err := decideOldDay(ctx, judge, job, dayName, fromID, toID, stories, questions)
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = fmt.Errorf("topic %d: %w", job.topicID, err)
					}
				} else {
					decided++
					if opts.OnDecision != nil {
						opts.OnDecision(row, job.title, storyTitle)
					}
				}
				mu.Unlock()
			}
		}()
	}
	for _, id := range candidates {
		mu.Lock()
		stop := firstErr != nil
		mu.Unlock()
		if stop {
			break
		}
		jobs <- oldDayJob{topicID: id, title: titles[id]}
	}
	close(jobs)
	wg.Wait()
	return decided, firstErr
}

func decideOldDay(ctx context.Context, judge Judge, job oldDayJob, day string, fromID, toID uint64, stories []activeStory, questions []string) (model.TopicStoryDay, string, error) {
	row := model.TopicStoryDay{TopicID: job.topicID, Day: day, Model: judge.ModelName()}
	entries, err := model.TopicEntriesBetween(job.topicID, fromID, toID, 40)
	if err != nil {
		return row, "", err
	}
	entries = spread(entries, oldDaySample)
	if len(entries) == 0 {
		return row, "", model.AddTopicStoryDay(&row)
	}
	created := ""
	if first, err := model.FirstTopicEntryTime(job.topicID); err == nil && first > 0 {
		created = time.Unix(first, 0).Format("2006-01-02")
	}

	probs, err := judge.YesNo(ctx, oldDayInput(job.title, created, day, entries), questions)
	if errors.Is(err, ErrBlocked) && len(entries) > 3 {
		probs, err = judge.YesNo(ctx, oldDayInput(job.title, created, day, entries[:3]), questions)
	}
	if errors.Is(err, ErrBlocked) {
		slog.Warn("stories: old topic blocked, saving without a story", "topic_id", job.topicID, "day", day)
		return row, "", model.AddTopicStoryDay(&row)
	}
	if err != nil {
		return row, "", err
	}
	best := 0
	for i := range probs {
		if probs[i] > probs[best] {
			best = i
		}
	}
	row.RootTopicID = &stories[best].root
	row.Score = probs[best]
	return row, stories[best].title, model.AddTopicStoryDay(&row)
}

func oldDayInput(title, created, day string, entries []model.Entry) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Ekşi Sözlük topic: %s (an old page, created %s). A sample of entries posted in it on %s:", title, created, day)
	for _, text := range CleanEntries(entries, oldDayEntryChars) {
		sb.WriteString("\n- ")
		sb.WriteString(text)
	}
	return sb.String()
}

type activeStory struct {
	root  uint64
	title string
	text  string
}

// activeStories returns the stories with at least two topics ready before
// dayEnd, one of them with enough entries on the day. Each is described by its
// root's title, the day's busiest other topics and the root's first entry.
func activeStories(nodes []model.TopicStory, counts map[uint64]int, dayEnd int64) []activeStory {
	members := map[uint64][]model.TopicStory{}
	byID := map[uint64]model.TopicStory{}
	for _, node := range nodes {
		byID[node.TopicID] = node
		if node.ReadyAt < dayEnd {
			members[node.RootTopicID] = append(members[node.RootTopicID], node)
		}
	}
	var out []activeStory
	for root, list := range members {
		if len(list) < 2 {
			continue
		}
		sort.SliceStable(list, func(i, j int) bool { return counts[list[i].TopicID] > counts[list[j].TopicID] })
		if counts[list[0].TopicID] < oldDayMinEntries {
			continue
		}
		rootNode := byID[root]
		var also []string
		for _, node := range list {
			if node.TopicID != root && len(also) < 4 {
				also = append(also, node.Title)
			}
		}
		out = append(out, activeStory{
			root:  root,
			title: rootNode.Title,
			text:  fmt.Sprintf("%s (also: %s). First entry: %s", rootNode.Title, strings.Join(also, "; "), rootNode.FirstEntry),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].root < out[j].root })
	return out
}

// oldestNewTopic returns the lowest topic id among new topics born at or after
// since. Ekşi topic ids grow with creation time, so lower ids are old pages.
func oldestNewTopic(nodes []model.TopicStory, since int64) uint64 {
	var lowest, overall uint64
	for _, node := range nodes {
		if overall == 0 || node.TopicID < overall {
			overall = node.TopicID
		}
		if node.BornAt >= since && (lowest == 0 || node.TopicID < lowest) {
			lowest = node.TopicID
		}
	}
	if lowest == 0 {
		return overall
	}
	return lowest
}

// spread keeps at most k entries, evenly spaced over the day.
func spread(entries []model.Entry, k int) []model.Entry {
	if len(entries) <= k {
		return entries
	}
	out := make([]model.Entry, k)
	for i := range out {
		out[i] = entries[(i*(len(entries)-1)+(k-1)/2)/(k-1)]
	}
	return out
}
