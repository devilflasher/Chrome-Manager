package utils

import "container/list"

// LRUCache implementation
type LRUCache struct {
	maxSize int
	ll      *list.List
	cache   map[string]*list.Element
}

type entry struct {
	key   string
	value int64 // size
}

func NewLRUCache(maxSize int) *LRUCache {
	return &LRUCache{
		maxSize: maxSize,
		ll:      list.New(),
		cache:   make(map[string]*list.Element),
	}
}

func (c *LRUCache) Add(key string, value int64) {
	if ee, ok := c.cache[key]; ok {
		c.ll.MoveToFront(ee)
		ee.Value.(*entry).value = value
		return
	}
	ele := c.ll.PushFront(&entry{key, value})
	c.cache[key] = ele
	if c.maxSize > 0 && c.ll.Len() > c.maxSize {
		c.RemoveOldest()
	}
}

func (c *LRUCache) RemoveOldest() string {
	ele := c.ll.Back()
	if ele != nil {
		c.ll.Remove(ele)
		kv := ele.Value.(*entry)
		delete(c.cache, kv.key)
		return kv.key
	}
	return ""
}
