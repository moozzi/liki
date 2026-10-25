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
// replacing its previous index.
func (s *Store) ReplaceChunks(linkID int64, model string, texts []string, vecs [][]float32) error {
	if len(texts) != len(vecs) {
		return fmt.Errorf("replace chunks: %d texts but %d vectors", len(texts), len(vecs))
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM chunks WHERE link_id = ?`, linkID); err != nil {
		return err
	}
	for i, t := range texts {
		if _, err := tx.Exec(`INSERT INTO chunks (link_id, model, text, vec) VALUES (?, ?, ?, ?)`,
			linkID, model, t, encodeVec(vecs[i])); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	return s.query(`WHERE NOT EXISTS (SELECT 1 FROM chunks ch WHERE ch.link_id = l.id AND ch.model = ?)`, model)
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
