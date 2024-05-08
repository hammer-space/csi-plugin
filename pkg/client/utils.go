package client

import (
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
