package eval

import (
	"fmt"
	"io"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
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
	DuplicateShare              float64 // comments answered by an identical comment of the same thread
	EscalationShare             float64 // of analysed comments
	AuditShare                  float64 // of text items
}

// DefaultAssumptions are the design's estimates for the current output
// format (prompt p3, schema s3): compact answers (about 50 output tokens
// per comment instead of about 70, including the usually empty new-topic
// list; ad and breach flags cost nothing when absent), identical comments
// of a thread answered once (10%, to be measured by `make pilot-stats`),
// and a 0.02% audit sample. The cached prefix grows by the flag
// definitions.
var DefaultAssumptions = Assumptions{
	Comment: Tokens{60, 110, 50}, Post: Tokens{700, 600, 150}, Image: Tokens{1100, 550, 180}, Video: Tokens{3250, 550, 220},
	PostsPerItem: 1 / 58.3, MediaPerPost: 1, VideoShare: 0.2, VideoMinutes: 0.75,
	DuplicateShare: 0.10, EscalationShare: 0.02, AuditShare: 0.0002,
}

// PreviousAssumptions are the first design's (prompt p1, schema s1):
// verbose answers, every duplicate analysed, a 0.1% audit sample. Kept to
// show what the changes save.
var PreviousAssumptions = Assumptions{
	Comment: Tokens{60, 85, 70}, Post: Tokens{700, 500, 180}, Image: Tokens{1100, 500, 200}, Video: Tokens{3250, 500, 250},
	PostsPerItem: 1 / 58.3, MediaPerPost: 1, VideoShare: 0.2, VideoMinutes: 0.75,
	EscalationShare: 0.02, AuditShare: 0.001,
}

// STT is how videos' speech is turned into text: an API priced per
// minute, or an own server with a fixed yearly cost.
type STT struct {
	Name         string
	PerMinute    float64
	FixedPerYear float64
}

// Speech-to-text options. Gemini 3.5 Transcribe: $0.0051 a minute (Vertex
// AI price page, checked 2026-11-01). Own Whisper: Whisper large-v3-turbo
// on one 8-CPU server, an estimate of about $2,000 a year, which keeps up
// with the audio of about 1 billion text items a year (design section 16).
var (
	STTGemini  = STT{Name: "Gemini 3.5 Transcribe", PerMinute: 0.0051}
	STTWhisper = STT{Name: "own Whisper server", FixedPerYear: 2000}
)

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
	STT                         STT
}

var (
	haikuBatch  = Choice{"claude-haiku-5-5", true}
	haikuRT     = Choice{"claude-haiku-5-5", false}
	sonnetBatch = Choice{"claude-sonnet-5-5", true}
	sonnetRT    = Choice{"claude-sonnet-5-5", false}
	opusBatch   = Choice{"claude-opus-5-5", true}
)

// Mixes compared in the design. "lean" is the default configuration;
// "lean+whisper" moves speech-to-text to an own server (cheaper above
// about 150 million items a year).
var Mixes = []Mix{
	{"lean", haikuBatch, haikuBatch, haikuRT, haikuRT, sonnetBatch, opusBatch, STTGemini},
	{"lean+whisper", haikuBatch, haikuBatch, haikuRT, haikuRT, sonnetBatch, opusBatch, STTWhisper},
	{"heavier", haikuBatch, sonnetBatch, sonnetRT, sonnetRT, sonnetBatch, opusBatch, STTGemini},
	{"all-sonnet", sonnetBatch, sonnetBatch, sonnetRT, sonnetRT, Choice{}, opusBatch, STTGemini},
	{"gemini-3.5-flash-lite", Choice{"gemini-3.5-flash-lite", true}, Choice{"gemini-3.5-flash-lite", true},
		Choice{"gemini-3.5-flash-lite", false}, Choice{"gemini-3.5-flash-lite", false}, sonnetBatch, opusBatch, STTGemini},
	{"gpt-5.4-nano", Choice{"gpt-5.4-nano", true}, Choice{"gpt-5.4-nano", true}, Choice{"gpt-5.4-nano", false},
		Choice{"gpt-5.4-nano", false}, sonnetBatch, opusBatch, STTGemini},
}

// MixByName returns a mix of Mixes.
func MixByName(name string) (Mix, bool) {
	for _, m := range Mixes {
		if m.Name == name {
			return m, true
		}
	}
	return Mix{}, false
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
	sent := comments * (1 - a.DuplicateShare) // duplicates share an answer
	media := posts * a.MediaPerPost
	b := Breakdown{Items: items, Comments: comments, Posts: posts, Images: media * (1 - a.VideoShare), Videos: media * a.VideoShare,
		Lines: map[string]float64{}}
	b.Lines["comments"] = sent * Unit(p, m.Comment, a.Comment)
	b.Lines["escalation"] = sent * a.EscalationShare * Unit(p, m.Escalation, a.Comment)
	b.Lines["posts"] = posts * Unit(p, m.Post, a.Post)
	b.Lines["images"] = b.Images * Unit(p, m.Image, a.Image)
	b.Lines["videos"] = b.Videos * Unit(p, m.Video, a.Video)
	b.Lines["speech"] = b.Videos*a.VideoMinutes*m.STT.PerMinute + m.STT.FixedPerYear
	// Audit answers are longer (thinking on): about 3x a comment's tokens.
	b.Lines["audit"] = (comments + posts) * a.AuditShare * 3 * Unit(p, m.Audit, a.Comment)
	for _, v := range b.Lines {
		b.Total += v
	}
	return b
}

// WriteCostReport prints per-1M-comment prices and the yearly mixes.
func WriteCostReport(w io.Writer, p provider.Pricing, a Assumptions, volumes []float64) {
	_, _ = fmt.Fprintf(w, "Tokens per comment (uncached in / cached in / out): %.0f / %.0f / %.0f; duplicates %.0f%%, escalated %.1f%%, audited %.2f%%\n\n",
		a.Comment.In, a.Comment.Cached, a.Comment.Out, 100*a.DuplicateShare, 100*a.EscalationShare, 100*a.AuditShare)
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
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "By line (lean | lean+whisper):")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprint(w, "| Line |")
	for _, v := range volumes {
		_, _ = fmt.Fprintf(w, " %s |", human(v))
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprint(w, "|---|")
	for range volumes {
		_, _ = fmt.Fprint(w, "---|")
	}
	_, _ = fmt.Fprintln(w)
	lean, _ := MixByName("lean")
	whisper, _ := MixByName("lean+whisper")
	for _, line := range []string{"comments", "escalation", "posts", "images", "videos", "speech", "audit"} {
		_, _ = fmt.Fprintf(w, "| %s |", line)
		for _, v := range volumes {
			a1, a2 := Yearly(p, a, lean, v).Lines[line], Yearly(p, a, whisper, v).Lines[line]
			if a1 == a2 {
				_, _ = fmt.Fprintf(w, " $%s |", money(a1))
			} else {
				_, _ = fmt.Fprintf(w, " $%s \\| $%s |", money(a1), money(a2))
			}
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
