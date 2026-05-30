# gok – Copilot Instructions

## Project Overview
`gok` is a Go scraper + AI digest pipeline that periodically collects popular topics and entries from [eksisozluk.com](https://eksisozluk.com) (a Turkish social platform), stores them in PostgreSQL, and generates a structured Turkish-language news digest via RAG + Gemini.

## Architecture

```
cmd/gok/main.go          → entrypoint: init DB, start gocron for scraping + digest generation
cmd/print-digest/main.go → CLI tool: reads latest digest from DB, writes .txt + .html
internal/config/         → loads configs/config.json at init() via gonfig
internal/scraper/        → eksi.go (HTML scraping), scraper.go (orchestration + DB writes)
internal/model/          → GORM models; db.go holds all private DB funcs, model files expose public wrappers
internal/embedder/       → HTTP client for Python embedder sidecar (/embed, /health)
internal/rag/            → digest.go (hot-topic selection + budget allocation), viewpoints.go (cosine clustering)
internal/llm/            → gemini.go (Gemini API client: SynthesizeDigest, SynthesizeQuery, StreamQuery)
internal/digestfmt/      → digest formatting helpers
embedder/main.py         → FastAPI sidecar: loads intfloat/multilingual-e5-small, exposes /embed
scripts/embed_entries.py → one-off backfill script: embeds unembedded entries from the last N days
configs/config.json      → runtime config (DB creds, scrape interval, Gemini key, embedder URL)
```

## Digest Pipeline (the "why")
`cmd/gok` runs two cron jobs: the scraper (`ScrapeInterval` min) and the digest generator (`DigestIntervalMinutes`, default 240).

Digest flow in `rag.GenerateDigest`:
1. **Hot topic selection** – `model.GetHotTopics(3h, top15)` scores topics by `AvgRank` from `popular_topics`.
2. **Budget allocation** – 250 entries total, proportional to heat score, clamped `[5, 60]` per topic.
3. **Viewpoint clustering** – `rag.ExtractViewpoints` clusters entries by cosine similarity (threshold 0.7); falls back to positional splitting if embeddings are absent.
4. **LLM synthesis** – `llm.Client.SynthesizeDigest` sends `[]model.TopicBundle` to Gemini with a Turkish system prompt; response is forced to `application/json` and unmarshalled into `model.DigestPayload`.
5. **Persistence** – `model.AddDigest` stores the JSON payload in the `digests` table.

`DigestSynthesizer` is an interface in `rag/digest.go`; `llm.Client` implements it — keep this decoupled.

## Model Conventions
- Most GORM models embed `gorm.Model`. Exceptions:
  - `EntryAttachment`: composite PK (`EntryId` + `Url`), manual timestamps, no soft-delete.
  - `Request`: plain `ID uint`, `CreatedAt time.Time`, no `gorm.Model`.
- `PTopic` is transient (scrape-time only); `Topic` + `PopularTopic` are persisted.
- DB functions are **private** in `db.go`; each model file exposes **public** wrappers.
- `Filters` struct in `db.go` (`CreatedAfter`, `CreatedBefore`, `QueryText`, `Author`) — defaults to last 12 hours.
- Composite index `idx_entries_topic_entry` on `(TopicId ASC, EntryId DESC)`; `idx_entries_timestamp` on `Timestamp`.
- All upserts use `clause.OnConflict{DoNothing: true}`.
- `Entry` has `Embedding vector(384)` (pgvector) and `EmbeddingAt time.Time` fields for semantic search.
- Structured logging via `log/slog` throughout.

## Scraper Conventions
- Two colly collectors: `topicCollector` and `entryCollector`.
- Pagination: `?focusto=<lastEntryId+1>` fetches only new entries since last run.
- Entry timestamps parsed as `"02.01.2006 15:04"` (Turkish locale), shifted **−3 hours** to UTC.
- `"b"` in raw new-entry count strings means `"00"` (e.g. `"1b"` → `"100"`), commas stripped.
- All shared goroutine state is mutex-protected (`entriesMutex`, `scrapedIdsMutex`, etc.).
- Results flow through four channels: `topicsChan`, `entriesChan`, `attachmentsChan`, `requestsChan`.
- Batch-insert entries in chunks of 250 via `model.AddEntries`.

## Config
Key fields in `configs/config.json` (copy from `configs/templates/config_template.json`):
- `ScrapeInterval`, `EntryCollectorDelay`, `EntryCollectorRandomDelay`
- `DbHost/Port/Name/User/Password/DbSSLMode`
- `EmbedderUrl` – e.g. `"http://localhost:8765"` (Python sidecar)
- `GeminiApiKey`, `GeminiModel` – digest generation is skipped if `GeminiApiKey` is empty
- `DigestIntervalMinutes` – digest cron period (default 240)
- `ApiPort` – HTTP server port

## Build & Run
```bash
# Build binaries
go build -o gok ./cmd/gok
go build -o print-digest ./cmd/print-digest

# Start embedder sidecar (required for semantic features)
python embedder/main.py

# Run scraper + digest cron (auto-restart loop → log.txt)
./run.sh

# Backfill embeddings for past entries
python scripts/embed_entries.py --days 7

# Print latest digest to file
./print-digest [output.txt]
```

GORM auto-migrates all tables on `model.InitDb()` — no manual schema changes needed.

## Key Dependencies
| Package | Purpose |
|---|---|
| `github.com/gocolly/colly` | HTML scraping |
| `gorm.io/gorm` + `gorm.io/driver/postgres` | ORM + PostgreSQL |
| `github.com/pgvector/pgvector-go` | pgvector embedding type |
| `google.golang.org/genai` | Gemini API client |
| `github.com/go-co-op/gocron` | Cron scheduling |
| `github.com/tkanos/gonfig` | JSON config loading |
| `embedder/main.py` (FastAPI) | `intfloat/multilingual-e5-small` embedding sidecar |
