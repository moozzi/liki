package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"liki/internal/embed"
	"liki/internal/store"
)

func TestChunksMeta(t *testing.T) {
	got := Chunks(store.Link{URL: "https://x.example", Tags: []string{"go", "maps"}}, "")
	want := []string{"https://x.example\ntags: go, maps"}
	if !slices.Equal(got, want) {
		t.Errorf("Chunks = %q, want %q", got, want)
	}
}

func TestChunksKeepTextWithinSize(t *testing.T) {
	var paras []string
	for i := range 40 {
		paras = append(paras, strings.Repeat(fmt.Sprintf("p%dword ", i), 10+i*7))
	}
	// One paragraph much longer than a chunk.
	paras = append(paras, strings.Repeat("long ", 1000))
	text := strings.Join(paras, "\n\n")

	chunks := Chunks(store.Link{URL: "https://x.example"}, text)[1:]
	for i, c := range chunks {
		if len(c) > chunkSize {
			t.Errorf("chunk %d has %d bytes, more than %d", i, len(c), chunkSize)
		}
	}
	// Splitting must not lose, duplicate or cut words.
	if got, want := strings.Fields(strings.Join(chunks, " ")), strings.Fields(text); !slices.Equal(got, want) {
		t.Errorf("chunks hold %d words, text has %d", len(got), len(want))
	}
}

func TestChunksSplitLongWordsOnRuneBoundaries(t *testing.T) {
	text := strings.Repeat("ż", chunkSize) // 2 bytes each, no spaces
	chunks := Chunks(store.Link{URL: "https://x.example"}, text)[1:]
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want the text split", len(chunks))
	}
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Errorf("chunk %d is not valid UTF-8", i)
		}
	}
	if strings.Join(chunks, "") != text {
		t.Error("chunks do not add up to the text")
	}
}

func TestQueryWords(t *testing.T) {
	got := queryWords("Enum, enum POSTGRES! żółw pg_enum")
	want := []string{"enum", "postgres", "żółw", "pg"}
	if !slices.Equal(got, want) {
		t.Errorf("queryWords = %q, want %q", got, want)
	}
}

func TestQuoteEscapesQuerySyntax(t *testing.T) {
	if got := orQuery([]string{`a"b`, "c-d"}); got != `"a""b" OR "c-d"` {
		t.Errorf("orQuery = %s", got)
	}
}

func TestResultKeyword(t *testing.T) {
	for _, tt := range []struct {
		words, total int
		want         bool
	}{
		{1, 1, true}, {1, 2, true}, {1, 3, false}, {2, 3, true}, {0, 0, false},
	} {
		r := Result{Words: tt.words, TotalWords: tt.total}
		if r.Keyword() != tt.want {
			t.Errorf("%d of %d words: Keyword() = %v, want %v", tt.words, tt.total, r.Keyword(), tt.want)
		}
	}
}

// fakeOllama serves /api/embed with vectors from vec.
func fakeOllama(t *testing.T, vec func(text string) []float32) *embed.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var res struct {
			Embeddings [][]float32 `json:"embeddings"`
		}
		for _, in := range req.Input {
			res.Embeddings = append(res.Embeddings, vec(in))
		}
		json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OLLAMA_HOST", srv.URL)
	t.Setenv("LIKI_EMBED_MODEL", "fake")
	return embed.New()
}

// topicVec gives each text a vector by its topic words, so meaning is
// controlled by the test: "topica" and its synonym "alef" point one way,
// "topicb" another, and text with neither points a third way.
func topicVec(text string) []float32 {
	text = strings.ToLower(text)
	a := float32(strings.Count(text, "topica") + strings.Count(text, "alef"))
	b := float32(strings.Count(text, "topicb"))
	if a == 0 && b == 0 {
		return []float32{0, 0, 1}
	}
	return []float32{a, b, 0}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "liki.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// addIndexed saves a link with text as its content and indexes it.
func addIndexed(t *testing.T, s *store.Store, c *embed.Client, url, text string) store.Link {
	t.Helper()
	l, err := s.Add(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveContent(l.ID, "", text, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := Index(context.Background(), s, c, l); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestSearch(t *testing.T) {
	s := openStore(t)
	c := fakeOllama(t, topicVec)
	one := addIndexed(t, s, c, "https://one.example", "topica alpha beta")
	two := addIndexed(t, s, c, "https://two.example", "topicb alpha gamma")
	addIndexed(t, s, c, "https://three.example", "topicb delta")
	mixed := addIndexed(t, s, c, "https://mixed.example", "topica topicb")                   // similarity 0.71
	mostlyB := addIndexed(t, s, c, "https://mostlyb.example", "topica topicb topicb topicb") // similarity 0.32

	type row struct {
		id    int64
		words int
	}
	// The order of results depends on fusion details, so only the set is
	// checked, plus the first result where it is clear-cut (first).
	for _, tt := range []struct {
		name  string
		query string
		want  []row
		first bool
	}{
		{
			// No query word occurs in any text: only links close in meaning, above the threshold.
			name: "meaning only", query: "alef",
			want: []row{{one.ID, 0}, {mixed.ID, 0}},
		},
		{
			// "mostlyb" is too far in meaning (0.32) and has no query word.
			name: "below similarity threshold", query: "alef",
			want: []row{{one.ID, 0}, {mixed.ID, 0}},
		},
		{
			// "two" has one of two words, enough for a keyword match despite unrelated meaning.
			name: "half of the words", query: "alef gamma",
			want: []row{{one.ID, 0}, {mixed.ID, 0}, {two.ID, 1}},
		},
		{
			// One of three words is not enough: "two" and "three" are left out.
			name: "less than half", query: "alef gamma delta",
			want: []row{{one.ID, 0}, {mixed.ID, 0}},
		},
		{
			// Matching both ways ranks first; "mostlyb" is in by its keyword.
			name: "both", query: "topica alpha",
			want:  []row{{one.ID, 2}, {mixed.ID, 1}, {two.ID, 1}, {mostlyB.ID, 1}},
			first: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			results, err := Search(context.Background(), s, c, tt.query, 10)
			if err != nil {
				t.Fatal(err)
			}
			var got []row
			for _, r := range results {
				got = append(got, row{r.Link.ID, r.Words})
				if r.Link.URL == "" {
					t.Errorf("result #%d has no link loaded", r.Link.ID)
				}
			}
			if !sameRows(got, tt.want) || (tt.first && got[0] != tt.want[0]) {
				t.Errorf("results (id, words) = %v, want %v", got, tt.want)
			}
		})
	}
}

func sameRows[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

func TestSearchLimit(t *testing.T) {
	s := openStore(t)
	c := fakeOllama(t, topicVec)
	for i := range 5 {
		addIndexed(t, s, c, fmt.Sprintf("https://%d.example", i), "topica")
	}
	results, err := Search(context.Background(), s, c, "topica", 3)
	if err != nil || len(results) != 3 {
		t.Errorf("got %d results, %v; want 3", len(results), err)
	}
}

func TestRelated(t *testing.T) {
	s := openStore(t)
	add := func(vecs ...[]float32) int64 {
		t.Helper()
		l, err := s.Add(fmt.Sprintf("https://%d.example", len(vecs)*100+len(vecs[0])), nil)
		if err != nil {
			t.Fatal(err)
		}
		texts := make([]string, len(vecs))
		for i, v := range vecs {
			embed.Normalize(v)
			texts[i] = "chunk"
		}
		if err := s.ReplaceChunks(l.ID, "m", "", texts, vecs); err != nil {
			t.Fatal(err)
		}
		return l.ID
	}
	target := add([]float32{1, 0, 0}, []float32{1, 0.1, 0})
	same := add([]float32{1, 0, 0})
	close := add([]float32{0.8, 0.6, 0})
	// One chunk on topic, most of the article about something else.
	mostlyOther := add([]float32{1, 0, 0}, []float32{0, 1, 0}, []float32{0, 1, 0}, []float32{0, 1, 0})
	add([]float32{0, 0, 1})
	unindexed, err := s.Add("https://unindexed.example", nil)
	if err != nil {
		t.Fatal(err)
	}

	results, err := Related(s, "m", target, 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for _, r := range results {
		got = append(got, r.Link.ID)
	}
	if want := []int64{same, close}; !slices.Equal(got, want) {
		t.Errorf("Related = %v, want %v (not itself, not #%d which is mostly off topic)", got, want, mostlyOther)
	}
	if results, _ := Related(s, "m", target, 1); len(results) != 1 {
		t.Errorf("with n=1 got %d results", len(results))
	}
	if _, err := Related(s, "m", unindexed.ID, 10); !errors.Is(err, ErrNotIndexed) {
		t.Errorf("unindexed link: err = %v, want ErrNotIndexed", err)
	}
}
