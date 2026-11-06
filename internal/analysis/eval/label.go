package eval

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/entities"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/prompt"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/taxonomy"
)

// LabelServer is the labelling tool: a small web page, served on
// 127.0.0.1 only, that shows one item at a time with its context and saves
// the label into the same JSONL file (atomically, after every item).
type LabelServer struct {
	Path     string
	Tax      *taxonomy.Taxonomy
	Entities []entities.SeedEntity

	mu    sync.Mutex
	items []Item
	tmpl  *template.Template
}

// NewLabelServer loads the file to label.
func NewLabelServer(path string, tax *taxonomy.Taxonomy, ents []entities.SeedEntity) (*LabelServer, error) {
	items, err := ReadItems(path)
	if err != nil {
		return nil, err
	}
	s := &LabelServer{Path: path, Tax: tax, Entities: ents, items: items}
	s.tmpl = template.Must(template.New("page").Funcs(template.FuncMap{
		"has": func(xs []string, x string) bool {
			for _, v := range xs {
				if v == x {
					return true
				}
			}
			return false
		},
		"seq": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = i
			}
			return out
		},
		"entity": func(l *Label, i int) Entity {
			if l != nil && i < len(l.Entities) {
				return l.Entities[i]
			}
			return Entity{}
		},
		"join": strings.Join,
	}).Parse(page))
	return s, nil
}

// Serve listens on addr (forced to 127.0.0.1) until ctx ends.
func (s *LabelServer) Serve(ctx context.Context, addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host != "127.0.0.1" && host != "localhost" && host != "" {
		return errors.New("the labelling tool only listens on 127.0.0.1 (reach it through an SSH tunnel)")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.show)
	mux.HandleFunc("/save", s.save)
	mux.HandleFunc("/image", s.image)
	srv := &http.Server{Addr: net.JoinHostPort("127.0.0.1", port), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	slog.Info("labelling tool", "url", "http://"+srv.Addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type view struct {
	I, Total, Done int
	Item           Item
	Label          *Label
	Tax            *taxonomy.Taxonomy
	Entities       []entities.SeedEntity
	Langs          []string
	Dialects       []string
	Sentiments     []string
	Types          []string
	Intents        []string
	Emotions       []string
	Toxicities     []string
	Legal, Ads     []string
	Aspects        []string
	Labeller       string
	Saved          bool
}

func (s *LabelServer) show(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, done := -1, 0
	for k, it := range s.items {
		if it.Label != nil {
			done++
		} else if i < 0 {
			i = k
		}
	}
	if q := r.URL.Query().Get("i"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n >= 0 && n < len(s.items) {
			i = n
		}
	}
	if i < 0 {
		_, _ = fmt.Fprintf(w, "All %d items are labelled. Thank you! The file is %s.", len(s.items), s.Path)
		return
	}
	labeller := ""
	if c, err := r.Cookie("labeller"); err == nil {
		labeller = c.Value
	}
	v := view{I: i, Total: len(s.items), Done: done, Item: s.items[i], Label: s.items[i].Label, Tax: s.Tax, Entities: s.Entities,
		Langs: prompt.Langs, Dialects: prompt.Dialects, Sentiments: prompt.Sentiments, Types: labelTypes(), Intents: prompt.Intents,
		Emotions: prompt.Emotions, Toxicities: prompt.Toxicities, Aspects: prompt.Aspects, Labeller: labeller, Saved: r.URL.Query().Get("saved") != "",
		Legal: prompt.LegalCategories, Ads: []string{"none", "affiliate", "own", "paid", "hidden", "sale"}}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.Execute(w, v); err != nil {
		slog.Error("label page", "err", err)
	}
}

// labelTypes are the entity types a labeller may pick (private persons are
// never labelled as entities: only counted by the model).
func labelTypes() []string {
	var out []string
	for _, t := range prompt.EntityTypes {
		if t != "person_private" {
			out = append(out, t)
		}
	}
	return out
}

func (s *LabelServer) save(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	i, err := strconv.Atoi(r.FormValue("i"))
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil || i < 0 || i >= len(s.items) {
		http.Error(w, "bad item", http.StatusBadRequest)
		return
	}
	l := &Label{
		Lang: r.FormValue("lang"), Dialect: r.FormValue("dialect"), Sentiment: r.FormValue("sentiment"),
		Sarcasm: r.FormValue("sarcasm") != "", Topics: r.Form["topics"], Intent: r.FormValue("intent"), Emotion: r.FormValue("emotion"),
		Toxicity: r.FormValue("toxicity"), Spam: r.FormValue("spam") != "", Urgent: r.FormValue("urgent") != "",
		Unsure: r.FormValue("unsure") != "", Notes: strings.TrimSpace(r.FormValue("notes")), Labeller: strings.TrimSpace(r.FormValue("labeller")),
		LabelledAt: time.Now().UTC(), Legal: r.Form["legal"],
	}
	if ad := r.FormValue("ad"); ad != "none" {
		l.Ad = ad
	}
	if len(l.Topics) > prompt.MaxTopics {
		l.Topics = l.Topics[:prompt.MaxTopics]
	}
	for k := 0; k < 6; k++ {
		name := strings.TrimSpace(r.FormValue(fmt.Sprintf("e%d_name", k)))
		if name == "" {
			continue
		}
		l.Entities = append(l.Entities, Entity{Name: name, Type: r.FormValue(fmt.Sprintf("e%d_type", k)),
			ID: strings.TrimSpace(r.FormValue(fmt.Sprintf("e%d_id", k))), Sentiment: r.FormValue(fmt.Sprintf("e%d_sent", k)),
			Aspect: r.FormValue(fmt.Sprintf("e%d_aspect", k))})
	}
	for _, f := range []struct {
		dst *[]string
		key string
	}{{&l.Logos, "logos"}, {&l.OCRContains, "ocr"}} {
		for _, x := range strings.Split(r.FormValue(f.key), ",") {
			if x = strings.TrimSpace(x); x != "" {
				*f.dst = append(*f.dst, x)
			}
		}
	}
	if l.Sentiment == "" && !l.Unsure {
		http.Error(w, "choose a sentiment, or tick 'unsure'", http.StatusBadRequest)
		return
	}
	s.items[i].Label = l
	if err := WriteItems(s.Path, s.items); err != nil {
		http.Error(w, "could not save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Browsers treat http://127.0.0.1 as a secure context, so Secure works
	// through the SSH tunnel.
	http.SetCookie(w, &http.Cookie{Name: "labeller", Value: l.Labeller, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
	next := i + 1
	for k := i + 1; k < len(s.items); k++ {
		if s.items[k].Label == nil {
			next = k
			break
		}
	}
	if next >= len(s.items) {
		http.Redirect(w, r, "/?saved=1", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/?i=%d&saved=1", next), http.StatusSeeOther)
}

func (s *LabelServer) image(w http.ResponseWriter, r *http.Request) {
	i, err := strconv.Atoi(r.URL.Query().Get("i"))
	s.mu.Lock()
	var path string
	if err == nil && i >= 0 && i < len(s.items) {
		path = s.items[i].ImagePath
	}
	s.mu.Unlock()
	if path == "" {
		http.NotFound(w, r)
		return
	}
	b, err := os.ReadFile(path) // #nosec G304 -- paths from the gold file, served on 127.0.0.1
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(b))
	_, _ = w.Write(b)
}

const page = `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>SecureBrand labelling {{.I}}/{{.Total}}</title>
<style>
body{font-family:system-ui,sans-serif;max-width:980px;margin:16px auto;padding:0 16px;color:#1d2433}
.ctx{background:#f3f5f8;border-radius:8px;padding:10px 14px;margin:8px 0;white-space:pre-wrap}
.text{font-size:1.35em;background:#fff8e1;border-radius:8px;padding:14px;margin:10px 0;white-space:pre-wrap}
fieldset{border:1px solid #d7dce4;border-radius:8px;margin:10px 0}
label{margin-right:14px;display:inline-block;margin-bottom:4px}
select,input[type=text],textarea{font-size:1em;padding:3px}
table td{padding:2px 4px}
.bar{display:flex;gap:16px;align-items:center;justify-content:space-between}
.ok{color:#1b7f3b}
button{font-size:1.1em;padding:8px 22px}
</style></head><body>
<div class="bar"><b>Item {{.I}} of {{.Total}}</b><span>{{.Done}} labelled{{if .Saved}} · <span class="ok">saved</span>{{end}}</span>
<span><a href="/?i={{if gt .I 0}}{{.I}}{{end}}">reload</a>{{if gt .I 0}} · <a href="/?i=0">first</a>{{end}}</span></div>
<p>Guide: docs/analysis/labelling-guide.md. Kind: <b>{{.Item.Kind}}</b>{{if .Item.Page}} · Page: <b dir="auto">{{.Item.Page}}</b>{{end}}</p>
{{if .Item.Post}}<div class="ctx" dir="auto"><small>Post (context, do not label):</small>
{{.Item.Post}}</div>{{end}}
{{if .Item.Parent}}<div class="ctx" dir="auto"><small>Replying to (context):</small>
{{.Item.Parent}}</div>{{end}}
{{if .Item.ImagePath}}<img src="/image?i={{.I}}" style="max-width:100%;max-height:520px;border-radius:8px">{{end}}
{{if .Item.Text}}<div class="text" dir="auto">{{.Item.Text}}</div>{{end}}
<form method="post" action="/save"><input type="hidden" name="i" value="{{.I}}">
{{$l := .Label}}
<fieldset><legend>Language</legend>
{{range .Langs}}<label><input type="radio" name="lang" value="{{.}}" {{if and $l (eq $l.Lang .)}}checked{{end}}>{{.}}</label>{{end}}
<br>Dialect (Arabic only):
{{range .Dialects}}<label><input type="radio" name="dialect" value="{{.}}" {{if and $l (eq $l.Dialect .)}}checked{{end}}>{{.}}</label>{{end}}
</fieldset>
<fieldset><legend>Overall sentiment of the author</legend>
{{range .Sentiments}}<label><input type="radio" name="sentiment" value="{{.}}" {{if and $l (eq $l.Sentiment .)}}checked{{end}}>{{.}}</label>{{end}}
<label><input type="checkbox" name="sarcasm" {{if and $l $l.Sarcasm}}checked{{end}}>sarcastic</label>
</fieldset>
<fieldset><legend>Entities and the sentiment toward each (no private persons)</legend>
<datalist id="ids">{{range .Entities}}<option value="{{.ID}}">{{.Name}} / {{.NameAR}}</option>{{end}}</datalist>
<table><tr><td>Name as written</td><td>Type</td><td>Registry id (if listed)</td><td>Sentiment</td><td>About</td></tr>
{{$v := .}}{{range $k := seq 6}}{{$e := entity $l $k}}<tr>
<td><input type="text" name="e{{$k}}_name" value="{{$e.Name}}" dir="auto" size="18"></td>
<td><select name="e{{$k}}_type">{{range $v.Types}}<option {{if eq $e.Type .}}selected{{end}}>{{.}}</option>{{end}}</select></td>
<td><input type="text" name="e{{$k}}_id" value="{{$e.ID}}" list="ids" size="14"></td>
<td><select name="e{{$k}}_sent">{{range $v.Sentiments}}<option {{if eq $e.Sentiment .}}selected{{end}}>{{.}}</option>{{end}}</select></td>
<td><select name="e{{$k}}_aspect">{{range $v.Aspects}}<option {{if eq $e.Aspect .}}selected{{end}}>{{.}}</option>{{end}}</select></td>
</tr>{{end}}</table></fieldset>
<fieldset><legend>Topics (every one that applies; hold Ctrl or Cmd to pick several)</legend>
<select name="topics" multiple size="14" style="width:100%">
{{range .Tax.Topics}}<optgroup label="{{.EN}} / {{.AR}}"><option value="{{.ID}}" {{if and $l (has $l.Topics .ID)}}selected{{end}}>{{.ID}} (general)</option>
{{range .Children}}<option value="{{.ID}}" {{if and $l (has $l.Topics .ID)}}selected{{end}}>{{.EN}} / {{.AR}}</option>{{range .Children}}<option value="{{.ID}}" {{if and $l (has $l.Topics .ID)}}selected{{end}}>&nbsp;&nbsp;&nbsp;– {{.EN}} / {{.AR}}</option>{{end}}{{end}}</optgroup>{{end}}
</select></fieldset>
<fieldset><legend>Signals</legend>
Intent <select name="intent">{{range .Intents}}<option {{if and $l (eq $l.Intent .)}}selected{{end}}>{{.}}</option>{{end}}</select>
Emotion <select name="emotion">{{range .Emotions}}<option {{if and $l (eq $l.Emotion .)}}selected{{end}}>{{.}}</option>{{end}}</select>
Toxicity {{range .Toxicities}}<label><input type="radio" name="toxicity" value="{{.}}" {{if and $l (eq $l.Toxicity .)}}checked{{else if and (not $l) (eq . "none")}}checked{{end}}>{{.}}</label>{{end}}
<label><input type="checkbox" name="spam" {{if and $l $l.Spam}}checked{{end}}>spam</label>
<label><input type="checkbox" name="urgent" {{if and $l $l.Urgent}}checked{{end}}>urgent</label>
<br>Advertising {{range .Ads}}<label><input type="radio" name="ad" value="{{.}}" {{if and $l (eq $l.Ad .)}}checked{{else if and $l (eq $l.Ad "") (eq . "none")}}checked{{else if and (not $l) (eq . "none")}}checked{{end}}>{{.}}</label>{{end}}
<br>Possible legal or policy breach {{range .Legal}}<label><input type="checkbox" name="legal" value="{{.}}" {{if and $l (has $l.Legal .)}}checked{{end}}>{{.}}</label>{{end}}
</fieldset>
{{if .Item.ImagePath}}<fieldset><legend>Image</legend>
Logos (registry ids or names, comma separated) <input type="text" name="logos" size="40" value="{{if $l}}{{join $l.Logos ", "}}{{end}}"><br>
Words that must be read from the image (comma separated) <input type="text" name="ocr" size="40" dir="auto" value="{{if $l}}{{join $l.OCRContains ", "}}{{end}}">
</fieldset>{{end}}
<fieldset><legend>Notes</legend>
<label><input type="checkbox" name="unsure" {{if and $l $l.Unsure}}checked{{end}}>unsure / cannot tell</label>
<textarea name="notes" rows="2" cols="60" dir="auto">{{if $l}}{{$l.Notes}}{{end}}</textarea>
<br>Labeller <input type="text" name="labeller" value="{{.Labeller}}" size="10">
</fieldset>
<button type="submit">Save and next</button>
</form></body></html>`
