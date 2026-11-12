package stories

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"gok/internal/model"

	"github.com/pgvector/pgvector-go"
)

type memStore struct {
	pending []model.StoryTopicEntries
	nodes   []model.TopicStory
}

func (s *memStore) PendingTopics(context.Context, time.Time) ([]model.StoryTopicEntries, error) {
	return s.pending, nil
}

func (s *memStore) Nodes(_ context.Context, readySince int64) ([]model.TopicStory, error) {
	var out []model.TopicStory
	for _, node := range s.nodes {
		if node.ReadyAt >= readySince {
			out = append(out, node)
		}
	}
	return out, nil
}

func (s *memStore) Save(_ context.Context, node *model.TopicStory) error {
	s.nodes = append(s.nodes, *node)
	return nil
}

// fakeEmbedder maps the first word of a title to a fixed direction.
type fakeEmbedder struct{}

func (fakeEmbedder) EmbedStoryTexts(_ context.Context, texts []string) ([][]float32, error) {
	axes := map[string]int{"fon": 0, "sel": 1, "maç": 2}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		v := make([]float32, 4)
		v[3] = 0.1
		if axis, ok := axes[strings.Fields(text)[0]]; ok {
			v[axis] = 1
		}
		out[i] = v
	}
	return out, nil
}

// fakeJudge says yes to candidates that share the title's first word, with a
// higher probability for the earlier-listed ones.
type fakeJudge struct{ inputs []string }

func (j *fakeJudge) ModelName() string { return "fake" }

func (j *fakeJudge) YesNo(_ context.Context, input string, questions []string) ([]float64, error) {
	j.inputs = append(j.inputs, input)
	newWord := strings.Fields(strings.SplitN(strings.SplitN(input, "NEW topic: ", 2)[1], "\n", 2)[0])[0]
	earlier := strings.SplitN(input, "EARLIER topics:\n", 2)[1]
	var probs []float64
	for _, line := range strings.Split(earlier, "\n") {
		if line == "" || line[0] < '1' || line[0] > '9' {
			continue
		}
		p := 0.1
		if strings.Fields(line)[1] == newWord {
			p = 0.9 - 0.1*float64(len(probs))
		}
		probs = append(probs, p)
	}
	if len(probs) != len(questions) {
		return nil, fmt.Errorf("%d candidates for %d questions", len(probs), len(questions))
	}
	return probs, nil
}

func topic(id uint64, title string, born int64) model.StoryTopicEntries {
	return model.StoryTopicEntries{TopicID: id, Title: title, Entries: []model.Entry{
		{Timestamp: born, Text: "ilk girdi https://x.com/a burada"},
		{Timestamp: born + 60, Text: "ok"},
		{Timestamp: born + 120, Text: "üçüncü   girdi"},
	}}
}

func TestRunLinksTopicsIntoTreesInReadyOrder(t *testing.T) {
	const day = int64(86400)
	start := time.Unix(10*day, 0).UTC()
	s := &memStore{pending: []model.StoryTopicEntries{
		topic(4, "fon açıklaması", 10*day+3000),
		topic(1, "fon tasfiyeleri", 10*day+100),
		topic(2, "sel baskını", 10*day+200),
		topic(3, "fon mağdurları", 10*day+2000),
		topic(5, "eski başlık", 9*day), // born before Since
		{TopicID: 6, Title: "fon iki girdi", Entries: []model.Entry{{Timestamp: 10*day + 50}, {Timestamp: 10*day + 60}}},
	}}
	judge := &fakeJudge{}
	n, err := Run(context.Background(), s, fakeEmbedder{}, judge, Options{Since: start, Until: start.Add(time.Hour), Save: true})
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("decided %d topics, want 4", n)
	}
	got := map[uint64]model.TopicStory{}
	order := []uint64{}
	for _, node := range s.nodes {
		got[node.TopicID] = node
		order = append(order, node.TopicID)
	}
	if want := []uint64{1, 2, 3, 4}; !reflect.DeepEqual(order, want) {
		t.Errorf("decision order %v, want %v", order, want)
	}
	if got[1].ParentTopicID != nil || got[1].RootTopicID != 1 {
		t.Errorf("first topic = %+v, want its own root", got[1])
	}
	if got[3].ParentTopicID == nil || *got[3].ParentTopicID != 1 || got[3].RootTopicID != 1 {
		t.Errorf("topic 3 = %+v, want parent 1 root 1", got[3])
	}
	if got[4].RootTopicID != 1 {
		t.Errorf("topic 4 root = %d, want 1", got[4].RootTopicID)
	}
	if got[2].ParentTopicID != nil {
		t.Errorf("topic 2 got parent %d, want none", *got[2].ParentTopicID)
	}
	if want := "fon tasfiyeleri\nilk girdi  burada\nüçüncü girdi"; got[1].InputText != want {
		t.Errorf("input text %q, want %q", got[1].InputText, want)
	}
	if got[3].Score < LinkThreshold || got[2].Score >= LinkThreshold {
		t.Errorf("scores %v / %v, want the linked topic above the threshold and the other below", got[3].Score, got[2].Score)
	}
	if len(judge.inputs) != 3 {
		t.Fatalf("judge called %d times, want 3 (the first topic has no earlier topics)", len(judge.inputs))
	}
	last := judge.inputs[2]
	if !strings.Contains(last, "[starts a story of 2 topics]") {
		t.Errorf("last input does not show the earlier story:\n%s", last)
	}
	if !strings.Contains(last, "  entry 2: üçüncü girdi\n") {
		t.Errorf("last input lacks the cleaned entries:\n%s", last)
	}
}

func TestRunDryRunSavesNothingAndHonoursLimit(t *testing.T) {
	start := time.Unix(1000, 0).UTC()
	s := &memStore{pending: []model.StoryTopicEntries{topic(1, "fon a", 1100), topic(2, "fon b", 1200), topic(3, "fon c", 1300)}}
	var seen []uint64
	n, err := Run(context.Background(), s, fakeEmbedder{}, &fakeJudge{}, Options{
		Since: start, Until: start.Add(time.Hour), Limit: 2,
		OnDecision: func(node model.TopicStory, _ string) { seen = append(seen, node.TopicID) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(seen) != 2 || len(s.nodes) != 0 {
		t.Errorf("decided %d, callbacks %v, saved %d; want 2, 2, 0", n, seen, len(s.nodes))
	}
}

func TestNearestEarlierSkipsLaterAndTooOldTopics(t *testing.T) {
	node := model.TopicStory{TopicID: 9, ReadyAt: int64(PoolWindow.Seconds()) + 1000}
	node.Embedding = vec(1, 0)
	pool := []model.TopicStory{
		{TopicID: 1, ReadyAt: 500, Embedding: vec(1, 0)},               // older than the window
		{TopicID: 2, ReadyAt: node.ReadyAt - 10, Embedding: vec(0, 1)}, // in window, dissimilar
		{TopicID: 3, ReadyAt: node.ReadyAt - 20, Embedding: vec(1, 0)}, // in window, similar
		{TopicID: 4, ReadyAt: node.ReadyAt + 10, Embedding: vec(1, 0)}, // ready later
	}
	got := nearestEarlier(pool, node, 5)
	if len(got) != 2 || got[0].TopicID != 3 || got[1].TopicID != 2 {
		t.Errorf("candidates %v, want [3 2]", ids(got))
	}
}

func vec(values ...float32) pgvector.Vector { return pgvector.NewVector(values) }

func ids(nodes []model.TopicStory) []uint64 {
	out := make([]uint64, len(nodes))
	for i, node := range nodes {
		out[i] = node.TopicID
	}
	return out
}
