package embedder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client calls the Python embedder sidecar.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 180 * time.Second,
		},
	}
}

type embedRequest struct {
	Texts []string `json:"texts"`
	Type  string   `json:"type"` // "query" or "passage"
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Dim        int         `json:"dim"`
}

func (c *Client) embed(ctx context.Context, texts []string, embedType string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Texts: texts, Type: embedType})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedder request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embedder returned status %d", resp.StatusCode)
	}

	var result embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("embedder response decode failed: %w", err)
	}

	return result.Embeddings, nil
}

// EmbedPassages embeds a slice of passage texts (for indexing).
func (c *Client) EmbedPassages(ctx context.Context, texts []string) ([][]float32, error) {
	return c.embed(ctx, texts, "passage")
}

// EmbedQuery embeds a single user query string (for retrieval).
func (c *Client) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	vecs, err := c.embed(ctx, []string{text}, "query")
	if err != nil {
		return nil, err
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("embedder returned empty result")
	}
	return vecs[0], nil
}

// Healthy checks if the sidecar is ready.
func (c *Client) Healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
