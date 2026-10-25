// Package store persists links in a local SQLite database.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Link struct {
	ID      int64
	URL     string
	Title   string
	Tags    []string
	AddedAt time.Time
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
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
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
	links, err := s.query(`WHERE l.id = ?`, id)
	if err != nil {
		return Link{}, err
	}
	if len(links) == 0 {
		return Link{}, ErrNotFound
	}
	return links[0], nil
}

// List returns all links ordered by id, with their tags.
func (s *Store) List() ([]Link, error) {
	return s.query("")
}

// query selects links with their tags; where is an optional WHERE clause on alias l.
func (s *Store) query(where string, args ...any) ([]Link, error) {
	rows, err := s.db.Query(`
		SELECT l.id, l.url, l.title, l.added_at, COALESCE(GROUP_CONCAT(t.tag, char(31)), '')
		FROM links l
		LEFT JOIN link_tags t ON t.link_id = l.id
		`+where+`
		GROUP BY l.id
		ORDER BY l.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var links []Link
	for rows.Next() {
		var (
			l     Link
			added int64
			tags  string
		)
		if err := rows.Scan(&l.ID, &l.URL, &l.Title, &added, &tags); err != nil {
			return nil, err
		}
		l.AddedAt = time.Unix(added, 0)
		if tags != "" {
			l.Tags = strings.Split(tags, "\x1f")
		}
		links = append(links, l)
	}
	return links, rows.Err()
}
