# gok

A Go scraper that periodically collects popular topics and entries from [eksisozluk.com](https://eksisozluk.com) and persists them to PostgreSQL. It also generates a structured AI digest of the day's discourse.

## Scraper

`gocron` calls `scraper.ScrapeAll()` every `ScrapeInterval` minutes. Two colly collectors scrape the topic list and then each topic's entry pages (paginated via `?focusto=<lastEntryId+1>`). Results flow through channels and are batch-inserted in chunks of 250.

## Digest Pipeline

1. **Embedder service** (`embedder/main.py`): FastAPI service that loads `intfloat/multilingual-e5-small` and exposes an `/embed` endpoint. Entry texts are embedded and stored as `vector(384)` in PostgreSQL (pgvector).

2. **Hot topic selection** (`rag/digest.go`): fetches the top 15 hottest topics from the last 3 hours, allocates an entry budget of 250 proportional to each topic's heat score.

3. **Viewpoint clustering** (`rag/viewpoints.go`): for each topic, entries are clustered into 3 perspective groups by cosine similarity of their embeddings.

4. **LLM synthesis** (`llm/gemini.go`): topic bundles (clusters of entries) are serialised into a prompt and sent to Gemini, which returns a structured `DigestPayload` JSON.

## Setup

```bash
# Copy and fill in config
cp configs/templates/config_template.json configs/config.json

# Create DB
psql -U postgres -c "CREATE USER gok WITH PASSWORD 'gok';"
psql -U postgres -c "CREATE DATABASE gok OWNER gok;"

# Install embedder deps
pip install -r embedder/requirements.txt

# Build
go build -o gok ./cmd/gok
go build -o print-digest ./cmd/print-digest
```

## Run

```bash
# Start embedder sidecar
python embedder/main.py

# Run scraper (with auto-restart loop)
./run.sh
```
