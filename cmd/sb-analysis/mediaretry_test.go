package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/testenv"
)

// sb-analysis media-retry: videos failed because speech-to-text failed are
// counted, and with --apply put back from their first try, only while their
// files have time left; other failures are left alone.
func TestMediaRetry(t *testing.T) {
	db := testenv.Postgres(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := store.New(db, time.UTC)
	job := func(id string, deleteIn time.Duration) *core.MediaJob {
		return &core.MediaJob{MediaID: id, OwnerContentID: "tt:post:1", Platform: "tiktok", Kind: "video", Role: "content",
			Bucket: "b", Key: id, AckFormat: core.AckContract, DeleteAfter: time.Now().Add(deleteIn)}
	}
	if _, err := s.InsertMediaJobs(ctx, []*core.MediaJob{job("in-time", 48*time.Hour), job("too-late", 2*time.Hour), job("other", 48*time.Hour)},
		"sb-analysis"); err != nil {
		t.Fatal(err)
	}
	for id, e := range map[string]string{
		"in-time":  "transcribe: provider: status 500: /audio/transcriptions: Internal Server Error",
		"too-late": "transcribe: provider: status 500: /audio/transcriptions: Internal Server Error",
		"other":    "media: undecodable image",
	} {
		if _, err := db.Exec(ctx, `UPDATE analysis.media_jobs SET state = 'failed', tries = 5, done_at = now(), last_error = $2
			WHERE media_id = $1`, id, e); err != nil {
			t.Fatal(err)
		}
	}
	f := store.MediaRetryFilter{Reason: "transcribe", Platform: "tiktok", FailedSince: time.Now().Add(-time.Hour), MinLeft: 6 * time.Hour}
	r, err := s.RetryFailedMedia(ctx, f, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.Matched != 2 || r.InTime != 1 || r.Retried != 0 {
		t.Fatalf("dry run %+v, want 2 matched, 1 in time, none put back", r)
	}
	var out bytes.Buffer
	if err := printMediaRetry(&out, f, r, false); err != nil || !strings.Contains(out.String(), `"transcribe" (tiktok): 2 match; 1 still have`) ||
		!strings.Contains(out.String(), "none changed") {
		t.Fatalf("output %q %v", out.String(), err)
	}
	if r, err = s.RetryFailedMedia(ctx, f, true); err != nil || r.Retried != 1 {
		t.Fatalf("apply %+v %v, want 1 put back", r, err)
	}
	states := map[string]string{}
	rows, err := db.Query(ctx, `SELECT media_id, state || ':' || tries FROM analysis.media_jobs`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			t.Fatal(err)
		}
		states[id] = st
	}
	rows.Close()
	if states["in-time"] != "pending:0" || states["too-late"] != "failed:5" || states["other"] != "failed:5" {
		t.Fatalf("states %v", states)
	}
}
