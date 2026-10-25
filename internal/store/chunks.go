package store

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Chunk is an indexed piece of a link with its embedding vector.
type Chunk struct {
	LinkID int64
	Text   string
	Vec    []float32
}

// ReplaceChunks stores the search index of a link built with model,
// replacing its previous index. title goes into the full-text index.
func (s *Store) ReplaceChunks(linkID int64, model, title string, texts []string, vecs [][]float32) error {
	if len(texts) != len(vecs) {
		return fmt.Errorf("replace chunks: %d texts but %d vectors", len(texts), len(vecs))
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// The chunks_fts_delete trigger drops the old full-text rows too.
	if _, err := tx.Exec(`DELETE FROM chunks WHERE link_id = ?`, linkID); err != nil {
		return err
	}
	for i, t := range texts {
		res, err := tx.Exec(`INSERT INTO chunks (link_id, model, text, vec) VALUES (?, ?, ?, ?)`,
			linkID, model, t, encodeVec(vecs[i]))
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO chunks_fts (rowid, title, text) VALUES (?, ?, ?)`, id, title, t); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// KeywordHit is a link found by full-text search.
type KeywordHit struct {
	LinkID int64
	Text   string // best matching chunk
	Meta   bool   // whether that chunk is the link's first one (URL and tags)
}

// KeywordSearch returns up to limit links whose chunks (indexed with model)
// match the FTS5 query, best first by BM25. Title matches weigh more than text.
func (s *Store) KeywordSearch(model, match string, limit int) ([]KeywordHit, error) {
	// bm25() can't be used in an aggregate, so chunks are ranked in a subquery
	// and each link keeps its best chunk (lowest bm25 is best).
	rows, err := s.db.Query(`
		SELECT link_id, text, meta FROM (
			SELECT c.link_id, c.text,
				c.id = (SELECT MIN(id) FROM chunks WHERE link_id = c.link_id AND model = c.model) AS meta,
				ROW_NUMBER() OVER (PARTITION BY c.link_id ORDER BY f.rank) AS n,
				MIN(f.rank) OVER (PARTITION BY c.link_id) AS best
			FROM (SELECT rowid, bm25(chunks_fts, 5.0, 1.0) AS rank FROM chunks_fts WHERE chunks_fts MATCH ?) f
			JOIN chunks c ON c.id = f.rowid
			WHERE c.model = ?
		)
		WHERE n = 1
		ORDER BY best
		LIMIT ?`, match, model, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hits []KeywordHit
	for rows.Next() {
		var h KeywordHit
		if err := rows.Scan(&h.LinkID, &h.Text, &h.Meta); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// KeywordLinks returns the ids of links with any chunk (indexed with model)
// matching the FTS5 query.
func (s *Store) KeywordLinks(model, match string) ([]int64, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT c.link_id
		FROM chunks_fts f JOIN chunks c ON c.id = f.rowid
		WHERE chunks_fts MATCH ? AND c.model = ?`, match, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Chunks returns every chunk indexed with model.
func (s *Store) Chunks(model string) ([]Chunk, error) {
	rows, err := s.db.Query(`SELECT link_id, text, vec FROM chunks WHERE model = ?`, model)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chunks []Chunk
	for rows.Next() {
		var (
			c   Chunk
			raw []byte
		)
		if err := rows.Scan(&c.LinkID, &c.Text, &raw); err != nil {
			return nil, err
		}
		c.Vec = decodeVec(raw)
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// Unindexed returns links that have no search index built with model.
func (s *Store) Unindexed(model string) ([]Link, error) {
	return s.query(`WHERE NOT EXISTS (SELECT 1 FROM chunks ch WHERE ch.link_id = l.id AND ch.model = ?)`, byID, model)
}

func encodeVec(v []float32) []byte {
	b := make([]byte, 0, 4*len(v))
	for _, x := range v {
		b = binary.LittleEndian.AppendUint32(b, math.Float32bits(x))
	}
	return b
}

func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}
