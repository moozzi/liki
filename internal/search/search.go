// Package search indexes links as embedding vectors and finds them by meaning.
package search

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"liki/internal/embed"
	"liki/internal/store"
)

// chunkSize is the target length of one indexed piece of an article, in bytes.
// Short chunks keep each vector focused on one topic.
const chunkSize = 1200

// Index embeds the link's metadata and article text and stores the vectors,
// replacing any previous index of the link.
func Index(ctx context.Context, s *store.Store, c *embed.Client, l store.Link) error {
	content, err := s.GetContent(l.ID)
	if err != nil && !errors.Is(err, store.ErrNoContent) {
		return err
	}
	chunks := Chunks(l, content.Text)

	// Every chunk is embedded with the title so it keeps the article's context.
	inputs := make([]string, len(chunks))
	for i, ch := range chunks {
		inputs[i] = l.Title + "\n\n" + ch
	}
	vecs, err := c.Embed(ctx, inputs)
	if err != nil {
		return err
	}
	return s.ReplaceChunks(l.ID, c.Model, chunks, vecs)
}

// Chunks splits a link into pieces for indexing. The first piece describes the
// link itself (title, URL, tags) so links without content are searchable too.
func Chunks(l store.Link, text string) []string {
	meta := l.URL
	if len(l.Tags) > 0 {
		meta += "\ntags: " + strings.Join(l.Tags, ", ")
	}
	chunks := []string{meta}

	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			chunks = append(chunks, s)
		}
		cur.Reset()
	}
	for _, para := range strings.Split(text, "\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		if cur.Len() > 0 && cur.Len()+len(para) > chunkSize {
			flush()
		}
		// Paragraphs longer than a chunk are split on word boundaries.
		for len(para) > chunkSize {
			cut := strings.LastIndexByte(para[:chunkSize], ' ')
			if cut <= 0 {
				cut = chunkSize
				for cut > 0 && !utf8.RuneStart(para[cut]) {
					cut--
				}
			}
			cur.WriteString(para[:cut])
			flush()
			para = strings.TrimSpace(para[cut:])
		}
		if cur.Len() > 0 {
			cur.WriteByte('\n')
		}
		cur.WriteString(para)
	}
	flush()
	return chunks
}

type Result struct {
	Link    store.Link
	Score   float32
	Snippet string // the best matching piece of the link
}

// Search returns up to n links whose chunks are closest in meaning to query,
// best first. A link scores as its best matching chunk.
func Search(ctx context.Context, s *store.Store, c *embed.Client, query string, n int) ([]Result, error) {
	qv, err := c.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	chunks, err := s.Chunks(c.Model)
	if err != nil {
		return nil, err
	}

	best := map[int64]Result{}
	for _, ch := range chunks {
		score := embed.Dot(qv[0], ch.Vec)
		if r, ok := best[ch.LinkID]; !ok || score > r.Score {
			best[ch.LinkID] = Result{Link: store.Link{ID: ch.LinkID}, Score: score, Snippet: ch.Text}
		}
	}
	results := make([]Result, 0, len(best))
	for _, r := range best {
		results = append(results, r)
	}
	slices.SortFunc(results, func(a, b Result) int { return cmp.Compare(b.Score, a.Score) })
	results = results[:min(n, len(results))]

	for i := range results {
		if results[i].Link, err = s.Get(results[i].Link.ID); err != nil {
			return nil, err
		}
	}
	return results, nil
}
