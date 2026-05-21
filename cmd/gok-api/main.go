package main

import (
"context"
"fmt"
"log/slog"
"net/http"
"os"
"os/signal"
"syscall"
"time"

"github.com/go-chi/chi/v5"
"github.com/go-chi/chi/v5/middleware"
"gok/internal/api"
"gok/internal/config"
"gok/internal/embedder"
"gok/internal/llm"
"gok/internal/model"
)

func main() {
if err := model.InitDb(); err != nil {
slog.Error("couldn't connect to the database", "error", err)
os.Exit(1)
}

ctx := context.Background()

embedClient := embedder.NewClient(config.Config.EmbedderUrl)
llmClient, err := llm.NewClient(ctx, config.Config.GeminiApiKey, config.Config.GeminiModel)
if err != nil {
slog.Error("couldn't create LLM client", "error", err)
os.Exit(1)
}

h := api.NewHandler(embedClient, llmClient)

r := chi.NewRouter()
r.Use(middleware.Logger)
r.Use(middleware.Recoverer)
r.Use(corsMiddleware)

r.Post("/chat", h.ChatHandler)
r.Get("/digest/latest", h.DigestHandler)

// Serve frontend UI
r.Handle("/*", http.FileServer(http.Dir("./ui")))

port := config.Config.ApiPort
if port == 0 {
port = 8080
}
addr := fmt.Sprintf(":%d", port)

srv := &http.Server{
Addr:         addr,
Handler:      r,
ReadTimeout:  15 * time.Second,
WriteTimeout: 120 * time.Second, // long for SSE streaming
IdleTimeout:  60 * time.Second,
}

slog.Info("gok-api starting", "addr", addr)

go func() {
if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
slog.Error("server error", "error", err)
os.Exit(1)
}
}()

quit := make(chan os.Signal, 1)
signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
<-quit

slog.Info("shutting down server")
shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
srv.Shutdown(shutdownCtx)
}

func corsMiddleware(next http.Handler) http.Handler {
return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
w.Header().Set("Access-Control-Allow-Origin", "*")
w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
if r.Method == http.MethodOptions {
w.WriteHeader(http.StatusNoContent)
return
}
next.ServeHTTP(w, r)
})
}
