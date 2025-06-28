package client

import (
	"sync/atomic"
	"time"

	"github.com/hammer-space/csi-plugin/pkg/common"
)

var cache = common.CsiCache()

func GetCacheData(key string) (interface{}, error) {
	if cachedData, ok := cache.Get(key); ok {
		return cachedData, nil
	}
	return nil, nil
}

func SetCacheData(key string, value interface{}, cacheExpireTime int) {
	if cacheExpireTime != 0 {
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
