package common

import (
	"sync"
	"time"
)

type Cache struct {
	data map[string]cacheValue
	lock sync.Mutex
}

type cacheValue struct {
	value      interface{}
	expiration time.Time
}

func CsiCache() *Cache {
	return &Cache{
		data: make(map[string]cacheValue),
	}
}

func (c *Cache) Set(key string, value interface{}, expiration time.Duration) {
	c.lock.Lock()
	defer c.lock.Unlock()

	expirationTime := time.Now().Add(expiration)
	c.data[key] = cacheValue{
		value:      value,
		expiration: expirationTime,
	}
}

func (c *Cache) Get(key string) (interface{}, bool) {
	c.lock.Lock()
	defer c.lock.Unlock()

	value, ok := c.data[key]
	if !ok || time.Now().After(value.expiration) {
		delete(c.data, key)
		return nil, false
	}

	return value.value, true
}
