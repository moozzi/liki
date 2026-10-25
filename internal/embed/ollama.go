// Package embed turns text into embedding vectors using a local Ollama server.
package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

const (
	defaultHost  = "http://localhost:11434"
	defaultModel = "bge-m3"
	// batchSize limits how many texts go into one request.
	batchSize = 32
)

type Client struct {
	Host  string
	Model string
	http  *http.Client
}

// New returns a client configured from OLLAMA_HOST and LIKI_EMBED_MODEL.
func New() *Client {
	host := os.Getenv("OLLAMA_HOST")
	if host == "" {
		host = defaultHost
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	model := os.Getenv("LIKI_EMBED_MODEL")
	if model == "" {
		model = defaultModel
	}
	// Generous timeout: the first request also loads the model into memory.
	return &Client{Host: strings.TrimRight(host, "/"), Model: model, http: &http.Client{Timeout: 5 * time.Minute}}
}

// Embed returns one unit-length vector per text, so cosine similarity is a dot product.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	var out [][]float32
	for start := 0; start < len(texts); start += batchSize {
		batch := texts[start:min(start+batchSize, len(texts))]
		vecs, err := c.embedBatch(ctx, batch)
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (c *Client) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(map[string]any{"model": c.Model, "input": texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Host+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if errors.Is(err, syscall.ECONNREFUSED) {
		return nil, fmt.Errorf("ollama is not running at %s (start it with 'brew services start ollama')", c.Host)
	}
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var res struct {
		Embeddings [][]float32 `json:"embeddings"`
		Error      string      `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("ollama: %s: %w", resp.Status, err)
	}
	if res.Error != "" {
		if strings.Contains(res.Error, "not found") {
			return nil, fmt.Errorf("ollama: %s (download it with 'ollama pull %s')", res.Error, c.Model)
		}
		return nil, fmt.Errorf("ollama: %s", res.Error)
	}
	if len(res.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama: got %d embeddings for %d texts", len(res.Embeddings), len(texts))
	}
	for _, v := range res.Embeddings {
		normalize(v)
	}
	return res.Embeddings, nil
}

func normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
}

// Dot returns the dot product of a and b, which must have the same length.
func Dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
