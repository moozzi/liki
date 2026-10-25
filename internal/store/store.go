// Package store persists links in a local JSON file.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Link struct {
	ID      int       `json:"id"`
	URL     string    `json:"url"`
	Title   string    `json:"title,omitempty"`
	Tags    []string  `json:"tags,omitempty"`
	AddedAt time.Time `json:"added_at"`
}

type Store struct {
	path  string
	Links []Link `json:"links"`
}

// DefaultPath returns $LIKI_DB or ~/.liki/links.json.
func DefaultPath() (string, error) {
	if p := os.Getenv("LIKI_DB"); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".liki", "links.json"), nil
}

// Open loads the store from path; a missing file yields an empty store.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

func (s *Store) Add(url, title string, tags []string) Link {
	l := Link{
		ID:      s.nextID(),
		URL:     url,
		Title:   title,
		Tags:    tags,
		AddedAt: time.Now(),
	}
	s.Links = append(s.Links, l)
	return l
}

// Save writes the store atomically (temp file + rename).
func (s *Store) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) nextID() int {
	max := 0
	for _, l := range s.Links {
		if l.ID > max {
			max = l.ID
		}
	}
	return max + 1
}
