package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHotAccountsListRefreshAndFailure(t *testing.T) {
	watch, reads := []string{"fb:page:2"}, 0
	var fail error
	clock := time.Unix(0, 0)
	h := NewHotAccounts([]string{"fb:page:1"}, func(context.Context) ([]string, error) { reads++; return watch, fail }, time.Minute)
	h.now = func() time.Time { return clock }
	for src, want := range map[string]bool{"fb:page:1": true, "fb:page:2": true, "fb:page:3": false, "": false} {
		if h.Hot(src) != want {
			t.Errorf("Hot(%q) = %v, want %v", src, !want, want)
		}
	}
	if reads != 1 {
		t.Fatalf("watchlists read %d times within a minute, want 1", reads)
	}
	watch = []string{"fb:page:3"}
	clock = clock.Add(time.Minute)
	if !h.Hot("fb:page:3") || h.Hot("fb:page:2") {
		t.Fatal("a changed watchlist was not picked up after refresh")
	}
	fail = errors.New("database down")
	clock = clock.Add(time.Minute)
	if !h.Hot("fb:page:3") {
		t.Fatal("a failed read dropped the last hot accounts")
	}
	if !h.Hot("fb:page:1") {
		t.Fatal("the config list must not depend on the database")
	}
	var none *HotAccounts
	if none.Hot("fb:page:1") {
		t.Fatal("no hot tier, no hot account")
	}
}
