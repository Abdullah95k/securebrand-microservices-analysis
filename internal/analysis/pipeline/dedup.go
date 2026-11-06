package pipeline

import "github.com/abdullah95k/securebrand-microservices/internal/analysis/core"

// Identical items of one thread are analysed once. Contest posts ("علق تم
// للمشاركة"), condolence and prayer threads collect hundreds of identical
// comments; with the same thread context, the same parent and the same
// input (text, kind, media) the model would give the same answer each
// time. The first such item of a request (the leader) is sent; the others
// (followers) ride in the same request, are not shown to the model, and
// get the leader's answer. Posts and audits are never collapsed.

// dedupRoute reports whether a route collapses identical items.
func dedupRoute(route string) bool { return route != core.RoutePost && route != core.RouteAudit }

// dupKey is the identity of an item's analysis input within a request.
func dupKey(it *core.Item, hash []byte) string {
	return it.ThreadID + "\x00" + it.ParentID + "\x00" + string(hash)
}

// leaders maps each item of a request to the position of the item whose
// answer it shares (itself for a leader). It depends only on the items'
// thread, parent and the input hashes recorded with the request, so the
// batch lane recomputes the same mapping when results arrive hours later.
// An item erased meanwhile (a placeholder without kind) leads only itself:
// its followers then miss from the answer and are retried.
func leaders(route string, items []core.Item, hashes [][]byte) []int {
	lead := make([]int, len(items))
	first := map[string]int{}
	for i := range items {
		lead[i] = i
		// Posts (short ones use the comment route) belong to different
		// pages even when their text is identical: never collapsed.
		if !dedupRoute(route) || items[i].Kind == "" || items[i].Kind == core.KindPost || i >= len(hashes) {
			continue
		}
		k := dupKey(&items[i], hashes[i])
		if j, ok := first[k]; ok {
			lead[i] = j
			continue
		}
		first[k] = i
	}
	return lead
}
