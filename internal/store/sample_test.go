package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
)

// TestIngestSamplesComments: with analysis.comments.sample_share 0.10 (the
// owner's D-053) a new comment is stored and queued only if its ID is in
// the sample, or it takes the real-time lane; the rest are counted per day
// and platform, not stored. Posts aren't sampled; a comment stored before
// keeps getting its edits; a re-send counts again and stays out.
func TestIngestSamplesComments(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	// Stored before the sample, and outside it.
	var before string
	for i := 0; before == ""; i++ {
		if id := fmt.Sprintf("fb:comment:old-%d", i); !core.InSample(id, 0.10) {
			before = id
		}
	}
	if _, err := s.Ingest(ctx, []*core.Item{comment(before, "تعليق قديم", at, "a0")}, plan); err != nil {
		t.Fatal(err)
	}

	s.CommentShare = 0.10
	urgentPlan := func(it *core.Item) Plan {
		p := plan(it)
		if strings.Contains(it.Text, "عاجل") {
			p.Lane, p.Priority = core.LaneRealtime, core.PriorityUrgent
		}
		return p
	}
	var items []*core.Item
	inSample, urgentOut := 0, 0
	for i := range 2000 {
		id := fmt.Sprintf("fb:comment:%d", i)
		text := fmt.Sprintf("تعليق رقم %d", i)
		if i%100 == 0 {
			text += " عاجل"
		}
		items = append(items, comment(id, text, at, fmt.Sprintf("a%d", i)))
		switch {
		case core.InSample(id, 0.10):
			inSample++
		case i%100 == 0:
			urgentOut++
		}
	}
	post := &core.Item{ContentID: "fb:post:1", Platform: "facebook", Kind: core.KindPost, SourceID: "fb:page:1", CreatedAt: at,
		ObservedAt: at, Text: "منشور", Status: core.StatusVisible}
	edit := comment(before, "تعليق قديم بعد التعديل", at, "a0")
	edit.ObservedAt = at.Add(time.Minute)
	st, err := s.Ingest(ctx, append(items, post, edit), urgentPlan)
	if err != nil {
		t.Fatal(err)
	}
	if inSample < 150 || inSample > 250 {
		t.Fatalf("%d of 2000 in the 10%% sample", inSample)
	}
	want := 2000 - inSample - urgentOut
	if st.NotSampled != want || st.Edited != 1 || st.New != inSample+urgentOut+1 {
		t.Fatalf("stats %+v: want %d not sampled, the old comment edited, %d new (sampled, urgent, the post)", st, want, inSample+urgentOut+1)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.content_items WHERE kind = 'comment'`); n != inSample+urgentOut+1 {
		t.Fatalf("%d comments stored, want %d (the sample, the urgent ones, the old one)", n, inSample+urgentOut+1)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.analysis_queue`); n != inSample+urgentOut+2 {
		t.Fatalf("%d queued, want %d (the sample, the urgent ones, the post, the edit)", n, inSample+urgentOut+2)
	}
	var received, sampled, urgent int
	var share float64
	if err := s.DB.QueryRow(ctx, `SELECT received, sampled, urgent, share::float8 FROM analysis.comment_sample_daily WHERE platform = 'facebook'`).
		Scan(&received, &sampled, &urgent, &share); err != nil {
		t.Fatal(err)
	}
	// The old comment's first record (at the default share of 1, so in),
	// the 2,000 and its edit (outside the sample now, kept as stored).
	if received != 2002 || sampled != inSample+1 || urgent != urgentOut || share != 0.10 {
		t.Fatalf("counted %d received, %d sampled, %d urgent at %v; want 2002, %d, %d at 0.1", received, sampled, urgent, share, inSample+1, urgentOut)
	}

	// The same comments again (a collector's re-send): counted again, the
	// same ones out, nothing new stored.
	st, err = s.Ingest(ctx, items, urgentPlan)
	if err != nil {
		t.Fatal(err)
	}
	if st.NotSampled != want || st.New != 0 {
		t.Fatalf("re-send: %+v, want %d not sampled and nothing new", st, want)
	}
	if n := count(t, s, `SELECT received FROM analysis.comment_sample_daily WHERE platform = 'facebook'`); n != 4002 {
		t.Fatalf("received %d after the re-send, want 4002", n)
	}
	cs, err := s.CommentSampleCounts(ctx, time.Now().Add(-24*time.Hour))
	if err != nil || len(cs) != 1 || cs[0].Received != 4002 || cs[0].Sampled != int64(2*inSample+1) || cs[0].Share != 0.10 {
		t.Fatalf("CommentSampleCounts %+v %v", cs, err)
	}
}
