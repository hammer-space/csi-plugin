package common

import (
	"sync/atomic"
	"time"
)

var cache = CsiCache()

func GetCacheData(key string) (interface{}, error) {
	if cachedData, ok := cache.Get(key); ok {
		return cachedData, nil
	}
	return nil, nil
}

func SetCacheData(key string, value interface{}, cacheExpireTime int) {
	// The condition used to be inverted (!= 0), so a caller-supplied TTL was
	// always discarded in favour of 60s and a caller passing 0 got an entry
	// that had already expired. Every cache in the driver was therefore capped
	// at one minute: the objective lists and free capacity asked for 5 minutes,
	// the NFS export list for an hour.
	if cacheExpireTime == 0 {
		cacheExpireTime = 60 // 1 min is default timeout
	}
	cache.Set(key, value, time.Duration(cacheExpireTime)*time.Second)
}

// GetRoundRobinOrderedList returns a round-robin ordered list of items
func GetRoundRobinOrderedList(index *uint32, list []string) []string {
	count := len(list)
	if count == 0 {
		return []string{}
	}
	start := int(atomic.AddUint32(index, 1)) % count
	ordered := make([]string, 0, count)
	for i := 0; i < count; i++ {
		ordered = append(ordered, list[(start+i)%count])
	}
	return ordered
}
