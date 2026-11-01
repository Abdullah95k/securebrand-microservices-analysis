package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
)

// AgentLimits are the server-side limits of an agent role.
type AgentLimits struct {
	StatementTimeout time.Duration
	IdleInTx         time.Duration
	WorkMem          string // e.g. "32MB"
	TempFileLimit    string // e.g. "1GB"
	ConnLimit        int
}

// DefaultAgentLimits are the limits of docs/design/analysis-service.md 12.3.
var DefaultAgentLimits = AgentLimits{StatementTimeout: 15 * time.Second, IdleInTx: 30 * time.Second,
	WorkMem: "32MB", TempFileLimit: "1GB", ConnLimit: 5}

var roleName = regexp.MustCompile(`^[a-z_][a-z0-9_]{2,62}$`)
var sizeSetting = regexp.MustCompile(`^[0-9]+(kB|MB|GB)$`)

// CreateAgentRole creates (or updates) a client's read-only login role for
// the analytics agent: SELECT on the agent views only, read-only
// transactions, statement and idle timeouts, memory and temp-file caps,
// and a connection limit. The views show the client the mentions of the
// entities it tracks and everything from the sources it monitors
// (analysis.client_entities / client_sources), filtered on current_user.
// The password comes from the caller (read from a file), never logged.
func (s *Store) CreateAgentRole(ctx context.Context, clientID, clientName, role, password string, l AgentLimits) error {
	if !roleName.MatchString(role) {
		return fmt.Errorf("role %q: use 3-63 lower-case letters, digits and _", role)
	}
	if len(password) < 16 {
		return fmt.Errorf("password must be at least 16 characters")
	}
	if !sizeSetting.MatchString(l.WorkMem) || !sizeSetting.MatchString(l.TempFileLimit) {
		return fmt.Errorf("work_mem and temp_file_limit must look like 32MB")
	}
	ident := pgx.Identifier{role}.Sanitize()
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO analysis.clients (client_id, name) VALUES ($1, $2)
			ON CONFLICT (client_id) DO UPDATE SET name = EXCLUDED.name`, clientID, clientName); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
			return err
		}
		// Role DDL takes no bind parameters: the password is quoted with
		// quote_literal on the server.
		var lit string
		if err := tx.QueryRow(ctx, `SELECT quote_literal($1)`, password).Scan(&lit); err != nil {
			return err
		}
		verb := "CREATE"
		if exists {
			verb = "ALTER"
		}
		stmts := []string{
			fmt.Sprintf("%s ROLE %s LOGIN NOINHERIT NOCREATEDB NOCREATEROLE CONNECTION LIMIT %d PASSWORD %s", verb, ident, l.ConnLimit, lit),
			fmt.Sprintf("ALTER ROLE %s SET default_transaction_read_only = on", ident),
			fmt.Sprintf("ALTER ROLE %s SET statement_timeout = '%dms'", ident, l.StatementTimeout.Milliseconds()),
			fmt.Sprintf("ALTER ROLE %s SET idle_in_transaction_session_timeout = '%dms'", ident, l.IdleInTx.Milliseconds()),
			fmt.Sprintf("ALTER ROLE %s SET work_mem = '%s'", ident, l.WorkMem),
			fmt.Sprintf("ALTER ROLE %s SET temp_file_limit = '%s'", ident, l.TempFileLimit),
			fmt.Sprintf("GRANT USAGE ON SCHEMA agent TO %s", ident),
			fmt.Sprintf("GRANT SELECT ON ALL TABLES IN SCHEMA agent TO %s", ident),
			// The views call analysis.mask_pii and analysis.ar_normalize; the
			// tables of schema analysis stay without grants.
			fmt.Sprintf("GRANT USAGE ON SCHEMA analysis TO %s", ident),
		}
		for _, q := range stmts {
			if _, err := tx.Exec(ctx, q); err != nil {
				return fmt.Errorf("%s: %w", strings.SplitN(q, " PASSWORD", 2)[0], err)
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO analysis.client_roles (role_name, client_id) VALUES ($1, $2)
			ON CONFLICT (role_name) DO UPDATE SET client_id = EXCLUDED.client_id`, role, clientID)
		return err
	})
}

// TrackEntity makes a client track an entity (own, competitor, watch).
func (s *Store) TrackEntity(ctx context.Context, clientID, entityID, role string) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO analysis.client_entities (client_id, entity_id, role) VALUES ($1, $2, $3)
		ON CONFLICT (client_id, entity_id) DO UPDATE SET role = EXCLUDED.role`, clientID, entityID, role)
	return err
}

// MonitorSource gives a client every item of a source.
func (s *Store) MonitorSource(ctx context.Context, clientID, sourceID string) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO analysis.client_sources (client_id, source_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		clientID, sourceID)
	return err
}

// ReanalyzeFilter selects items to analyse again.
type ReanalyzeFilter struct {
	Since, Until time.Time
	Platform     string
	SourceID     string
	EntityID     string // items mentioning it
	Model        string // items whose current analysis used it
	TaxonomyNot  string // items analysed with another taxonomy version
	Sample       float64
	Limit        int
}

// SelectForReanalysis returns the items matching f (current analyses).
func (s *Store) SelectForReanalysis(ctx context.Context, f ReanalyzeFilter) ([]core.Item, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	where = append(where, "a.is_current")
	if !f.Since.IsZero() {
		add("a.created_at >= ?", f.Since)
	}
	if !f.Until.IsZero() {
		add("a.created_at < ?", f.Until)
	}
	if f.Platform != "" {
		add("a.platform = ?", f.Platform)
	}
	if f.SourceID != "" {
		add("a.source_id = ?", f.SourceID)
	}
	if f.Model != "" {
		add("a.model = ?", f.Model)
	}
	if f.TaxonomyNot != "" {
		add("a.taxonomy_version <> ?", f.TaxonomyNot)
	}
	if f.EntityID != "" {
		add("EXISTS (SELECT 1 FROM analysis.entity_mentions m WHERE m.created_at = a.created_at AND m.content_id = a.content_id AND m.entity_id = ?)", f.EntityID)
	}
	if f.Sample > 0 && f.Sample < 1 {
		add("abs(hashtext(a.content_id)) % 10000 < ?", int(f.Sample*10000))
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100000
	}
	args = append(args, limit)
	q := `SELECT a.content_id, a.created_at, a.kind, coalesce(c.thread_id, '') FROM analysis.analyses a
		JOIN analysis.content_items c ON c.created_at = a.created_at AND c.content_id = a.content_id AND c.status <> 'redacted'
		WHERE ` + strings.Join(where, " AND ") + fmt.Sprintf(" ORDER BY a.created_at DESC LIMIT $%d", len(args))
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Item
	for rows.Next() {
		var it core.Item
		if err := rows.Scan(&it.ContentID, &it.CreatedAt, &it.Kind, &it.ThreadID); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// RecordModelChange writes a series break when a route's model, prompt or
// taxonomy differs from the last recorded one.
func (s *Store) RecordModelChange(ctx context.Context, route, model, prompt, taxonomy string) (bool, error) {
	var lastModel, lastPrompt, lastTax string
	err := s.DB.QueryRow(ctx, `SELECT to_model, to_prompt, taxonomy FROM analysis.model_changes WHERE route = $1
		ORDER BY id DESC LIMIT 1`, route).Scan(&lastModel, &lastPrompt, &lastTax)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if err == nil && lastModel == model && lastPrompt == prompt && lastTax == taxonomy {
		return false, nil
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO analysis.model_changes (route, from_model, to_model, from_prompt, to_prompt, taxonomy)
		VALUES ($1, NULLIF($2,''), $3, NULLIF($4,''), $5, $6)`, route, lastModel, model, lastPrompt, prompt, taxonomy)
	return err == nil, err
}

// ReplayFailed moves dead letters back to the queue.
func (s *Store) ReplayFailed(ctx context.Context, route string, limit int) (int64, error) {
	tag, err := s.DB.Exec(ctx, `
		WITH d AS (DELETE FROM analysis.dead_letters WHERE ($1 = '' OR route = $1)
		           AND content_id IN (SELECT content_id FROM analysis.dead_letters WHERE ($1 = '' OR route = $1) LIMIT $2)
		           RETURNING content_id, route)
		INSERT INTO analysis.analysis_queue (content_id, route, created_at, thread_id, lane, priority, reason)
		SELECT d.content_id, d.route, ci.created_at, c.thread_id, 'batch', 2, 'replay'
		FROM d JOIN analysis.content_index ci ON ci.content_id = d.content_id
		JOIN analysis.content_items c ON c.created_at = ci.created_at AND c.content_id = ci.content_id
		ON CONFLICT (content_id, route) DO NOTHING`, route, limit)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
