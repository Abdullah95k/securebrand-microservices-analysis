package store

import (
	"context"
	"crypto/sha1" // #nosec G505 -- a short id suffix, not security
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	analysisv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/analysis/v1"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/taxonomy"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/textnorm"
	"github.com/abdullah95k/securebrand-microservices/internal/topics"
)

// Topic statuses.
const (
	TopicActive    = "active"
	TopicCandidate = "candidate"
	TopicMerged    = "merged"
	TopicRejected  = "rejected"
)

// lockTopics serialises changes of the topic list across replicas.
const lockTopics = 4200103

// ImportTaxonomy adds a taxonomy file's topics to the topic list as active
// topics (names of existing ones updated) and records a taxonomy version.
// Topics missing from the file are kept: retire them with a merge.
func (s *Store) ImportTaxonomy(ctx context.Context, tax *taxonomy.Taxonomy, source string) (string, error) {
	var version string
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockTopics); err != nil {
			return err
		}
		ord := 0
		add := func(t taxonomy.Topic, parent string) error {
			ord++
			if _, err := tx.Exec(ctx, `INSERT INTO analysis.topics (topic_id, parent_id, name_en, name_ar, status, source, ord, approved_at)
				VALUES ($1, NULLIF($2,''), $3, NULLIF($4,''), 'active', $5, $6, now())
				ON CONFLICT (topic_id) DO UPDATE SET name_en = EXCLUDED.name_en, name_ar = EXCLUDED.name_ar, status = 'active',
					merged_into = NULL, ord = EXCLUDED.ord, updated_at = now()`, t.ID, parent, t.EN, t.AR, source, ord); err != nil {
				return err
			}
			return addAliases(ctx, tx, t.ID, t.EN, t.AR)
		}
		for _, top := range tax.Topics {
			if err := add(top, ""); err != nil {
				return err
			}
			for _, c := range top.Children {
				if err := add(c, top.ID); err != nil {
					return err
				}
			}
		}
		// Topics the model added keep their place after the file's.
		if _, err := tx.Exec(ctx, `UPDATE analysis.topics SET ord = $1 + ord WHERE source = 'model'`, ord); err != nil {
			return err
		}
		var err error
		version, err = newVersion(ctx, tx, tax.Version, "import "+source)
		return err
	})
	return version, err
}

// SeedTopicsIfEmpty imports the taxonomy file when the topic list is empty.
func (s *Store) SeedTopicsIfEmpty(ctx context.Context, tax *taxonomy.Taxonomy) (bool, error) {
	var n int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.topics`).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	_, err := s.ImportTaxonomy(ctx, tax, "seed")
	return err == nil, err
}

// newVersion records a new taxonomy version: the file's version for the
// first, then "<first>.<n>". base is used only for the first version or an
// import of a file with a new version string.
func newVersion(ctx context.Context, tx pgx.Tx, base, note string) (string, error) {
	var firstPtr *string
	var seq int
	if err := tx.QueryRow(ctx, `SELECT (SELECT version FROM analysis.taxonomy_versions ORDER BY seq LIMIT 1), coalesce(max(seq), 0)
		FROM analysis.taxonomy_versions`).Scan(&firstPtr, &seq); err != nil {
		return "", err
	}
	first := ""
	if firstPtr != nil {
		first = *firstPtr
	}
	version := base
	switch {
	case seq == 0 && base == "":
		version = "tax-v1"
	case seq > 0 && (base == "" || base == first || strings.HasPrefix(base, first+".")):
		version = first + "." + strconv.Itoa(seq)
	}
	var taken bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM analysis.taxonomy_versions WHERE version = $1)`, version).Scan(&taken); err != nil {
		return "", err
	}
	if taken {
		version = first + "." + strconv.Itoa(seq)
	}
	var active int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM analysis.topics WHERE status = 'active'`).Scan(&active); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO analysis.taxonomy_versions (version, seq, active_topics, note) VALUES ($1, $2, $3, $4)`,
		version, seq+1, active, note); err != nil {
		return "", err
	}
	return version, nil
}

// addAliases records a topic's names as duplicate keys; a key already
// taken by another topic stays with it.
func addAliases(ctx context.Context, tx pgx.Tx, id string, names ...string) error {
	for _, n := range names {
		k := textnorm.TopicKey(n)
		if k == "" {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO analysis.topic_aliases (alias_key, topic_id, alias) VALUES ($1, $2, $3)
			ON CONFLICT (alias_key) DO NOTHING`, k, id, n); err != nil {
			return err
		}
	}
	return nil
}

// LoadTaxonomy builds the active topic list of the latest version.
func (s *Store) LoadTaxonomy(ctx context.Context) (*taxonomy.Taxonomy, error) {
	var version string
	if err := s.DB.QueryRow(ctx, `SELECT version FROM analysis.taxonomy_versions ORDER BY seq DESC LIMIT 1`).Scan(&version); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("topics: the topic list is empty (seed it first)")
		}
		return nil, err
	}
	rows, err := s.DB.Query(ctx, `SELECT topic_id, coalesce(parent_id, ''), name_en, coalesce(name_ar, '')
		FROM analysis.topics WHERE status = 'active' ORDER BY ord, topic_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type row struct {
		t      taxonomy.Topic
		parent string
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.t.ID, &r.parent, &r.t.EN, &r.t.AR); err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Build the tree (areas, topics, sub-topics) in display order.
	kids := map[string][]taxonomy.Topic{}
	for _, r := range all {
		kids[r.parent] = append(kids[r.parent], r.t)
	}
	var build func(parent string, depth int) []taxonomy.Topic
	build = func(parent string, depth int) []taxonomy.Topic {
		if depth > taxonomy.MaxDepth {
			return nil
		}
		ts := kids[parent]
		for i := range ts {
			ts[i].Children = build(ts[i].ID, depth+1)
		}
		return ts
	}
	tops := build("", 1)
	return taxonomy.New(version, tops)
}

// ResolveTopics turns the model's proposals of missing topics into topic
// ids, one list per item: a name of a registry entity becomes "@<entity>";
// a name whose duplicate key (textnorm.TopicKey, English or Arabic) is
// known becomes that topic (following merges; a rejected topic is
// dropped); anything else becomes a new candidate topic under the proposed
// parent. Candidates count the items that use them.
func (s *Store) ResolveTopics(ctx context.Context, props [][]core.TopicProposal, entityOf func(string) string) ([][]string, error) {
	out := make([][]string, len(props))
	some := false
	for _, p := range props {
		some = some || len(p) > 0
	}
	if !some {
		return out, nil
	}
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockTopics); err != nil {
			return err
		}
		for i, list := range props {
			seen := map[string]bool{}
			for _, p := range list {
				id, err := resolveOne(ctx, tx, p, entityOf)
				if err != nil {
					return err
				}
				if id == "" || seen[id] {
					continue
				}
				seen[id] = true
				out[i] = append(out[i], id)
				if !strings.HasPrefix(id, "@") {
					if _, err := tx.Exec(ctx, `UPDATE analysis.topics SET seen = seen + 1, last_seen = now()
						WHERE topic_id = $1 AND status = 'candidate'`, id); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	return out, err
}

func resolveOne(ctx context.Context, tx pgx.Tx, p core.TopicProposal, entityOf func(string) string) (string, error) {
	if entityOf != nil {
		for _, n := range []string{p.EN, p.AR} {
			if n == "" {
				continue
			}
			if e := entityOf(n); e != "" {
				return "@" + e, nil
			}
		}
	}
	var keys []string
	for _, n := range []string{p.EN, p.AR} {
		if k := textnorm.TopicKey(n); k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return "", nil
	}
	var id, status string
	err := tx.QueryRow(ctx, `SELECT t.topic_id, t.status FROM analysis.topic_aliases a JOIN analysis.topics t ON t.topic_id = a.topic_id
		WHERE a.alias_key = ANY($1) ORDER BY array_position($1, a.alias_key) LIMIT 1`, keys).Scan(&id, &status)
	switch {
	case err == nil:
		return follow(ctx, tx, id, status)
	case !errors.Is(err, pgx.ErrNoRows):
		return "", err
	}
	parent := p.Parent
	if parent == "" {
		parent = "other"
	}
	id, err = freeTopicID(ctx, tx, parent+"."+slug(p.EN, p.AR))
	if err != nil {
		return "", err
	}
	name := p.EN
	if name == "" {
		name = p.AR
	}
	if _, err := tx.Exec(ctx, `INSERT INTO analysis.topics (topic_id, parent_id, name_en, name_ar, status, source, ord)
		VALUES ($1, $2, $3, NULLIF($4,''), 'candidate', 'model', (SELECT coalesce(max(ord), 0) + 1 FROM analysis.topics))`,
		id, parent, name, p.AR); err != nil {
		return "", err
	}
	return id, addAliases(ctx, tx, id, p.EN, p.AR)
}

// follow resolves a merged topic to the one it was merged into; "" for a
// rejected topic.
func follow(ctx context.Context, tx pgx.Tx, id, status string) (string, error) {
	for range 10 {
		switch status {
		case TopicRejected:
			return "", nil
		case TopicMerged:
			var into *string
			if err := tx.QueryRow(ctx, `SELECT merged_into FROM analysis.topics WHERE topic_id = $1`, id).Scan(&into); err != nil {
				return "", err
			}
			if into == nil {
				return "", nil
			}
			if err := tx.QueryRow(ctx, `SELECT topic_id, status FROM analysis.topics WHERE topic_id = $1`, *into).Scan(&id, &status); err != nil {
				return "", err
			}
		default:
			return id, nil
		}
	}
	return "", fmt.Errorf("topics: merge chain of %s too long", id)
}

// slug makes an id part from an English name (lower-case ASCII words
// joined by "_"), or a short hash of the Arabic name.
func slug(en, ar string) string {
	var b strings.Builder
	sep := false
	for _, r := range strings.ToLower(en) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			if sep && b.Len() > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r)
			sep = false
		default:
			sep = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	if b.Len() > 0 {
		return b.String()
	}
	h := sha1.Sum([]byte(textnorm.TopicKey(ar) + en)) // #nosec G401 -- id suffix only
	return "t" + hex.EncodeToString(h[:4])
}

func freeTopicID(ctx context.Context, tx pgx.Tx, base string) (string, error) {
	id := base
	for n := 2; n < 100; n++ {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM analysis.topics WHERE topic_id = $1)`, id).Scan(&exists); err != nil {
			return "", err
		}
		if !exists {
			return id, nil
		}
		id = base + "_" + strconv.Itoa(n)
	}
	return "", fmt.Errorf("topics: no free id for %s", base)
}

// TopicRow is a topic of the list.
type TopicRow struct {
	ID, Parent, EN, AR, Status, MergedInto, Source string
	Seen                                           int
	FirstSeen, LastSeen                            time.Time
}

// Topics lists topics with the given status ("" for all), most used
// candidates first.
func (s *Store) Topics(ctx context.Context, status string, minSeen int) ([]TopicRow, error) {
	// A candidate's use is counted from the current analyses that carry it
	// (the seen counter can over-count when a write is retried).
	rows, err := s.DB.Query(ctx, `SELECT topic_id, parent_id, name_en, name_ar, status, merged_into, source, used, first_seen, last_seen FROM (
		SELECT t.topic_id, coalesce(t.parent_id, '') AS parent_id, t.name_en, coalesce(t.name_ar, '') AS name_ar, t.status,
			coalesce(t.merged_into, '') AS merged_into, t.source, t.ord, t.first_seen, t.last_seen,
			CASE WHEN t.status = 'candidate' THEN (SELECT count(*) FROM analysis.analyses a WHERE a.is_current AND a.topics @> ARRAY[t.topic_id])
			     ELSE t.seen END AS used
		FROM analysis.topics t WHERE ($1 = '' OR t.status = $1)) x
		WHERE used >= $2 ORDER BY (status = 'candidate') DESC, used DESC, ord`, status, minSeen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopicRow
	for rows.Next() {
		var t TopicRow
		if err := rows.Scan(&t.ID, &t.Parent, &t.EN, &t.AR, &t.Status, &t.MergedInto, &t.Source, &t.Seen, &t.FirstSeen, &t.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TopicDecision changes one topic: approve (a candidate joins the list,
// optionally renamed or moved under another parent), merge (into another
// topic), reject, or rename (an active topic).
type TopicDecision struct {
	ID, Action, Into, EN, AR, Parent string
}

// ApplyTopicDecisions applies decisions in one transaction: approvals and
// renames first, then merges and rejections. Topic lists of stored
// analyses and mentions are re-pointed (merges) or cleaned (rejections),
// their rollup days rebuilt, and a TopicUpdate is published for each
// change. A change of the active list records a new taxonomy version,
// which it returns ("" when the list is unchanged).
func (s *Store) ApplyTopicDecisions(ctx context.Context, ds []TopicDecision, note string) (string, error) {
	var version string
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockTopics); err != nil {
			return err
		}
		var events []*analysisv1.TopicUpdate
		changed := false
		order := map[string]int{"approve": 0, "rename": 0, "merge": 1, "reject": 2}
		for pass := 0; pass < 3; pass++ {
			for _, d := range ds {
				if order[d.Action] != pass {
					continue
				}
				ev, ch, err := s.applyTopic(ctx, tx, d)
				if err != nil {
					return fmt.Errorf("topic %s (%s): %w", d.ID, d.Action, err)
				}
				changed = changed || ch
				events = append(events, ev...)
			}
		}
		for _, d := range ds {
			if _, ok := order[d.Action]; !ok {
				return fmt.Errorf("topic %s: unknown action %q (approve, rename, merge, reject)", d.ID, d.Action)
			}
		}
		if !changed {
			return nil
		}
		var err error
		if version, err = newVersion(ctx, tx, "", note); err != nil {
			return err
		}
		for _, ev := range events {
			ev.TaxonomyVersion = version
			ev.EventId = ev.TopicId + ":" + version
			if err := outbox(ctx, tx, topics.AnalysisTopic, ev.TopicId, ev); err != nil {
				return err
			}
		}
		return nil
	})
	return version, err
}

func (s *Store) applyTopic(ctx context.Context, tx pgx.Tx, d TopicDecision) ([]*analysisv1.TopicUpdate, bool, error) {
	var t TopicRow
	err := tx.QueryRow(ctx, `SELECT topic_id, coalesce(parent_id, ''), name_en, coalesce(name_ar, ''), status FROM analysis.topics
		WHERE topic_id = $1 FOR UPDATE`, d.ID).Scan(&t.ID, &t.Parent, &t.EN, &t.AR, &t.Status)
	if err != nil {
		return nil, false, err
	}
	en, ar := orDefault(strings.TrimSpace(d.EN), t.EN), orDefault(strings.TrimSpace(d.AR), t.AR)
	switch d.Action {
	case "approve", "rename":
		if d.Action == "approve" && t.Status != TopicCandidate && t.Status != TopicActive {
			return nil, false, fmt.Errorf("status %s cannot be approved", t.Status)
		}
		if d.Action == "rename" && t.Status != TopicActive {
			return nil, false, errors.New("only an active topic can be renamed")
		}
		if d.Parent != "" && taxonomy.Depth(d.Parent) >= taxonomy.MaxDepth {
			return nil, false, fmt.Errorf("parent %s is a sub-topic; topics nest at most %d levels", d.Parent, taxonomy.MaxDepth)
		}
		if d.Parent != "" && d.Parent != t.Parent && t.Parent != "" {
			// Another parent means another id: a new topic, the old one
			// merged into it.
			id, err := freeTopicID(ctx, tx, d.Parent+"."+slug(en, ar))
			if err != nil {
				return nil, false, err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO analysis.topics (topic_id, parent_id, name_en, name_ar, status, source, ord, approved_at, seen)
				SELECT $1, $2, $3, NULLIF($4,''), 'active', source, ord, now(), seen FROM analysis.topics WHERE topic_id = $5`,
				id, d.Parent, en, ar, t.ID); err != nil {
				return nil, false, err
			}
			if err := addAliases(ctx, tx, id, en, ar); err != nil {
				return nil, false, err
			}
			evs, _, err := s.applyTopic(ctx, tx, TopicDecision{ID: t.ID, Action: "merge", Into: id})
			if err != nil {
				return nil, false, err
			}
			return append([]*analysisv1.TopicUpdate{topicEvent(id, d.Parent, en, ar, TopicActive, "")}, evs...), true, nil
		}
		if _, err := tx.Exec(ctx, `UPDATE analysis.topics SET status = 'active', name_en = $2, name_ar = NULLIF($3,''),
			approved_at = coalesce(approved_at, now()), updated_at = now() WHERE topic_id = $1`, t.ID, en, ar); err != nil {
			return nil, false, err
		}
		if err := addAliases(ctx, tx, t.ID, en, ar); err != nil {
			return nil, false, err
		}
		return []*analysisv1.TopicUpdate{topicEvent(t.ID, t.Parent, en, ar, TopicActive, "")}, true, nil
	case "merge", "reject":
		var hasKids bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM analysis.topics WHERE parent_id = $1 AND status IN ('active', 'candidate'))`,
			t.ID).Scan(&hasKids); err != nil {
			return nil, false, err
		}
		if hasKids {
			return nil, false, errors.New("it has sub-topics: merge or reject them first")
		}
	}
	switch d.Action {
	case "merge":
		if d.Into == "" || d.Into == t.ID {
			return nil, false, errors.New("merge needs another topic to merge into")
		}
		var intoStatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM analysis.topics WHERE topic_id = $1`, d.Into).Scan(&intoStatus); err != nil {
			return nil, false, fmt.Errorf("merge into %s: %w", d.Into, err)
		}
		into, err := follow(ctx, tx, d.Into, intoStatus)
		if err != nil {
			return nil, false, err
		}
		if into == "" || into == t.ID {
			return nil, false, fmt.Errorf("cannot merge into %s (rejected or itself)", d.Into)
		}
		if err := s.repointTopic(ctx, tx, t.ID, &into); err != nil {
			return nil, false, err
		}
		for _, q := range []string{
			`UPDATE analysis.topic_aliases SET topic_id = $2 WHERE topic_id = $1`,
			`UPDATE analysis.topics SET status = 'merged', merged_into = $2, updated_at = now() WHERE topic_id = $1`,
			// Topics merged earlier into this one follow it.
			`UPDATE analysis.topics SET merged_into = $2 WHERE merged_into = $1 AND topic_id <> $2`,
		} {
			if _, err := tx.Exec(ctx, q, t.ID, into); err != nil {
				return nil, false, err
			}
		}
		return []*analysisv1.TopicUpdate{topicEvent(t.ID, t.Parent, t.EN, t.AR, TopicMerged, into)}, true, nil
	case "reject":
		if err := s.repointTopic(ctx, tx, t.ID, nil); err != nil {
			return nil, false, err
		}
		if _, err := tx.Exec(ctx, `UPDATE analysis.topics SET status = 'rejected', updated_at = now() WHERE topic_id = $1`, t.ID); err != nil {
			return nil, false, err
		}
		return []*analysisv1.TopicUpdate{topicEvent(t.ID, t.Parent, t.EN, t.AR, TopicRejected, "")}, true, nil
	}
	return nil, false, fmt.Errorf("unknown action %q", d.Action)
}

// repointTopic replaces topic id by into (or removes it when into is nil)
// in the topic lists of analyses and mentions, and marks their days dirty.
func (s *Store) repointTopic(ctx context.Context, tx pgx.Tx, id string, into *string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO analysis.rollup_dirty (day)
		SELECT DISTINCT (created_at AT TIME ZONE $2)::date FROM analysis.analyses WHERE topics @> ARRAY[$1] AND is_current
		ON CONFLICT (day) DO UPDATE SET marked_at = now()`, id, s.Loc.String()); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO analysis.rollup_dirty (day) SELECT DISTINCT day FROM analysis.entity_mentions
		WHERE topics @> ARRAY[$1] ON CONFLICT (day) DO UPDATE SET marked_at = now()`, id); err != nil {
		return err
	}
	for _, q := range []string{
		`UPDATE analysis.analyses SET topics = analysis.topics_replace(topics, $1, $2) WHERE topics @> ARRAY[$1]`,
		`UPDATE analysis.entity_mentions SET topics = analysis.topics_replace(topics, $1, $2) WHERE topics @> ARRAY[$1]`,
	} {
		if _, err := tx.Exec(ctx, q, id, into); err != nil {
			return err
		}
	}
	return nil
}

func topicEvent(id, parent, en, ar, status, into string) *analysisv1.TopicUpdate {
	return &analysisv1.TopicUpdate{TopicId: id, ParentId: parent, NameEn: en, NameAr: ar, Status: status, MergedInto: into,
		UpdatedAt: timestamppb.Now()}
}

// TopicHit is a topic found for a search, with its sub-topics.
type TopicHit struct {
	ID, EN, AR string
	Score      float64
	// Under is set on a sub-topic included because its parent matched.
	Under string
}

// FindTopics finds the topics a search means, in English or Arabic
// ("electrical cuts", "انقطاع الكهرباء"): every name and alias of the
// active and candidate topics is compared word by word (trigram
// similarity, so "electrical" finds "electricity"); the best-scoring
// topics are returned with all their sub-topics, so a query by their ids
// fetches every item about it, whatever its language. The analytics agent
// can instead read agent.topics and agent.topic_aliases and choose ids
// itself.
func (s *Store) FindTopics(ctx context.Context, q string) ([]TopicHit, error) {
	query := strings.Fields(textnorm.TopicKey(q))
	if len(query) == 0 {
		return nil, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT t.topic_id, t.name_en, coalesce(t.name_ar, ''),
		coalesce((SELECT array_agg(a.alias) FROM analysis.topic_aliases a WHERE a.topic_id = t.topic_id), '{}')
		FROM analysis.topics t WHERE t.status IN ('active', 'candidate') ORDER BY t.ord`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type topic struct {
		id, en, ar string
		score      float64
	}
	var all []topic
	best := 0.0
	for rows.Next() {
		var t topic
		var aliases []string
		if err := rows.Scan(&t.id, &t.en, &t.ar, &aliases); err != nil {
			return nil, err
		}
		for _, name := range append([]string{t.en, t.ar}, aliases...) {
			t.score = max(t.score, nameScore(query, strings.Fields(textnorm.TopicKey(name))))
		}
		best = max(best, t.score)
		all = append(all, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if best < 0.25 {
		return nil, nil
	}
	var out []TopicHit
	matched := map[string]bool{}
	for _, t := range all {
		if t.score >= 0.8*best {
			matched[t.id] = true
			out = append(out, TopicHit{ID: t.id, EN: t.en, AR: t.ar, Score: t.score})
		}
	}
	for _, t := range all {
		if matched[t.id] {
			continue
		}
		for id := range matched {
			if strings.HasPrefix(t.id, id+".") {
				out = append(out, TopicHit{ID: t.id, EN: t.en, AR: t.ar, Score: t.score, Under: id})
				break
			}
		}
	}
	return out, nil
}

// nameScore is the share of query words found in a name (each word's best
// trigram similarity, counted from 0.45).
func nameScore(query, name []string) float64 {
	if len(name) == 0 {
		return 0
	}
	sum := 0.0
	for _, q := range query {
		bestWord := 0.0
		for _, n := range name {
			bestWord = max(bestWord, textnorm.Similarity(q, n))
		}
		if bestWord >= 0.45 {
			sum += bestWord
		}
	}
	return sum / float64(len(query))
}

// TopicItems counts the current analyses carrying any of the topic ids.
func (s *Store) TopicItems(ctx context.Context, ids []string) (int64, error) {
	var n int64
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.analyses WHERE is_current AND topics && $1`, ids).Scan(&n)
	return n, err
}
