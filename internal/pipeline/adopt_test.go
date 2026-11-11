package pipeline_test

import (
	"context"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	analysisv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/analysis/v1"
	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"

	"github.com/abdullah95k/securebrand-contracts/conformance"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/adapter"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/ingest"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/kafka"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/topics"
	fbv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/fb/v1"
)

// TestCutOverAdoptsWhatFbAnalysed runs fbs's cut-over end to end through
// ingest, the batch lane and the outbox: a post read from fbs's own topic
// (fb.post.v1) is analysed and its result stays in Postgres (decision 17).
// The same post then arrives through the contract (content.post.v1), worded
// and naming its media slightly differently: it is adopted, with no AI call,
// and its stored analysis is published once on analysis.result.v1 with the
// contract's envelope.version, passing the contract's conformance checks. The
// same version again changes nothing; an edit (version 2) is analysed and
// published as usual.
func TestCutOverAdoptsWhatFbAnalysed(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	c, err := ingest.New(ingest.Config{Kafka: kafka.ClientConfig{Brokers: []string{"127.0.0.1:1"}}, Group: "test",
		Decoder: adapter.NewDecoder([]string{"content", "fb"}), Store: e.s, Plan: e.p.Plan, Media: true, Consumer: "analysis"})
	if err != nil {
		t.Fatal(err)
	}
	flush := func(topic string, m proto.Message) {
		t.Helper()
		b, err := proto.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Flush(ctx, []*kgo.Record{{Topic: topic, Value: b}}); err != nil {
			t.Fatal(err)
		}
	}
	analyse := func() {
		t.Helper()
		if err := e.p.SubmitBatches(ctx); err != nil {
			t.Fatal(err)
		}
		if err := e.p.PollBatches(ctx); err != nil {
			t.Fatal(err)
		}
	}
	published := func() []*analysisv1.ItemAnalysis {
		t.Helper()
		rows, err := e.s.DB.Query(ctx, `SELECT payload FROM analysis.outbox WHERE topic = $1 AND key = 'fb:post:7' ORDER BY id`, topics.AnalysisResult)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []*analysisv1.ItemAnalysis
		for rows.Next() {
			var b []byte
			if err := rows.Scan(&b); err != nil {
				t.Fatal(err)
			}
			var m analysisv1.ItemAnalysis
			if err := proto.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
			out = append(out, &m)
		}
		return out
	}
	calls := func() int { return e.count(t, `SELECT count(*) FROM analysis.provider_requests`) }
	at := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)

	// fbs's own topic: analysed, nothing published.
	flush(topics.Post, &fbv1.Post{PostId: 7, PageId: 9, AuthorName: "Asiacell", CreatedAt: timestamppb.New(at),
		Text: "عرض جديد على باقات الانترنت", Links: []*fbv1.LinkAttachment{{Title: "باقات الشهر"}}, MediaIds: []string{"9001"}})
	analyse()
	if n := e.count(t, `SELECT count(*) FROM analysis.analyses WHERE content_id = 'fb:post:7' AND is_current`); n != 1 {
		t.Fatalf("%d current analyses of the fb.* post, want 1", n)
	}
	if got := published(); len(got) != 0 {
		t.Fatalf("an fb.* result was published: %d records", len(got))
	}
	before := calls()

	// The contract's version 1: adopted, no AI call, published once.
	post := func(version int64, text string) *contentv1.Post {
		return &contentv1.Post{
			Envelope:  &contentv1.Envelope{Id: "fb:post:7", Platform: "fb", Version: version, ObservedAt: timestamppb.Now()},
			AccountId: "fb:account:9", Text: &contentv1.Text{Text: text}, Description: &contentv1.Text{Text: "باقات الشهر"},
			PublishedAt: &contentv1.Time{At: timestamppb.New(at), Precision: contentv1.TimePrecision_TIME_PRECISION_EXACT},
			Status:      &contentv1.Status{Availability: contentv1.Availability_AVAILABILITY_VISIBLE},
			Media:       []*contentv1.MediaRef{{MediaId: "fb:media:00000000-0000-5000-8000-000000009001"}},
		}
	}
	flush(topics.ContentPost, post(1, "عرض جديد على باقات الانترنت"))
	if n := e.count(t, `SELECT count(*) FROM analysis.analysis_queue WHERE content_id = 'fb:post:7'`); n != 0 {
		t.Fatalf("the adopted post was queued (%d entries)", n)
	}
	analyse()
	if calls() != before {
		t.Fatalf("an AI call for the adopted post: %d requests, were %d", calls(), before)
	}
	got := published()
	if len(got) != 1 {
		t.Fatalf("%d records published for the adopted post, want 1", len(got))
	}
	m := got[0]
	if m.ContentId != "fb:post:7" || m.ContentVersion != 1 || m.AccountId != "fb:account:9" || m.GetProvenance().GetAnalysisVersion() != 1 ||
		m.GetProvenance().GetModel() == "" || m.GetSentiment().GetLabel() == "" {
		t.Fatalf("published %v", m)
	}
	if err := conformance.ItemAnalysis(m, conformance.Options{Production: true}); err != nil {
		t.Fatalf("the adopted result fails the contract's conformance checks: %v", err)
	}
	if n := e.count(t, `SELECT content_version FROM analysis.content_items WHERE content_id = 'fb:post:7'`); n != 1 {
		t.Fatalf("stored content_version %d, want the contract's 1", n)
	}

	// The same version again: unchanged, nothing new.
	flush(topics.ContentPost, post(1, "عرض جديد على باقات الانترنت"))
	analyse()
	if len(published()) != 1 || calls() != before {
		t.Fatal("the same contract version was published or analysed again")
	}

	// An edit: analysed and published as usual.
	flush(topics.ContentPost, post(2, "عرض جديد على باقات الانترنت — تم التحديث"))
	analyse()
	if calls() == before {
		t.Fatal("the edit wasn't analysed")
	}
	if got := published(); len(got) != 2 || got[1].ContentVersion != 2 || got[1].GetProvenance().GetAnalysisVersion() != 2 {
		t.Fatalf("after the edit: %d records, last %v", len(got), got[len(got)-1])
	}
}

// TestCutOverWithoutAnalysisIsAnEdit: an fb.* item marked analysed but with
// no current analysis (none to publish) is analysed through the contract as
// an edit, not dropped.
func TestCutOverWithoutAnalysisIsAnEdit(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	fb := adapter.FromFBPost(&fbv1.Post{PostId: 8, PageId: 9, CreatedAt: timestamppb.New(time.Now().Add(-time.Hour)), Text: "خبر قديم"})
	e.ingest(t, fb)
	if _, err := e.s.DB.Exec(ctx, `UPDATE analysis.content_index SET state = 'analyzed' WHERE content_id = 'fb:post:8'`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.DB.Exec(ctx, `DELETE FROM analysis.analysis_queue WHERE content_id = 'fb:post:8'`); err != nil {
		t.Fatal(err)
	}
	contract := *fb
	contract.Version = 1
	st, err := e.s.Ingest(ctx, []*core.Item{&contract}, e.p.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if st.Adopted != 0 || st.Edited != 1 {
		t.Fatalf("stats %+v, want 1 edited and none adopted", st)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analysis_queue WHERE content_id = 'fb:post:8'`); n != 1 {
		t.Fatalf("%d queue entries, want 1", n)
	}
}
