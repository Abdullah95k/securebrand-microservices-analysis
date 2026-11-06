package main

import (
	"bytes"
	"testing"
)

func TestAuthorKeysAndLabels(t *testing.T) {
	k, err := authorKey("tt:account:7000000000000000001")
	if err != nil || string(k) != "tt:account:7000000000000000001" || accountPlatform(string(k)) != "tt" {
		t.Fatalf("account ID: %q %v", k, err)
	}
	if k, err = authorKey("6175"); err != nil || !bytes.Equal(k, []byte("au")) {
		t.Fatalf("hex key: %q %v", k, err)
	}
	for _, bad := range []string{"", "xyz", "tt:post:1", "tt:account:", ":account:1"} {
		if _, err := authorKey(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if l := authorLabel([]byte("fb:account:9")); l != "fb:account:9" {
		t.Errorf("account ID printed as %q", l)
	}
	if l := authorLabel([]byte{0xff, 0x01}); l != "ff01" {
		t.Errorf("fbs key printed as %q", l)
	}
	if l := authorLabel([]byte("net-0")); l != "6e65742d30" {
		t.Errorf("a key that is not an account ID printed as %q", l)
	}
}
