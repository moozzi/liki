// Package search indexes links as embedding vectors and finds them by meaning.
package search

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"unicode"
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

	// Every chunk is embedded with the page title so it keeps the article's context.
	inputs := make([]string, len(chunks))
	for i, ch := range chunks {
		inputs[i] = ch
		if content.Title != "" {
			inputs[i] = content.Title + "\n\n" + ch
		}
	}
	vecs, err := c.Embed(ctx, inputs)
	if err != nil {
		return err
	}
	return s.ReplaceChunks(l.ID, c.Model, content.Title, chunks, vecs)
}

// Chunks splits a link into pieces for indexing. The first piece describes the
// link itself (URL, tags) so links without content are searchable too.
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

const (
	// candidates is how many links each ranking contributes to the fusion.
	candidates = 50
	// rrfK damps the weight of top ranks in reciprocal rank fusion; 60 is the usual choice.
	rrfK = 60
)

// minSimilarity hides links that match only by meaning, and only weakly.
// With bge-m3, unrelated texts usually score below 0.45.
const minSimilarity = 0.45

// minRelated hides links that are not really related. Whole-article centroids
// score higher than chunks against a short query: with bge-m3, articles on
// unrelated topics are around 0.55.
const minRelated = 0.6

type Result struct {
	Link       store.Link
	Score      float64 // reciprocal rank fusion score, used for ordering
	Similarity float32 // cosine similarity of the best matching chunk
	Words      int     // how many query words the link contains
	TotalWords int     // how many distinct words the query has
	Snippet    string  // the best matching piece of the link
}

// Keyword reports whether the link counts as a keyword match: it contains at
// least half of the query words, so one common word alone is not enough.
func (r Result) Keyword() bool {
	return r.TotalWords > 0 && 2*r.Words >= r.TotalWords
}

// Search returns up to n links matching query, best first. It combines two
// rankings with reciprocal rank fusion: similarity in meaning (embedding
// vectors) and keyword relevance (FTS5 BM25), so both related topics and
// exact names like "pg_enum" are found.
func Search(ctx context.Context, s *store.Store, c *embed.Client, query string, n int) ([]Result, error) {
	byMeaning, err := vectorRanking(ctx, s, c, query)
	if err != nil {
		return nil, err
	}

	fused := map[int64]*Result{}
	get := func(id int64) *Result {
		if fused[id] == nil {
			fused[id] = &Result{Link: store.Link{ID: id}}
		}
		return fused[id]
	}
	for i, v := range byMeaning[:min(candidates, len(byMeaning))] {
		r := get(v.Link.ID)
		r.Score += 1.0 / float64(rrfK+i+1)
		r.Similarity, r.Snippet = v.Similarity, v.Snippet
	}

	words := queryWords(query)
	if len(words) > 0 {
		// Count how many of the query words each link contains.
		found := map[int64]int{}
		for _, w := range words {
			ids, err := s.KeywordLinks(c.Model, quote(w))
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				found[id]++
			}
		}
		hits, err := s.KeywordSearch(c.Model, orQuery(words), candidates)
		if err != nil {
			return nil, err
		}
		rank := 0
		for _, h := range hits {
			r := get(h.LinkID)
			r.Words, r.TotalWords = found[h.LinkID], len(words)
			if !r.Keyword() {
				continue
			}
			rank++
			r.Score += 1.0 / float64(rrfK+rank)
			// A keyword chunk shows the user's words, so it explains the match
			// better, unless it is just the URL-and-tags chunk.
			if !h.Meta {
				r.Snippet = h.Text
			}
		}
	}

	results := make([]Result, 0, len(fused))
	for _, r := range fused {
		if r.Keyword() || r.Similarity >= minSimilarity {
			results = append(results, *r)
		}
	}
	slices.SortFunc(results, func(a, b Result) int {
		return cmp.Or(cmp.Compare(b.Score, a.Score), cmp.Compare(b.Similarity, a.Similarity))
	})
	results = results[:min(n, len(results))]

	for i := range results {
		if results[i].Link, err = s.Get(results[i].Link.ID); err != nil {
			return nil, err
		}
	}
	return results, nil
}

// vectorRanking returns all indexed links ordered by similarity in meaning to
// query. A link scores as its best matching chunk.
func vectorRanking(ctx context.Context, s *store.Store, c *embed.Client, query string) ([]Result, error) {
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
		sim := embed.Dot(qv[0], ch.Vec)
		if r, ok := best[ch.LinkID]; !ok || sim > r.Similarity {
			best[ch.LinkID] = Result{Link: store.Link{ID: ch.LinkID}, Similarity: sim, Snippet: ch.Text}
		}
	}
	ranked := make([]Result, 0, len(best))
	for _, r := range best {
		ranked = append(ranked, r)
	}
	slices.SortFunc(ranked, func(a, b Result) int { return cmp.Compare(b.Similarity, a.Similarity) })
	return ranked, nil
}

// queryWords splits a query into distinct lowercase words.
func queryWords(query string) []string {
	var words []string
	for _, w := range strings.FieldsFunc(query, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if w = strings.ToLower(w); !slices.Contains(words, w) {
			words = append(words, w)
		}
	}
	return words
}

// orQuery builds an FTS5 query matching any of words.
func orQuery(words []string) string {
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = quote(w)
	}
	return strings.Join(quoted, " OR ")
}

// quote makes w an FTS5 string, so characters like '-' or '*' are not query syntax.
func quote(w string) string {
	return `"` + strings.ReplaceAll(w, `"`, `""`) + `"`
}

// ErrNotIndexed is returned by Related for a link without a search index.
var ErrNotIndexed = errors.New("link is not indexed yet")

// Related returns up to n links most similar in meaning to the link with the
// given id, best first. Each link is represented by the mean of its chunk
// vectors, so whole articles are compared rather than single passages.
func Related(s *store.Store, model string, id int64, n int) ([]Result, error) {
	chunks, err := s.Chunks(model)
	if err != nil {
		return nil, err
	}
	centroids := map[int64][]float32{}
	for _, ch := range chunks {
		c := centroids[ch.LinkID]
		if c == nil {
			c = make([]float32, len(ch.Vec))
			centroids[ch.LinkID] = c
		}
		for i, x := range ch.Vec {
			c[i] += x
		}
	}
	target := centroids[id]
	if target == nil {
		return nil, ErrNotIndexed
	}
	// Normalized, the dot product of two centroids is their cosine similarity.
	for _, c := range centroids {
		embed.Normalize(c)
	}

	var results []Result
	for linkID, c := range centroids {
		if linkID == id {
			continue
		}
		if sim := embed.Dot(target, c); sim >= minRelated {
			results = append(results, Result{Link: store.Link{ID: linkID}, Similarity: sim})
		}
	}
	slices.SortFunc(results, func(a, b Result) int { return cmp.Compare(b.Similarity, a.Similarity) })
	results = results[:min(n, len(results))]
	for i := range results {
		if results[i].Link, err = s.Get(results[i].Link.ID); err != nil {
			return nil, err
		}
	}
	return results, nil
}
