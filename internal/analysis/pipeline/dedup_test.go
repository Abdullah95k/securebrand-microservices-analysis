package pipeline

import (
	"testing"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/store"
	"github.com/abdullah95k/securebrand-microservices/internal/config"
)

func item(id, thread, parent, kind, text string) core.Item {
	return core.Item{ContentID: id, ThreadID: thread, ParentID: parent, Kind: kind, Text: text}
}

func hashesOf(items []core.Item) [][]byte {
	out := make([][]byte, len(items))
	for i := range items {
		out[i] = items[i].InputHash()
	}
	return out
}

func TestLeaders(t *testing.T) {
	items := []core.Item{
		item("c1", "p1", "p1", core.KindComment, "تم"),
		item("c2", "p1", "p1", core.KindComment, "تم"),
		item("c3", "p1", "p1", core.KindComment, "تم ✅"),
		item("r1", "p1", "c1", core.KindReply, "تم"),   // another parent
		item("c4", "p2", "p2", core.KindComment, "تم"), // another thread
		item("c5", "p1", "p1", core.KindComment, "  تم "),
		{ContentID: "gone"}, // erased meanwhile: placeholder
		item("r2", "p1", "c1", core.KindReply, "تم"),
		item("r3", "p1", "c2", core.KindReply, "تم"), // same reply under another comment
	}
	got := leaders(core.RouteComment, items, hashesOf(items))
	want := []int{0, 0, 2, 3, 4, 0, 6, 3, 8}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("leaders = %v, want %v", got, want)
		}
	}
	for _, route := range []string{core.RoutePost, core.RouteAudit} {
		for i, l := range leaders(route, items, hashesOf(items)) {
			if l != i {
				t.Fatalf("%s must not collapse items", route)
			}
		}
	}
	if l := leaders(core.RouteEscalation, items, hashesOf(items)); l[1] != 0 {
		t.Fatal("escalations collapse too")
	}
	posts := []core.Item{item("p1", "", "", core.KindPost, "عرض"), item("p2", "", "", core.KindPost, "عرض")}
	if l := leaders(core.RouteComment, posts, hashesOf(posts)); l[1] != 1 {
		t.Fatal("identical posts (other pages) must not collapse")
	}
	// A short hash list (corrupt row) never panics or merges.
	if l := leaders(core.RouteComment, items, nil); l[1] != 1 {
		t.Fatal("no hashes: no merging")
	}
}

func TestGroupsCollapseDuplicates(t *testing.T) {
	cfg := config.Analysis{}
	cfg.Grouping.MaxItems, cfg.Grouping.MaxChars = 2, 1000
	p := &Pipeline{Cfg: cfg}
	claim := func(it core.Item, attempts, prio int) store.Claimed {
		return store.Claimed{Item: it, Attempts: attempts, Priority: prio}
	}
	cs := []store.Claimed{
		claim(item("c1", "p1", "p1", core.KindComment, "تم"), 0, core.PriorityNormal),
		claim(item("c2", "p1", "p1", core.KindComment, "حلو"), 0, core.PriorityNormal),
		claim(item("c3", "p1", "p1", core.KindComment, "تم"), 0, core.PriorityNormal),   // joins the closed first group
		claim(item("c4", "p1", "p1", core.KindComment, "سيء"), 0, core.PriorityNormal),  // second group
		claim(item("c5", "p1", "p1", core.KindComment, "حلو"), 0, core.PriorityUrgent),  // joins the first, makes it urgent
		claim(item("c6", "p1", "p1", core.KindComment, "سيء"), 0, core.PriorityNormal),  // joins the open second group
		claim(item("c7", "p1", "p1", core.KindComment, "عادي"), 0, core.PriorityNormal), // second group: its second leader
		claim(item("c8", "p1", "p1", core.KindComment, "تم"), 2, core.PriorityNormal),   // a retry: alone
		claim(item("c9", "p2", "p2", core.KindComment, "تم"), 0, core.PriorityNormal),   // another thread
	}
	gs := p.Groups(core.RouteComment, cs)
	got := map[string]string{}
	for i, g := range gs {
		for _, it := range g.items {
			got[it.ContentID] = string(rune('A' + i))
		}
	}
	// c8 is a retry (first, alone), then thread p1 (two groups), then p2.
	want := map[string]string{"c8": "A", "c1": "B", "c2": "B", "c3": "B", "c5": "B", "c4": "C", "c6": "C", "c7": "C", "c9": "D"}
	for id, g := range want {
		if got[id] != g {
			t.Fatalf("groups %v, want %v", got, want)
		}
	}
	if len(gs) != 4 || !gs[1].urgent || gs[2].urgent {
		t.Fatalf("%d groups, urgent %v %v", len(gs), gs[1].urgent, gs[2].urgent)
	}
	// Posts are not collapsed and pack by the limits.
	posts := []store.Claimed{
		claim(item("p1", "", "", core.KindPost, "خبر"), 0, core.PriorityNormal),
		claim(item("p2", "", "", core.KindPost, "خبر"), 0, core.PriorityNormal),
		claim(item("p3", "", "", core.KindPost, "خبر"), 0, core.PriorityNormal),
	}
	if gs := p.Groups(core.RoutePost, posts); len(gs) != 2 || len(gs[0].items) != 2 {
		t.Fatalf("posts: %d groups", len(gs))
	}
}
