package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serve points the client at handler, a fake Ollama /api/embed endpoint.
func serve(t *testing.T, handler func(w http.ResponseWriter, input []string)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Model string
			Input []string
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model != "test-model" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		handler(w, req.Input)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OLLAMA_HOST", srv.URL)
	t.Setenv("LIKI_EMBED_MODEL", "test-model")
	return New()
}

func reply(w http.ResponseWriter, vecs [][]float32) {
	json.NewEncoder(w).Encode(map[string]any{"embeddings": vecs})
}

func TestEmbedBatchesInOrder(t *testing.T) {
	var batches []int
	c := serve(t, func(w http.ResponseWriter, input []string) {
		batches = append(batches, len(input))
		// The vector encodes which text it belongs to: (1, n) for "text n".
		var vecs [][]float32
		for _, in := range input {
			var n int
			fmt.Sscanf(in, "text %d", &n)
			vecs = append(vecs, []float32{1, float32(n)})
		}
		reply(w, vecs)
	})

	texts := make([]string, 70)
	for i := range texts {
		texts[i] = fmt.Sprintf("text %d", i)
	}
	vecs, err := c.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(batches) != "[32 32 6]" {
		t.Errorf("batch sizes = %v, want [32 32 6]", batches)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("got %d vectors for %d texts", len(vecs), len(texts))
	}
	for i, v := range vecs {
		if n := math.Round(float64(v[1] / v[0])); n != float64(i) {
			t.Errorf("vector %d belongs to text %v", i, n)
		}
		// Unit length, so Dot is cosine similarity.
		if l := Dot(v, v); math.Abs(float64(l)-1) > 1e-5 {
			t.Errorf("vector %d has squared length %v, want 1", i, l)
		}
	}
}

func TestEmbedErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		handler func(w http.ResponseWriter, input []string)
		want    string
	}{
		{
			name: "model not pulled",
			handler: func(w http.ResponseWriter, _ []string) {
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(map[string]string{"error": `model "test-model" not found, try pulling it first`})
			},
			want: "ollama pull test-model",
		},
		{
			name: "wrong number of vectors",
			handler: func(w http.ResponseWriter, _ []string) {
				reply(w, [][]float32{{1}})
			},
			want: "got 1 embeddings for 2 texts",
		},
		{
			name: "not json",
			handler: func(w http.ResponseWriter, _ []string) {
				http.Error(w, "upstream down", http.StatusBadGateway)
			},
			want: "502",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := serve(t, tt.handler)
			_, err := c.Embed(context.Background(), []string{"a", "b"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestEmbedOllamaNotRunning(t *testing.T) {
	// A port that was just free refuses connections.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	t.Setenv("OLLAMA_HOST", addr)
	_, err = New().Embed(context.Background(), []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "ollama is not running") {
		t.Errorf("err = %v, want a hint that Ollama is not running", err)
	}
}

func TestNewHost(t *testing.T) {
	for _, tt := range []struct{ env, want string }{
		{"", "http://localhost:11434"},
		// Ollama's own OLLAMA_HOST is often just host:port.
		{"127.0.0.1:5000", "http://127.0.0.1:5000"},
		{"https://ollama.lan/", "https://ollama.lan"},
	} {
		t.Setenv("OLLAMA_HOST", tt.env)
		if got := New().Host; got != tt.want {
			t.Errorf("OLLAMA_HOST=%q: Host = %q, want %q", tt.env, got, tt.want)
		}
	}
}
