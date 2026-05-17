# gok – Copilot Instructions

## Project Overview
`gok` is a Go scraper that periodically collects popular topics and entries from [eksisozluk.com](https://eksisozluk.com) (a Turkish social platform) and persists them into a PostgreSQL database. It runs as a scheduled cron job.

## Architecture

```
cmd/gok/main.go          → entrypoint: init DB, start gocron scheduler
internal/config/         → loads configs/config.json at init() time via gonfig
internal/scraper/        → scraping logic; eksi.go does the HTML scraping, scraper.go orchestrates and writes to DB
internal/model/          → GORM models + DB interaction; db.go has all private DB functions, each model file exposes public wrappers
configs/config.json      → runtime config (DB creds, scrape interval, colly delays)
```

## Data Flow
1. `gocron` calls `scraper.ScrapeAll()` every `ScrapeInterval` minutes.
2. `scrapeEksiTopicsAndEntries()` runs in a goroutine, pushing results to four channels: `topicsChan`, `entriesChan`, `attachmentsChan`, `requestsChan`.
3. Topics arrive first (channel read blocks), then entries/attachments/requests after `entryCollector.Wait()`.
4. `ScrapeAll` batch-inserts entries in chunks of 250 (`model.AddEntries`).
5. Failed HTTP requests are stored via `model.AddRequests` for diagnostics.

## Model Conventions
- All GORM models embed `gorm.Model` (adds `ID`, `CreatedAt`, `UpdatedAt`, `DeletedAt`).
- `PTopic` is a transient scrape-time struct; `Topic` + `PopularTopic` are the persisted DB models.
- DB functions are **private** in `db.go`; each model file (e.g., `entry.go`, `topic.go`) exposes **public** wrapper functions.
- Composite index on `entries`: `(TopicId ASC, EntryId DESC)` – `idx_entries_topic_entry`.
- `Entry.Url` and `Topic.Url` are `unique` constraints; upserts use `clause.OnConflict{DoNothing: true}`.

## Scraper Conventions
- Two colly collectors: `topicCollector` (topic list pages) and `entryCollector` (per-topic entry pages).
- Pagination: uses `?focusto=<lastEntryId+1>` to fetch only new entries since last run.
- Entry timestamps are parsed as `"02.01.2006 15:04"` (Turkish locale) and shifted **−3 hours** to UTC.
- New-entry count parsing: `"b"` in the raw string represents `"00"` (e.g., `"1b"` → `"100"`), commas stripped.
- All shared state inside the goroutine is mutex-protected (`entriesMutex`, `scrapedIdsMutex`, etc.).

## Config
Loaded from `configs/config.json` at `init()`. Key fields:
- `ScrapeInterval` – cron period in minutes
- `EntryCollectorDelay` / `EntryCollectorRandomDelay` – colly rate-limit in ms
- DB connection fields (`DbHost`, `DbPort`, `DbUser`, `DbPassword`, `DbName`, `DbSSLMode`)

Copy `configs/templates/config_template.json` to `configs/config.json` and fill in values before running.

## Environment Setup
Requirements: **Go** (1.15+) and **PostgreSQL**.

```bash
# Create DB and user (example)
psql -U postgres -c "CREATE USER gok WITH PASSWORD 'gok';"
psql -U postgres -c "CREATE DATABASE gok OWNER gok;"
```

GORM auto-migrates all tables on `model.InitDb()` — no manual schema setup needed.

## Build & Run
```bash
# Build
go build -o gok ./cmd/gok

# Run (with auto-restart loop, appending to log.txt)
./run.sh

# Direct run
./gok
```

> `gok-topic` in the repo root is a compiled binary for a one-off scrape tool (different environment) — ignore it.

## Key Dependencies
| Package | Purpose |
|---|---|
| `github.com/gocolly/colly` | HTML scraping |
| `gorm.io/gorm` + `gorm.io/driver/postgres` | ORM + PostgreSQL |
| `github.com/go-co-op/gocron` | Cron scheduling |
| `github.com/tkanos/gonfig` | JSON config loading |
