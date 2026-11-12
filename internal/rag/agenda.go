package rag

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"gok/internal/model"
	"gok/internal/stories"
)

// AgendaJudge asks yes/no questions about a topic.
type AgendaJudge interface {
	YesNo(ctx context.Context, input string, questions []string) ([]float64, error)
	ModelName() string
}

const (
	// AgendaThreshold is the score at which a topic shows in the Radar's gündem view.
	AgendaThreshold = 0.6

	agendaTopTopics  = 120
	agendaMaxAge     = 24 * time.Hour
	agendaEntries    = 3 // first and latest entries shown
	agendaEntryChars = 300
)

// agendaQuestion was tuned on 400 hand-labelled topics: precision and recall 0.96 at 0.6 on the held-out half.
const agendaQuestion = "Bu Ekşi Sözlük başlığı, Türkiye'de ve dünyada şu an olup bitenleri gösteren 'yalnızca gündem' akışına girer " +
	"mi? Yalnızca başlık ve entryler belirli, yeni bir olay ya da süren bir kamu meselesi etrafında dönüyorsa evet " +
	"de: siyaset ve devlet, mahkemeler, soruşturmalar ve gözaltılar, ekonomi ve fiyatlar, savaşlar ve dış " +
	"ilişkiler, suç, kazalar ve afetler, yetkililerin açıklamaları ya da viral bir skandal veya olay (bir ünlünün " +
	"ya da futbolcunun gözaltına alınması veya skandalı da dahil). Futbol ve diğer spor konuşmaları (maçlar, " +
	"oyuncular, teknik direktörler, transferler, hakemler, kulüp çekişmeleri), özel hayat, flört ve ilişkiler, " +
	"itiraflar, görüş ve tavsiyeler, gündelik ya da zamansız konular (aşk, kedi, yemek, şehirler, tarih, din), " +
	"sözlük oyunları ve listeler, espriler ve capsler, eğlence olarak diziler, müzik, filmler ve oyunlar, ünlü " +
	"ölümleri ve magazin, ürün ya da teknoloji sohbeti için hayır de. Bir kişi, ülke, kulüp, marka ya da fikir " +
	"hakkındaki genel bir başlık, yalnızca son entryleri belirli ve yeni bir olayla ilgiliyse evet sayılır."

// ScoreAgenda scores the hottest Radar topics that have no score from the last day.
func ScoreAgenda(ctx context.Context, judge AgendaJudge) (int, error) {
	now := time.Now().UTC()
	topics, err := hotTopics(now, agendaTopTopics)
	if err != nil {
		return 0, err
	}
	ids := make([]uint64, len(topics))
	for i, topic := range topics {
		ids[i] = topic.TopicId
	}
	scored, err := model.GetTopicAgendas(ids)
	if err != nil {
		return 0, err
	}

	decided := 0
	for _, topic := range topics {
		if row, ok := scored[topic.TopicId]; ok && now.Sub(row.DecidedAt) < agendaMaxAge {
			continue
		}
		score, err := scoreAgenda(ctx, judge, topic)
		if err != nil {
			slog.Warn("agenda: scoring failed", "topic_id", topic.TopicId, "error", err)
			continue
		}
		row := model.TopicAgenda{TopicID: topic.TopicId, Score: score, Model: judge.ModelName(), DecidedAt: now}
		if err := model.SaveTopicAgenda(&row); err != nil {
			slog.Warn("agenda: persistence failed", "topic_id", topic.TopicId, "error", err)
			continue
		}
		decided++
	}
	slog.Info("agenda scored", "count", decided, "candidates", len(topics))
	return decided, nil
}

// scoreAgenda asks once with the title and first and latest entries. A blocked
// input is retried with the title and first entry, then the title; a topic the
// model refuses outright scores 0.
func scoreAgenda(ctx context.Context, judge AgendaJudge, topic model.Topic) (float64, error) {
	first, err := model.GetFirstTopicEntries(topic.TopicId, agendaEntries)
	if err != nil {
		return 0, err
	}
	latest, err := model.GetRecentTopicEntries(topic.TopicId, 0, agendaEntries+len(first))
	if err != nil {
		return 0, err
	}
	seen := make(map[uint64]bool, len(first))
	for _, entry := range first {
		seen[entry.EntryId] = true
	}
	var recent []model.Entry
	for i := len(latest) - 1; i >= 0; i-- {
		if !seen[latest[i].EntryId] {
			recent = append(recent, latest[i])
		}
	}
	if len(recent) > agendaEntries {
		recent = recent[len(recent)-agendaEntries:]
	}

	attempts := [][2][]model.Entry{{first, recent}, {first[:min(1, len(first))], nil}, {nil, nil}}
	for _, attempt := range attempts {
		probs, err := judge.YesNo(ctx, agendaInput(topic.Text, attempt[0], attempt[1]), []string{agendaQuestion})
		if errors.Is(err, stories.ErrBlocked) {
			continue
		}
		if err != nil {
			return 0, err
		}
		return probs[0], nil
	}
	return 0, nil
}

func agendaInput(title string, first, recent []model.Entry) string {
	var sb strings.Builder
	sb.WriteString("Ekşi Sözlük topic title: ")
	sb.WriteString(title)
	for _, part := range []struct {
		head    string
		entries []model.Entry
	}{{"First entries:", first}, {"Recent entries:", recent}} {
		texts := stories.CleanEntries(part.entries, agendaEntryChars)
		if len(texts) == 0 {
			continue
		}
		sb.WriteString("\n" + part.head)
		for _, text := range texts {
			sb.WriteString("\n- " + text)
		}
	}
	return sb.String()
}
