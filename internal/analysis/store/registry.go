package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/entities"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/textnorm"
)

// LoadRegistry reads the entity registry into an immutable snapshot.
func (s *Store) LoadRegistry(ctx context.Context) (*entities.Registry, error) {
	rows, err := s.DB.Query(ctx, `SELECT e.entity_id, e.name, coalesce(e.name_ar, ''), e.type, e.public_figure, e.status,
		coalesce(e.merged_into, ''), EXISTS (SELECT 1 FROM analysis.client_entities ce WHERE ce.entity_id = e.entity_id)
		FROM analysis.entities e WHERE e.status IN ('active', 'merged')`)
	if err != nil {
		return nil, err
	}
	var ents []entities.Entity
	merged := map[string]string{}
	for rows.Next() {
		var e entities.Entity
		var status, into string
		if err := rows.Scan(&e.ID, &e.Name, &e.NameAR, &e.Type, &e.PublicFigure, &status, &into, &e.Tracked); err != nil {
			rows.Close()
			return nil, err
		}
		if status == "merged" {
			merged[e.ID] = into
			continue
		}
		ents = append(ents, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	arows, err := s.DB.Query(ctx, `SELECT a.alias, a.entity_id FROM analysis.entity_aliases a
		JOIN analysis.entities e ON e.entity_id = a.entity_id AND e.status = 'active'`)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	var aliases []entities.Alias
	for arows.Next() {
		var a entities.Alias
		if err := arows.Scan(&a.Alias, &a.EntityID); err != nil {
			return nil, err
		}
		aliases = append(aliases, a)
	}
	if err := arows.Err(); err != nil {
		return nil, err
	}
	return entities.New(ents, aliases, merged), nil
}

// ImportSeed upserts entities and their aliases (source: seed or owner).
// Existing entities keep their status; names and types are updated.
func (s *Store) ImportSeed(ctx context.Context, seed *entities.Seed, source string) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		for _, e := range seed.Entities {
			if _, err := tx.Exec(ctx, `INSERT INTO analysis.entities (entity_id, name, name_ar, type, public_figure)
				VALUES ($1, $2, NULLIF($3,''), $4, $5)
				ON CONFLICT (entity_id) DO UPDATE SET name = EXCLUDED.name, name_ar = EXCLUDED.name_ar, type = EXCLUDED.type,
					public_figure = EXCLUDED.public_figure, updated_at = now()`,
				e.ID, e.Name, e.NameAR, e.Type, e.PublicFigure); err != nil {
				return fmt.Errorf("entity %s: %w", e.ID, err)
			}
			for _, a := range append([]string{e.Name, e.NameAR}, e.Aliases...) {
				norm := textnorm.Normalize(a)
				if norm == "" {
					continue
				}
				if _, err := tx.Exec(ctx, `INSERT INTO analysis.entity_aliases (alias_norm, entity_id, alias, source)
					VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, norm, e.ID, a, source); err != nil {
					return err
				}
			}
			n++
		}
		return nil
	})
	return n, err
}

// SeedIfEmpty imports the seed when the registry has no entity yet.
func (s *Store) SeedIfEmpty(ctx context.Context, seed *entities.Seed) (bool, error) {
	var exists bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM analysis.entities)`).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	_, err := s.ImportSeed(ctx, seed, "seed")
	return err == nil, err
}

// Candidate is a name the model reported that the registry does not know.
type Candidate struct {
	Surface   string
	Type      string
	ContentID string
}

// AddCandidates counts unknown names for the review queue (up to five
// example content ids each). Private persons never reach this point.
func (s *Store) AddCandidates(ctx context.Context, cs []Candidate) error {
	if len(cs) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, c := range cs {
		norm := textnorm.Normalize(c.Surface)
		if norm == "" || c.Type == "person_private" {
			continue
		}
		b.Queue(`INSERT INTO analysis.entity_candidates (norm, type, surface, examples) VALUES ($1, $2, $3, ARRAY[$4])
			ON CONFLICT (norm, type) DO UPDATE SET seen = analysis.entity_candidates.seen + 1, last_seen = now(),
				examples = CASE WHEN cardinality(analysis.entity_candidates.examples) < 5
					AND NOT ($4 = ANY(analysis.entity_candidates.examples))
					THEN analysis.entity_candidates.examples || $4 ELSE analysis.entity_candidates.examples END`,
			norm, c.Type, c.Surface, c.ContentID)
	}
	return s.DB.SendBatch(ctx, b).Close()
}

// CandidateRow is a pending review item.
type CandidateRow struct {
	Norm, Type, Surface string
	Seen                int
	FirstSeen, LastSeen time.Time
}

// PendingCandidates lists candidates seen at least minSeen times.
func (s *Store) PendingCandidates(ctx context.Context, minSeen, limit int) ([]CandidateRow, error) {
	rows, err := s.DB.Query(ctx, `SELECT norm, type, surface, seen, first_seen, last_seen FROM analysis.entity_candidates
		WHERE status = 'pending' AND seen >= $1 ORDER BY seen DESC LIMIT $2`, minSeen, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CandidateRow
	for rows.Next() {
		var c CandidateRow
		if err := rows.Scan(&c.Norm, &c.Type, &c.Surface, &c.Seen, &c.FirstSeen, &c.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ResolveCandidate accepts a candidate as a new entity (entityID new),
// merges it into an existing one as an alias, or rejects it.
func (s *Store) ResolveCandidate(ctx context.Context, norm, typ, action, entityID, name string) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var surface string
		if err := tx.QueryRow(ctx, `SELECT surface FROM analysis.entity_candidates WHERE norm = $1 AND type = $2 FOR UPDATE`,
			norm, typ).Scan(&surface); err != nil {
			return fmt.Errorf("candidate %q: %w", norm, err)
		}
		switch action {
		case "accept":
			if name == "" {
				name = surface
			}
			if _, err := tx.Exec(ctx, `INSERT INTO analysis.entities (entity_id, name, type, public_figure) VALUES ($1, $2, $3, $4)`,
				entityID, name, typ, typ == "person_public"); err != nil {
				return err
			}
			fallthrough
		case "merge":
			if _, err := tx.Exec(ctx, `INSERT INTO analysis.entity_aliases (alias_norm, entity_id, alias, source)
				VALUES ($1, $2, $3, 'discovered') ON CONFLICT DO NOTHING`, norm, entityID, surface); err != nil {
				return err
			}
			// Earlier unlinked mentions of the name now point at the entity.
			if _, err := tx.Exec(ctx, `UPDATE analysis.entity_mentions SET entity_id = $1 WHERE entity_id IS NULL
				AND analysis.ar_normalize(lower(name)) = analysis.ar_normalize(lower($2))`, entityID, surface); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO analysis.rollup_dirty (day)
				SELECT DISTINCT day FROM analysis.entity_mentions WHERE entity_id = $1 ON CONFLICT (day) DO UPDATE SET marked_at = now()`,
				entityID); err != nil {
				return err
			}
		case "reject":
		default:
			return fmt.Errorf("unknown action %q (accept, merge, reject)", action)
		}
		status := map[string]string{"accept": "accepted", "merge": "merged", "reject": "rejected"}[action]
		_, err := tx.Exec(ctx, `UPDATE analysis.entity_candidates SET status = $3, resolved_entity_id = NULLIF($4,'')
			WHERE norm = $1 AND type = $2`, norm, typ, status, entityID)
		return err
	})
}

// MergeEntity merges entity from into into: mentions re-pointed, aliases
// moved, client tracking moved, affected rollup days marked dirty.
func (s *Store) MergeEntity(ctx context.Context, from, into string) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		one, two := []any{from}, []any{from, into}
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO analysis.rollup_dirty (day) SELECT DISTINCT day FROM analysis.entity_mentions WHERE entity_id = $1
			  ON CONFLICT (day) DO UPDATE SET marked_at = now()`, one},
			{`UPDATE analysis.entity_mentions SET entity_id = $2 WHERE entity_id = $1`, two},
			{`INSERT INTO analysis.entity_aliases (alias_norm, entity_id, alias, source)
			  SELECT alias_norm, $2, alias, source FROM analysis.entity_aliases WHERE entity_id = $1 ON CONFLICT DO NOTHING`, two},
			{`DELETE FROM analysis.entity_aliases WHERE entity_id = $1`, one},
			{`INSERT INTO analysis.client_entities (client_id, entity_id, role)
			  SELECT client_id, $2, role FROM analysis.client_entities WHERE entity_id = $1 ON CONFLICT DO NOTHING`, two},
			{`DELETE FROM analysis.client_entities WHERE entity_id = $1`, one},
			{`UPDATE analysis.entities SET status = 'merged', merged_into = $2, updated_at = now() WHERE entity_id = $1`, two},
		} {
			if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	})
}
