package eval

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"regexp"
	"sort"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/adapter"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/entities"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/textnorm"
	fbv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/fb/v1"
)

// SampleConfig sizes the gold set.
type SampleConfig struct {
	Comments int
	Posts    int
	// BrandShare of comments mention a registry entity (oversampled: these
	// are what clients pay for); ScriptShare are Kurdish or Latin-script.
	BrandShare  float64
	ScriptShare float64
	Seed        uint64
	// KeepTags keeps the names of tagged people in the texts. By default
	// they are written as "@user", as production sends them to providers.
	KeepTags bool
}

// DefaultSample is the design's gold set: 1,000 comments, 200 posts.
var DefaultSample = SampleConfig{Comments: 1000, Posts: 200, BrandShare: 0.25, ScriptShare: 0.10, Seed: 1}

var (
	kurdish = regexp.MustCompile(`[ڕڵۆێە]`)
	arabic  = regexp.MustCompile(`[\x{0600}-\x{06FF}]`)
	latin   = regexp.MustCompile(`[A-Za-z]`)
)

type candidate struct {
	item   Item
	page   int64
	strata []string
}

// Sample draws a stratified gold set from the fbs tables (posts, comments,
// pages) of the pilot database: comments spread over pages and lengths,
// with brand mentions and Kurdish/Latin-script comments oversampled, and
// posts spread over pages and types. Item ids are opaque (GoldID); no
// author data is read.
func Sample(ctx context.Context, db *pgxpool.Pool, reg *entities.Registry, cfg SampleConfig) ([]Item, error) {
	r := rand.New(rand.NewPCG(cfg.Seed, 99))
	pages, err := countPages(ctx, db)
	if err != nil {
		return nil, err
	}
	perPage := max(3, 4*cfg.Comments/max(pages, 1))
	rows, err := db.Query(ctx, `
		SELECT c.comment_id, c.page_id, c.depth, c.text, coalesce(p.text, ''), coalesce(pg.name, ''), coalesce(par.text, ''),
		       c.text_ranges, p.text_ranges, par.text_ranges
		FROM (SELECT *, row_number() OVER (PARTITION BY page_id ORDER BY md5(comment_id::text || $2::text)) AS rn
		      FROM comments WHERE text IS NOT NULL AND char_length(btrim(text)) >= 2 AND NOT redacted) c
		LEFT JOIN post_index pi ON pi.post_id = c.post_id
		LEFT JOIN posts p ON p.post_created_at = pi.post_created_at AND p.post_id = c.post_id
		LEFT JOIN pages pg ON pg.page_id = c.page_id
		LEFT JOIN comments par ON par.post_created_at = c.post_created_at AND par.post_id = c.post_id AND par.comment_id = c.parent_comment_id
		WHERE c.rn <= $1`, perPage, strconv.FormatUint(cfg.Seed, 10))
	if err != nil {
		return nil, err
	}
	var brand, script, short, mid, long []candidate
	for rows.Next() {
		var (
			id, page                 int64
			depth                    int
			text, post, name, parent string
			textRg, postRg, parentRg []byte
		)
		if err := rows.Scan(&id, &page, &depth, &text, &post, &name, &parent, &textRg, &postRg, &parentRg); err != nil {
			rows.Close()
			return nil, err
		}
		if !cfg.KeepTags {
			text, post, parent = untag(text, textRg, reg), untag(post, postRg, reg), untag(parent, parentRg, reg)
		}
		kind := "comment"
		if depth > 0 {
			kind = "reply"
		}
		c := candidate{page: page, item: Item{ID: GoldID("fb:comment:" + strconv.FormatInt(id, 10)), Kind: kind, Page: name,
			Post: textnorm.Truncate(post, 1500), Parent: textnorm.Truncate(parent, 300), Text: text}}
		n := len([]rune(text))
		switch {
		case len(reg.Find(text)) > 0:
			c.strata = []string{"brand"}
			brand = append(brand, c)
		case kurdish.MatchString(text) || (latin.MatchString(text) && !arabic.MatchString(text)):
			c.strata = []string{"script"}
			script = append(script, c)
		case n < 20:
			c.strata = []string{"short"}
			short = append(short, c)
		case n < 80:
			c.strata = []string{"medium"}
			mid = append(mid, c)
		default:
			c.strata = []string{"long"}
			long = append(long, c)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []Item
	take := func(cs []candidate, n int) int {
		r.Shuffle(len(cs), func(i, j int) { cs[i], cs[j] = cs[j], cs[i] })
		cs = spreadPages(cs)
		k := min(n, len(cs))
		for _, c := range cs[:k] {
			c.item.Strata = c.strata
			out = append(out, c.item)
		}
		return k
	}
	left := cfg.Comments
	left -= take(brand, int(float64(cfg.Comments)*cfg.BrandShare))
	left -= take(script, int(float64(cfg.Comments)*cfg.ScriptShare))
	third := left / 3
	left -= take(short, third)
	left -= take(mid, third)
	take(long, left)
	posts, err := samplePosts(ctx, db, reg, cfg)
	if err != nil {
		return nil, err
	}
	out = append(out, posts...)
	return out, nil
}

// spreadPages orders candidates round-robin over pages so a quota draws
// from as many pages as possible.
func spreadPages(cs []candidate) []candidate {
	byPage := map[int64][]candidate{}
	var pages []int64
	for _, c := range cs {
		if _, ok := byPage[c.page]; !ok {
			pages = append(pages, c.page)
		}
		byPage[c.page] = append(byPage[c.page], c)
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i] < pages[j] })
	out := make([]candidate, 0, len(cs))
	for len(out) < len(cs) {
		for _, p := range pages {
			if l := byPage[p]; len(l) > 0 {
				out = append(out, l[0])
				byPage[p] = l[1:]
			}
		}
	}
	return out
}

func samplePosts(ctx context.Context, db *pgxpool.Pool, reg *entities.Registry, cfg SampleConfig) ([]Item, error) {
	rows, err := db.Query(ctx, `
		SELECT p.post_id, p.post_type, p.text, coalesce(pg.name, ''), p.text_ranges
		FROM (SELECT *, row_number() OVER (PARTITION BY page_id, post_type ORDER BY md5(post_id::text || $2::text)) AS rn
		      FROM posts WHERE text IS NOT NULL AND char_length(btrim(text)) >= 10) p
		LEFT JOIN pages pg ON pg.page_id = p.page_id
		WHERE p.rn <= 2 ORDER BY md5(p.post_id::text || $2::text) LIMIT $1`, cfg.Posts, strconv.FormatUint(cfg.Seed, 10))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var id int64
		var typ, text, name string
		var ranges []byte
		if err := rows.Scan(&id, &typ, &text, &name, &ranges); err != nil {
			return nil, err
		}
		if !cfg.KeepTags {
			text = untag(text, ranges, reg)
		}
		out = append(out, Item{ID: GoldID("fb:post:" + strconv.FormatInt(id, 10)), Kind: "post", Page: name,
			Text: textnorm.Truncate(text, 4000), Strata: []string{"post", typ}})
	}
	return out, rows.Err()
}

// untag writes the people tagged in a Facebook text as "@user", from fbs's
// stored text_ranges (JSON with proto field names). Tagged registry
// entities stay. Unreadable ranges leave the text as it is.
func untag(text string, rangesJSON []byte, reg *entities.Registry) string {
	if len(rangesJSON) == 0 || text == "" {
		return text
	}
	var rs []struct {
		Offset     int32  `json:"offset"`
		Length     int32  `json:"length"`
		EntityType string `json:"entity_type"`
		Name       string `json:"name"`
	}
	if err := json.Unmarshal(rangesJSON, &rs); err != nil {
		return text
	}
	ranges := make([]*fbv1.TextRange, 0, len(rs))
	for _, r := range rs {
		ranges = append(ranges, &fbv1.TextRange{Offset: r.Offset, Length: r.Length, EntityType: r.EntityType, Name: r.Name})
	}
	return textnorm.MaskSpans(text, adapter.FBTagSpans(text, ranges), textnorm.TagPlaceholder, reg.Known)
}

func countPages(ctx context.Context, db *pgxpool.Pool) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(DISTINCT page_id) FROM comments`).Scan(&n)
	return n, err
}
