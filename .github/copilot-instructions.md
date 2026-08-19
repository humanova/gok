# gok - Copilot Instructions

## Product And Priorities

`gok` collects public Ekşi Sözlük discussion data and makes it explorable in two primary experiences:

- **Radar** (`/`): a live, 24-hour activity view. It ranks topics by recent entries, supports a replayable timeline, and can display AI-generated topic briefs.
- **Atlas** (`/map`, `/atlas`): a browsable snapshot of active, durable topics. Map links mean shared writer participation, not semantic similarity.
- **Long-term Atlas** (`/long-term-map`, `/archivemap`): the published long-term map snapshot.

The scraper and PostgreSQL corpus are the foundation. Embeddings, topic briefs, and the Turkish digest are optional supporting features. Do not frame the digest as the application's main path.

## Architecture

```
cmd/gok/main.go               -> scraper scheduler
cmd/api/                      -> Radar and Atlas HTTP server; static UI assets are embedded
cmd/digen/main.go             -> Gemini-backed digest and Radar topic-brief scheduler
cmd/map/                      -> offline map profiling, graph building, region labeling, and layout tools
cmd/print-digest/main.go      -> exports the latest stored digest as text and HTML
internal/config/              -> loads configs/config.json during package initialization
internal/scraper/             -> Colly scraping, pagination, and database writes
internal/model/               -> GORM models and model-scoped public database wrappers
internal/rag/                 -> semantic search, digest generation, viewpoint clustering, topic briefs
internal/mapcuration/         -> shared CSV, JSON, Gemini, path, and region helpers for map tools
internal/llm/                 -> Gemini client
internal/embedder/            -> HTTP client for the FastAPI embedding sidecar
embedder/main.py              -> multilingual-e5-small embedding sidecar
scripts/build-map-pipeline.sh -> builds and atomically publishes a named Atlas snapshot
reports/maps/<name>/current   -> symlink to the currently published snapshot for a map name
```

## Backend Conventions

- Models normally embed `gorm.Model`. `EntryAttachment` uses a composite primary key and manual timestamps; `Request` has a plain `ID` and `CreatedAt`.
- Keep database functions private in `internal/model/db.go`; expose model-specific public wrappers from the corresponding model file.
- Use `clause.OnConflict{DoNothing: true}` for inserts that must deduplicate scraped data.
- `PTopic` is scrape-time only. `Topic` and `PopularTopic` are persisted.
- `Entry.Embedding` is `vector(384)` and `EmbeddingAt` records the semantic-index update.
- Keep structured logging with `log/slog` and propagate errors with useful context.
- `model.InitDb()` auto-migrates tables. Do not add manual schema-migration machinery unless the task explicitly requires it.

## Scraper Conventions

- `cmd/gok` schedules scraping only. `cmd/digen` separately schedules digest and topic-brief generation.
- The scraper uses separate Colly topic and entry collectors. The pagination query `?focusto=<lastEntryId+1>` fetches new entries.
- Parse timestamps using `"02.01.2006 15:04"` and shift Turkish local time by minus three hours to UTC.
- In a raw new-entry count, `"b"` means `"00"` and commas are removed; for example, `"1b"` becomes `100`.
- Protect shared goroutine state with its existing mutexes. Preserve the topic, entry, attachment, and request channel flow.
- Batch entry inserts through `model.AddEntries` in chunks of 250.

## Radar And API

- The Radar API derives pulse data from the last 24 hours and serves current data at `/api/pulse`; range replay is `/api/pulse/range`.
- Topic briefs are available from `/api/topics/{topicID}/brief` only when `digen` has generated them.
- The API loads map snapshots from published artifacts when it starts. Restart it after publishing a fresh snapshot.
- Static files under `cmd/api/static/` are embedded with `go:embed`. Rebuild and restart `cmd/api` after changing them; a browser refresh alone cannot load the change.
- Keep API responses and the static client in lockstep. When changing a response shape, update the corresponding client behavior and validation together.

## Atlas Pipeline

- Build maps through `./scripts/build-map-pipeline.sh`; do not manually assemble a partial published snapshot.
- The pipeline runs `profile-map`, `build-map`, `reconcile-map`, `reconcile-map-nodes`, and `layout-map`, validates the layout, then atomically switches `reports/maps/<name>/current`.
- `build-map` models writer overlap. It applies the durable-topic policy by default, ignores overly broad writers, retains reciprocal top neighbors, and then detects communities.
- Gemini labels regions during reconciliation. Preserve the distinction between behavior-derived graph structure and AI-generated browse labels.
- Use `--skip-durability --edge-days 30` only when a task explicitly calls for an all-recent-topic map. The normal default is a 548-day edge window.
- Treat published map artifacts as outputs. Do not edit timestamped snapshots directly; produce a new one through the pipeline.
- Consult `docs/map-curation.md` before altering eligibility, edge, community, or labeling logic. Keep `docs/map-curation.tr.md` aligned when documentation changes affect reader-facing curation policy.

## Configuration And Optional AI

`configs/config.json` is local runtime configuration copied from `configs/templates/config_template.json`. Relevant fields include:

- Scraper and database: `ScrapeInterval`, `EntryCollectorDelay`, `EntryCollectorRandomDelay`, and the `Db*` fields.
- API: `ApiPort`.
- Optional AI: `EmbedderUrl`, `GeminiApiKey`, `GeminiModel`, `DigestIntervalMinutes`, and `TopicBriefIntervalMinutes`.

Gemini is required by `digen` and map-region reconciliation. The digest and semantic search require the embedder sidecar; Radar and Atlas otherwise operate without embeddings. `digen` uses `/tmp/gok-digen.lock` so only one scheduler runs at a time.

## Build, Run, And Validate

```bash
# Build the primary executables
go build -o gok ./cmd/gok
go build -o api ./cmd/api
go build -o digen ./cmd/digen
go build -o print-digest ./cmd/print-digest

# Start scraping and the API
./run.sh
./api

# Generate topic briefs and a digest once (requires Gemini)
./digen --once

# Publish a current map snapshot (requires Gemini for labels)
./scripts/build-map-pipeline.sh --map-name current
```

- Prefer focused `go test` or `go test ./<touched-package>` when tests cover the changed behavior.
- At minimum, run `go build ./<touched-command>` after editing a command or its direct dependencies. For cross-cutting Go changes, run `go test ./...` when practical.
- After an API/static UI change, rebuild `./cmd/api` and exercise the affected route or browser workflow.
- Never commit `configs/config.json`, generated binaries, logs, or unrequested map artifacts. Respect an already-dirty worktree and do not revert the user's changes.
