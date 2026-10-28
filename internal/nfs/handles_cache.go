package nfs

import (
	"container/list"
	"crypto/sha256"
	"sync"
)

const hotHandleLimit = 1024

// Only committed wire records enter this bounded cache. Files keep immutable
// attributes; directory operations still refresh state through the provider.
type handleCache struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]*list.Element
	order   list.List
}

type cachedHandle struct {
	key   [sha256.Size]byte
	value record
}

func (c *handleCache) get(key [sha256.Size]byte) (record, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	element := c.entries[key]
	if element == nil {
		return record{}, false
	}

	value, ok := element.Value.(cachedHandle)
	if !ok {
		return record{}, false
	}

	c.order.MoveToFront(element)

	return value.value, true
}

func (c *handleCache) put(r record) {
	key := [sha256.Size]byte(r.id)
	r.id = append([]byte(nil), r.id...)

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.entries == nil {
		c.entries = make(map[[sha256.Size]byte]*list.Element)
	}

	if element := c.entries[key]; element != nil {
		element.Value = cachedHandle{key: key, value: r}
		c.order.MoveToFront(element)

		return
	}

	if len(c.entries) == hotHandleLimit {
		last := c.order.Back()

		value, ok := last.Value.(cachedHandle)
		if !ok {
			return // An unavailable cache entry must fall back to the database.
		}

		delete(c.entries, value.key)
		c.order.Remove(last)
	}

	c.entries[key] = c.order.PushFront(cachedHandle{key: key, value: r})
}
