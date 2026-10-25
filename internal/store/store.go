// Package store persists links in a local SQLite database.
package store

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Link struct {
	ID          int64
	URL         string
	Title       string
	Tags        []string
	AddedAt     time.Time
	FetchedAt   time.Time // zero if content was never fetched
	HasSnapshot bool      // whether a full-page snapshot is saved
}

// Content is the downloaded, readable version of a linked article.
type Content struct {
	Text      string
	HTML      string
	FetchedAt time.Time
}

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS links (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	url      TEXT NOT NULL,
	title    TEXT NOT NULL DEFAULT '',
	added_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS link_tags (
	link_id INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE,
	tag     TEXT NOT NULL,
	PRIMARY KEY (link_id, tag)
);
CREATE INDEX IF NOT EXISTS link_tags_tag ON link_tags(tag);
CREATE TABLE IF NOT EXISTS contents (
	link_id    INTEGER PRIMARY KEY REFERENCES links(id) ON DELETE CASCADE,
	text       TEXT NOT NULL,
	html       TEXT NOT NULL,
	fetched_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS snapshots (
	link_id INTEGER PRIMARY KEY REFERENCES links(id) ON DELETE CASCADE,
	html_gz BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS chunks (
	id      INTEGER PRIMARY KEY,
	link_id INTEGER NOT NULL REFERENCES links(id) ON DELETE CASCADE,
	model   TEXT NOT NULL,
	text    TEXT NOT NULL,
	vec     BLOB NOT NULL -- little-endian float32, unit length
);
CREATE INDEX IF NOT EXISTS chunks_link ON chunks(link_id);
-- Full-text index of chunks (rowid = chunks.id) for keyword search.
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
	title, text,
	tokenize = 'porter unicode61 remove_diacritics 2'
);
CREATE TRIGGER IF NOT EXISTS chunks_fts_delete AFTER DELETE ON chunks BEGIN
	DELETE FROM chunks_fts WHERE rowid = old.id;
END;
`

// DefaultPath returns $LIKI_DB or ~/.liki/liki.db.
func DefaultPath() (string, error) {
	if p := os.Getenv("LIKI_DB"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".liki", "liki.db"), nil
}

// Open opens (creating if needed) the database at path and applies the schema.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	var hadFTS bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE name = 'chunks_fts')`).Scan(&hadFTS); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	// Databases created before keyword search have chunks but no full-text index.
	if !hadFTS {
		if _, err := db.Exec(`INSERT INTO chunks_fts (rowid, title, text)
			SELECT c.id, l.title, c.text FROM chunks c JOIN links l ON l.id = c.link_id`); err != nil {
			db.Close()
			return nil, fmt.Errorf("build full-text index: %w", err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Add(url, title string, tags []string) (Link, error) {
	l := Link{URL: url, Title: title, Tags: tags, AddedAt: time.Now()}

	tx, err := s.db.Begin()
	if err != nil {
		return Link{}, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`INSERT INTO links (url, title, added_at) VALUES (?, ?, ?)`,
		l.URL, l.Title, l.AddedAt.Unix())
	if err != nil {
		return Link{}, err
	}
	if l.ID, err = res.LastInsertId(); err != nil {
		return Link{}, err
	}
	for _, t := range tags {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO link_tags (link_id, tag) VALUES (?, ?)`, l.ID, t); err != nil {
			return Link{}, err
		}
	}
	return l, tx.Commit()
}

// ErrNotFound is returned when a link with the given id does not exist.
var ErrNotFound = errors.New("link not found")

// Delete removes the link with the given id; its tags go with it via ON DELETE CASCADE.
func (s *Store) Delete(id int64) error {
	res, err := s.db.Exec(`DELETE FROM links WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Patch describes changes to a link; nil fields are left unchanged.
type Patch struct {
	URL   *string
	Title *string
	Tags  *[]string // replaces all tags
}

// Update applies p to the link with the given id and returns the updated link.
func (s *Store) Update(id int64, p Patch) (Link, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Link{}, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE links SET url = COALESCE(?, url), title = COALESCE(?, title) WHERE id = ?`,
		p.URL, p.Title, id)
	if err != nil {
		return Link{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Link{}, err
	}
	if n == 0 {
		return Link{}, ErrNotFound
	}
	// The search index embeds title, URL and tags, so any change makes it stale.
	if _, err := tx.Exec(`DELETE FROM chunks WHERE link_id = ?`, id); err != nil {
		return Link{}, err
	}
	// Content saved for the old URL no longer matches the link.
	if p.URL != nil {
		for _, q := range []string{`DELETE FROM contents WHERE link_id = ?`, `DELETE FROM snapshots WHERE link_id = ?`} {
			if _, err := tx.Exec(q, id); err != nil {
				return Link{}, err
			}
		}
	}
	if p.Tags != nil {
		if _, err := tx.Exec(`DELETE FROM link_tags WHERE link_id = ?`, id); err != nil {
			return Link{}, err
		}
		for _, t := range *p.Tags {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO link_tags (link_id, tag) VALUES (?, ?)`, id, t); err != nil {
				return Link{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Link{}, err
	}
	return s.Get(id)
}

// Get returns the link with the given id.
func (s *Store) Get(id int64) (Link, error) {
	links, err := s.query(`WHERE l.id = ?`, byID, id)
	if err != nil {
		return Link{}, err
	}
	if len(links) == 0 {
		return Link{}, ErrNotFound
	}
	return links[0], nil
}

// ErrNoContent is returned when a link has no fetched content yet.
var ErrNoContent = errors.New("content not fetched yet")

// SaveContent stores the article content and page snapshot for a link,
// replacing any previous version. title is used only if the link has no title yet.
func (s *Store) SaveContent(id int64, title, text, html string, snapshot []byte) error {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write(snapshot); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`UPDATE links SET title = CASE WHEN title = '' THEN ? ELSE title END WHERE id = ?`, title, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO contents (link_id, text, html, fetched_at) VALUES (?, ?, ?, ?)`,
		id, text, html, time.Now().Unix()); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR REPLACE INTO snapshots (link_id, html_gz) VALUES (?, ?)`, id, gz.Bytes()); err != nil {
		return err
	}
	// New content needs a new search index.
	if _, err := tx.Exec(`DELETE FROM chunks WHERE link_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// GetSnapshot returns the saved full-page snapshot of the link with the given id.
func (s *Store) GetSnapshot(id int64) ([]byte, error) {
	var gz []byte
	err := s.db.QueryRow(`SELECT html_gz FROM snapshots WHERE link_id = ?`, id).Scan(&gz)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoContent
	}
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// GetContent returns the fetched content of the link with the given id.
func (s *Store) GetContent(id int64) (Content, error) {
	var (
		c       Content
		fetched int64
	)
	err := s.db.QueryRow(`SELECT text, html, fetched_at FROM contents WHERE link_id = ?`, id).
		Scan(&c.Text, &c.HTML, &fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return Content{}, ErrNoContent
	}
	if err != nil {
		return Content{}, err
	}
	c.FetchedAt = time.Unix(fetched, 0)
	return c, nil
}

// List returns all links ordered by id, with their tags.
func (s *Store) List() ([]Link, error) {
	return s.query("", byID)
}

// Page returns up to limit links, newest first, skipping the first offset,
// and how many links match in total. With tags, only links tagged with any
// of them (case-insensitive) match.
func (s *Store) Page(tags []string, offset, limit int) ([]Link, int, error) {
	where, args := tagFilter(tags)
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM links l `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	links, err := s.query(where, `ORDER BY l.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	return links, total, err
}

// Stats summarizes the saved links.
type Stats struct {
	Links, Tags int
	LastAdded   time.Time // zero when there are no links
}

func (s *Store) Stats() (Stats, error) {
	var (
		st   Stats
		last int64
	)
	err := s.db.QueryRow(`SELECT
		(SELECT COUNT(*) FROM links),
		(SELECT COUNT(DISTINCT lower(tag)) FROM link_tags),
		(SELECT COALESCE(MAX(added_at), 0) FROM links)`).Scan(&st.Links, &st.Tags, &last)
	if last != 0 {
		st.LastAdded = time.Unix(last, 0)
	}
	return st, err
}

// tagFilter returns a WHERE clause on alias l keeping links tagged with any
// of tags, or "" when tags is empty.
func tagFilter(tags []string) (string, []any) {
	if len(tags) == 0 {
		return "", nil
	}
	args := make([]any, len(tags))
	for i, t := range tags {
		args[i] = strings.ToLower(t)
	}
	placeholders := "?" + strings.Repeat(", ?", len(tags)-1)
	return `WHERE l.id IN (SELECT link_id FROM link_tags WHERE lower(tag) IN (` + placeholders + `))`, args
}

// byID is the default order of links: oldest first.
const byID = `ORDER BY l.id`

// query selects links with their tags; where is an optional WHERE clause on
// alias l and tail orders (and may limit) the result.
func (s *Store) query(where, tail string, args ...any) ([]Link, error) {
	rows, err := s.db.Query(`
		SELECT l.id, l.url, l.title, l.added_at, COALESCE(c.fetched_at, 0),
			EXISTS (SELECT 1 FROM snapshots s WHERE s.link_id = l.id),
			COALESCE(GROUP_CONCAT(t.tag, char(31)), '')
		FROM links l
		LEFT JOIN link_tags t ON t.link_id = l.id
		LEFT JOIN contents c ON c.link_id = l.id
		`+where+`
		GROUP BY l.id
		`+tail, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []Link
	for rows.Next() {
		var (
			l              Link
			added, fetched int64
			tags           string
		)
		if err := rows.Scan(&l.ID, &l.URL, &l.Title, &added, &fetched, &l.HasSnapshot, &tags); err != nil {
			return nil, err
		}
		l.AddedAt = time.Unix(added, 0)
		if fetched != 0 {
			l.FetchedAt = time.Unix(fetched, 0)
		}
		if tags != "" {
			l.Tags = strings.Split(tags, "\x1f")
		}
		links = append(links, l)
	}
	return links, rows.Err()
}
