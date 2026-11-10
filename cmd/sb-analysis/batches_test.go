package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/testenv"
)

type statusOf map[string]provider.BatchStatus

func (s statusOf) BatchStatus(_ context.Context, id string) (provider.BatchStatus, error) {
	st, ok := s[id]
	if !ok {
		return provider.BatchStatus{}, errors.New("not found")
	}
	return st, nil
}

// sb-analysis batches: each open batch with its requests here and the
// provider's own counts, without content.
func TestBatchesListsOpenBatchesWithTheProvidersCounts(t *testing.T) {
	db := testenv.Postgres(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Asia/Baghdad")
	s := store.New(db, loc)
	var reqs []uuid.UUID
	for i := range 3 {
		id := uuid.New()
		reqs = append(reqs, id)
		if err := s.StartRequest(ctx, store.RequestRow{ID: id, Route: core.RouteComment, Lane: core.LaneBatch, Provider: "anthropic",
			Model: "claude-haiku-5-5", ContentIDs: []string{"tt:comment:" + string(rune('1'+i))}, InputHashes: [][]byte{{byte(i)}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetBatchID(ctx, "msgbatch_1", "anthropic", "claude-haiku-5-5", core.RouteComment, reqs, 26*time.Hour); err != nil {
		t.Fatal(err)
	}
	list, err := s.RecentBatches(ctx, time.Now().Add(-time.Hour))
	if err != nil || len(list) != 1 || list[0].Submitted != 3 || list[0].Status != "in_progress" {
		t.Fatalf("batches %+v, %v", list, err)
	}
	var out bytes.Buffer
	st := statusOf{"msgbatch_1": {Status: "in_progress", Processing: 2, Succeeded: 1, ExpiresAt: time.Now().Add(23 * time.Hour)}}
	if err := printBatches(ctx, &out, list, loc, func(string) (provider.BatchStatuser, bool) { return st, true }); err != nil {
		t.Fatal(err)
	}
	line := out.String()
	for _, want := range []string{"msgbatch_1", "anthropic/claude-haiku-5-5", "comment", "here: in_progress, 3 requests (submitted 3, done 0, failed 0)",
		"provider: in_progress, processing 2, succeeded 1, errored 0, canceled 0, expired 0"} {
		if !strings.Contains(line, want) {
			t.Errorf("output lacks %q:\n%s", want, line)
		}
	}
	// A provider without a status read says so.
	out.Reset()
	if err := printBatches(ctx, &out, list, loc, func(string) (provider.BatchStatuser, bool) { return nil, false }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "provider: status not available") {
		t.Fatalf("output %q", out.String())
	}
}
