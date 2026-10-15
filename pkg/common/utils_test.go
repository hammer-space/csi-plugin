package common

import (
	"testing"
	"time"
)

// SetCacheData used to invert its condition, discarding every caller-supplied
// TTL in favour of 60s and giving a caller who passed 0 an entry that had
// already expired.
func TestSetCacheDataHonoursCallerTTL(t *testing.T) {
	SetCacheData("ttl-explicit", "v", 300)
	if _, ok := cache.Get("ttl-explicit"); !ok {
		t.Fatal("entry with an explicit 300s TTL should be present")
	}
	if entry, ok := cache.data["ttl-explicit"]; !ok {
		t.Fatal("entry missing from cache")
	} else if remaining := time.Until(entry.expiration); remaining < 250*time.Second {
		t.Fatalf("TTL was capped: %v remaining, want ~300s", remaining)
	}

	// 0 means "use the default", not "expire immediately".
	SetCacheData("ttl-default", "v", 0)
	if _, ok := cache.Get("ttl-default"); !ok {
		t.Fatal("a 0 TTL must fall back to the default, not expire the entry instantly")
	}
}
