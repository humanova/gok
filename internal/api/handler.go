package api

import (
"context"
"encoding/json"
"fmt"
"log/slog"
"net/http"
"time"

"gok/internal/config"
"gok/internal/embedder"
"gok/internal/llm"
"gok/internal/model"
"gok/internal/rag"
)

// Handler holds shared clients for request handling.
type Handler struct {
embedClient *embedder.Client
llmClient   *llm.Client
}

func NewHandler(e *embedder.Client, l *llm.Client) *Handler {
return &Handler{embedClient: e, llmClient: l}
}

// ChatRequest is the incoming JSON body for /chat.
type ChatRequest struct {
Query string `json:"query"`
}

// ChatHandler handles POST /chat.
// Embeds the query, runs hybrid search, extracts viewpoints, and streams the LLM response via SSE.
func (h *Handler) ChatHandler(w http.ResponseWriter, r *http.Request) {
var req ChatRequest
if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Query == "" {
http.Error(w, `{"error":"query is required"}`, http.StatusBadRequest)
return
}

ctx := r.Context()
db := model.DB()

opts := rag.RetrievalOpts{
TopK:         20,
HoursBack:    24,
VectorWeight: 0.6,
RRFConstant:  60,
}

entries, err := rag.HybridSearch(ctx, db, h.embedClient, req.Query, opts)
if err != nil {
slog.Error("hybrid search failed", "error", err)
http.Error(w, `{"error":"retrieval failed"}`, http.StatusInternalServerError)
return
}

viewpoints := rag.ExtractViewpoints(entries, 3)

// SSE headers
w.Header().Set("Content-Type", "text/event-stream")
w.Header().Set("Cache-Control", "no-cache")
w.Header().Set("Connection", "keep-alive")
w.Header().Set("X-Accel-Buffering", "no")

flusher, ok := w.(http.Flusher)
if !ok {
http.Error(w, "streaming not supported", http.StatusInternalServerError)
return
}

tokenCh, errCh := h.llmClient.StreamQuery(ctx, req.Query, entries, viewpoints)

for {
select {
case token, open := <-tokenCh:
if !open {
fmt.Fprintf(w, "event: done\ndata: {}\n\n")
flusher.Flush()
return
}
escaped, _ := json.Marshal(token)
fmt.Fprintf(w, "data: %s\n\n", escaped)
flusher.Flush()
case err, open := <-errCh:
if open && err != nil {
slog.Error("LLM stream error", "error", err)
fmt.Fprintf(w, "event: error\ndata: {\"error\":%q}\n\n", err.Error())
flusher.Flush()
return
}
case <-ctx.Done():
return
}
}
}

// DigestHandler handles GET /digest/latest.
// Returns the latest pre-computed digest immediately (sub-10ms from DB).
func (h *Handler) DigestHandler(w http.ResponseWriter, r *http.Request) {
digest, err := model.GetLatestDigest()
if err != nil {
slog.Error("could not fetch latest digest", "error", err)
http.Error(w, `{"error":"database error"}`, http.StatusInternalServerError)
return
}

if digest == nil {
// No digest yet — generate one on-demand synchronously
ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
defer cancel()

embedClient := embedder.NewClient(config.Config.EmbedderUrl)
llmClient, err := llm.NewClient(ctx, config.Config.GeminiApiKey, config.Config.GeminiModel)
if err != nil {
http.Error(w, `{"error":"llm client error"}`, http.StatusInternalServerError)
return
}

payload, err := rag.GenerateDigest(ctx, embedClient, llmClient)
if err != nil || payload == nil {
http.Error(w, `{"error":"could not generate digest"}`, http.StatusServiceUnavailable)
return
}

w.Header().Set("Content-Type", "application/json")
json.NewEncoder(w).Encode(payload)
return
}

w.Header().Set("Content-Type", "application/json")
w.Write(digest.Payload)
}
