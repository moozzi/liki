package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"
)

const model = "test-model"

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "liki.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustAdd(t *testing.T, s *Store, url string, tags ...string) Link {
	t.Helper()
	l, err := s.Add(url, tags)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// index stores one chunk per text, with a dummy vector.
func index(t *testing.T, s *Store, id int64, title string, texts ...string) {
	t.Helper()
	vecs := make([][]float32, len(texts))
	for i := range vecs {
		vecs[i] = []float32{1, 0}
	}
	if err := s.ReplaceChunks(id, model, title, texts, vecs); err != nil {
		t.Fatal(err)
	}
}

func isIndexed(t *testing.T, s *Store, id int64) bool {
	t.Helper()
	links, err := s.Unindexed(model)
	if err != nil {
		t.Fatal(err)
	}
	return !slices.ContainsFunc(links, func(l Link) bool { return l.ID == id })
}

func TestUpdateTagsKeepsContentButDropsIndex(t *testing.T) {
	s := openTemp(t)
	l := mustAdd(t, s, "https://go.dev/blog/maps", "go", "maps")
	if err := s.SaveContent(l.ID, "Maps", "text", "<p>text</p>", []byte("<html>")); err != nil {
		t.Fatal(err)
	}
	index(t, s, l.ID, "Maps", "meta", "text")

	tags := []string{"golang"}
	got, err := s.Update(l.ID, Patch{Tags: &tags})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Tags, tags) {
		t.Errorf("tags = %q, want %q", got.Tags, tags)
	}
	if _, err := s.GetContent(l.ID); err != nil {
		t.Errorf("content lost after changing tags: %v", err)
	}
	// Tags are part of the indexed text, so the index must be rebuilt.
	if isIndexed(t, s, l.ID) {
		t.Error("link still indexed after its tags changed")
	}

	none := []string{}
	if got, err = s.Update(l.ID, Patch{Tags: &none}); err != nil || len(got.Tags) != 0 {
		t.Errorf("clearing tags: got %q, %v", got.Tags, err)
	}
}

func TestUpdateURLDropsContent(t *testing.T) {
	s := openTemp(t)
	l := mustAdd(t, s, "https://old.example/a")
	if err := s.SaveContent(l.ID, "Old", "text", "<p>text</p>", []byte("<html>")); err != nil {
		t.Fatal(err)
	}

	u := "https://new.example/b"
	if _, err := s.Update(l.ID, Patch{URL: &u}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetContent(l.ID); !errors.Is(err, ErrNoContent) {
		t.Errorf("GetContent after URL change: err = %v, want ErrNoContent", err)
	}
	if _, err := s.GetSnapshot(l.ID); !errors.Is(err, ErrNoContent) {
		t.Errorf("GetSnapshot after URL change: err = %v, want ErrNoContent", err)
	}
	// Duplicate detection must follow the new URL.
	if dups, _ := s.FindURL("http://www.new.example/b/"); len(dups) != 1 || dups[0].ID != l.ID {
		t.Errorf("FindURL(new url) = %v, want link #%d", dups, l.ID)
	}
	if dups, _ := s.FindURL("https://old.example/a"); len(dups) != 0 {
		t.Errorf("FindURL(old url) = %v, want none", dups)
	}
}

func TestUpdateMissingLink(t *testing.T) {
	s := openTemp(t)
	u := "https://x.example"
	if _, err := s.Update(42, Patch{URL: &u}); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestPageFiltersByAnyTag(t *testing.T) {
	s := openTemp(t)
	a := mustAdd(t, s, "https://a.example", "go")
	mustAdd(t, s, "https://b.example", "bread")
	c := mustAdd(t, s, "https://c.example", "Postgres", "go")
	d := mustAdd(t, s, "https://d.example", "postgres")

	links, total, err := s.Page([]string{"GO", "postgres"}, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	// c has both tags but is one link; matching ignores case.
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if got := ids(links); !slices.Equal(got, []int64{d.ID, c.ID}) {
		t.Errorf("first page = %v, want newest first %v", got, []int64{d.ID, c.ID})
	}
	links, _, err = s.Page([]string{"GO", "postgres"}, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(links); !slices.Equal(got, []int64{a.ID}) {
		t.Errorf("second page = %v, want %v", got, []int64{a.ID})
	}

	if _, total, _ := s.Page(nil, 0, 10); total != 4 {
		t.Errorf("unfiltered total = %d, want 4", total)
	}
}

func ids(links []Link) []int64 {
	var out []int64
	for _, l := range links {
		out = append(out, l.ID)
	}
	return out
}

func TestSaveContent(t *testing.T) {
	s := openTemp(t)
	l := mustAdd(t, s, "https://a.example")
	if _, err := s.GetContent(l.ID); !errors.Is(err, ErrNoContent) {
		t.Errorf("before fetch: err = %v, want ErrNoContent", err)
	}

	snap := []byte("<html><body>offline copy</body></html>")
	if err := s.SaveContent(l.ID, "Title", "text", "<p>text</p>", snap); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSnapshot(l.ID)
	if err != nil || string(got) != string(snap) {
		t.Errorf("GetSnapshot = %q, %v; want %q", got, err, snap)
	}
	if l, _ := s.Get(l.ID); l.FetchedAt.IsZero() || !l.HasSnapshot {
		t.Errorf("link after fetch: FetchedAt %v, HasSnapshot %v", l.FetchedAt, l.HasSnapshot)
	}

	if err := s.SaveContent(99, "", "", "", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("saving content of a missing link: err = %v, want ErrNotFound", err)
	}
}

func TestDeleteRemovesEverything(t *testing.T) {
	s := openTemp(t)
	l := mustAdd(t, s, "https://a.example", "go")
	if err := s.SaveContent(l.ID, "Title", "text", "", []byte("<html>")); err != nil {
		t.Fatal(err)
	}
	index(t, s, l.ID, "Title", "meta", "postgres enums")

	if err := s.Delete(l.ID); err != nil {
		t.Fatal(err)
	}
	if chunks, _ := s.Chunks(model); len(chunks) != 0 {
		t.Errorf("%d chunks left after delete", len(chunks))
	}
	// The full-text index has no foreign key; a trigger keeps it in sync.
	if found, _ := s.KeywordLinks(model, `"enums"`); len(found) != 0 {
		t.Errorf("keyword search still finds deleted link: %v", found)
	}
	if _, err := s.GetSnapshot(l.ID); !errors.Is(err, ErrNoContent) {
		t.Errorf("snapshot left after delete: err = %v", err)
	}
	if err := s.Delete(l.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete: err = %v, want ErrNotFound", err)
	}
}

func TestKeywordSearch(t *testing.T) {
	s := openTemp(t)
	a := mustAdd(t, s, "https://a.example")
	b := mustAdd(t, s, "https://b.example")
	c := mustAdd(t, s, "https://c.example")
	index(t, s, a.ID, "", "https://a.example\ntags: database", "postgres enum types", "unrelated part")
	index(t, s, b.ID, "PostgreSQL enums", "https://b.example", "a guide to types")
	// Indexed with another model, so never returned.
	if err := s.ReplaceChunks(c.ID, "other", "", []string{"enum"}, [][]float32{{1}}); err != nil {
		t.Fatal(err)
	}

	hits, err := s.KeywordSearch(model, `"enum"`, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits = %+v, want links #%d and #%d", hits, a.ID, b.ID)
	}
	// A title match outweighs a match in the text.
	if hits[0].LinkID != b.ID {
		t.Errorf("first hit #%d, want #%d (title match)", hits[0].LinkID, b.ID)
	}
	if hits[1].Text != "postgres enum types" || hits[1].Meta {
		t.Errorf("hit for #%d = %+v, want its matching text chunk", a.ID, hits[1])
	}

	hits, err = s.KeywordSearch(model, `"database"`, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !hits[0].Meta {
		t.Errorf("tag match = %+v, want the meta chunk", hits)
	}
}

// TestOpenMigratesOldDatabase opens a database as created by the first
// versions of liki: links had titles, there was no full-text index and no
// URL keys.
func TestOpenMigratesOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE links (
			id       INTEGER PRIMARY KEY AUTOINCREMENT,
			url      TEXT NOT NULL,
			title    TEXT NOT NULL DEFAULT '',
			added_at INTEGER NOT NULL
		);
		CREATE TABLE link_tags (link_id INTEGER NOT NULL, tag TEXT NOT NULL, PRIMARY KEY (link_id, tag));
		CREATE TABLE contents (
			link_id INTEGER PRIMARY KEY, text TEXT NOT NULL, html TEXT NOT NULL, fetched_at INTEGER NOT NULL
		);
		CREATE TABLE chunks (id INTEGER PRIMARY KEY, link_id INTEGER NOT NULL, model TEXT NOT NULL, text TEXT NOT NULL, vec BLOB NOT NULL);
		INSERT INTO links (url, title, added_at) VALUES ('https://www.go.dev/blog/maps/', 'Go maps in action', 1700000000);
		INSERT INTO link_tags VALUES (1, 'go');
		INSERT INTO contents VALUES (1, 'Maps are hash tables.', '<p>Maps are hash tables.</p>', 1700000000);
		INSERT INTO chunks (link_id, model, text, vec) VALUES (1, 'test-model', 'Maps are hash tables.', x'0000803f');`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	c, err := s.GetContent(1)
	if err != nil || c.Title != "Go maps in action" {
		t.Errorf("content title = %q, %v; want the old link title", c.Title, err)
	}
	if l, err := s.Get(1); err != nil || l.URL != "https://www.go.dev/blog/maps/" || !slices.Equal(l.Tags, []string{"go"}) {
		t.Errorf("link = %+v, %v", l, err)
	}
	if dups, _ := s.FindURL("https://go.dev/blog/maps"); len(dups) != 1 {
		t.Errorf("FindURL on migrated link = %v, want it found", dups)
	}
	// Existing chunks are added to the new full-text index, title included.
	for _, word := range []string{`"hash"`, `"action"`} {
		if found, _ := s.KeywordLinks(model, word); !slices.Equal(found, []int64{1}) {
			t.Errorf("KeywordLinks(%s) = %v, want [1]", word, found)
		}
	}
	// New links work with the migrated schema.
	if _, err := s.Add("https://a.example", nil); err != nil {
		t.Errorf("Add after migration: %v", err)
	}

	// Opening again must not repeat the migration.
	s.Close()
	if s, err = Open(path); err != nil {
		t.Fatalf("second open: %v", err)
	}
	if found, _ := s.KeywordLinks(model, `"hash"`); len(found) != 1 {
		t.Errorf("after second open KeywordLinks = %v, want one link", found)
	}
}
