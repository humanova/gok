// Package stories links new topics into story threads. When a new topic gets
// its third entry, the Gemini Decisions API says for each of the five nearest
// earlier new topics (by embedding) how likely the new topic belongs to its
// story; the most likely one becomes its parent if it clears LinkThreshold. A
// story is the tree of these links.
package stories

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"gok/internal/model"

	"github.com/pgvector/pgvector-go"
)

const (
	ReadyEntries   = 3                   // a topic is decided when it has this many entries
	PoolWindow     = 45 * 24 * time.Hour // how far back candidates and new topics reach
	CandidateCount = 5
	LinkThreshold  = 0.5 // minimum yes probability for a parent link

	embedEntryChars     = 300
	promptEntryChars    = 260
	candidateEntryChars = 200
)

// Embedder turns story texts into unit-length vectors.
type Embedder interface {
	EmbedStoryTexts(ctx context.Context, texts []string) ([][]float32, error)
}

// ErrBlocked is returned by a Judge when the model refuses the input. The topic
// is then saved without a parent instead of stopping the run.
var ErrBlocked = errors.New("input blocked by the model")

// Judge answers yes/no questions about one input with the probability of yes.
type Judge interface {
	YesNo(ctx context.Context, input string, questions []string) ([]float64, error)
	ModelName() string
}

// Store reads pending topics and decided story nodes and saves decisions.
type Store interface {
	PendingTopics(ctx context.Context, since time.Time) ([]model.StoryTopicEntries, error)
	Nodes(ctx context.Context, readySince int64) ([]model.TopicStory, error)
	Save(ctx context.Context, node *model.TopicStory) error
}

// Options bound one run. Topics born before Since and topics that reach their
// third entry after Until are left for a later run.
type Options struct {
	Since time.Time
	Until time.Time
	Limit int  // stop after this many decisions; 0 means no limit
	Save  bool // false keeps decisions in memory only (dry run)
	// OnDecision, if set, is called after every decision.
	OnDecision func(node model.TopicStory, parentTitle string)
}

// Run decides every pending new topic in the order they reached their third
// entry, so each topic only sees topics that were ready before it.
func Run(ctx context.Context, store Store, embedder Embedder, judge Judge, opts Options) (int, error) {
	if opts.Until.IsZero() {
		opts.Until = time.Now().UTC()
	}
	pendingTopics, err := store.PendingTopics(ctx, opts.Since)
	if err != nil {
		return 0, fmt.Errorf("loading pending topics: %w", err)
	}
	pending := readyTopics(pendingTopics, opts.Since.Unix(), opts.Until.Unix())
	if len(pending) == 0 {
		return 0, nil
	}

	pool, err := store.Nodes(ctx, pending[0].ReadyAt-int64(PoolWindow.Seconds()))
	if err != nil {
		return 0, fmt.Errorf("loading story nodes: %w", err)
	}

	decided := 0
	for i := range pending {
		if opts.Limit > 0 && decided >= opts.Limit {
			break
		}
		node := pending[i]
		vectors, err := embedder.EmbedStoryTexts(ctx, []string{node.InputText})
		if err != nil {
			return decided, fmt.Errorf("embedding topic %d: %w", node.TopicID, err)
		}
		node.Embedding = pgvector.NewVector(vectors[0])

		parentTitle := ""
		candidates := nearestEarlier(pool, node, CandidateCount)
		if len(candidates) > 0 {
			probs, err := judge.YesNo(ctx, BuildInput(node, candidates, pool), Questions(len(candidates)))
			if errors.Is(err, ErrBlocked) {
				slog.Warn("stories: input blocked, saving topic without a parent", "topic_id", node.TopicID)
			} else if err != nil {
				return decided, fmt.Errorf("judging topic %d: %w", node.TopicID, err)
			}
			node.Model = judge.ModelName()
			best := -1
			for i, p := range probs {
				if best < 0 || p > probs[best] {
					best = i
				}
			}
			if best >= 0 {
				node.Score = probs[best]
			}
			if best >= 0 && probs[best] >= LinkThreshold {
				chosen := candidates[best]
				node.ParentTopicID = &chosen.TopicID
				node.RootTopicID = chosen.RootTopicID
				parentTitle = chosen.Title
			}
		}
		if node.RootTopicID == 0 {
			node.RootTopicID = node.TopicID
		}

		if opts.Save {
			if err := store.Save(ctx, &node); err != nil {
				return decided, fmt.Errorf("saving topic %d: %w", node.TopicID, err)
			}
		}
		pool = append(pool, node)
		decided++
		if opts.OnDecision != nil {
			opts.OnDecision(node, parentTitle)
		}
	}
	return decided, nil
}

// readyTopics keeps topics with enough entries whose first entry is not older
// than since and whose third entry is not later than until, oldest ready first.
func readyTopics(topics []model.StoryTopicEntries, since, until int64) []model.TopicStory {
	var out []model.TopicStory
	for _, topic := range topics {
		if len(topic.Entries) < ReadyEntries {
			continue
		}
		bornAt := topic.Entries[0].Timestamp
		readyAt := topic.Entries[ReadyEntries-1].Timestamp
		if bornAt < since || readyAt > until {
			continue
		}
		entries := CleanEntries(topic.Entries[:ReadyEntries], embedEntryChars)
		first := ""
		if len(entries) > 0 {
			first = truncateRunes(entries[0], candidateEntryChars)
		}
		out = append(out, model.TopicStory{
			TopicID:    topic.TopicID,
			Title:      topic.Title,
			BornAt:     bornAt,
			ReadyAt:    readyAt,
			InputText:  strings.Join(append([]string{topic.Title}, entries...), "\n"),
			FirstEntry: first,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ReadyAt == out[j].ReadyAt {
			return out[i].TopicID < out[j].TopicID
		}
		return out[i].ReadyAt < out[j].ReadyAt
	})
	return out
}

// nearestEarlier returns the k pool nodes most similar to node among those that
// were ready before it, at most PoolWindow earlier.
func nearestEarlier(pool []model.TopicStory, node model.TopicStory, k int) []model.TopicStory {
	type scored struct {
		node  model.TopicStory
		score float64
	}
	from := node.ReadyAt - int64(PoolWindow.Seconds())
	query := node.Embedding.Slice()
	var candidates []scored
	for _, other := range pool {
		if other.TopicID == node.TopicID || other.ReadyAt >= node.ReadyAt || other.ReadyAt < from {
			continue
		}
		candidates = append(candidates, scored{other, dot(query, other.Embedding.Slice())})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].score > candidates[j].score })
	if len(candidates) > k {
		candidates = candidates[:k]
	}
	out := make([]model.TopicStory, len(candidates))
	for i, c := range candidates {
		out[i] = c.node
	}
	return out
}

func dot(a, b []float32) float64 {
	if len(a) != len(b) {
		return math.Inf(-1)
	}
	sum := 0.0
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

const inputHead = `Ekşi Sözlük is a Turkish forum where each topic (başlık) is a title with entries under it. When something happens (an incident, a scandal, a crisis, a decision, a statement), people open many topics about it: the event itself, later developments, official statements about it, reactions, jokes and spin-offs, and the people involved as seen through it.

A STORY is one specific event or one unfolding affair (one crisis, scandal, investigation, case or dispute). A topic belongs to an earlier topic's story when it is a later development in it, a statement or decision about it, a reaction, a joke or a spin-off. A statement counts when it is about that affair, even if it was made days later. First entries can be vague, so read the title too. Sharing only a broad theme, a person, a club, a party or a genre is not enough: two different matches, two different episodes, two unrelated statements by the same politician, or two relationship/advice questions are NOT the same story. Most new topics have no parent.

`

// Questions returns one yes/no question per candidate.
func Questions(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("Does the NEW topic belong to the same story as EARLIER topic %d: the same specific event or unfolding affair, not merely the same theme, person, club, party or genre?", i+1)
	}
	return out
}

// BuildInput writes the shared input for node's questions. Each candidate shows
// its age and the story it already belongs to among the pool's nodes.
func BuildInput(node model.TopicStory, candidates, pool []model.TopicStory) string {
	sizes := map[uint64]int{}
	titles := map[uint64]string{}
	for _, other := range pool {
		if other.ReadyAt < node.ReadyAt {
			sizes[other.RootTopicID]++
		}
		titles[other.TopicID] = other.Title
	}

	var sb strings.Builder
	sb.WriteString(inputHead)
	sb.WriteString("NEW topic: ")
	sb.WriteString(node.Title)
	sb.WriteString("\n")
	for i, entry := range promptEntries(node) {
		fmt.Fprintf(&sb, "  entry %d: %s\n", i+1, entry)
	}
	sb.WriteString("EARLIER topics:\n")
	for i, c := range candidates {
		age := "today"
		if days := float64(node.ReadyAt-c.ReadyAt) / 86400; days >= 1 {
			age = fmt.Sprintf("%.0f days ago", days)
		}
		tag := ""
		if size := sizes[c.RootTopicID]; size > 1 {
			if c.RootTopicID == c.TopicID {
				tag = fmt.Sprintf(" [starts a story of %d topics]", size)
			} else {
				tag = fmt.Sprintf(" [story: %s, %d topics]", titles[c.RootTopicID], size)
			}
		}
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "%d. %s (%s)%s", i+1, c.Title, age, tag)
		if c.FirstEntry != "" {
			fmt.Fprintf(&sb, "\n   - %s", c.FirstEntry)
		}
	}
	return sb.String()
}

// promptEntries recovers the node's cleaned entries from its stored input text
// and shortens them for the prompt.
func promptEntries(node model.TopicStory) []string {
	lines := strings.Split(node.InputText, "\n")
	if len(lines) <= 1 {
		return nil
	}
	out := make([]string, 0, len(lines)-1)
	for _, line := range lines[1:] {
		out = append(out, truncateRunes(line, promptEntryChars))
	}
	return out
}

var (
	urlPattern   = regexp.MustCompile(`https?://\S+|\b\S+\.(com|org|net)\S*`)
	spacePattern = regexp.MustCompile(`\s+`)
)

// CleanEntries collapses whitespace, drops links, cuts each entry to max runes
// and skips entries left with three characters or fewer.
func CleanEntries(entries []model.Entry, max int) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		text := spacePattern.ReplaceAllString(entry.Text, " ")
		text = strings.TrimSpace(urlPattern.ReplaceAllString(text, ""))
		text = truncateRunes(text, max)
		if len([]rune(text)) > 3 {
			out = append(out, text)
		}
	}
	return out
}

func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
