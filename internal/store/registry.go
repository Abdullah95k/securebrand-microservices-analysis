package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/entities"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/textnorm"
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
	arows, err := s.DB.Query(ctx, `SELECT a.alias, a.entity_id, a.ambiguous FROM analysis.entity_aliases a
		JOIN analysis.entities e ON e.entity_id = a.entity_id AND e.status = 'active'`)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	var aliases []entities.Alias
	for arows.Next() {
		var a entities.Alias
		if err := arows.Scan(&a.Alias, &a.EntityID, &a.Ambiguous); err != nil {
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
			generic := map[string]bool{}
			for _, a := range e.Ambiguous {
				generic[textnorm.Normalize(a)] = true
			}
			names := append(append([]string{e.Name, e.NameAR}, e.Aliases...), e.Ambiguous...)
			for _, a := range names {
				norm := textnorm.Normalize(a)
				if norm == "" {
					continue
				}
				if _, err := tx.Exec(ctx, `INSERT INTO analysis.entity_aliases (alias_norm, entity_id, alias, source, ambiguous)
					VALUES ($1, $2, $3, $4, $5) ON CONFLICT (alias_norm, entity_id) DO UPDATE SET ambiguous = EXCLUDED.ambiguous`,
					norm, e.ID, a, source, generic[norm]); err != nil {
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
	// Examples are up to five content ids that named it.
	Examples []string
}

// PendingCandidates lists candidates seen at least minSeen times.
func (s *Store) PendingCandidates(ctx context.Context, minSeen, limit int) ([]CandidateRow, error) {
	rows, err := s.DB.Query(ctx, `SELECT norm, type, surface, seen, first_seen, last_seen, examples FROM analysis.entity_candidates
		WHERE status = 'pending' AND seen >= $1 ORDER BY seen DESC LIMIT $2`, minSeen, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CandidateRow
	for rows.Next() {
		var c CandidateRow
		if err := rows.Scan(&c.Norm, &c.Type, &c.Surface, &c.Seen, &c.FirstSeen, &c.LastSeen, &c.Examples); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ResolveCandidate accepts a candidate as a new entity (entityID new),
// merges it into an existing one as an alias, or rejects it (the owner's
// command).
func (s *Store) ResolveCandidate(ctx context.Context, norm, typ, action, entityID, name string) error {
	d := EntityDecision{Action: action, ID: entityID, Into: entityID, Name: name, Type: typ, PublicFigure: typ == "person_public", By: "owner"}
	if action == "accept" {
		d.Action = "approve"
	}
	return s.DecideCandidate(ctx, norm, typ, d)
}

// EntityDecision resolves a candidate: approve (a new entity; the id gets a
// suffix if taken), merge (into an existing entity, as an alias), or
// reject. By records who decided (the curation model or the owner).
type EntityDecision struct {
	Action, Into, ID, Name, NameAR, Type string
	PublicFigure                         bool
	By                                   string
}

// DecideCandidate applies a decision in one transaction. Earlier unlinked
// mentions of the name are linked to the entity (and their items get its
// "@" topic), their rollup days rebuilt.
func (s *Store) DecideCandidate(ctx context.Context, norm, typ string, d EntityDecision) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var surface, status string
		if err := tx.QueryRow(ctx, `SELECT surface, status FROM analysis.entity_candidates WHERE norm = $1 AND type = $2 FOR UPDATE`,
			norm, typ).Scan(&surface, &status); err != nil {
			return fmt.Errorf("candidate %q: %w", norm, err)
		}
		// Decided once: a second decision (the curator and a person at
		// the same time, or a repeated run) would duplicate the entity. A
		// person may still revisit a rejection.
		if status != "pending" && (d.By != "owner" || status != "rejected") {
			return fmt.Errorf("candidate %q already %s", norm, status)
		}
		entityID := ""
		switch d.Action {
		case "approve":
			name := orDefault(d.Name, surface)
			etype := orDefault(d.Type, typ)
			if etype == "person_private" {
				return errors.New("a private person is never stored")
			}
			id, err := freeEntityID(ctx, tx, d.ID)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO analysis.entities (entity_id, name, name_ar, type, public_figure) VALUES ($1, $2, NULLIF($3,''), $4, $5)`,
				id, name, d.NameAR, etype, d.PublicFigure || etype == "person_public"); err != nil {
				return err
			}
			for _, a := range []string{name, d.NameAR} {
				if n := textnorm.Normalize(a); n != "" {
					if _, err := tx.Exec(ctx, `INSERT INTO analysis.entity_aliases (alias_norm, entity_id, alias, source)
						VALUES ($1, $2, $3, 'discovered') ON CONFLICT DO NOTHING`, n, id, a); err != nil {
						return err
					}
				}
			}
			entityID = id
		case "merge":
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM analysis.entities WHERE entity_id = $1 AND status = 'active')`,
				d.Into).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("merge into %q: no such active entity", d.Into)
			}
			entityID = d.Into
		case "reject":
		default:
			return fmt.Errorf("unknown action %q (accept/approve, merge, reject)", d.Action)
		}
		if entityID != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO analysis.entity_aliases (alias_norm, entity_id, alias, source)
				VALUES ($1, $2, $3, 'discovered') ON CONFLICT DO NOTHING`, norm, entityID, surface); err != nil {
				return err
			}
			// Earlier unlinked mentions of the name now point at the entity,
			// and their items are about it ("@<entity>" topic).
			if _, err := tx.Exec(ctx, `UPDATE analysis.entity_mentions SET entity_id = $1 WHERE entity_id IS NULL
				AND analysis.ar_normalize(lower(name)) = analysis.ar_normalize(lower($2))`, entityID, surface); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE analysis.analyses a SET topics = analysis.topics_replace(coalesce(a.topics, '{}') || ('@' || $1), NULL, NULL)
				FROM (SELECT DISTINCT created_at, content_id FROM analysis.entity_mentions WHERE entity_id = $1 AND content_id NOT LIKE 'media:%') m
				WHERE a.created_at = m.created_at AND a.content_id = m.content_id AND a.is_current
				  AND NOT coalesce(a.topics, '{}') @> ARRAY['@' || $1]`, entityID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO analysis.rollup_dirty (day)
				SELECT DISTINCT day FROM analysis.entity_mentions WHERE entity_id = $1 ON CONFLICT (day) DO UPDATE SET marked_at = now()`,
				entityID); err != nil {
				return err
			}
		}
		st := map[string]string{"approve": "accepted", "merge": "merged", "reject": "rejected"}[d.Action]
		_, err := tx.Exec(ctx, `UPDATE analysis.entity_candidates SET status = $3, resolved_entity_id = NULLIF($4,''),
			resolved_by = NULLIF($5,''), resolved_at = now() WHERE norm = $1 AND type = $2`, norm, typ, st, entityID, d.By)
		return err
	})
}

// freeEntityID returns id, or id with a numeric suffix when taken.
func freeEntityID(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	if id == "" {
		return "", errors.New("approve needs an entity id")
	}
	cand := id
	for n := 2; n < 100; n++ {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM analysis.entities WHERE entity_id = $1)`, cand).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return cand, nil
		}
		cand = fmt.Sprintf("%s-%d", id, n)
	}
	return "", fmt.Errorf("no free entity id for %s", id)
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
			{`UPDATE analysis.analyses SET topics = analysis.topics_replace(topics, '@' || $1, '@' || $2) WHERE topics @> ARRAY['@' || $1]`, two},
			{`UPDATE analysis.entity_mentions SET topics = analysis.topics_replace(topics, '@' || $1, '@' || $2) WHERE topics @> ARRAY['@' || $1]`, two},
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
