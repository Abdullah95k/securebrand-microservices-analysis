package eval

import (
	"fmt"
	"io"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider"
)

// Tokens per unit of work: uncached input, cached input (the shared system
// prompt's share), output. These are the design's estimates
// (docs/design/analysis-service.md section 16) until `sb-eval run`
// measures them.
type Tokens struct{ In, Cached, Out float64 }

// Assumptions of the cost model.
type Assumptions struct {
	Comment, Post, Image, Video Tokens
	PostsPerItem                float64 // posts / text items
	MediaPerPost                float64
	VideoShare                  float64
	VideoMinutes                float64
	STTPerMinute                float64
	EscalationShare             float64 // of comments
	AuditShare                  float64 // of text items
}

// DefaultAssumptions are the design's estimates.
var DefaultAssumptions = Assumptions{
	Comment: Tokens{60, 85, 70}, Post: Tokens{700, 500, 180}, Image: Tokens{1100, 500, 200}, Video: Tokens{3250, 500, 250},
	PostsPerItem: 1 / 58.3, MediaPerPost: 1, VideoShare: 0.2, VideoMinutes: 0.75, STTPerMinute: 0.0051,
	EscalationShare: 0.02, AuditShare: 0.001,
}

// Choice is a model and lane for one kind of work.
type Choice struct {
	Model string
	Batch bool
}

// Mix assigns models to the routes.
type Mix struct {
	Name                        string
	Comment, Post, Image, Video Choice
	Escalation, Audit           Choice
}

// Mixes compared in the design.
var Mixes = []Mix{
	{"lean", Choice{"claude-haiku-5-5", true}, Choice{"claude-haiku-5-5", true}, Choice{"claude-haiku-5-5", false}, Choice{"claude-haiku-5-5", false},
		Choice{"claude-sonnet-5-5", true}, Choice{"claude-opus-5-5", true}},
	{"heavier", Choice{"claude-haiku-5-5", true}, Choice{"claude-sonnet-5-5", true}, Choice{"claude-sonnet-5-5", false}, Choice{"claude-sonnet-5-5", false},
		Choice{"claude-sonnet-5-5", true}, Choice{"claude-opus-5-5", true}},
	{"all-sonnet", Choice{"claude-sonnet-5-5", true}, Choice{"claude-sonnet-5-5", true}, Choice{"claude-sonnet-5-5", false}, Choice{"claude-sonnet-5-5", false},
		Choice{}, Choice{"claude-opus-5-5", true}},
	{"gemini-3.5-flash-lite", Choice{"gemini-3.5-flash-lite", true}, Choice{"gemini-3.5-flash-lite", true}, Choice{"gemini-3.5-flash-lite", false},
		Choice{"gemini-3.5-flash-lite", false}, Choice{"claude-sonnet-5-5", true}, Choice{"claude-opus-5-5", true}},
	{"gpt-5.4-nano", Choice{"gpt-5.4-nano", true}, Choice{"gpt-5.4-nano", true}, Choice{"gpt-5.4-nano", false}, Choice{"gpt-5.4-nano", false},
		Choice{"claude-sonnet-5-5", true}, Choice{"claude-opus-5-5", true}},
}

// Unit is the cost of one unit of work.
func Unit(p provider.Pricing, c Choice, t Tokens) float64 {
	if c.Model == "" {
		return 0
	}
	return p.Cost(c.Model, provider.Usage{InputTokens: int(t.In), CacheReadTokens: int(t.Cached), OutputTokens: int(t.Out)}, c.Batch)
}

// Breakdown is a yearly cost by line.
type Breakdown struct {
	Items, Comments, Posts, Images, Videos float64
	Lines                                  map[string]float64
	Total                                  float64
}

// Yearly computes the yearly cost of mix for items text items a year.
func Yearly(p provider.Pricing, a Assumptions, m Mix, items float64) Breakdown {
	posts := items * a.PostsPerItem
	comments := items - posts
	media := posts * a.MediaPerPost
	b := Breakdown{Items: items, Comments: comments, Posts: posts, Images: media * (1 - a.VideoShare), Videos: media * a.VideoShare,
		Lines: map[string]float64{}}
	b.Lines["comments"] = comments * Unit(p, m.Comment, a.Comment)
	b.Lines["escalation"] = comments * a.EscalationShare * Unit(p, m.Escalation, a.Comment)
	b.Lines["posts"] = posts * Unit(p, m.Post, a.Post)
	b.Lines["images"] = b.Images * Unit(p, m.Image, a.Image)
	b.Lines["videos"] = b.Videos * (Unit(p, m.Video, a.Video) + a.STTPerMinute*a.VideoMinutes)
	// Audit answers are longer (thinking on): about 3x a comment's tokens.
	b.Lines["audit"] = (comments + posts) * a.AuditShare * 3 * Unit(p, m.Audit, a.Comment)
	for _, v := range b.Lines {
		b.Total += v
	}
	return b
}

// WriteCostReport prints per-1M-comment prices and the yearly mixes.
func WriteCostReport(w io.Writer, p provider.Pricing, a Assumptions, volumes []float64) {
	_, _ = fmt.Fprintf(w, "Tokens per comment (uncached in / cached in / out): %.0f / %.0f / %.0f\n\n", a.Comment.In, a.Comment.Cached, a.Comment.Out)
	_, _ = fmt.Fprintln(w, "| Model | Per 1M comments, batch | Per 1M comments, real time |")
	_, _ = fmt.Fprintln(w, "|---|---|---|")
	for _, model := range sortedKeys(p) {
		pr := p.Price(model)
		if pr.Input == 0 && pr.Output == 0 {
			continue
		}
		_, _ = fmt.Fprintf(w, "| %s | $%.1f | $%.1f |\n", model, 1e6*Unit(p, Choice{model, true}, a.Comment), 1e6*Unit(p, Choice{model, false}, a.Comment))
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprint(w, "| Mix |")
	for _, v := range volumes {
		_, _ = fmt.Fprintf(w, " %s items/year |", human(v))
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprint(w, "|---|")
	for range volumes {
		_, _ = fmt.Fprint(w, "---|")
	}
	_, _ = fmt.Fprintln(w)
	for _, m := range Mixes {
		_, _ = fmt.Fprintf(w, "| %s |", m.Name)
		for _, v := range volumes {
			_, _ = fmt.Fprintf(w, " $%s |", money(Yearly(p, a, m, v).Total))
		}
		_, _ = fmt.Fprintln(w)
	}
}

func human(v float64) string {
	switch {
	case v >= 1e9:
		return fmt.Sprintf("%gB", v/1e9)
	case v >= 1e6:
		return fmt.Sprintf("%gM", v/1e6)
	}
	return fmt.Sprintf("%g", v)
}

func money(v float64) string {
	if v >= 1000 {
		return fmt.Sprintf("%.1fk", v/1000)
	}
	return fmt.Sprintf("%.0f", v)
}
