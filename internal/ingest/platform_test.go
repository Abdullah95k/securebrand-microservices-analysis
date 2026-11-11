package ingest

import (
	"testing"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
)

// A batch mixing collectors is stored per platform, so each item counts
// under its own platform in items_ingested_total; order is kept.
func TestByPlatformKeepsOrder(t *testing.T) {
	it := func(id, p string) *core.Item { return &core.Item{ContentID: id, Platform: p} }
	got := byPlatform([]*core.Item{it("tt:post:1", "tiktok"), it("yt:post:1", "youtube"), it("tt:post:2", "tiktok"), it("yt:comment:1", "youtube")})
	if len(got) != 2 || got[0][0].Platform != "tiktok" || got[1][0].Platform != "youtube" {
		t.Fatalf("groups %v", got)
	}
	if got[0][0].ContentID != "tt:post:1" || got[0][1].ContentID != "tt:post:2" || got[1][1].ContentID != "yt:comment:1" {
		t.Fatal("order within a platform not kept")
	}
	if len(byPlatform(nil)) != 0 {
		t.Fatal("no items, no groups")
	}
}
